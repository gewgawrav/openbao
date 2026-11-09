# Max Parallel & Storage Concurrency Analysis 🤖

## Executive Summary
This report examines OpenBao's `max_parallel` parameter, the underlying channel-based concurrency mechanics, and how OpenBao synchronizes requests when interacting with PostgreSQL.

---

## 1. Concurrency Control Mechanisms in the Codebase

### Code Examination
In `internal/physical/postgresql/postgresql.go`:
```go
maxParStr, ok := conf["max_parallel"]
var maxParInt int
if ok {
    maxParInt, err = strconv.Atoi(maxParStr)
    ...
} else {
    maxParInt = physical.DefaultParallelOperations // 128
}

db.SetMaxOpenConns(maxParInt)
```
- There is **no `physical.PermitPool`** guarding `PostgreSQLBackend.Put()`, `Get()`, `Delete()`, or `List()`.
- Concurrency limiting for simple operations relies solely on Go's `database/sql.DB` connection pool mutex and wait queue (`db.connRequests`).

### Transaction Concurrency
In `internal/physical/postgresql/transaction.go`:
```go
func (b *PostgreSQLBackend) newTransaction(ctx context.Context, readOnly bool) (physical.Transaction, error) {
    b.txnPermitPool.Acquire()
    tx, err := b.client.BeginTx(ctx, &sql.TxOptions{
        Isolation: sql.LevelRepeatableRead,
        ReadOnly:  readOnly,
    })
    ...
}
```
`b.txnPermitPool` is a buffered channel semaphore (`chan struct{}` of capacity `txnMaxParInt`).
- If `txn_max_parallel` (default 64) is reached, any subsequent call to `BeginTx` or `BeginReadOnlyTx` blocks on channel send (`b.txnPermitPool.Acquire()`). [SOURCE-CONFIRMED]

---

## 2. Queueing Behavior Under High Concurrency

When 500 concurrent HTTP requests arrive at OpenBao:
1. **Reads (`Get`)**:
   - 2Q Cache Hit: Immediately resolved in-memory with RLock on stripped mutexes (`locksutil.LockForKey`). No database round trip, no connection acquired. [SOURCE-CONFIRMED]
   - 2Q Cache Miss: Enters `PostgreSQLBackend.Get()`. Calls `m.client.QueryRowContext()`.
   - `database/sql` attempts to acquire an idle connection. If 128 connections are active, the goroutine is queued on `db.connRequests` list.
   - Goroutine sleeps until a connection is returned.
2. **Writes (`Put` / `Delete`)**:
   - Acquires lock in `cache.locks`.
   - Calls `validateFence(ctx)`. Acquires connection 1 from pool to execute `haCheckLockHeldQuery`. Returns connection to pool.
   - Calls `m.client.ExecContext()` with `m.putQuery`. Acquires connection 2 from pool. Returns connection to pool.
   - Updates LRU cache and metric counters.

### Bottleneck Identification
- Because write operations execute two sequential SQL statements without holding a single connection across both, a write can succeed in `validateFence`, release the connection, and then get queued on `connRequests` waiting for a connection to execute `Put`.
- While waiting, the HA lock TTL (15s) ticks down. If pool starvation delays the second query, the write risks executing near or past the fencing deadline. [INFERRED / POTENTIAL BOTTLENECK]

---

## 3. Adaptive Concurrency vs Fixed Limits

OpenBao currently supports only static integer limits for `max_parallel`.
- Under sudden load spikes, fixed limits cause high latency percentiles (P99) due to linear FIFO queueing in `database/sql`.
- **Recommendation**: Implement Little's Law / Vegas-style adaptive concurrency limiter (e.g. Netflix concurrency-limits algorithm) at OpenBao's API boundary, rejecting or shedding excess load before it stalls the database connection pool.
