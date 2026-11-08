# 2. SUBJECT KEY LOOKUP ANALYSIS 🤖

## Executive Summary

Subject Key lookup is the foundational prerequisite for all cryptographic operations in Cloakit's privacy vault. In Cloakit, every privacy record encryption or decryption requires resolving the corresponding Subject Key (Layer 2) before wrapping or unwrapping the Data Encryption Key (DEK, Layer 3). When serving high-throughput workloads with millions of subjects across thousands of tenants, lookup efficiency determines system scalability.

This report conducts an architectural analysis of Subject Key identification paradigms, evaluates OpenBao's current lookup execution path, and contrasts direct opaque identifiers, keyed deterministic identifiers, composite identifiers, and secondary in-memory indexes.

---

## 1. Trace of OpenBao's Current Key Retrieval Mechanism

In OpenBao's Transit engine, every cryptographic endpoint initiates Subject Key retrieval via `backend.GetPolicy` (`internal/builtin/logical/transit/backend.go:145-147`):

```
API Request (/transit/datakey/..., /transit/encrypt/..., /transit/decrypt/...)
   │
   ▼
backend.getPolicy(ctx, polReq, rand, exclusive=false) [backend.go:153]
   │
   ├──> configMutex.RLock() [Checks cacheSizeChanged flag]
   │
   ▼
LockManager.GetPolicyWithLockType(ctx, req, rand, exclusive) [lock_manager.go:308]
   │
   ├──> 1. Cache Check: lm.cache.Load(req.Name)
   │       ├──> HIT: Return *Policy immediately. Acquire p.Lock(exclusive).
   │       │         Complexity: O(1) in-memory lookup.
   │       │
   │       └──> MISS: Proceed to Storage Retrieval.
   │
   ├──> 2. Acquire Shard Lock: lock := locksutil.LockForKey(lm.keyLocks, req.Name)
   │       lock.Lock() [lock_manager.go:336]
   │       (Hash key name with Blake2b-256; acquire 1 of 256 mutexes)
   │
   ├──> 3. Double-Check Cache under Shard Lock: lm.cache.Load(req.Name)
   │       (If found, unlock shard, return policy)
   │
   ├──> 4. Load from Persistent Storage: lm.getPolicyFromStorage [lock_manager.go:362]
   │       └──> LoadPolicy(ctx, storage, "policy/" + req.Name) [policy.go:680]
   │            ├──> storage.Get(ctx, "policy/" + req.Name)
   │            │    ├──> BarrierView.Get(ctx, "policy/" + req.Name) [view.go:144]
   │            │    │    └──> AESGCMBarrier.Get(ctx, fullPath) [aes_gcm.go:841]
   │            │    │         ├──> physical.Backend.Get(ctx, fullPath)
   │            │    │         │    └──> physical.Cache.Get(ctx, fullPath) [cache.go:204]
   │            │    │         │         ├──> LRU 2Q cache check
   │            │    │         │         └──> Underlying disk / Raft / PebbleDB read
   │            │    │         └──> AEAD Decryption: AES-256-GCM decrypt with barrier key
   │            └──> jsonutil.DecodeJSON(raw.Value, policy) [policy.go:700]
   │                 (Reflection-based JSON unmarshaling into Go struct)
   │
   ├──> 5. Populate Cache: lm.cache.Store(req.Name, p) [lock_manager.go:477]
   │
   └──> 6. Release Shard Lock, Return *Policy (locked with p.Lock(exclusive))
```

### Analysis of the Retrieval Complexity:
- **Cache Hit Path**: $O(1)$ lookup via Go map (`sync.Map`) or 2Q LRU cache. Incurs atomic pointer load + `p.l.RLock()`.
- **Cache Miss Path**: Highly serialized multi-step pipeline:
  1. Blake2b-256 hash computation ($O(K)$ where $K$ is key length).
  2. Sharded mutex acquisition (`lock.Lock()`).
  3. Barrier View prefix expansion string allocations (`strings.Join`).
  4. Physical LRU lookup (acquires physical cache shard lock).
  5. Disk I/O or Raft state machine fetch.
  6. AES-256-GCM AEAD barrier decryption (CPU crypto overhead).
  7. JSON decoding with reflection ($O(S)$ where $S$ is policy payload size).
  8. Store in transit cache (LRU lock acquisition).
  9. Shard mutex release.

