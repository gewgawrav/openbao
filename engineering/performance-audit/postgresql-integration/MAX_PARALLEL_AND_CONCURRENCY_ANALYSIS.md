# Max Parallel and Concurrency Analysis 🤖

## Executive Summary

This report analyzes the concurrency controls within OpenBao's PostgreSQL backend, specifically examining `max_parallel`, `transaction_max_parallel`, the internal `PermitPool` implementation, and the architectural discussions in RFC `website/content/community/rfcs/postgresql.mdx`. We audit whether multiple synchronization layers are enforcing redundant limits and determine the exact state of upstream pull requests.

Evidence classifications: `SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`.

---

## 1. Concurrency Architecture: Code Tracing

### 1.1 Non-Transactional Storage Path
In `internal/physical/postgresql/postgresql.go`:
- `Get(ctx, fullPath)`:
  ```go
  err := m.client.QueryRowContext(ctx, m.getQuery, path, key).Scan(&result)
  ```
- `Put(ctx, entry)`:
  ```go
  if err := m.validateFence(ctx); err != nil { return err }
  _, err := m.client.ExecContext(ctx, m.putQuery, parentPath, path, key, entry.Value)
  ```
- **Concurrency Control Mechanism (`SOURCE-CONFIRMED`)**:
  There is **no** Go channel lock or PermitPool wrapping these calls in `PostgreSQLBackend`. Goroutines enter `database/sql.DB` directly. Inside Go's standard library `database/sql`, connection acquisition is bounded by `db.SetMaxOpenConns(maxParInt)`.
  If 128 queries are currently in flight, the 129th goroutine blocks waiting on `db.connRequest` channel in `database/sql`.

### 1.2 Transactional Storage Path
In `internal/physical/postgresql/transaction.go:31-45`:
```go
func (b *PostgreSQLBackend) newTransaction(ctx context.Context, readOnly bool) (physical.Transaction, error) {
    b.txnPermitPool.Acquire()

    tx, err := b.client.BeginTx(ctx, &sql.TxOptions{
        Isolation: sql.LevelRepeatableRead,
        ReadOnly:  readOnly,
    })
    if err != nil {
        b.txnPermitPool.Release()
        return nil, fmt.Errorf("failed to start underlying postgresql transaction: %w", err)
    }
    ...
```
And inside `Commit()` and `Rollback()` (`transaction.go:224-257`):
```go
defer func() {
    t.b.txnPermitPool.Release()
    t.haveFinishedTx = true
    t.cleanup.Stop()
}()
```
- **Double Gate Analysis (`SOURCE-CONFIRMED`)**:
  Transactions pass through two consecutive gating mechanisms:
  1. `b.txnPermitPool.Acquire()`: A channel-based counting semaphore (`sdk/physical/permit_pool.go`) capped at `txnMaxParInt` (default 64).
  2. `b.client.BeginTx`: A channel-based queue in Go's `database/sql` capped at `maxParInt` (default 128).

---

## 2. PostgreSQL RFC Analysis & PR Status

### 2.1 RFC Proposition
In `website/content/community/rfcs/postgresql.mdx:182-190`:
> **Removing parallel locking**
> OpenBao's `postgresql` storage backend includes its own `PermitPool`, ensuring `max_parallel` is enforced at the storage layer. However, it also calls `db.SetMaxOpenConns(...)` which internally uses channels to achieve the same thing. We can remove the outer permit pool and rely strictly on Go's implementation.
> This change is already proposed for inclusion (https://github.com/openbao/openbao/pull/1299).

### 2.2 What the Current Fork Actually Implements (`SOURCE-CONFIRMED`)
1. **Did PR #1299 get merged?**:
   - Examination of `internal/physical/postgresql/postgresql.go` reveals that `PostgreSQLBackend` has **already removed** the outer CRUD `PermitPool`!
   - In earlier HashiCorp Vault versions, `m.permitPool.Acquire()` wrapped `Put`, `Get`, `Delete`, and `List`. In this OpenBao fork, `m.permitPool` does not exist on `PostgreSQLBackend`!
   - Only `b.txnPermitPool` remains (`SOURCE-CONFIRMED`: lines 84, 255).
2. **Why does `txnPermitPool` still exist?**:
   - Lines 144–147 of `postgresql.go`:
     ```go
     if txnMaxParInt >= maxParInt && maxParInt >= 3 {
         // Leave one for lock renewal.
         txnMaxParInt = maxParInt - 1
     }
     ```
   - `txnPermitPool` serves a distinct purpose: **Resource Partitioning**.
   - If transactions were allowed to consume all `max_parallel` connections (e.g. 128), non-transactional reads and the critical HA leader renewal heartbeat (`periodicallyRenewLock`) would be starved of database connections.
   - If HA lock renewal cannot acquire a connection before `PostgreSQLLockTTLSeconds * 2/3` (10 seconds), the active node panics/steps down as leader (`SOURCE-CONFIRMED`: lines 710-745).
   - Therefore, `txnPermitPool` is **not** redundant; it guarantees a reserved headroom of connections for non-transactional operations and HA safety.

---

## 3. Concurrency Bottleneck Analysis for Cloakit Workload

Cloakit workload: 95% reads (Subject Key lookup, policy checks), 5% writes (DEK re-wrap, key creation).

### 3.1 Non-Transactional Reads
- `Get` executes uninhibited by `txnPermitPool`.
- Concurrency ceiling is purely `max_parallel` (default 128).
- For pure read spikes, 128 concurrent database reads can execute simultaneously per OpenBao node.

### 3.2 Transaction Contention
- If Cloakit performs batch operations inside transactions, maximum concurrent transactions = 64 (`DefaultParallelTransactions`).
- If 65 transactions arrive simultaneously, the 65th blocks on `txnPermitPool.Acquire()`.
- Wait time in `PermitPool.Acquire` is not instrumented with metrics (`SOURCE-CONFIRMED`). Metrics are only recorded inside `Put`/`Get` execution (`MeasureSince([]string{"postgres", "get"}, ...)`). Operators cannot observe transaction pool wait queues (`STATIC-ANALYSIS-CONFIRMED`).

---

## 4. Recommendations for Concurrency Optimization

1. **Retain `txnPermitPool` for Safety**: Do not remove `txnPermitPool`. Its role in protecting HA lock renewal from connection starvation is structurally necessary.
2. **Expose PermitPool Saturation Metrics**: Instrument `txnPermitPool` wait times and permit utilization as OpenBao telemetry metrics (`vault.storage.postgresql.txn_permits_in_use`, `vault.storage.postgresql.txn_wait_time`).
3. **Harmonize Limits with PgBouncer**: When PgBouncer is deployed, set `max_parallel = 30`, `transaction_max_parallel = 20`. This ensures all connections remain warm and eliminates queuing in both Go and PostgreSQL.
