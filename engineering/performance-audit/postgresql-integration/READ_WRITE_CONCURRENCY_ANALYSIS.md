# Read and Write Concurrency Analysis 🤖

## Executive Summary
This report models the Cloakit 95:5 workload (95% concurrent Subject Key lookups, DEK wrapping/unwrapping; 5% Subject Key creations, rotations, and revocations) across OpenBao's PostgreSQL backend, analyzing row-level locking, table contention, connection wait states, and serialization behavior.

---

## 1. Concurrency Interaction Model (95:5 Workload)

### Concurrent Reads (`Get`)
- OpenBao issues `SELECT value FROM openbao_kv_store WHERE path = $1 AND key = $2`.
- In PostgreSQL MVCC, `SELECT` acquires an `AccessShareLock` on `openbao_kv_store`.
- `AccessShareLock` conflicts **only** with `AccessExclusiveLock` (e.g. `ALTER TABLE`, `DROP TABLE`, `VACUUM FULL`).
- Independent reads never block other concurrent reads. Hundreds of concurrent reads execute in parallel without lock contention. [SOURCE-CONFIRMED / STATIC-ANALYSIS-CONFIRMED]

### Concurrent Writes (`Put` / `Delete`)
- OpenBao issues `INSERT INTO openbao_kv_store ... ON CONFLICT (path, key) DO UPDATE ...` or `DELETE FROM openbao_kv_store WHERE path = $1 AND key = $2`.
- Writes acquire `RowExclusiveLock` on the table and an exclusive row-level lock on the target tuple.
- **Row-Level Independence**: Writes targeting different keys (e.g., rotating Subject Key A while rotating Subject Key B) execute fully concurrently without row lock contention!
- **Read/Write Independence**: In PostgreSQL, readers do not block writers, and writers do not block readers. A concurrent `Get` for Key A will simply read the committed MVCC snapshot of Key A while a `Put` for Key A is in flight. [STATIC-ANALYSIS-CONFIRMED]

---

## 2. Real Systemic Contention Points

If PostgreSQL locks are non-blocking between readers and writers, where does contention actually occur in OpenBao?

1. **Connection Pool Depletion**:
   - Both reads (cache misses) and writes acquire connections from the shared `sql.DB` pool (`max_parallel`).
   - If a sudden burst of writes or cache-miss reads exhausts the pool, all subsequent read requests stall waiting for an idle connection.
2. **HA Lock Fencing Contention**:
   - All writes query `openbao_ha_locks` via `validateFence`.
   - While `validateFence` is a `SELECT`, executing hundreds of fencing queries concurrently consumes connection slots and PostgreSQL CPU cycles.
3. **In-Memory Cache Mutex Contention**:
   - `sdk/physical/cache.go` uses 256 striped locks (`locksutil.LockForKey`).
   - For 10,000 concurrent requests across distinct keys, probability of hash collision on the same stripe lock is minimal (`1 / 256`), but concurrent writes to the *same* hot Subject Key serialize on that key's stripe lock. [SOURCE-CONFIRMED]
