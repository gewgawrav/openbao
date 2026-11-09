# PostgreSQL Server Configuration & Tuning Guide 🤖

## Executive Summary

This guide details the optimal PostgreSQL server parameters for OpenBao's physical backend supporting the Cloakit Privacy Vault workload (95% read / 5% write). We analyze memory allocations, connection scaling, WAL parameters, autovacuum aggressiveness, and query planner cost models specifically calibrated for OpenBao's access patterns.

Strict Durability Mandate: Under no circumstances should `fsync`, `full_page_writes`, or `synchronous_commit` be disabled in production. Security and durability guarantees must remain inviolate.

Evidence classifications: `SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`.

---

## 1. Memory Subsystem Sizing

### 1.1 `shared_buffers`
- **Default**: 128 MB (PostgreSQL vanilla default).
- **OpenBao Workload Context**: Cloakit reads Subject Keys repeatedly. The total working set of active Subject Keys and metadata fits well within modern RAM.
- **Recommendation**: Set to **25%–40% of total system RAM** (e.g., 8 GB to 16 GB on a 32–64 GB database host).
- **Impact**: Keeps 100% of `openbao_kv_store` heap and index pages in PostgreSQL shared memory, achieving sub-100 microsecond read latencies without hitting Linux page cache or disk.

### 1.2 `effective_cache_size`
- **Default**: 4 GB.
- **Recommendation**: Set to **75% of total system RAM**.
- **Impact**: Informs the PostgreSQL cost planner that index scans on `openbao_kv_store_pkey` will almost certainly reside in memory, encouraging index-only and bitmap index paths.

### 1.3 `work_mem`
- **Default**: 4 MB.
- **OpenBao Workload Context**: OpenBao storage queries do not perform multi-table joins or large analytical sorts (`SOURCE-CONFIRMED`). Only `List` and `ListPage` queries execute `ORDER BY key`.
- **Recommendation**: Set to **16 MB**.
- **Safety**: Generous enough to ensure `ORDER BY key` on directory listings completes entirely in memory (using fast quicksort/radixsort) without spilling temporary work files to disk.

---

## 2. WAL and Durability Configuration

| Parameter | Recommended Production Value | Default Value | Controller | Justification for OpenBao Workload |
|---|---|---|---|---|
| `wal_level` | `replica` | `replica` | PostgreSQL | Required for physical replication and point-in-time recovery. Minimal overhead. |
| `synchronous_commit` | `on` | `on` | PostgreSQL | **Mandatory**. Ensures every Subject Key rotation and DEK write is durably flushed before returning success. |
| `fsync` | `on` | `on` | PostgreSQL | **Mandatory**. Prevents database corruption upon sudden OS or power failure. |
| `full_page_writes` | `on` | `on` | PostgreSQL | **Mandatory**. Protects against torn pages during partial power loss. |
| `wal_buffers` | `64MB` | `-1` (auto 16MB) | PostgreSQL | Accommodates bursts of envelope writes without WAL buffer lock contention. |
| `wal_compression` | `zstd` (or `lz4`) | `off` | PostgreSQL | Drastically compresses full-page images in WAL, reducing disk write IOPS by 40–60%. |
| `checkpoint_timeout` | `15min` (or `30min`) | `5min` | PostgreSQL | Smooths checkpoint I/O spikes by spreading writes over longer intervals. |
| `max_wal_size` | `16GB` to `32GB` | `1GB` | PostgreSQL | Prevents aggressive, premature checkpoints driven by WAL volume during write bursts. |
| `checkpoint_completion_target`| `0.9` | `0.9` | PostgreSQL | Spreads checkpoint writes across 90% of the checkpoint window, preventing I/O throttling. |

---

## 3. Query Planner & Cost Calibration

OpenBao runs primarily on enterprise cloud NVMe SSD storage (e.g., AWS EBS `gp3`/`io2`, GCP Hyperdisk, Azure Premium SSD v2). PostgreSQL's default planner costs assume spinning magnetic hard drives.

