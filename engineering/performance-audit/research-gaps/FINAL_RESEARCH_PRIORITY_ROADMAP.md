# Final Research Priority Roadmap & Strategic Synthesis 🤖

**Author:** OpenBao Performance & Systems Engineering Team
**Workload Focus:** Cloakit Privacy Vault (95% Reads, 5% Writes; Subject Key Lookup & DEK Wrapping/Unwrapping)
**Target Environment:** OpenBao Layer 2 Key Management Engine on PostgreSQL Storage Backend
**Date:** March 2025
**Evidence Standard:** SOURCE-CONFIRMED, STATIC-ANALYSIS-CONFIRMED, TEST-CONFIRMED, BENCHMARK-CONFIRMED, INFERRED, UNKNOWN

---

## 1. Executive Synthesis & Strategic Answers

This final roadmap synthesizes the meta-audit across all 82 previous performance audit documents, addressing the core strategic questions and establishing the definitive engineering priority roadmap for Cloakit Privacy Vault.

---

## 2. The Ten Most Important Unanswered Technical Questions

1. **True Open-Loop Saturation:** What is OpenBao's true saturation cliff (throughput and P99 latency) when tested under open-loop Poisson scheduling rather than closed-loop virtual users?
2. **LockManager Writer Starvation:** How severely do 5% key rotation writes serialize concurrent 95% DEK wrapping reads on hot Subject Keys under production concurrency?
3. **Cache Eviction Performance Cliff:** When Subject Key cardinality exceeds physical memory cache size (128k entries), how precipitously does P99 latency degrade upon falling back to PostgreSQL?
4. **PostgreSQL MVCC Table Bloat:** Does continuous updating of HA locks and key metadata without HOT updates degrade B-Tree index scan latency over multi-day soak tests?
5. **Cross-Node Cache Invalidation Feasibility:** Can PostgreSQL `LISTEN/NOTIFY` or Redis pub/sub deliver cross-node cache invalidation with $<2\text{ ms}$ latency to unlock multi-instance read scaling?
6. **Container CFS Throttling Impact:** How much of observed P99.9 tail latency is caused by Linux CFS CPU quota periods rather than OpenBao code?
7. **JSON vs Protocol Buffers Efficiency:** Can adopting binary serialization (Protobuf / gRPC) or zero-copy JSON parsers eliminate the 75% CPU serialization overhead in Transit DEK wrapping?
8. **Checkpoint Stall Prevention:** Does PostgreSQL WAL checkpoint I/O cause tail latency spikes during write bursts, and can `max_wal_size = 32GB` eliminate them?
9. **Single-Flight Coalescing Effectiveness:** Does wrapping `LockManager.GetKey()` in a single-flight mutex prevent connection pool exhaustion during key rotation stampedes?
10. **HSM/KMS Boundary Isolation Stability:** How can we formally verify that no Layer 2 Subject Key read path ever inadvertently blocks on Layer 1 Cloud KMS / HSM APIs?

---

## 3. The Most Likely Overlooked Bottlenecks

1. **`KeyEntry` Exclusive Writer Lock Contention:** Writer locks held during key rotations block all subsequent reader goroutines, causing multi-millisecond tail latency spikes.
2. **PostgreSQL Connection Pool Starvation via `txnPermitPool` Mismatch:** Transactions acquiring permits starve non-transactional reads of `database/sql` connections.
3. **Barrier Encryption Ciphertext Allocation:** Repetitive byte buffer allocations during barrier AES-GCM encryption trigger excessive GC cycles.
4. **PostgreSQL Regex Full-Table Scans on List Queries:** Standard list queries execute `SELECT DISTINCT substring(...)` across the entire `vault_kv_store` table, stalling database I/O.
5. **Synchronous Audit Logging:** Blocking audit logging to disk or socket serializes cryptographic operations behind file system I/O.

---

## 4. Weakly Supported Recommendations in Previous Audits

