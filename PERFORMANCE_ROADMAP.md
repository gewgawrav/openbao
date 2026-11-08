# OpenBao Performance & Scalability Roadmap :robot:

This document establishes the recommended engineering roadmap, implementation sequence, risk assessment matrix, and verification criteria for resolving the performance bottlenecks identified in the OpenBao audit.

---

## Recommended Implementation Sequence

The remediation plan is structured across three execution phases, prioritized by the ratio of performance benefit to architectural risk.

```
+-------------------------------------------------------------------------------+
| PHASE 1: Quick Wins & Micro-Allocations (Low Risk, Immediate Return)         |
| - Transit cipher reuse & AEAD caching                                        |
| - AESGCMBarrier buffer recycling with sync.Pool                              |
| - Fast-path user-space request UUID generation                               |
| - Router consolidated route lookup                                           |
+-------------------------------------------------------------------------------+
                                      |
                                      v
+-------------------------------------------------------------------------------+
| PHASE 2: Core Engine & Subsystem Concurrency (Medium Risk, High Return)      |
| - Immutable ACL compilation & caching                                        |
| - Sharded striped locking in ExpirationManager                               |
| - Atomic multi-write Raft commits for TokenStore                             |
| - Single-pass streaming snapshot in Raft FSM                                 |
+-------------------------------------------------------------------------------+
                                      |
                                      v
+-------------------------------------------------------------------------------+
| PHASE 3: Architectural Scaling & Storage Modernization (Higher Complexity)   |
| - Asynchronous / Ring-Buffered Audit logging pipeline                        |
| - Identity StoragePacker sub-key indexing on LSM backends                    |
| - High-throughput PKI certificate batching & storage offload                 |
| - Multi-Raft / Consensus partitioning evaluation                             |
+-------------------------------------------------------------------------------+
```

---

## Detailed Phase Breakdown

### Phase 1: Quick Wins & Micro-Allocations (Weeks 1–3)
**Goal**: Eliminate unnecessary heap allocations and redundant computation with minimal code modification.

1. **Transit AEAD Cipher Reuse (`FIND-CRYPTO-01`)**
   - *Action*: Cache constructed `cipher.AEAD` directly on `keysutil.KeyEntry` or instantiate once per key version across the batch in `pathEncryptWrite`.
   - *Impact*: Eliminates 42.6% of heap memory allocation in batch cryptographic operations.
   - *Effort*: 2 engineer-days.
   - *Verification*: `go test -run=^$ -bench=BenchmarkTransit_Batch -benchmem ./internal/builtin/logical/transit/`

2. **AES-GCM Barrier Buffer Pooling (`FIND-BARRIER-01`)**
   - *Action*: Introduce `sync.Pool` for ciphertext output buffers in `AESGCMBarrier.encrypt` and `decrypt`.
   - *Impact*: Reduces barrier memory allocation by ~80%, lowering overall GC CPU pressure.
   - *Effort*: 3 engineer-days.
   - *Verification*: Barrier microbenchmark suite.

3. **User-Space UUID Generation & Header Pooling (`FIND-HTTP-01`)**
   - *Action*: Replace `uuid.GenerateUUID()` in `buildLogicalRequestNoAuth` with a fast thread-local/atomic generator for request trace IDs; avoid defensive `r.Header.Clone()`.
   - *Impact*: Eliminates kernel syscalls on every incoming HTTP request; saves 1–2 µs per request.
   - *Effort*: 2 engineer-days.
   - *Verification*: HTTP handler benchmark suite.

4. **Router Lookup Consolidation (`FIND-ROUTER-01`)**
   - *Action*: Replace multiple separate calls to `MatchingMountEntry`, `MatchingBackend`, and `MatchingSystemView` with a single `MatchingRouteEntry` lookup attached to the request context.
   - *Impact*: Reduces mutex acquisitions and radix tree traversals on every logical request.
   - *Effort*: 3 engineer-days.
   - *Verification*: Request handling benchmarks.

---

### Phase 2: Core Engine & Subsystem Concurrency (Weeks 4–8)
**Goal**: Remove multi-core lock contention bottlenecks and multi-roundtrip storage patterns.

1. **Immutable Compiled ACL Caching (`FIND-CORE-01`)**
   - *Action*: Cache compiled `*ACL` objects in `policy/policy_store.go` keyed by policy set hash and entity ID. Invalidate cache on policy updates.
   - *Impact*: 40% reduction in P50/P95 latency for token-authenticated endpoints; eliminates hundreds of allocations per request.
   - *Effort*: 2 engineer-weeks.
   - *Verification*: Token authentication benchmarks.

2. **ExpirationManager Sharded Striped Locking (`FIND-LEASE-01`)**
   - *Action*: Replace global `m.pendingLock` with 256 striped mutexes partitioned by `hash(leaseID)`. Convert `leaseCount` and `irrevocableLeaseCount` to atomic counters.
   - *Impact*: Removes the primary concurrency bottleneck for dynamic secret and token issuance.
   - *Effort*: 1.5 engineer-weeks.
   - *Verification*: `BenchmarkExpiration_Create_Leases` with 32 parallel goroutines.