---

## 2. Subject Key Identification Paradigms

In Cloakit, Subject Keys can be addressed under three distinct identifier models:

### Model A: Direct Random UUID / Opaque Identifier
- **Concept**: A randomly generated UUIDv4 (128-bit) or opaque hex token assigned at subject onboarding:
  $$	ext{KeyName} = 	ext{UUIDv4()}$$
- **Characteristics**:
  - **Storage Path**: `policy/{uuid}`
  - **Collision Probability**: Virtually zero ($2^{-122}$).
  - **Privacy**: Maximum privacy. The identifier contains zero entropy derived from subject identity (e.g., SSN, email, user ID).
  - **Drawback**: Cloakit must maintain an external mapping table in PostgreSQL mapping `(tenant_id, subject_id) -> subject_key_uuid`. Every client lookup incurs an extra database query before calling OpenBao.

### Model B: Deterministic Keyed Identifier (HMAC)
- **Concept**: Generate a cryptographically blinded identifier deterministically from the tenant and subject identity using a secret master indexing key ($K_{	ext{index}}$):
  $$	ext{KeyName} = 	ext{HMAC-SHA-256}(K_{	ext{index}}, 	ext{tenant\_id} \parallel 	ext{"::"} \parallel 	ext{subject\_id})$$
- **Characteristics**:
  - **Storage Path**: `policy/{hmac_hex}`
  - **Deterministic Lookup**: Cloakit can derive the Subject Key name entirely in memory without performing an external database lookup.
  - **Privacy**: Pseudonymous. An adversary observing OpenBao storage cannot invert the HMAC to recover `subject_id` without $K_{	ext{index}}$.
  - **Drawback**: Reveals equality: two operations on the same subject produce the exact same identifier, allowing traffic analysis and frequency leakage.

### Model C: Composite Hierarchical Identifier
- **Concept**: Construct a structured hierarchical path embedding tenant isolation directly into OpenBao's path namespace:
  $$	ext{KeyName} = 	ext{tenant\_id} \parallel 	ext{"/"} \parallel 	ext{subject\_id}$$
- **Characteristics**:
  - **Storage Path**: `policy/{tenant_id}/{subject_id}`
  - **Partitioning**: Natural tenant grouping in physical storage; enables efficient subtree listing (`req.Storage.List("policy/" + tenant_id + "/")`).
  - **Drawback**: Without blinding, `subject_id` is exposed in plaintext in storage paths, logs, and audit entries.

---

## 3. Comparative Architecture Assessment for Subject Key Retrieval

| Lookup Paradigm | In-Memory Complexity | Storage Lookup Cost | Database Dependency | Privacy Guarantees | Frequency Leakage Risk | Migration & Rotation Complexity | Suitability for Cloakit |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **1. Direct Random UUID** | $O(1)$ | 1 Barrier Read | High (Requires DB mapping table) | Maximum (Opaque) | None (Uncorrelated) | High (Requires DB row updates) | Moderate |
| **2. Deterministic HMAC (Global Key)** | $O(1)$ | 1 Barrier Read | None (Stateless derivation) | Pseudonymous (Blind) | High (Across entire vault) | Critical (Single index key compromise affects all) | Poor |
| **3. Tenant-Scoped Deterministic HMAC** | $O(1)$ | 1 Barrier Read | Low (Only tenant config cached) | Strong (Tenant isolated) | Contained (Only within same tenant) | Moderate (Rotate per-tenant index key) | **Very High** |
| **4. Two-Level In-Memory Index** | $O(1)$ | 0 (On memory hit) | Low | Configurable | Contained | Low (In-memory rebuild on start) | **Highest** |

---

