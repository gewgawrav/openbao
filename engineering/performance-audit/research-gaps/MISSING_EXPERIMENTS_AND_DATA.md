# Missing Experiments and Empirical Data 🤖

## 1. Executive Summary

This report defines the specific experimental test matrix, profiling requirements, and empirical data sets required to validate all performance hypotheses, eliminating guesswork and unverified assumptions from the OpenBao and Cloakit roadmap.

---

## 2. The 10 Most Valuable Missing Experiments

```
+--------------------------------------------------------------------------------------------------------------------------------+
|                                              TOP 10 MISSING EXPERIMENTS MATRIX                                                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| Exp | Experiment Name                     | Methodology / Tooling       | Target Metric                      | Expected Value  |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E01 | PostgreSQL Standby Read Scalability | 3x OpenBao on 3x PG Replicas| Throughput scaling factor vs       | CRITICAL (High) |
|     | Under Streaming WAL                 | (Open-loop Poisson load)    | replication lag (LSN delta)        |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E02 | Open-Loop vs Closed-Loop Coordinated| Open-loop load generator vs | Real P99/P99.9 distortion factor   | CRITICAL (High) |
|     | Omission Discrepancy Test           | closed-loop `k6`/`wrk`      | under injected 50ms GC stalls      |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E03 | HOT Update & Autovacuum Bloat Impact| Measure table & index size  | Query latency degradation over     | HIGH            |
|     | Over 10M Token/Lease Writes         | during 48hr continuous write| 10M writes with default vs tuned PG|                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E04 | High-Concurrency RWMutex Cache-Line | `perf c2c` (cache-to-cache) | L1/L2 MESI invalidations & CPU bus | HIGH            |
|     | Bouncing on Hot Subject Keys        | on 64-core AMD EPYC server  | lock cycles under 2,000 goroutines |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E05 | 2Q LRU Mutex Contention vs Sharded  | Microbenchmark comparing    | Throughput limit under 100% cache  | HIGH            |
|     | Read-Mostly Cache at 64 Threads     | `TransitLRU` vs Sharded Map | hits with 64 parallel workers      |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E06 | Cold-Start Cache Stampede &         | Evict hot Subject Key and   | PostgreSQL pool queue wait time &  | HIGH            |
|     | Single-Flight Coalescing            | fire 10,000 concurrent reqs | peak connection saturation         |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E07 | Batch DEK Decrypt Amortization Curve| Test batch sizes: 1, 5, 10, | Per-DEK latency and throughput knee| MEDIUM-HIGH     |
|     |                                     | 25, 50, 100, 500, 1,000     | point identification               |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E08 | Linux CFS CPU Quota Throttling in   | Docker container with 4 CPU | P99 latency distribution vs        | MEDIUM-HIGH     |
|     | Kubernetes Deployment               | quota under bursty traffic  | `/sys/fs/cgroup/cpu.stat` throttled|                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E09 | Connection Pool Starvation Impact on| Inject 200ms query delay to | Verify if HA leader renews lock    | MEDIUM          |
|     | PostgreSQL HA Leader Heartbeats     | saturate 127 data connections| within 15s or spuriously steps down|                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
| E10 | Binary BYTEA vs Base64 Transit DEK  | PostgreSQL table with 10M   | Total table & index disk footprint,| MEDIUM          |
|     | Column Storage Benchmark            | records in text vs bytea    | sequential scan buffer cache hits  |                 |
+-----+-------------------------------------+-----------------------------+------------------------------------+-----------------+
```

---

## 3. Data Collection & Instrumentation Specifications

For each missing experiment, the following instrumentation channels must be captured simultaneously:
1. **Application Profiles**: Continuous `pprof` CPU, heap allocation, block contention (`pprof/block`), and mutex contention (`pprof/mutex`).
2. **PostgreSQL Telemetry**: `pg_stat_statements`, `pg_stat_database`, `pg_statio_user_tables`, and `pg_stat_activity` wait events.
3. **Linux Kernel Metrics**: `/proc/net/snmp` (TCP retransmissions, resets), `vmstat 1`, `/proc/interrupts`, and `perf stat -e cache-misses,branch-misses,context-switches`.
