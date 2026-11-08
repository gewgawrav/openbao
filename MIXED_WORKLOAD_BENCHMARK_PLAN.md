# OpenBao Mixed Workload Benchmark Plan & Test Specifications

## 1. Overview & Methodology

To validate OpenBao's behavior in a shared-resource environment and quantify cross-workload interference without inventing unverified benchmark results, this document defines a **rigorous, reproducible benchmark specification**.

The test harness uses a 3-node OpenBao Raft cluster running on dedicated hardware with an external load generation framework (such as `ghz`, `k6`, or custom Go workload generators).

---

## 2. Cluster Deployment & Hardware Baseline

To eliminate external virtualization noise, benchmarks should be executed on bare-metal or dedicated cloud compute instances:

- **Cluster Topology**: 3 OpenBao Server nodes forming a Raft consensus cluster; 1 dedicated load generator node.
- **Node Sizing**:
  - CPU: 8 vCPUs (AMD EPYC or Intel Xeon, pinned frequency).
  - RAM: 32 GB ECC DDR4/DDR5.
  - Storage: Dedicated NVMe SSD (PCIe 4.0, ext4/xfs with `noatime,nodiratime`).
  - Network: 10 Gbps low-latency interconnect (<0.2ms inter-node ping).
- **Operating System & Kernel**: Linux 6.x (Ubuntu 22.04 LTS / 24.04 LTS), `sysctl` optimized (`net.core.somaxconn=32768`, `vm.max_map_count=262144`).
- **OpenBao Configuration**: Raft storage backend, TLS enabled (ECDSA P-256), file audit device enabled on local NVMe.

---

## 3. Required Metrics Collection per Scenario

For each benchmark scenario, the harness captures the following 11 performance and runtime dimensions:

1. **P50 Latency (ms)**: Median client-perceived round-trip time.
2. **P95 Latency (ms)**: 95th percentile latency (early indicator of queue buildup).
3. **P99 Latency (ms)**: 99th percentile latency (tail latency, lock contention indicator).
4. **Throughput (RPS)**: Successful operations completed per second.
5. **CPU Usage (%)**: Node-level CPU utilization broke down into user, system, and iowait (`top`, `pidstat`).
6. **Memory Usage (RSS / Go Heap)**: Resident Set Size, Go heap alloc, and heap in-use (`runtime.MemStats`).
7. **Goroutine Count**: Active goroutines monitored via `runtime.NumGoroutine()` or `/v1/sys/pprof/goroutine`.
8. **Storage Latency (ms)**: Physical backend write and read durations (`raft_storage.put`, `raft_storage.get`, `bbolt` txn commit time).
9. **Lock Contention (ms / wait count)**: Mutex contention profiles captured via Go runtime lock profiling (`runtime.SetMutexProfileFraction`, `runtime.SetBlockProfileRate`).
10. **Queue Waiting Time (ms)**: HTTP request wait time from socket read to handler dispatch.
11. **Error Rate (%)**: Proportion of requests returning HTTP 4xx, 5xx, or network timeouts.

---

## 4. Detailed Test Specifications for the 7 Mixed Workload Scenarios

### Scenario 1: 80% Reads / 20% Writes
- **Objective**: Establish baseline performance and observe how concurrent BoltDB write transactions affect memory-mapped read latencies.
- **Workload Profile**:
  - Target Throughput: Ramp up from 1,000 RPS to 20,000 RPS over 10 minutes.
  - 80% Traffic: KV v2 secret retrieval (`GET /v1/secret/data/workload/app-*`).
  - 20% Traffic: KV v2 secret creation/updates (`POST /v1/secret/data/workload/app-*`, payload 1KB JSON).
- **Hypothesis**: P50 read latency remains sub-millisecond (<0.8ms); P99 read latency degrades proportionally to BoltDB dirty page flushes and Raft write serialization.
- **Execution Script**:
  ```bash
  # Scenario 1 Load Command (k6 / ghz equivalent)
  k6 run --vus 100 --duration 10m \
    -e READ_RATIO=0.8 -e WRITE_RATIO=0.2 \
    scripts/benchmarks/scenario1_read_write.js
  ```