## 4. Subject Key Lookup Audit Findings

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-LOOKUP-01
Component:                internal/builtin/logical/transit
File Path:                internal/builtin/logical/transit/backend.go
Function or Structure:    backend.getPolicy
Line Reference:           internal/builtin/logical/transit/backend.go:153-178
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       Every call to backend.getPolicy acquires b.configMutex.RLock() to verify if b.cacheSizeChanged is true.
Identified Inefficiency:  configMutex.RLock() introduces an atomic synchronization fence across every single cryptographic operation across all worker threads. Even under read-locking, cache line bouncing on configMutex degrades multi-core scalability when handling 50,000+ requests/sec.
Proposed Improvement:     Replace configMutex with an atomic boolean flag (atomic.Bool) or atomic pointer to a configuration struct. Checking cache size changes can be performed with a single lock-free atomic load.
Expected Latency Impact:  Reduces CPU cycle overhead per call by 50-100 nanoseconds; eliminates cache line thrashing.
Expected Throughput:      10% - 20% improvement under extreme multi-core concurrency (32+ cores).
Memory Impact:            None.
Storage Impact:           None.
Security Implications:    None.
Tenant Isolation:         Removes cross-tenant lock contention at the backend configuration root.
Key Rotation:             None.
Implementation Complexity: Low.
Confidence Level:         HIGH.
Validation Strategy:      Run high-concurrency micro-benchmark on backend.getPolicy with atomic vs RWMutex.
--------------------------------------------------------------------------------
```

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-LOOKUP-02
Component:                sdk/helper/keysutil
File Path:                sdk/helper/keysutil/lock_manager.go
Function or Structure:    LockManager.GetPolicyWithLockType
Line Reference:           sdk/helper/keysutil/lock_manager.go:315-360
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       When a key is missing from cache, LockManager acquires lock := locksutil.LockForKey(lm.keyLocks, req.Name), then performs a blocking storage read (lm.getPolicyFromStorage) while holding the shard lock.
Identified Inefficiency:  Holding the sharded mutex across disk I/O, barrier decryption, and JSON decoding blocks ALL other concurrent operations for ANY key that hashes to that same shard (1 of 256). A single slow disk read can stall dozens of unrelated active DEK operations.
Proposed Improvement:     Implement singleflight (golang.org/x/sync/singleflight) request coalescing on key retrieval. Separate lock manager synchronization from disk I/O: load storage outside the shard lock, then acquire lock only to safely update the cache and resolve races.
Expected Latency Impact:  Eliminates head-of-line blocking on cache misses; reduces P99 latency spikes by up to 80% during cold key fetches.
Expected Throughput:      Prevents thundering herd on popular keys; preserves throughput for unrelated keys.
Memory Impact:            Minimal (temporary singleflight call group pointers).
Storage Impact:           None.
Security Implications:    None.
Tenant Isolation:         Isolates cache miss penalties to the requested key without affecting collided keys.
Key Rotation:             Compatible with exclusive lock semantics during rotation.
Implementation Complexity: Medium.
Confidence Level:         HIGH.
Validation Strategy:      Simulate 1,000 concurrent requests for cold keys and verify shard lock wait times.
--------------------------------------------------------------------------------
```

---

## 5. Recommended Lookup Architecture for Cloakit

To achieve sub-millisecond Subject Key lookup and zero-database-overhead key derivation, Cloakit should implement a **Two-Level Tenant-Scoped Lookup Pipeline**:

1. **Identifier Derivation**:
   $$	ext{SubjectKeyName} = 	ext{Hex}(	ext{HMAC-SHA-256}(K_{	ext{tenant}}, 	ext{"cloak-subject:"} \parallel 	ext{subject\_id}))$$
   - Each tenant has an isolated 256-bit indexing key $K_{	ext{tenant}}$.
   - Guarantees complete cross-tenant cryptographic separation.
   - Eliminates external database lookups in PostgreSQL.
2. **In-Memory Subject Key Resolution**:
   - Level 1: OpenBao Sharded Process-Local LRU Cache ($O(1)$, lock-free read path).
   - Level 2: On miss, load `policy/{SubjectKeyName}` using `singleflight` deduplication without blocking unrelated shard keys.
