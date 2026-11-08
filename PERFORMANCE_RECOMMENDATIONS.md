# OpenBao Performance Recommendations & High-Value Optimizations

Based on profiling traces (`cpu.pprof` and `mem.pprof`) and empirical benchmark saturation points, this document details prioritized engineering and operational recommendations to increase sustainable throughput and eliminate P95/P99 tail latency spikes.

---

## 1. High-Value Architectural & Code Optimizations

### Priority 1: Zero-Allocation Fast-Path for TokenStore Lookup & Caching
- **The Issue**: Every authenticated request calls `TokenStore.Lookup`, which consumes 193 KB and nearly 3,000 heap allocations per operation (`PopulateTokenEntry`). GC cycles account for 18.15% of runtime CPU under load.
- **Optimization**:
  1. Implement an LRU read cache for parsed, immutable token descriptors in memory (`sync.Map` or `arc.ARCCache`).
  2. Cache verified HMACs to skip redundant cryptographic checks during repeated API calls using the same token within short time windows (e.g. 5-30 seconds).
  3. Replace slice recreation for common policies (such as `["default"]`) with shared singleton references.
- **Projected Gain**: **+40% to +60% throughput** on authenticated secret reads; **-65% reduction in P99 latency** by mitigating GC mark spikes.

---

### Priority 2: Pre-Parsed AST & Regex Caching in Policy ACL Engine
- **The Issue**: Dynamic ACL evaluation (`policy.(*Store).ACL`) frequently decodes HCL structs and re-evaluates glob paths via regex backtracking, accounting for 376 KB of heap allocations per request and 12.1% of CPU time.
- **Optimization**:
  1. Pre-compile glob patterns into prefix-tree / radix matchers rather than converting globs to standard regexes (`tryBacktrack`).
  2. Cache evaluated capabilities per `(TokenID, Path, Method)` tuple using a short-lived thread-local or bounded cache.
  3. Avoid dynamic templating evaluations if the policy contains no variable interpolation syntax.
- **Projected Gain**: **+35% throughput** on policy-evaluated requests; reduction in CPU allocation churn.

---

### Priority 3: Asynchronous Pipelined Audit Logging
- **The Issue**: Audit logging is fully synchronous. In benchmarks, enabling audit logging cut read throughput by 45.4% (from 19,247 to 10,503 ops/sec) due to JSON serialization, HMAC salting, and lock synchronization on the writer interface.
- **Optimization**:
  1. Implement a ring-buffered, lock-free batching dispatcher for audit events (`chan` with configurable backpressure buffer, e.g. 10,000 events).
  2. Perform HMAC salting and JSON serialization concurrently across worker goroutines rather than holding the main request execution path.
  3. Maintain strict security semantics: if the buffer reaches capacity, apply backpressure or block only when the ring buffer overflows.
- **Projected Gain**: **+60% to +80% increase** in throughput when audit logging is active.

---

### Priority 4: Sharded Lock Stripping in Expiration Manager
- **The Issue**: The `ExpirationManager` utilizes a single global mutex protecting its binary heap priority queue. Under concurrent lease creations and renewals, threads serialize on this lock, capping lease renewal throughput at ~3,355 ops/sec.
- **Optimization**:
  1. Shard the lease priority queue into $K$ independent sub-queues (e.g., $K = 16$ or $32$ shards hashed by `LeaseID`).
  2. Each shard maintains its own `sync.Mutex` and internal timer heap, allowing parallel lease renewals.
- **Projected Gain**: **+300% to +400% scaling** for lease renewals on multi-core systems (up to ~12,000 renewals/sec on 8-16 cores).

---

### Priority 5: Integrated Storage (Raft) Batching & Pipelined Fsync
- **The Issue**: Individual writes block on Raft WAL disk fsyncs, capping unbatched write throughput to the disk's physical latency floor (typically 500 - 2,000 IOPS).
- **Optimization**:
  1. Introduce adaptive group-commit batching in the barrier storage layer: coalesce writes arriving within a microsecond window (e.g., 50µs - 200µs) into a single Raft WAL entry and disk sync.
  2. Utilize `io_uring` or direct asynchronous I/O where supported by the Linux kernel to minimize user-kernel context switching during WAL flushes.
- **Projected Gain**: Increases write throughput from 1,500 ops/sec to **8,000 - 15,000 ops/sec** on SSD/NVMe backends.

---

## 2. Infrastructure & Operational Recommendations

### 1. Mandate Batch Tokens for Microservices
- Service-to-service calls that only read secrets or use Transit encryption should authenticate using `batch` tokens.
- Batch tokens are cryptographically signed tokens containing their own authorization state; they require **zero writes to storage** and bypass the Expiration Manager queue entirely.

### 2. Configure Dedicated High-IOPS NVMe Drives for Raft WAL
- Never run OpenBao's Raft storage directory on standard cloud network storage (such as unprovisioned EBS or NFS shares).
- Ensure disk latency P99 is consistently below 1.0ms.

### 3. Deploy OpenBao Performance Standbys
- In high-read production environments, route all read traffic (KV reads, Transit decryption, token lookups) across Performance Standby nodes to horizontally scale throughput while shielding the active leader from read-induced GC pressure.

### 4. Go Runtime Tuning
- Set memory limits and GC pacing for high-concurrency instances:
  ```bash
  # Set memory limit to prevent unexpected container OOM termination
  export GOMEMLIMIT="7GiB"
  # Set GC pacing to balance latency vs memory headroom
  export GOGC=100
  ```
- Use Linux Transparent Huge Pages (`madvise`) and ensure adequate file descriptor limits (`ulimit -n 65536`).