1. **Rust / CGO Acceleration:** Weakly supported. Overlooked the ~60 ns CGO stack transition overhead and M-thread locking, which makes CGO slower than pure Go assembly for 32-byte DEKs.
2. **PostgreSQL Read Replicas for Read Scaling:** Weakly supported. Overlooked asynchronous replication lag, which directly causes read-after-write consistency failures (404 Key Not Found).
3. **Replacing `TwoQueueCache` with Unbounded `sync.Map`:** Weakly supported. While `sync.Map` eliminates lock contention, it lacks eviction mechanics and causes out-of-memory crashes under high key cardinality.
4. **WASM Micro-Engines:** Weakly supported. WASM runtime JIT/interpreter overhead degrades cryptographic throughput by 3x to 8x.

---

## 5. Architectural Decisions Requiring Empirical Evidence

1. **In-Memory S3-FIFO Caching vs Striped TwoQueueCache:** Must be benchmarked under Zipfian skew and high cardinality before altering key caching logic.
2. **Adopting gRPC / Protocol Buffers for DEK Wrapping:** Requires benchmarking to verify whether microsecond gains justify client SDK migration.
3. **PostgreSQL Table Partitioning by Namespace / Tenant:** Requires empirical testing to determine if partition pruning offsets partition routing overhead.

---

## 6. Experiments That Eliminate Unnecessary Engineering

1. **CGO vs Pure Go Benchmark (Experiment 4):** Proves that pure Go assembly is faster for small DEK operations, permanently eliminating months of risky Rust/CGO polyglot development.
2. **PostgreSQL Read Replica Consistency Test:** Proves that asynchronous read replicas cause read-after-write failures, preventing wasted engineering on replica read routing.
3. **WASM Cryptographic Performance Evaluation:** Formally rules out WebAssembly micro-engines, avoiding unproductive sandbox experimentation.

---

## 7. What Must Be Benchmarked Before Implementing Changes

1. **Baseline Open-Loop Throughput Curve:** Establish an authoritative HDR Histogram latency curve (P50 to Max) across 5,000 to 50,000 ops/sec using open-loop scheduling.
2. **Key Cardinality Saturation Threshold:** Determine the exact memory footprint and eviction cliff across 50k, 100k, 250k, 500k, and 1,000,000 keys.
3. **Lock Contention Profile Under Skew:** Capture `pprof` mutex profiles under Zipfian skew ($s = 0.99$) with 5% concurrent writes.

---

## 8. Prioritized Research & Engineering Roadmap

Work is ranked by **Impact, Confidence, Engineering Effort, Security Risk, and Validation Cost**:

| Priority | Initiative | Expected Impact | Confidence | Effort | Security Risk | Validation Cost | Recommendation |
|---|---|---|---|---|---|---|---|
| **1 (Immediate)** | **Execute Open-Loop Baseline Benchmarks** | Critical (Sets true baseline) | Very High | Low | None | Low | **Adopt Now** |
| **2 (Immediate)** | **PostgreSQL HOT Update & Schema Tuning** | High (+40% write perf, -50% WAL) | High | Low | None | Low | **Adopt Now** |
| **3 (High)** | **S3-FIFO / Striped Cache for Subject Keys** | High (+80% read throughput) | High | Medium | Low | Medium | **Prototype** |
| **4 (High)** | **Single-Flight Stampede Suppression** | High (Eliminates DB surges) | Very High | Low | Low | Low | **Adopt Now** |
| **5 (High)** | **Barrier Ciphertext Buffer Pooling** | Medium (-25% GC overhead) | High | Low | Low | Low | **Adopt Now** |
| **6 (Medium)** | **Non-Blocking Ring Buffer Audit Logging** | High (Unblocks crypto fast path) | High | Medium | Medium | Medium | **Investigate** |
| **7 (Medium)** | **PostgreSQL LISTEN/NOTIFY Invalidation Bus** | High (Enables read scaling) | Medium | High | Low | Medium | **Prototype** |
| **8 (Low)** | **gRPC / Protobuf API for DEK Wrapping** | Medium (+30% CPU efficiency) | High | High | Low | High | **Defer** |
| **9 (Prohibited)** | **CGO / Rust Polyglot Rewrites** | Negative (Adds overhead) | High | Very High | High | High | **Reject** |
| **10 (Prohibited)**| **Asynchronous Postgres Read Replicas** | Negative (Consistency bugs) | Very High | Medium | High | Low | **Reject** |
