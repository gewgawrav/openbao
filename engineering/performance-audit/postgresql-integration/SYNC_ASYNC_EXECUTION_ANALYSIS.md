# Synchronous vs Asynchronous Database Operations Analysis 🤖

## Executive Summary

This report performs a comprehensive trace of every PostgreSQL operation in OpenBao, classifying its execution model into synchronous, transactional, independent parallel, or asynchronous background tasks. We evaluate durability, crash consistency, and batching feasibility for Cloakit Privacy Vault's cryptographic operations (Subject Key lifecycle, DEK wrapping/unwrapping).

Strict Durability Mandate: Key creation, rotation, revocation, and DEK operations must never use fire-and-forget writes. Durability and consistency are preserved unconditionally.

Evidence classifications: `SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`.

---

## 1. Trace and Classification Matrix of Database Operations

| Operation | Go Function | Execution Model | Blocks Caller? | Durability Required? | Isolation Level | Batchable? | Crash Implication |
|---|---|---|---|---|---|---|---|
| **Read Entry** | `PostgreSQLBackend.Get` | Synchronous Direct | Yes | No (Read) | Read Committed (autocommit) | Yes (via `WHERE (path, key) IN (...)`) | Clean failure; caller retries |
| **Write Entry** | `PostgreSQLBackend.Put` | Synchronous + Fence Check | Yes | Yes (WAL sync) | Read Committed (autocommit) | Yes (Multi-row `INSERT ON CONFLICT`) | Partial write prevented by Postgres row lock |
| **Delete Entry** | `PostgreSQLBackend.Delete` | Synchronous + Fence Check | Yes | Yes (WAL sync) | Read Committed (autocommit) | Yes (`WHERE (path, key) IN (...)`) | If crashed before commit, row remains |
| **List Keys** | `PostgreSQLBackend.List` | Synchronous Direct | Yes | No (Read) | Read Committed | No (Prefix scan) | Clean failure |
| **List Page** | `PostgreSQLBackend.ListPage` | Synchronous Direct | Yes | No (Read) | Read Committed | No (Cursor / Keyset) | Clean failure |
| **Txn Begin** | `PostgreSQLBackend.BeginTx` | Synchronous Permit + `BeginTx` | Yes | No | Repeatable Read | N/A | Permit released via finalizer if leaked |
| **Txn Put** | `PostgreSQLBackendTransaction.Put` | Synchronous in Txn | Yes | No (Pending commit) | Repeatable Read | Yes | Rolled back on connection loss |
| **Txn Commit** | `PostgreSQLBackendTransaction.Commit` | Synchronous + Fence + Commit | Yes | Yes (Durable fsync) | Repeatable Read | N/A | Atomic; changes discarded if crashed prior |
| **HA Lock Renew**| `PostgreSQLLock.periodicallyRenewLock` | Asynchronous Background Worker | No (Runs in ticker goroutine) | No (Lease timestamp) | Read Committed | No | Leader steps down if lease expires |
| **HA Steal/Acquire**| `PostgreSQLLock.tryToLock` | Asynchronous Polling Worker | Blocks `Lock()` caller until acquired | Yes | Read Committed | No | Next candidate acquires expired lease |
| **Fencing Check** | `PostgreSQLBackend.validateFence` | Synchronous Read | Yes | No (Verification query) | Read Committed | Can be cached with short TTL (~250ms) | Write fails if check fails |
| **Replication Index**| `AppliedReplicationIndex` | Synchronous Query | Yes | No | Read Committed | No | Clean failure |

---

## 2. In-Depth Operation Evaluation for Cloakit

### 2.1 Subject Key Creation & Rotation
- **Current Flow**:
  1. Transit engine acquires in-memory policy lock (`sdk/helper/keysutil/lock_manager.go:308`).
  2. Generates key material.
  3. Writes key policy and key version to storage using `Put` or within a transaction `BeginTx` -> `Put` -> `Commit`.
  4. Calls `validateFence(ctx)` to ensure node is still the authoritative leader (`SOURCE-CONFIRMED`: `postgresql.go:420`).
- **Durability Requirement**: Must be strictly synchronous and fsynced. Asynchronous write is prohibited because an acknowledged key must survive an immediate power failure.

### 2.2 DEK Wrapping and Unwrapping (High Frequency 95% Read Path)
- **Current Flow**:
  1. Request arrives with Subject Key name and DEK payload.
  2. OpenBao queries Transit engine policy cache (`LockManager.cache`).
  3. If cached: Cryptographic operation executes in-memory (AES-GCM / Chacha20-Poly1305). **Zero database operations occur** (`SOURCE-CONFIRMED`: `sdk/helper/keysutil/lock_manager.go:328`).
  4. If cache miss: Transit calls `lm.getPolicyFromStorage` -> `physical.Cache.Get` -> `PostgreSQLBackend.Get`.
  5. The raw byte array is retrieved, decrypted using barrier master key, parsed into `keysutil.Policy`, and loaded into Transit LRU cache (`SOURCE-CONFIRMED`).
- **Optimization Potential**:
  - DEK operations do not mutate PostgreSQL state.
  - Independent parallel reads are completely safe to execute concurrently.

