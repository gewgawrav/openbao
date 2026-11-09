# PostgreSQL Server Tuning Guide 🤖

## Executive Summary
This guide provides precise, actionable PostgreSQL server parameter configurations tailored to OpenBao and Cloakit Privacy Vault's read-heavy (95:5) cryptographic workload on modern hardware (e.g., 8-16 vCPU, 32-64 GB RAM, NVMe SSD).

---

## 1. Coordinated Parameter Matrix

| Setting | PostgreSQL Default | Recommended for OpenBao / Cloakit | Controlled By | Safe to Change? | Workload Rationale & Impact |
|---|---|---|---|---|---|
| `max_connections` | 100 | 150 - 200 | PostgreSQL | Yes | Sized to accommodate 3 OpenBao instances (`3 * 30 conns = 90`) + pooler + admin. |
| `shared_buffers` | 128MB | 8GB - 16GB (25% of RAM) | PostgreSQL | Yes | Caches entire active Cloakit Subject Key working set in PostgreSQL memory. |
| `effective_cache_size` | 4GB | 24GB - 48GB (75% of RAM) | PostgreSQL | Yes | Informs query planner of available OS page cache. |
| `work_mem` | 4MB | 16MB | PostgreSQL | Yes | OpenBao queries are simple point lookups; 16MB is ample for listing sorts without memory waste. |
| `maintenance_work_mem` | 64MB | 1GB - 2GB | PostgreSQL | Yes | Accelerates autovacuum and index rebuilds. |
| `wal_level` | `replica` | `replica` | PostgreSQL | Yes | Required for standby read scalability and physical replication. |
| `wal_buffers` | -1 (auto 4MB) | 64MB | PostgreSQL | Yes | Prevents WAL buffer contention during bursts of key creations/rotations. |
| `wal_compression` | `off` | `lz4` | PostgreSQL | Yes | Compresses full page writes in WAL, cutting WAL I/O by 50% on NVMe. |
| `checkpoint_timeout` | 5min | 15min - 30min | PostgreSQL | Yes | Flattens checkpoint spikes under write bursts. |
| `max_wal_size` | 1GB | 16GB - 32GB | PostgreSQL | Yes | Prevents premature checkpoints caused by WAL volume. |
| `checkpoint_completion_target` | 0.9 | 0.9 | PostgreSQL | Yes | Spreads checkpoint I/O smoothly across the checkpoint window. |
| `synchronous_commit` | `on` | `on` | PostgreSQL | **Mandatory** | **NEVER set to off**. Essential for cryptographic key durability. |
| `fsync` | `on` | `on` | PostgreSQL | **Mandatory** | **NEVER disable**. Prevents database corruption on crash. |
| `full_page_writes` | `on` | `on` | PostgreSQL | **Mandatory** | **NEVER disable**. Protects against torn pages during OS crash. |
| `random_page_cost` | 4.0 | 1.1 | PostgreSQL | Yes | Accurate for SSD/NVMe storage; prevents planner from biasing toward sequential scans. |
| `effective_io_concurrency` | 1 | 200 | PostgreSQL | Yes | Maximizes concurrent read throughput on NVMe SSDs. |

---

## 2. Table-Specific Storage Settings

For the OpenBao storage table `openbao_kv_store`:
```sql
ALTER TABLE openbao_kv_store SET (
    fillfactor = 85,
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_vacuum_threshold = 250,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_analyze_threshold = 100
);
```

### Rationale
- `fillfactor = 85`: Reserves 15% free space in each 8KB heap page for in-place Heap-Only Tuple (HOT) updates during key rotations.
- Low vacuum scale factor: Ensures autovacuum triggers quickly as soon as 5% of tuples are replaced or deleted, preventing dead tuple accumulation.