### Cost Tuning Specifications:
```ini
# Calibrated for modern NVMe SSDs
random_page_cost = 1.1         # Default is 4.0 (assumes slow rotational seeks)
seq_page_cost = 1.0            # Default is 1.0
effective_io_concurrency = 200 # Default is 1 (allows asynchronous prefetching on NVMe)
default_statistics_target = 100 # Adequate for high-selectivity point lookups
```
- **Operational Effect**: Setting `random_page_cost = 1.1` prevents the planner from mistakenly choosing slow sequential table scans over index scans on large `openbao_kv_store` tables (`INFERRED`).

---

## 4. Connection Configuration & Proxy Integration

```ini
max_connections = 200                    # Bounded to protect CPU context switching
superuser_reserved_connections = 5      # Ensures DBA access during connection spikes
```
- For multi-instance OpenBao clusters (e.g., 5 to 10 nodes), deploy **PgBouncer** directly co-located or fronting PostgreSQL:
  ```ini
  # pgbouncer.ini
  pool_mode = transaction
  max_client_conn = 2000
  default_pool_size = 50
  reserve_pool_size = 10
  server_idle_timeout = 600
  ```

---

## 5. Comprehensive Parameter Impact Matrix

| Parameter Group | Parameter Name | OpenBao Impact Level | Recommended Measurement Command |
|---|---|---|---|
| Memory | `shared_buffers` | Critical | `SELECT heap_blks_read, heap_blks_hit FROM pg_statio_user_tables WHERE relname = 'openbao_kv_store';` |
| WAL | `checkpoint_timeout` | High | `SELECT * FROM pg_stat_checkpoints;` (or `pg_stat_bgwriter` in PG <17) |
| I/O | `effective_io_concurrency` | Medium | Storage IOPS queue depth monitoring via OS `iostat -xz 1` |
| Connections | `max_connections` | Critical | `SELECT count(*), state FROM pg_stat_activity GROUP BY state;` |

---

## 6. Audit Verdict

PostgreSQL default parameters are severely unoptimized for OpenBao's workload (e.g., 128MB shared buffers, 1GB max WAL, 4.0 random page cost). Implementing this coordinated tuning guide maximizes memory residency for Subject Keys, smooths WAL checkpoint I/O, and prevents connection exhaustion.

---

## 7. Storage Engine OS and Kernel Tuning Specifications

To maximize PostgreSQL throughput beneath OpenBao on Linux host instances:

```ini
# /etc/sysctl.d/99-postgresql.conf
vm.dirty_background_ratio = 5       # Start flushing dirty pages early to avoid I/O stalls
vm.dirty_ratio = 10                  # Prevent write queues from monopolizing page cache
vm.swappiness = 1                    # Avoid swapping PostgreSQL shared buffers into swap
vm.overcommit_memory = 2             # Protect PostgreSQL from Linux OOM-killer termination
vm.overcommit_ratio = 80             # Allow safe allocations up to 80% RAM + swap
```

### Filesystem Mount Options:
For ext4 / XFS block devices housing PostgreSQL `base` and `pg_wal`:
```bash
mount -o noatime,nodiratime,barrier=1,data=ordered /dev/nvme0n1 /var/lib/postgresql/data
```
- `noatime`: Disables filesystem metadata updates on read queries, eliminating write amplification on `Get` paths.
- `barrier=1`: Enforces strict block write barriers, ensuring battery-backed NVMe write caches flush durably on `fsync`.

---

## 8. Replication Latency and Standby Read Coordination

When OpenBao is configured with PostgreSQL physical streaming replication:
1. **Replication Protocol**: Physical streaming replication using WAL sender and receiver processes (`wal_level = replica`, `max_wal_senders = 10`).
2. **Synchronous Standbys**:
   ```ini
   synchronous_commit = on
   synchronous_standby_names = 'FIRST 1 (bao_pg_replica_1, bao_pg_replica_2)'
   ```
   Ensures zero data loss (RPO = 0) by requiring at least one replica to acknowledge WAL receipt before a Subject Key rotation returns success.
3. **Standby Query Conflict Tuning**:
   ```ini
   hot_standby = on
   max_standby_streaming_delay = 30s
   hot_standby_feedback = on
   ```
   Prevents vacuum cleanup on the primary from canceling long-running read transactions on read replicas.
