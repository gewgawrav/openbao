# Final PostgreSQL Optimisation Roadmap & Architecture Decision 🤖

## Executive Summary
This report synthesizes the complete findings of the OpenBao–PostgreSQL technical audit, provides authoritative answers to all 15 audit questions, categorizes every initial hypothesis, and presents a phased, prioritized implementation roadmap to achieve optimal performance for the Cloakit Privacy Vault workload (95% reads, 5% writes).

---

## 1. Categorization of Primary Hypotheses

| Suspected Limitation | Audit Classification | Technical Summary |
|---|---|---|
| Conservative / Incorrect Connection Pools | **CONFIRMED CONFIGURATION GAP** | `max_idle_connections` defaults to 2 (Go default), causing connection destruction and reconnection storms; `SetConnMaxLifetime` and `SetConnMaxIdleTime` are missing entirely. |
| Redundant Concurrency Controls | **PARTIALLY CONFIRMED** | Non-transactional `PermitPool` was already removed (relying on `sql.DB`), but `txnPermitPool` throttles transactional operations. |
| Inefficient Synchronous Execution | **CONFIRMED BOTTLENECK** | Active leader fencing (`validateFence`) synchronously queries `openbao_ha_locks` on every write/commit, doubling write round trips. |
| Missing Safe Batching | **CONFIRMED BOTTLENECK** | `physical.Backend` lacks batch `GetBatch` and `PutBatch` methods, requiring sequential round trips for multi-key lookups. |
| Inefficient Transaction Boundaries | **NOT A BOTTLENECK** | Repeatable Read isolation is correctly scoped; read-only transactions abort cleanly without WAL writes; leak detection is enforced via Go 1.24 finalizers. |
| Excessive Database Round Trips | **CONFIRMED BOTTLENECK** | Writes incur 2 round trips (fencing + DML); multi-key reads incur N round trips. |
| Poor Connection Lifecycle Management | **CONFIRMED CONFIGURATION GAP** | Unlimited connection lifetimes risk silent disconnection behind cloud NLBs and firewalls. |
| Missing Query Optimisation | **CONFIRMED BOTTLENECK** | `putQuery` updates non-modified columns (`parent_path`), disabling Heap-Only Tuple (HOT) updates and causing index write amplification. |
| Insufficient Database Observability | **CONFIRMED CONFIGURATION GAP** | `db.Stats()` (open, in-use, idle connections, wait duration) is not exported to OpenBao telemetry. |
| Lack of Coordination in Tuning | **CONFIRMED CONFIGURATION GAP** | OpenBao defaults (`max_parallel=128`) easily overwhelm PostgreSQL default `max_connections=100` when running multiple nodes. |
| Inefficient Read Caching | **ALREADY OPTIMISED (CORE)** | 2Q in-memory cache (128k entries) absorbs 95% of reads without database hits; transactional reads deliberately clone fresh caches. |
| PostgreSQL Maintenance Alignment | **NOT CONTROLLED BY OPENBAO** | Managed via PostgreSQL server settings; requires table-level autovacuum and fillfactor tuning. |

---

## 2. Answers to the 15 Final Questions

1. **Is OpenBao's PostgreSQL backend efficiently implemented?**
   - *Verdict*: Well-structured and robust, but hindered by missing connection lifecycle controls, non-HOT upserts, and synchronous fencing queries.
2. **Are connection pools configured correctly?**
   - *Verdict*: No. Default idle connection pool size (2) causes continuous connection thrashing. Lifetime limits are missing.
3. **Is `max_parallel` limiting throughput?**
   - *Verdict*: No, default 128 is actually overly aggressive for multi-node deployments without a pooler. Sizing down to 30 per node with equal idle connections yields superior throughput and predictable latency.
4. **Is redundant PermitPool and database pool synchronisation present?**
   - *Verdict*: General PermitPool was removed; only `txnPermitPool` remains to protect HA renewal slots.
5. **Are idle and open connections configured appropriately?**
   - *Verdict*: No. `max_idle_connections` should be set equal to `max_parallel`.
6. **Are transactions unnecessarily long or frequent?**
   - *Verdict*: No. OpenBao keeps transactions short and rolls back read-only transactions without WAL writes.
7. **Which operations can safely execute concurrently?**
   - *Verdict*: All independent point reads (`Get`) and writes to different keys (`Put`/`Delete`).
8. **Which operations can be batched?**
   - *Verdict*: Bulk Subject Key lookups, replication index queries, and telemetry flushing.
9. **Which operations must remain synchronous and durable?**
   - *Verdict*: Authoritative key creation, key rotation, key revocation, and transaction commits.
10. **Is PostgreSQL vacuuming and WAL management appropriate for the actual write pattern?**
    - *Verdict*: Default vacuuming is too slow for `openbao_ha_locks` (updated every 5s). Requires custom table autovacuum tuning.
11. **Is the existing cache effective for Subject Key reads?**
    - *Verdict*: Highly effective. Absorbs 95%+ of reads in-memory with sub-millisecond latency.
12. **Can OpenBao and PostgreSQL coordinate their resource limits more intelligently?**
    - *Verdict*: Yes, through unified connection sizing formulas, pool telemetry export, and coordinated timeout settings.
13. **Would PgBouncer help or add unnecessary overhead?**
    - *Verdict*: In transaction pooling mode, PgBouncer significantly helps large multi-node clusters by consolidating hundreds of OpenBao connections into 20-30 server backends.
14. **Which optimisations belong in OpenBao core versus PostgreSQL configuration versus Cloakit?**
    - *OpenBao Core*: Connection pool lifecycle parameters, HOT query optimization, fencing cache, batch physical API.
    - *PostgreSQL Configuration*: Memory buffers, WAL compression, autovacuum thresholds, fillfactor.
    - *Cloakit*: Warm Subject Key caching and batch retrieval requests.
15. **What is the optimal architecture for our 95:5 workload?**
    - *Verdict*: Architecture B + C + D: OpenBao with aligned connection pools (30 open / 30 idle), HOT-optimized upsert, 1s in-memory fencing cache, 2Q barrier cache, and coordinated PostgreSQL server tuning.

---

## 3. Prioritised Improvement Roadmap

### Phase 1: Immediate Configuration Actions (No Code Changes)
1. **OpenBao Config**:
   - Set `max_parallel = 30`
   - Set `max_idle_connections = 30`
2. **PostgreSQL Server Tuning**:
   - Set `shared_buffers = 25% RAM`, `effective_cache_size = 75% RAM`
   - Set `wal_compression = lz4`, `checkpoint_timeout = 15min`, `checkpoint_completion_target = 0.9`
   - Enforce table-specific parameters on `openbao_kv_store` (`fillfactor = 85`) and `openbao_ha_locks` (`autovacuum_vacuum_threshold = 20`).

### Phase 2: OpenBao Driver & Query Optimizations (Low Risk Code Changes)
1. Expose `max_connection_lifetime` and `max_connection_idle_time` in `internal/physical/postgresql/postgresql.go`.
2. Refactor `putQuery` to `UPDATE SET value = EXCLUDED.value` to enable Heap-Only Tuple (HOT) updates.
3. Introduce 1-second in-memory fencing lease validation to eliminate 50% of write-path round trips.

### Phase 3: Advanced Architectural Enhancements
1. Add `GetBatch(ctx, []string)` to `physical.Backend` and `PostgreSQLBackend`.
2. Export Go `db.Stats()` to OpenBao telemetry sink.
3. Deploy PgBouncer in transaction pooling mode for deployments exceeding 5 OpenBao nodes.