### Scenario 2: 50% Reads / 50% Writes
- **Objective**: Push the physical storage layer into heavy write saturation to determine the inflection point where write lock contention degrades read throughput.
- **Workload Profile**:
  - Target Throughput: Sustained 10,000 RPS for 15 minutes.
  - 50% Traffic: KV v2 reads on existing keys.
  - 50% Traffic: Rapid KV v2 writes on new and existing keys (1KB–4KB payloads).
- **Hypothesis**: BoltDB write transaction lock (`tx.lock`) becomes saturated; Raft commit backlog builds up; CPU iowait increases; Goroutine count swells as incoming writes wait for Raft proposal commits.

### Scenario 3: 80% Cryptographic Operations / 20% Ordinary API Requests
- **Objective**: Measure Go runtime cooperative scheduler latency (`schedlat`) and quantify interference from CPU-intensive cryptographic operations on latency-sensitive secret retrievals.
- **Workload Profile**:
  - 80% Traffic: Transit symmetric encrypt/decrypt (`POST /v1/transit/encrypt/app-key`, AES-256-GCM, 512B plaintext).
  - 20% Traffic: Latency-sensitive KV v2 secret reads (`GET /v1/secret/data/critical/api-token`).
- **Hypothesis**: Transit crypto saturates all CPU cores (100% CPU user time); KV reads experience significant tail-latency degradation (P99 increases from 1ms to 15ms+) solely due to Go runtime scheduler queue delay.

### Scenario 4: Heavy PKI Issuance Alongside Authentication
- **Objective**: Stress CPU threads with asymmetric key operations and password hashing while stressing the storage write pipeline with certificate storage and token persistence.
- **Workload Profile**:
  - Stream A (500 RPS): PKI certificate generation (`POST /v1/pki/issue/service-cert`, RSA 2048-bit and 4096-bit keys, `no_store=false`).
  - Stream B (2,000 RPS): AppRole or Userpass authentication (`POST /v1/auth/approle/login`, role ID / secret ID validation).
- **Hypothesis**: Extreme CPU contention between RSA signing and token cryptographic validation; simultaneous BoltDB write contention between `certs/` persistence and `TokenStore` entry creation; Auth P99 latency escalates, risking client login timeouts.

### Scenario 5: Large Batches Alongside Small Requests
- **Objective**: Evaluate head-of-line blocking and memory allocation pressure when massive batch requests execute concurrently with single-item requests.
- **Workload Profile**:
  - Stream A (50 RPS): Transit batch encryption (`POST /v1/transit/encrypt/bulk-key`) with `batch_input` containing 2,000 items per request (aggregate payload ~2MB).
  - Stream B (5,000 RPS): Single-item Transit encryption and single KV reads.
- **Hypothesis**: Stream A monopolizes `keysutil.Policy.l` read locks and causes transient heap allocation spikes; Go runtime triggers frequent GC cycles and invokes GC Mark Assist on Stream B worker goroutines, inducing micro-pauses in single-item requests.

### Scenario 6: Heavy Background Lease Processing Alongside API Traffic
- **Objective**: Quantify how background lease expiration storms generated by the `ExpirationManager` and executed by `FairShare` workers impact foreground user API operations.
- **Workload Profile**:
  - Setup: Pre-populate OpenBao with 100,000 dynamic secrets with synchronized 5-minute TTLs.
  - Background Event: At T=5m, 100,000 leases expire simultaneously, triggering `fairshare.Dispatcher` and 200 revocation worker goroutines.
  - Foreground Traffic: Sustained 4,000 RPS of mixed KV reads and writes.
