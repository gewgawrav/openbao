# Missing Experiments and Empirical Data Requirements 🤖

**Author:** OpenBao Performance & Systems Engineering Team
**Workload Focus:** Cloakit Privacy Vault (95% Reads, 5% Writes; Subject Key Lookup & DEK Wrapping/Unwrapping)
**Target Environment:** OpenBao Layer 2 Key Management Engine on PostgreSQL Storage Backend
**Date:** March 2025
**Evidence Standard:** SOURCE-CONFIRMED, STATIC-ANALYSIS-CONFIRMED, TEST-CONFIRMED, BENCHMARK-CONFIRMED, INFERRED, UNKNOWN

---

## 1. Executive Summary

A critical finding from our meta-analysis of all 82 prior audit documents is that many high-impact decisions were proposed based on theoretical inference rather than empirical validation.
This document outlines the **Ten Most Valuable Missing Experiments** required to close existing research gaps, detailing the hypothesis, experimental apparatus, independent variables, metric observables, and success/rejection criteria.

---

## 2. The Ten Most Valuable Missing Experiments

### Experiment 1: Open-Loop Saturation & Coordinated Omission Discovery
- **Hypothesis:** Under open-loop Poisson arrival, OpenBao's true P99 latency will diverge sharply at concurrency $C > 256$, revealing coordinated omission masking in prior benchmarks.
- **Methodology:** Use `wrk2` or `k6` in `constant-arrival-rate` mode to sweep throughput from 5,000 to 50,000 ops/sec against a 100,000-key dataset.
- **Metrics:** P50, P90, P99, P99.9, Max latency via HDRHistogram; connection drop count.
- **Rejection Criterion:** If open-loop P99 latency exceeds 5.0 ms at $\le 20,000\text{ ops/sec}$.

### Experiment 2: PostgreSQL HOT Update & FILLFACTOR Verification
- **Hypothesis:** Modifying `vault_kv_store` table definition to `FILLFACTOR = 80` and omitting primary key columns in the `DO UPDATE` clause enables Heap-Only Tuple (HOT) updates, reducing WAL volume by $\ge 40\%$ and cutting write P99 latency by $\ge 30\%$.
- **Methodology:** Run 2,000 key updates/sec for 30 minutes under `FILLFACTOR = 100` vs `FILLFACTOR = 80`. Query `pg_stat_user_tables.n_tup_hot_upd`.
- **Success Criterion:** `n_tup_hot_upd / n_tup_upd \ge 0.85` (over 85% HOT updates).

### Experiment 3: Zipfian Key Skew & KeyEntry Mutex Lock Contention
- **Hypothesis:** A Zipfian traffic distribution ($s = 0.99$) with 5% concurrent key rotations causes writer lock escalation in `KeyEntry.Lock()`, degrading concurrent reader P99 latency by over 10x compared to uniform distribution.
- **Methodology:** Benchmark 30,000 DEK wraps/sec across 100,000 keys with uniform vs Zipfian distribution; trigger 5 rotations/sec on the top 10 hottest keys.
- **Metrics:** `sync.RWMutex` wait duration via `go tool pprof --mutex`.

### Experiment 4: CGO Transition Overhead vs Pure Go AES-NI on 32-Byte DEKs
- **Hypothesis:** For small 32-byte cryptographic operations, pure Go assembly (`crypto/cipher`) is strictly faster and exhibits lower tail latency than a Rust C-ABI shared library due to CGO stack-switching and M-thread locking.
- **Methodology:** Benchmark 10,000,000 iterations of 32-byte AES-256-GCM seal/open in pure Go vs Rust via CGO across 1, 8, 16, 32 goroutines.
- **Metrics:** Nanoseconds per operation, allocations per op, P99.9 latency under memory pressure.

### Experiment 5: S3-FIFO vs TwoQueueCache Eviction Overhead Under High Cardinality
- **Hypothesis:** When key cardinality (500,000 keys) exceeds cache size (128,000 entries), an S3-FIFO cache replacement algorithm sustains a 15-20% higher hit ratio than `TwoQueueCache` with 60% lower mutex contention.
- **Methodology:** Replay a 1,000,000-request production access trace with Zipfian skew against both cache algorithms.
- **Metrics:** Hit ratio, lock acquisition wait time, memory overhead per entry.

### Experiment 6: Single-Flight Cache Stampede Suppression on Key Rotation
- **Hypothesis:** Introducing `golang.org/x/sync/singleflight` to `LockManager.GetKey()` suppresses PostgreSQL thundering herd spikes during Subject Key rotation, reducing peak DB connections by $\ge 80\%$.
- **Methodology:** Issue simultaneous DEK wrap requests on 500 client connections while rotating the target key.
- **Metrics:** Concurrent active PostgreSQL queries, max connection pool checkout queue.

### Experiment 7: Container CFS Throttling on P99.9 Tail Latency
- **Hypothesis:** Enforcing CPU quotas via Kubernetes CFS limits (`cpu.cfs_quota_us`) introduces 50-100 ms tail latency spikes during bursty traffic, which are eliminated by dedicated CPU core pinning (`cpuset`).
- **Methodology:** Run OpenBao in container with 4 CPU limit under CFS vs 4 dedicated pinned cores. Inject 200 ms burst traffic.
- **Metrics:** `/sys/fs/cgroup/cpu/cpu.stat` throttled count, HDR histogram tail percentiles.

### Experiment 8: PostgreSQL Invalidation Bus (LISTEN/NOTIFY) Latency
- **Hypothesis:** Using PostgreSQL `LISTEN/NOTIFY` enables cross-node cache invalidation with $<2\text{ ms}$ propagation delay, making multi-instance read scaling technically feasible without stale key anomalies.
- **Methodology:** Deploy a 3-node OpenBao cluster with PostgreSQL. Node 1 rotates a key and sends `NOTIFY key_invalidation, '<key_id>'`. Measure time until Nodes 2 and 3 evict the key from their local caches.
- **Metrics:** Invalidation propagation latency (P50, P99), CPU overhead on PostgreSQL.

### Experiment 9: Barrier Ciphertext Pre-Allocation Pooling
- **Hypothesis:** Pre-allocating barrier encryption byte buffers via `sync.Pool` eliminates 85% of heap allocations on physical storage writes, reducing Go GC frequency by $\ge 25\%$.
- **Methodology:** Run continuous 5% write workload for 60 minutes with baseline OpenBao vs `sync.Pool`-enabled barrier storage.
- **Metrics:** GC cycles, heap allocation rate (MB/sec), P99 latency.

### Experiment 10: PostgreSQL Checkpoint Stall & NVMe Fsync Latency
- **Hypothesis:** Under 2,000 writes/sec, default `max_wal_size = 1GB` forces frequent checkpoints that saturate NVMe write queues, causing 100ms+ latency spikes; tuning `max_wal_size = 32GB` and `checkpoint_completion_target = 0.9` smooths tail latency.
- **Methodology:** Measure WAL checkpoint frequency and query latency during a 2-hour soak test before and after tuning.
- **Metrics:** `pg_stat_bgwriter` checkpoint sync time, OpenBao HTTP P99.9 latency.
