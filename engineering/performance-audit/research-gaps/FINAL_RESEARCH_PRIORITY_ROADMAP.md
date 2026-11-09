# Final Research Priority Roadmap & Definitive Assessment 🤖

## 1. Executive Summary

This roadmap establishes the definitive research and engineering priorities for OpenBao in support of the Cloakit Privacy Vault workload (95% reads, 5% writes). We resolve previous ambiguities, rank upcoming initiatives by expected return on investment (ROI), and outline the definitive top ten technical answers and priorities.

---

## 2. The 10 Most Important Unanswered Technical Questions

1. **Replication Index Accuracy**: Can PostgreSQL WAL LSN tracking (`ReplicationIndexBackend`) provide sub-50ms read-after-write consistency guarantees across asynchronous read replicas during peak Subject Key rotation bursts?
2. **True P99 Tail Latency Under Open-Loop Load**: What is OpenBao's actual P99 and P99.9 latency profile when measured with an open-loop Poisson arrival generator that eliminates Coordinated Omission?
3. **Lockless Policy Cache Feasibility**: Can an atomic, copy-on-write (COW) or sharded lockless cache replace `TwoQueueCache` in `LockManager` without risking memory bloat under high key cardinality?
4. **PostgreSQL HOT Update Failure Cost**: How many gigabytes of dead-tuple table/index bloat accumulate per week in production, and does tuning `autovacuum_vacuum_scale_factor` to 0.02 prevent latency degradation?
5. **Connection Pool Starvation Threshold**: At what concurrency level does `max_parallel = 128` saturate, and does separating the HA heartbeat connection pool prevent spurious cluster failovers?
6. **Container CFS Throttling Impact**: Does containerized OpenBao under Kubernetes experience CFS quota throttling during bursty cryptographic traffic, and does setting `cpu.cfs_quota_us = -1` (guaranteed QoS) resolve bimodal tail latencies?
7. **Cache Stampede Vulnerability**: Does a sudden eviction of a heavily used Subject Key under 10,000 req/s cause a cascading database connection exhaustion event, or does OpenBao's internal single-flight coalescing protect PostgreSQL?
8. **Binary vs Base64 Storage Savings**: Exactly how much storage and buffer pool memory is saved in PostgreSQL when switching Cloakit DEK storage from `vault:v1:<base64>` text to binary `BYTEA`?
9. **Linux TCP Socket Buffer Bounds**: Does tuning `net.core.somaxconn` and socket receive buffers eliminate connection drops under 100,000 req/s bursts?
10. **Multi-Tenant Workload Isolation**: Can noisy tenants generating high write or rotation volume starve quiet tenants performing read-only DEK unwrapping on the same OpenBao cluster?

---

## 3. The 10 Most Valuable Missing Experiments

1. **Open-Loop Coordinated Omission Benchmark**: Benchmarking OpenBao transit decryption with Poisson arrival schedules versus closed-loop benchmarks.
2. **PostgreSQL Read-Replica Scaling**: Deploying 3 OpenBao standby nodes across 3 PostgreSQL read replicas to evaluate horizontal read throughput scaling.
3. **PostgreSQL Autovacuum and Bloat Soak Test**: Running 10,000,000 writes/rotations over 48 hours to measure dead tuple growth and vacuum lock wait times.
4. **`perf c2c` Cache-Line Bouncing Profile**: Profiling MESI invalidations on `sync.RWMutex` across 64 CPU cores during concurrent reads of a single Subject Key.
5. **2Q LRU Cache Mutex Saturation Test**: Benchmarking `TransitLRU` vs a read-mostly sharded cache under 100% cache-hit traffic at 64 concurrent goroutines.
6. **Cache Stampede Failure Injection**: Simulating sudden cache eviction under 10,000 concurrent requests for a single Subject Key.
7. **Batch DEK Unwrapping Scaling Curve**: Evaluating per-key throughput and latency across batch sizes from 1 to 1,000.
8. **Kubernetes CFS Quota Throttling Evaluation**: Profiling `/sys/fs/cgroup/cpu.stat` throttling counters under bursty DEK workloads.
9. **PostgreSQL HA Heartbeat Connection Isolation**: Injecting 200ms query delays into PostgreSQL data queries to verify HA lock renewal resilience.
10. **Binary DEK Storage Footprint Benchmark**: Measuring disk page utilization and query execution times for 10M records comparing Base64 string vs BYTEA.

---

