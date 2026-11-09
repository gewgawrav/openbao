# Distributed Scaling, Consistency and HA Replication Analysis 🤖

## 1. Executive Summary

This report establishes the distributed scaling architecture, replication topologies, cache invalidation mechanisms, and consistency models for OpenBao backed by PostgreSQL in Cloakit Privacy Vault deployments.

With Cloakit's workload consisting of **95% reads** (DEK decryption and Subject Key lookup) and **5% writes** (DEK wrapping, Subject Key creation and rotation), horizontal scaling of read capacity across multiple OpenBao instances while preserving strict cryptographic consistency is a primary requirement.

---

## 2. Distributed OpenBao & PostgreSQL Topology

```
                                  +-----------------------+
                                  | Client Application    |
                                  | (Cloakit Vault SDK)   |
                                  +-----------+-----------+
                                              |
                          HTTPS Reads & Writes| (L4/L7 Load Balancer)
                                              v
           +----------------------------------+----------------------------------+
           |                                  |                                  |
           v                                  v                                  v
+-----------------------+          +-----------------------+          +-----------------------+
|   OpenBao Instance 1  |          |   OpenBao Instance 2  |          |   OpenBao Instance 3  |
|      (HA Leader)      |          |     (Read Standby)    |          |     (Read Standby)    |
| - Local Policy Cache  |          | - Local Policy Cache  |          | - Local Policy Cache  |
| - HA Lock Holder      |          | - gRPC Invalidation   |          | - gRPC Invalidation   |
+-----------+-----------+          +-----------+-----------+          +-----------+-----------+
            |                                  |                                  |
            | Write / Read                     | Standby Read                     | Standby Read
            v                                  v                                  v
+-----------------------+          +-----------------------+          +-----------------------+
|  PostgreSQL Primary   | ==(WAL)==>  PostgreSQL Replica 1 | ==(WAL)==>  PostgreSQL Replica 2 |
|   (Read/Write RW)     | (Streaming)   (Read-Only RO)     | (Streaming)   (Read-Only RO)     |
+-----------------------+          +-----------------------+          +-----------------------+
```

---

## 3. Standby Read Routing & Replication Index Mechanics

### Standby Reads Activation
- In `internal/vault/ha.go:1718-1736`, OpenBao verifies if standby reads are allowed:
  ```go
  func (c *Core) StandbyReadsEnabled() bool {
      return c.standbyReadsAllowed() && !c.activeContext.Load().IsNil() &&
          (isCacheInvalidationBackend(c.underlyingPhysical) ||
              isReplicationIndexBackend(c.underlyingPhysical))
  }
  ```
- **`SOURCE-CONFIRMED`**: `PostgreSQLBackend` implements `physical.ReplicationIndexBackend` (`internal/physical/postgresql/index.go:15`).
- Standby read instances check their WAL LSN against the leader via `AppliedReplicationIndex`:
  ```sql
  SELECT CASE pg_is_in_recovery()
  WHEN true THEN pg_last_wal_replay_lsn()
  ELSE pg_current_wal_lsn()
  END AS index;
  ```
- When a write occurs on the leader, the leader records the resulting WAL LSN.
- Standby OpenBao instances querying secondary PostgreSQL read replicas must wait until `secondary.AppliedReplicationIndex >= leader.WriteIndex` before serving reads that require read-after-write consistency.

---

## 4. Cache Invalidation & Stale Read Windows

### The Stale Read Hazard:
1. Client issues `POST /transit/keys/subject-123/rotate` to OpenBao Leader.
2. OpenBao Leader writes new key version to PostgreSQL Primary. WAL streaming replicates this to PostgreSQL Replica 1 with lag $\Delta t_{repl} \approx 10 - 200\,\text{ms}$.
3. Client immediately issues `POST /transit/decrypt/subject-123` to OpenBao Standby (Instance 2).
4. **Cache Invalidation Latency**:
   - Instance 2 already has `subject-123` loaded in its local `LockManager` cache (`sdk/helper/keysutil/lock_manager.go:305`).
   - If cache invalidation relies on polling or periodic replication index checks, Instance 2 decrypts the request using the old key version or errors out if ciphertext refers to the new version.
5. **Mitigation**:
   - OpenBao uses gRPC-based cache invalidation fan-out (`shouldUseGRPCInvalidation(phys)` in `internal/vault/ha.go:1700`).
   - Upon completing any write/rotation, the leader issues gRPC `InvalidateKey` messages to all registered standby nodes, forcing `lm.InvalidatePolicy(name)` before releasing the client write response.

---

## 5. Failover, Split-Brain & Leader Transition Dynamics

### PostgreSQL HA Lock Heartbeat Lifecycle:
- OpenBao uses table `openbao_ha_locks` with a 15-second TTL (`PostgreSQLLockTTLSeconds = 15`) and renews every 5 seconds (`PostgreSQLLockRenewInterval = 5 * time.Second`) (`internal/physical/postgresql/postgresql.go:32-40`).
- **Failover Sequence**:
  1. Primary OpenBao node crashes or suffers network partition.
  2. Lock remains held in `openbao_ha_locks` until `NOW() > valid_until` (up to 15 seconds).
  3. Standby OpenBao nodes poll `haUpsertLockIdentityExec` every 1 second:
     ```sql
     UPDATE SET (ha_identity, ha_key, ha_value, valid_until) = ...
     WHERE (t.valid_until < NOW() AND t.ha_key = $2)
     ```
  4. The first standby to succeed becomes the active leader and triggers unseal/barrier setup.
- **Failover Downtime**: Write operations are blocked during the 15-second failover window. Reads on standby nodes continue uninterrupted if local caches are warm and standby reads are enabled.

---

## 6. Scaling Efficiency & Contention Modeling

When scaling OpenBao instances from 1 to $N$:
1. **Cache Hit Scalability (Near-Linear)**:
   - For warm-cache DEK unwrapping operations, requests execute purely in Go memory without touching PostgreSQL. Throughput scales linearly ($O(N)$) up to the load balancer and network interface capacity.
2. **Cold-Read & Write Contention (Sub-Linear)**:
   - Cold reads and writes route to PostgreSQL.
   - At $N$ instances, aggregate connection pool size equals $N \times \text{max\_parallel}$.
   - For $N=10$ and `max_parallel=128`, PostgreSQL must manage 1,280 active connections. Without an intermediate connection pooler (e.g., PgBouncer in transaction pooling mode), PostgreSQL backend process scheduling and lock contention degrade query throughput.
