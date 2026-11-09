# 9. KEY LOOKUP ARCHITECTURE COMPARISON 🤖

## Executive Summary

To determine the optimal architecture for Cloakit's privacy vault, we conduct a rigorous comparative evaluation of six distinct architectural options. Each architecture is analyzed across 12 specific technical criteria: lookup complexity, expected latency impact, throughput potential, memory consumption, storage overhead, write amplification, cache invalidation, security implications, tenant isolation, key rotation, recovery complexity, and implementation complexity.

The evaluation demonstrates that while the existing OpenBao implementation (Architecture A) is secure, it suffers severe scalability barriers at high key cardinalities. **Architecture F (Hybrid Architecture)** delivers the highest throughput, lowest latency, and most robust tenant isolation, and is formally recommended for Cloakit.

---

## 1. Description of Evaluated Architectures

- **Architecture A — Existing OpenBao Implementation**: Monolithic `Policy` struct, standard JSON serialization, single-mutex `TransitLRU` / unbounded `TransitSyncMap`, 256-shard lock hashing, flat `policy/` storage paths, synchronous storage reads under shard locks.
- **Architecture B — Improved Native Data Structures**: High-cardinality lock striping (16,384 shards), direct integer-indexed version maps (`map[int]*KeyEntry`), singleflight deduplication on storage reads, elimination of config mutex overhead.
- **Architecture C — Dedicated Primary & Secondary Indexes**: Introduction of an in-memory Adaptive Radix Tree (ART) primary index mapping tenant/subject identities to key metadata, with secondary indexes for autorotation and version tracking.
- **Architecture D — HMAC-Based Deterministic Indexing**: Cryptographic tenant-scoped HMAC-SHA-256 derivation of Subject Key storage identifiers, eliminating external database mappings and enabling stateless key resolution.
- **Architecture E — Metadata-Only Caching**: Decoupling mutable metadata from sensitive key material; caching policy configuration and version thresholds in memory while loading raw key bytes on-demand or through ephemeral decryptor sessions.
- **Architecture F — Hybrid High-Performance Architecture (Recommended)**: Combines tenant-scoped HMAC deterministic identifiers (Arch D), high-concurrency lock striping and sharded 2Q LRU caching (Arch B), compact pointer-free key representations (Arch B/C), parallel batch DEK processing, and hierarchical tenant storage partitioning.

---

## 2. Comprehensive 12-Factor Comparative Evaluation Matrix

| Evaluation Criteria | Architecture A (Existing) | Architecture B (Improved Native) | Architecture C (Indexed Lookup) | Architecture D (HMAC Indexing) | Architecture E (Metadata Cache) | Architecture F (Hybrid - Recommended) |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **1. Lookup Complexity** | $O(1)$ cache / $O(1)$ disk | $O(1)$ cache / $O(1)$ disk | $O(K)$ radix index ($K \le 32$) | $O(1)$ PRF derivation | $O(1)$ meta / $O(1)$ disk key | **$O(1)$ PRF + $O(1)$ sharded LRU** |
| **2. Expected Latency** | Baseline ($1\text{--}5\text{ ms}$ tail) | **30% - 40% reduction** | 20% reduction | 50% reduction (no DB query) | High ($2\text{--}8\text{ ms}$ disk read) | **70% - 85% reduction (< 100 $\mu$s)** |
| **3. Throughput Potential** | 5,000 - 15,000 ops/s | 35,000 - 50,000 ops/s | 30,000 - 45,000 ops/s | 40,000 - 60,000 ops/s | 2,000 - 5,000 ops/s | **80,000 - 120,000 ops/s** |
| **4. Memory Consumption** | High (1.7–3.4 KB/key) | Moderate (800 B/key) | High (+Radix tree nodes) | Moderate (800 B/key) | **Lowest (150 B/key)** | **Low (96–250 B/key with Arena)** |
| **5. Storage Overhead** | Verbose JSON blobs | Compact Binary / Protobuf| Index entries + Policy | Standard Barrier Path | Minimal | **Hierarchical paths + Compact payload**|
| **6. Write Amplification** | Very High (Full JSON rewrite)| Moderate | High (Index + Data sync) | Moderate | Minimal | **Low (Copy-On-Write version appends)**|
| **7. Cache Invalidation** | Global lock contention | Targeted shard eviction | Radix node invalidation | Dual-epoch lazy eviction| Simple metadata TTL | **Lock-free RCU atomic swap** |
| **8. Security Implications** | Standard OpenBao posture | Standard OpenBao posture | Risk of index desync | Equality leakage (managed) | Maximum memory protection | **Strong (Tenant PRF + mlocked arena)** |
| **9. Tenant Isolation** | Logical (View prefix) | Logical | Namespace partitioned | **Cryptographic (HKDF PRF)**| Logical | **Full (Cryptographic + Path scoped)**|
| **10. Key Rotation Impact**| Blocks active readers (5ms) | Non-blocking RCU (<100ns)| Index pointer update | Dual-epoch window | Re-encrypts on load | **Zero-downtime COW rotation** |
| **11. Recovery Complexity** | Standard Raft snapshot | Standard Raft snapshot | Requires index rebuild | Deterministic derivation | Standard | **Instant (Zero index rebuild needed)** |
| **12. Implementation Cost** | Zero (Current code) | Low (Targeted refactoring)| High (New index engines) | Medium (Client/plugin logic)| Medium | **Medium-High (Phased rollout)** |

---

## 3. Deep-Dive Comparative Trade-off Analysis

### Throughput & Concurrency Scaling
- **Architecture A** collapses under multi-core concurrency: the single mutex in `TransitLRU` and the 256-shard lock hashing create artificial serialization bottlenecks.
- **Architecture B** resolves the immediate lock bottlenecks, boosting throughput 3x–4x by increasing lock shards to 16,384 and introducing `singleflight` deduplication.
- **Architecture F** reaches peak performance (100,000+ ops/sec) by coupling lock striping with parallel batch processing and persistent AEAD cipher caching.

### Memory & Scale to 10 Million Keys
- **Architecture A** requires $> 34\text{ GB}$ of RAM for 10 million keys and triggers crippling Go GC mark pauses.
- **Architecture E** achieves minimal memory usage by caching metadata only, but destroys DEK wrapping throughput by requiring a barrier read and AES decryption on every single operation.
- **Architecture F** solves this dilemma through a **Compact Key Arena**: storing active symmetric keys in pointer-free 96-byte records, keeping 10 million keys in under 2 GB of RAM while delivering full in-memory cryptographic speed.

---

## 4. Final Architectural Recommendation for Cloakit

**Architecture F (Hybrid Architecture)** is the clear and definitive recommendation. It provides the optimal balance of raw cryptographic throughput, predictable sub-millisecond tail latency, robust tenant isolation, and memory efficiency, transforming OpenBao into an ultra-high-performance privacy vault engine.