## 4. Weakly Supported Previous Recommendations

The following recommendations from prior audits are evaluated as **weakly supported** and should NOT be pursued without new empirical evidence:

```
+----------------------------------------------------------------------------------------------------+
|                                WEAKLY SUPPORTED PRIOR RECOMMENDATIONS                              |
+--------------------------+-------------------------------------+-----------------------------------+
| Prior Recommendation     | Weakness / Missing Evidence         | Recommended Status                |
+--------------------------+-------------------------------------+-----------------------------------+
| Replace Go crypto with   | Cgo call overhead (~50ns) negates   | REJECTED                          |
| Cgo/Rust AVX extensions  | AES-NI gain on 32B DEKs; breaks     | (Net negative for throughput)     |
|                          | Go goroutine scheduling.            |                                   |
+--------------------------+-------------------------------------+-----------------------------------+
| Migrate storage engine   | Ignores that Cloakit uses PostgreSQL| REJECTED                          |
| to pure Raft/BoltDB      | as the Layer 3 database. Raft cannot| (Incompatible with multi-replica  |
|                          | scale horizontal read replicas.     | database architecture)            |
+--------------------------+-------------------------------------+-----------------------------------+
| Dynamically derive all   | Destroys GDPR/CCPA cryptographic    | REJECTED                          |
| Subject Keys via HKDF    | erasure capability; adds +1,800ns   | (Violates privacy compliance      |
|                          | CPU penalty to every read.          | requirements)                     |
+--------------------------+-------------------------------------+-----------------------------------+
| Use unbounded sync.Map   | Causes uncontrolled memory leak /   | REJECTED                          |
| for PolicyStore cache    | OOM under high key cardinality      | (Unbounded heap growth risk)      |
|                          | (e.g. 5M+ Subject Keys).            |                                   |
+--------------------------+-------------------------------------+-----------------------------------+
```

---

## 5. Prioritized Engineering Roadmap

```
+-----------------------------------------------------------------------------------------------------------------------------------+
|                                              RESEARCH & ENGINEERING PRIORITY MATRIX                                               |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| Rank | Initiative / Milestone                   | Impact | Confidence | Effort | Security Risk | Validation Cost | Implementation |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 1    | Batch DEK Processing Enforcement in SDK  | HIGH   | VERY HIGH  | LOW    | NONE          | LOW             | Phase 1 (Immed)|
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 2    | PostgreSQL HOT Update & Autovacuum Tuning| HIGH   | HIGH       | LOW    | NONE          | LOW             | Phase 1 (Immed)|
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 3    | Open-Loop Benchmark Suite Implementation | HIGH   | VERY HIGH  | MEDIUM | NONE          | MEDIUM          | Phase 1 (Immed)|
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 4    | Separate PostgreSQL HA Heartbeat Pool    | HIGH   | VERY HIGH  | LOW    | NONE          | LOW             | Phase 2 (Near) |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 5    | Sharded Lockless Read Cache for Policies | HIGH   | HIGH       | MEDIUM | LOW           | MEDIUM          | Phase 2 (Near) |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 6    | PostgreSQL Standby Read Horizontal Scale | HIGH   | HIGH       | MEDIUM | LOW           | MEDIUM          | Phase 2 (Near) |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 7    | Binary BYTEA Column Storage in PostgreSQL| MEDIUM | VERY HIGH  | LOW    | NONE          | LOW             | Phase 3 (Mid)  |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
| 8    | Linux Kernel & CFS Quota Tuning Profiles | MEDIUM | HIGH       | LOW    | NONE          | LOW             | Phase 3 (Mid)  |
+------+------------------------------------------+--------+------------+--------+---------------+-----------------+----------------+
```

---

## 6. What Should Be Benchmarked Before Implementing Changes

Before any production code changes are applied:
1. **Baseline Open-Loop Benchmark**: Establish true baseline P50, P95, P99, and P99.9 latency under Poisson arrival schedules across 10,000, 25,000, and 50,000 req/s.
2. **PostgreSQL Query Profiling**: Run `EXPLAIN (ANALYZE, BUFFERS)` on `openbao_kv_store` queries under concurrent read/write loads to establish baseline shared buffer hits and I/O wait times.
3. **Lock Profiling**: Capture Go `pprof/mutex` and `pprof/block` profiles under 1,000 concurrent goroutines to quantify the exact nanoseconds lost to `policy.l.RLock()` and `TwoQueueCache` mutex contention.
