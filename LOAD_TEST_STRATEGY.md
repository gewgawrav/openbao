# OpenBao Load Testing & Capacity Strategy

## 1. Objective and Testing Principles

The goal of this load testing strategy is to discover system limits, measure degradation boundaries, validate failover behaviors, and determine production capacity profiles for OpenBao under real-world conditions without causing uncontrolled downtime.

### Guiding Principles:
1. **Safety & Isolation**: Run load tests against dedicated non-production environments with identical hardware, storage, and networking configurations.
2. **Realistic Cardinality**: Synthesize workloads using thousands of unique paths, tokens, and roles to prevent unrealistic L1/L2 cache and memory-buffer skew.
3. **Progressive Stepping**: Test with progressive concurrency ramps (e.g. 1 -> 10 -> 50 -> 100 -> 250 -> 500 -> 1000 concurrent clients) to observe knees in latency curves.
4. **Separation of Concerns**: Isolate read vs. write workloads, cryptographic vs. storage operations, and control-plane vs. data-plane traffic.

---

## 2. Load Testing Framework and Tooling

### Tool Selection
- **CLI Benchmark Harness**: `scripts/benchmarks/benchmark_runner.go`
  - Purpose: Microsecond-precision in-cluster or external HTTP load generation across all 12 OpenBao workload categories.
  - Advantages: Native OpenBao API Go client with optimized connection pooling, HTTP/2 multiplexing, and direct quantile calculation.
- **Vegeta**:
  - Purpose: Constant-rate HTTP benchmarking (e.g., test system behavior at exactly 2,000 req/sec sustained for 30 minutes).
- **k6**:
  - Purpose: Multi-step realistic user journeys (e.g., Authenticate -> Lookup Token -> Read Secret -> Write Transit -> Renew Lease).
- **Go pprof & Execution Tracing**:
  - Purpose: Continuous capture of CPU, heap, goroutine, and mutex profiles while load is applied.
- **Prometheus & OpenBao Telemetry**:
  - Purpose: Real-time telemetry monitoring via `/v1/sys/metrics?format=prometheus`.

---

## 3. Step-by-Step Test Suites

### Suite 1: Normal Sustained Load Test
- **Objective**: Establish baseline P50, P95, and P99 latencies under nominal steady-state production throughput (30% to 50% capacity).
- **Execution**:
  ```bash
  # Execute sustained read workload for 15 minutes with 25 concurrent connections
  go run scripts/benchmarks/benchmark_runner.go \
    -address="http://127.0.0.1:8200" \
    -token="root" \
    -workload="secret-read" \
    -concurrency=25 \
    -duration=15m
  ```
- **Success Criteria**: Error rate = 0.00%, P95 latency < 5ms, P99 latency < 15ms, CPU utilization < 50%, no growing goroutine accumulation.

### Suite 2: Progressive Concurrency Stress Test
- **Objective**: Determine the knee of the curve where latency begins to degrade and throughput plateaus.
- **Concurreny Steps**: 1, 5, 20, 50, 100, 250, 500, 1000 workers.
- **Execution Script**:
  ```bash
  for c in 1 5 20 50 100 250 500 1000; do
    echo "Testing concurrency level: $c"
    go run scripts/benchmarks/benchmark_runner.go \
      -address="http://127.0.0.1:8200" \
      -token="root" \
      -workload="secret-read" \
      -concurrency=$c \
      -duration=30s
    sleep 5
  done
  ```
- **Analysis**: Plot Throughput vs. Concurrency. The saturation point is reached when doubling concurrency yields less than a 10% throughput increase while doubling P95 latency.

### Suite 3: Burst Traffic Stress Test
- **Objective**: Measure system elasticity during sudden 10x traffic spikes (e.g., application batch rollouts).
- **Profile**: 1,000 req/sec steady state for 2 minutes -> instantaneous jump to 10,000 req/sec for 30 seconds -> return to 1,000 req/sec.
- **Evaluation**: Observe recovery time, connection queue drops, and verify whether memory allocation drops back to baseline after the burst.

### Suite 4: Hot-Key vs. High-Cardinality Contention Test
- **Objective**: Quantify lock contention on high-frequency paths.
- **Scenarios**:
  1. *Uniform Write*: 1,000 distinct paths (`secret/data/item-{id}`).
  2. *Contended Write*: 1 hot path (`secret/data/shared-config`).
  ```bash
  # Uniform write test
  go run scripts/benchmarks/benchmark_runner.go -workload=secret-write -concurrency=50 -duration=1m
  # Hot-key write test
  go run scripts/benchmarks/benchmark_runner.go -workload=secret-write -hot-key-ratio=1.0 -concurrency=50 -duration=1m
  ```

### Suite 5: Large Payload Stress Test
- **Objective**: Measure serialization, barrier AES-GCM encryption, and memory pressure under large payloads.
- **Payload Sizes**: 1 KB, 10 KB, 100 KB, 1 MB.
- **Observation Metrics**: GC pause times (`go_gc_duration_seconds`), allocation rate, barrier throughput.

### Suite 6: Integrated Storage (Raft) Chaos & Slow Storage Test
- **Objective**: Induce disk I/O delay to measure Raft heartbeat stability and write backpressure.
- **Tooling**: Linux `tc` (traffic control) for network delay between cluster nodes, or Linux `fio` / cgroups `io.weight` to throttle disk bandwidth.
- **Evaluation**: Ensure leader elections do not trigger inappropriately when storage latency fluctuates between 5ms and 50ms.

---

## 4. Telemetry and Observability Configuration

### OpenBao Metrics Setup
In OpenBao configuration file (`config.hcl`):
```hcl
telemetry {
  prometheus_retention_time = "24h"
  disable_hostname = true
}
```

### Essential Prometheus Metrics to Monitor:

| Metric Name | Type | Description / Alert Condition |
| :--- | :--- | :--- |
| `openbao_core_handle_request` | Summary / Histogram | Overall request latency breakdown by method/path |
| `openbao_barrier_get` / `put` | Summary / Histogram | Raw barrier read/write latency to physical storage |
| `openbao_raft_replication_appendEntries` | Histogram | Latency of replicating WAL entries to followers |
| `openbao_raft_leader_lastContact` | Gauge | Milliseconds since leader contacted follower (Alert if > 200ms) |
| `openbao_expire_num_leases` | Gauge | Active lease count in expiration manager |
| `openbao_token_count` | Gauge | Number of active tokens in storage |
| `go_goroutines` | Gauge | Total active goroutines (Alert if growing monotonically) |
| `go_memstats_heap_alloc_bytes` | Gauge | Heap memory in use |
| `go_gc_duration_seconds` | Summary | Go runtime GC pause durations |

---

## 5. Post-Load Recovery Analysis

After load decreases:
1. **Goroutine Leak Check**: Confirm goroutines return to base level within 60 seconds of test completion.
2. **Memory Retention (RSS)**: Go’s runtime allocator may retain freed memory (`madvise`). Verify that memory does not grow unbounded across subsequent test iterations.
3. **Connection Pool Draining**: Ensure idle HTTP/TCP client sockets terminate cleanly without entering `CLOSE_WAIT` states.
