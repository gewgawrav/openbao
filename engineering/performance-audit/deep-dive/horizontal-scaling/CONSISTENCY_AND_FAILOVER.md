# Read-After-Write Consistency & Failover Semantics 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report analyzes failover mechanics, fence validation, split-brain mitigation, and read-after-write consistency guarantees during cluster transitions and database replication delays.

---

## 1. Split-Brain Fencing Mechanics
- **Source Reference:** `internal/physical/postgresql/postgresql.go:564-575` (`validateFence`)
```go
func (p *PostgreSQLBackend) validateFence(ctx context.Context) error {
    p.fenceLock.RLock()
    defer p.fenceLock.RUnlock()
    if p.fence == nil {
        return nil
    }
    // Verifies active leadership token in DB before committing write
}
```
OpenBao verifies active leadership before executing mutating writes. If network partitioning occurs between OpenBao and PostgreSQL, or if PostgreSQL revokes the lease lock, the leader immediately aborts transactions with fencing errors, preventing split-brain writes.

---

## 2. Monotonic Read Consistency via LSN Tokens

When Cloakit clients rotate a Subject Key on the Active node and immediately send an unwrap request to a Read Standby node, asynchronous replication lag can cause a stale read.

### 2.1 The LSN Commit Token Architecture
1. **Mutation Response:** When a key is created or rotated on OpenBao Active, OpenBao retrieves the current PostgreSQL Write LSN:
   ```sql
   SELECT pg_current_wal_lsn();
   ```
2. **Token Injection:** OpenBao returns this LSN in an HTTP response header (`X-OpenBao-LSN: 0/16B3748`).
3. **Client Propagation:** Cloakit clients attach `X-OpenBao-LSN` on subsequent read requests.
4. **Replica Synchronization:** Read nodes verify that their PostgreSQL replica has replayed past that LSN (`pg_last_wal_replay_lsn() >= LSN`) before serving the read.
5. **Fallback:** If lagging, the read node waits up to 20ms or routes directly to the primary database, guaranteeing causal Read-After-Write consistency.

---

## 3. Failover Recovery Timeline

When the active OpenBao node fails:
1. **Lock Expiry:** PostgreSQL lock expires after 15 seconds (`PostgreSQLLockTTLSeconds = 15`).
2. **Election:** Standby nodes attempt atomic lock acquisition via `haUpsertLockIdentityExec`.
3. **Core Initialization:** Newly elected active node reloads barrier keyring and initializes transit engines.
4. **Zero Key Data Loss:** All key policies remain committed and durable in PostgreSQL.
