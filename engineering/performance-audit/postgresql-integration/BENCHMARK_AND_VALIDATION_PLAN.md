# Benchmark and Validation Plan 🤖

## Executive Summary
This document specifies a rigorous, reproducible benchmark plan to empirically validate the OpenBao–PostgreSQL optimization recommendations under realistic Cloakit workloads without modifying production environments or fabricating numbers.

---

## 1. Test Matrix & Variables

### Workload Ratios
- **Cloakit Target**: 95% reads / 5% writes
- **Stress Read**: 99% reads / 1% writes
- **Stress Write**: 90% reads / 10% writes

### Concurrency Levels
- `C = [1, 10, 50, 100, 250, 500, 1000]` concurrent clients.

### Connection Pool & `max_parallel` Settings
- Current default: `max_parallel = 128`, `max_idle_connections = unset (2)`
- Test configurations:
  - Config A: `max_parallel = 10, max_idle = 10`
  - Config B: `max_parallel = 30, max_idle = 30`
  - Config C: `max_parallel = 50, max_idle = 50`
  - Config D: `max_parallel = 128, max_idle = 128`

---

## 2. Experimental Comparison Groups

| Test Group | OpenBao Configuration | Database / Proxy Setup | Expected Outcome |
|---|---|---|---|
| Baseline | `max_parallel=128`, `max_idle=unset` | Direct PostgreSQL | High connection thrashing; P99 latency spikes at C >= 100. |
| Test 1 | `max_parallel=30`, `max_idle=30` | Direct PostgreSQL | Stable connection pool; P99 latency reduced by 40%. |
| Test 2 | Test 1 + In-Memory Fencing Cache | Direct PostgreSQL | Write throughput doubles; write latency cut in half. |
| Test 3 | Test 1 + HOT Upsert Query | Direct PostgreSQL | WAL volume reduced by 60%; 0 index bloat on key rotation. |
| Test 4 | Test 1 + PgBouncer (Transaction mode) | OpenBao -> PgBouncer -> PG | PostgreSQL backend connection count drops to < 20; high concurrency stability. |

---

## 3. Metrics Collection Harness

During test execution, monitor and record:
1. **OpenBao**: Throughput (ops/sec), Latency (P50, P95, P99), Goroutine count, GC pause duration, `db.Stats().WaitDuration`.
2. **PostgreSQL**: CPU utilization (`pg_stat_activity`), Shared buffer hit ratio (`pg_statio_user_tables`), Dead tuple generation (`n_dead_tup`), WAL generation rate (`pg_wal_lsn_diff`), Checkpoint count (`pg_stat_bgwriter`).
3. **Reproducibility**: Run via official `github.com/openbao/benchmark-openbao` tooling.
