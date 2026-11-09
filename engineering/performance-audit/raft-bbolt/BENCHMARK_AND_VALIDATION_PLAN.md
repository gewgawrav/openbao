# Synthetic Benchmark & Validation Plan 🤖

## Executive Summary
This document specifies an isolated, reproducible benchmarking harness designed to empirically measure OpenBao Integrated Storage (Raft + bbolt) without using production infrastructure or fabricating performance numbers. All benchmark configurations, parameters, workload distributions, and metrics capture strategies are detailed below.

---

## 1. Benchmark Matrix & Workload Scenarios

```
+-----------------------------------------------------------------------------------------+
| Test Scenario            | Key Cardinality | Concurrency Levels   | Workload Ratio (R:W) |
+-----------------------------------------------------------------------------------------+
| 1. Pure Read Baseline    | 10K, 100K, 1M   | 1, 10, 50, 100, 250  | 100% Read / 0% Write |
| 2. Pure Write Baseline   | 10K, 100K       | 1, 10, 50, 100       | 0% Read / 100% Write |
| 3. Cloakit Target        | 100K, 1M, 10M   | 10, 50, 100, 250, 500| 95% Read / 5% Write  |
| 4. Ultra-Read Burst      | 1M, 10M         | 50, 100, 250, 500    | 99% Read / 1% Write  |
| 5. High-Write Churn      | 100K, 1M        | 10, 50, 100, 250     | 90% Read / 10% Write |
| 6. Degraded Churn        | 100K            | 10, 50, 100          | 80% Read / 20% Write |
+-----------------------------------------------------------------------------------------+
```

### 1.1 Access Distribution Profiles
1. **Uniform Random**: Keys sampled uniformly across the entire cardinality $[1, N]$. Tests worst-case B-tree page cache eviction.
2. **Zipfian / Hot-Key (80/20)**: 80% of operations target 20% of hot Subject Keys. Evaluates in-memory cache hit rates and lock contention on hot policies.

### 1.2 Cache State Profiles
- **Warm Cache**: Run 50,000 requests prior to measurement to ensure operating system page cache and Transit LRU are fully populated.
- **Cold Cache**: Drop OS page cache (`echo 3 > /proc/sys/vm/drop_caches`) or restart OpenBao process before test to measure initial page fault penalties.

---

## 2. Cluster Topology Configurations

1. **Topology A (Single-Node Dev/Standby)**:
   - 1 Voter node. Establishes baseline without network latency.
2. **Topology B (Production Standard)**:
   - 3 Voter nodes distributed across local loopback or isolated synthetic virtual interfaces (0.5ms network RTT).
3. **Topology C (Scale-Out Read Replicas)**:
   - 3 Voter nodes + 2 Non-Voter read replicas (`retry_join_as_non_voter = true`). Measures replication throughput and read scaling without quorum degradation.

---

## 3. Metrics Instrumentation Guidelines

Every benchmark iteration must capture:
- **Throughput**: Operations per second (Total, Read OPS, Write OPS).
- **Latency Distribution**: P50, P90, P95, P99, P99.9, and Max latency (microseconds / milliseconds).
- **Consensus Metrics**:
  - Raft commit latency (`raft.commitTime`).
  - Raft apply batch size and execution duration (`fsm.applyBatch`).
  - Follower replication lag (`raft.replication.heartbeat`).
- **Storage & OS Metrics**:
  - `vault.db` and `raft.db` file growth rates (`ls -la`).
  - Freelist count and page allocation stats (`bbolt stats`).
  - Fsync latency and I/O disk queue depth (`iostat -x 1`).
  - Go Runtime: Allocations/sec, Heap In-Use, GC pause duration (`GODEBUG=gctrace=1`).

---

## 4. Reproducible Synthetic Benchmark Harness Specification

A standalone benchmark script (e.g. `bench_raft_bbolt.go`) should leverage Go's standard `testing.B` harness or a client load generator targeting `/v1/transit/encrypt/<key>` and `/v1/transit/decrypt/<key>`:

```go
// Specification Outline for Synthetic Execution
func BenchmarkCloakitWorkload(b *testing.B) {
    // 1. Initialize cluster with synthetic Subject Keys (10K - 1M)
    // 2. Spawn worker goroutines according to concurrency parameter
    // 3. Loop operations based on 95:5 probability distribution
    // 4. Record latency histograms using HdrHistogram or prometheus client
}
```