3. **Atomic Multi-Write Raft Commits for TokenStore (`FIND-TOKEN-01`)**
   - *Action*: Use `logical.WithTransaction` to commit accessor index, parent index, and token entry into a single Raft log entry.
   - *Impact*: 65% reduction in service token creation latency; 3x increase in token issuance throughput.
   - *Effort*: 2 engineer-weeks.
   - *Verification*: `BenchmarkTokenStore_HandleCreateCommon`.

4. **Single-Pass Streaming Raft Snapshot (`FIND-RAFT-01`)**
   - *Action*: Rewrite `FSM.writeTo` to stream BoltDB entries directly to the snapshot sink while computing SHA-256 and metadata via a hashing tee-writer.
   - *Impact*: 50% reduction in snapshot duration; prevents disk bandwidth starvation during Raft snapshots.
   - *Effort*: 1 engineer-week.
   - *Verification*: Raft snapshot benchmarks with 100K+ keys.

---

### Phase 3: Architectural Scaling & Storage Modernization (Weeks 9–14)
**Goal**: Address fundamental architectural bottlenecks for enterprise scale.

1. **Asynchronous / Ring-Buffered Audit Pipeline (`FIND-AUDIT-01`)**
   - *Action*: Decouple HTTP request completion from synchronous disk writes using an in-memory lock-free ring buffer and batch flusher.
   - *Impact*: 75% reduction in P99 tail latency; isolates API latency from storage I/O jitter.
   - *Effort*: 3 engineer-weeks.
   - *Verification*: Parallel audit benchmarks under injected disk latency.

2. **Identity StoragePacker Sub-Key Indexing (`FIND-ID-01`)**
   - *Action*: Refactor identity storage on LSM-capable backends (PebbleDB, Raft BoltDB) to store individual entities under hierarchical prefixes rather than packed blobs.
   - *Impact*: Eliminates 450 µs full-bucket read-modify-write recompression cycles on user logins.
   - *Effort*: 3 engineer-weeks.
   - *Verification*: StoragePacker benchmark suite and high-concurrency login tests.

3. **High-Throughput PKI Storage Offload (`FIND-PKI-01`)**
   - *Action*: Optimize certificate issuing paths with asynchronous storage write buffering and native `no_store` support for ephemeral service certificates.
   - *Impact*: Increases PKI certificate issuance throughput from ~200 certs/sec to >2,000 certs/sec.
   - *Effort*: 2 engineer-weeks.
   - *Verification*: PKI issuance throughput benchmarks.

---

## Risk Assessment Matrix

| Optimization | Architectural Risk | Failure Mode | Mitigation Strategy |
|---|---|---|---|
| **ACL Caching** | Low | Stale policy applied after update | Robust cache invalidation on `SetPolicy`/`DeletePolicy` tied to cluster-wide invalidation events |
| **Cipher Reuse** | Low | Race conditions on concurrent encrypts | `cipher.AEAD` implementations in Go standard library are safe for concurrent `Seal`/`Open` with distinct nonces |
| **Audit Ring Buffer** | Medium | Potential audit loss on ungraceful crash | Bounded queue with blocking backpressure; flush-on-shutdown hooks; optional synchronous durability mode |
| **Expiration Sharding** | Low | Partition imbalance | High-entropy 64-bit FNV/Murmur hash distribution across 256 shards |
| **Token Raft Batching** | Medium | Partial write failure | Enforce atomic transaction semantics inside `physical.TransactionalBackend` |
| **Raft Snapshot Streaming** | Low | Snapshot corruption | Validate SHA-256 digest on receiver before committing snapshot metadata |

---

## Resource Estimates & Success Metrics

### Engineering Investment
- **Phase 1**: 2 Engineers × 3 Weeks (~6 engineering weeks)
- **Phase 2**: 2 Engineers × 5 Weeks (~10 engineering weeks)
- **Phase 3**: 2 Engineers × 6 Weeks (~12 engineering weeks)
- **Total Project Duration**: ~14 Weeks

### Key Performance Indicator (KPI) Targets
1. **API Latency (P50)**: Reduce token-authenticated read latency from ~1.2 ms to <0.5 ms (-58%).
2. **API Tail Latency (P99)**: Reduce P99 response time from ~15 ms to <3 ms under high concurrent load (-80%).
3. **Throughput (Peak QPS)**: Increase single-node KV read throughput from ~18,000 req/sec to >40,000 req/sec (+122%).
4. **Token Issuance Throughput**: Increase service token creation from ~450 tokens/sec to >1,500 tokens/sec (+233%).
5. **Memory Footprint**: Reduce GC allocation rate by >40% across sustained crypto and read workloads.