- **Hypothesis**: Background revocation workers flood the storage backend with `barrier.Delete` calls, consuming BoltDB write transactions and Raft bandwidth; foreground API write latency spikes dramatically (P99 > 200ms).

### Scenario 7: One High-Volume Tenant Alongside Several Low-Volume Tenants
- **Objective**: Demonstrate multi-tenant "noisy neighbor" starvation in the absence of cost-weighted quotas or namespace worker isolation.
- **Workload Profile**:
  - Tenant 1 ("Noisy Neighbor", Namespace `ns-finance`): 15,000 RPS of heavy Transit batch encrypts and rapid KV writes.
  - Tenants 2–5 ("Standard Tenants", Namespaces `ns-dev`, `ns-qa`, `ns-ops`, `ns-audit`): 100 RPS each of standard KV reads and token validations.
- **Hypothesis**: Tenant 1 exhausts node CPU and saturates the Raft consensus pipeline; Tenants 2–5 suffer severe latency degradation (P95/P99 latency increases by 500%–1000%) despite remaining well below their own intended traffic limits.

---

## 5. Non-Intrusive eBPF Tracing Protocol

To extract ground-truth contention latency without adding Go runtime profiling overhead during production-level runs, operators can use Linux eBPF (`bcc` / `bpftrace`):

```bash
# Trace Go scheduler runqueue latency (schedlat)
bpftrace -e '
tracepoint:sched:sched_wakeup { @ts[args->pid] = nsecs; }
tracepoint:sched:sched_switch {
    if (@ts[args->next_pid]) {
        @latency_us = hist((nsecs - @ts[args->next_pid]) / 1000);
        delete(@ts[args->next_pid]);
    }
}'

# Trace BoltDB transaction lock wait duration
bpftrace -e '
uprobe:/usr/local/bin/bao:"go.etcd.io/bbolt.(*DB).Update" {
    @start[tid] = nsecs;
}
uretprobe:/usr/local/bin/bao:"go.etcd.io/bbolt.(*DB).Update" {
    if (@start[tid]) {
        @bbolt_update_ns = hist(nsecs - @start[tid]);
        delete(@start[tid]);
    }
}'
```

---

## 6. Identified Missing OpenBao Instrumentation

During our codebase investigation, we identified key instrumentation gaps that currently prevent operators from directly diagnosing resource contention without intrusive profiling:

1. **Per-Mount & Per-Namespace Queue Wait Time**:
   - Currently, OpenBao records `core.handle_request` duration, but does not separate HTTP queueing delay from handler execution time.
   - *Recommendation*: Add a metric `core.request.queue_duration` measuring time elapsed from `http.Handler` entry to `Core.HandleRequest` invocation.
2. **Lock Hold & Wait Time Histograms**:
   - Critical locks (`Core.stateLock`, `AESGCMBarrier.l`, `keysutil.Policy.l`) lack telemetry recording lock acquisition wait times and lock holding durations.
   - *Recommendation*: Instrument barrier and policy locks with histograms (e.g., `barrier.lock_wait_time`, `barrier.lock_hold_time`).
3. **Storage Write Queue Depth & Flush Latency**:
   - The Raft and BoltDB integrations measure total put/get duration, but do not export internal BoltDB transaction wait times (`tx.lock` wait) or Raft proposal queue depths.
   - *Recommendation*: Export `raft.fsm.apply_queue_depth` and `bbolt.tx_wait_duration`.
4. **FairShare Dispatcher Queue Backlog**:
   - `ExpirationManager` tracks total pending leases, but does not expose real-time queue lengths or task latency inside `fairshare.Dispatcher`.
   - *Recommendation*: Add gauge metrics `expiration.fairshare.queued_jobs` and `expiration.fairshare.worker_active_count`.
5. **Cost-Weighted Quota Metrics**:
   - Quotas only expose counter rejections (`quota.rate_limit.rejections`).
   - *Recommendation*: Export client cost consumption metrics (`quota.rate_limit.consumed_tokens`).