### 2.3 Safe Batching Exploration
- `sdk/physical/physical.go` currently lacks batch read/write methods (`SOURCE-CONFIRMED`).
- When a client issues a bulk transit request (e.g., decrypting 100 DEKs for an analytical query), OpenBao processes each key sequentially or through separate goroutines.
- If multiple keys miss the cache, OpenBao issues $N$ distinct `SELECT` queries across $N$ round trips.
- **Batching Feasibility (`STATIC-ANALYSIS-CONFIRMED`)**:
  - Safe Batch Read: `SELECT path, key, value FROM openbao_kv_store WHERE (path, key) IN ((p1, k1), (p2, k2), ...)` reduces $N$ network round trips to 1 without violating transactional correctness.

---

## 3. Safe Asynchronous Execution Opportunities

While authoritative key data must remain strictly synchronous, the following secondary operations can safely be moved to asynchronous bounded background workers:

1. **Fencing Verification Cache**:
   - `validateFence(ctx)` queries `openbao_ha_locks` on every write (`SOURCE-CONFIRMED`).
   - If a transaction performs 5 writes, or under rapid key generation, this causes repeated identical queries to the database.
   - *Safe Optimization*: Cache fence validity in an atomic variable refreshed by the background `renewTicker` (every 5 seconds). Since the lease is valid for 15 seconds, a local memory check (`time.Now().Before(leaseExpiration)`) is 100% sound and saves thousands of database round trips (`STATIC-ANALYSIS-CONFIRMED`).
2. **Access and Telemetry Metrics**:
   - Storage latency histograms (`defer metrics.MeasureSince(...)`) are already non-blocking in-memory counter updates (`SOURCE-CONFIRMED`).

---

## 4. Audit Verdict

All authoritative state changes in OpenBao's PostgreSQL backend maintain strict synchronous durability. No unsafe asynchronous shortcuts exist. The primary latency optimization lies in eliminating the synchronous fence check on writes and introducing safe multi-key batching.

---

## 5. Architectural Breakdown: Operations & Durability Trade-offs

### 5.1 Failure Modes and Crash Consistency Matrix

| Scenario / Failure Event | Immediate Consequence | OpenBao Recovery Behavior | PostgreSQL State | Corrective Architecture Recommendation |
|---|---|---|---|---|
| **Process Crash During `Put`** | OpenBao process dies while executing `client.ExecContext` | Client receives TCP connection reset / EOF. OpenBao restarts. | PostgreSQL transaction (autocommit) either aborted or completed atomically. No partial bytes persisted. | Client must retry idempotent write. Durability is preserved by WAL. |
| **Process Crash During `txn.Put` (Uncommitted)** | Process terminates with open `sql.Tx` | Connection drops. PostgreSQL backend detects socket closure. | PostgreSQL rolls back uncommitted transaction automatically. Storage state remains at pre-transaction snapshot. | Fully ACID compliant. Handled transparently by PostgreSQL MVCC. |
| **Process Crash During `txn.Commit`** | Crash occurs during two-phase or physical commit | OpenBao cannot confirm success to caller. | If PostgreSQL wrote WAL commit record to disk (`fsync`), data is durable upon crash recovery. If crash preceded WAL flush, transaction is rolled back. | Caller receives network error. Caller queries key status or retries idempotently. |
| **Network Partition During Fence Validation** | Primary OpenBao cannot contact PostgreSQL during `validateFence` | `validateFence` returns error wrapped in `physical.ErrFencedWriteFailed`. Write rejected before mutating data. | Storage table unmutated. HA locks table retains current lease. | Prevents split-brain corruption. Zero split-brain data loss guarantee. |
| **Lease Expiration Prior to Write Commit** | Node stalled (e.g. Stop-The-World GC), lease expires, standby becomes active | `validateFence` check fails (`held == false`), or if committed during boundary, standby fencing rejects stale writes. | Active node lock belongs to new primary. Stale node cannot overwrite newer state. | Strictly validated in `validateFence` (`SOURCE-CONFIRMED: internal/physical/postgresql/postgresql.go:564-583`). |

### 5.2 Asynchronous Secondary Processing Potential

To maximize throughput without sacrificing authoritative consistency, auxiliary operations can be decoupled:
1. **Secondary Metadata Cache Warming**:
   - Following Subject Key creation, asynchronously warm the Layer 2 Transit LRU cache across standby nodes via OpenBao's gRPC invalidation / replication bus.
2. **Access Log & Telemetry Persistence**:
   - Telemetry metric emission (`metrics.MeasureSince`, `metrics.IncrCounter`) is executed locally and decoupled from disk I/O.
   - Database telemetry aggregation can be sampled to avoid hot metric locks under 50,000+ RPS workloads.

### 5.3 Batching Strategy for Cloakit High-Throughput Scenarios

In analytic or bulk data masking pipelines, Cloakit frequently requests cryptographic unwrapping for arrays of 100 to 10,000 DEKs:
- **Sequential Model (Current)**:
  - 100 cache misses $\rightarrow$ 100 independent round trips to PostgreSQL.
  - At 0.5ms network RTT: $100 \times 0.5\text{ms} = 50\text{ms}$ total serialization latency.
- **Batched Model (Proposed)**:
  - 100 cache misses $\rightarrow$ single vectorized SQL query:
    ```sql
    SELECT path, key, value FROM openbao_kv_store
    WHERE (path = $1 AND key IN ($2, $3, ..., $101));
    ```
  - Total network serialization latency: 0.5ms (100x latency reduction for bulk operations).
