# OpenBao Performance Implementation Strategy & Roadmap

## Executive Summary

This document specifies the engineering roadmap, benchmarking methodologies, risk mitigation protocols, and zero-downtime rolling upgrade strategies for implementing the architectural recommendations defined in `REARCHITECTURE_ASSESSMENT.md`, `ALTERNATIVE_ARCHITECTURES.md`, `WORKLOAD_OPTIMISATION.md`, and `ARCHITECTURE_DECISIONS.md`.

Rather than executing a high-risk monolithic rewrite, this strategy outlines an **evolutionary, three-phase implementation approach**:
- **Phase 1: Immediate In-Process Critical Path Optimizations** (Low risk, zero breaking changes, immediate 3x–5x gains).
- **Phase 2: Component-Level Modular Redesign** (Medium risk, lock-free pipelines, batch storage, HSM session pooling, 10x–20x gains).
- **Phase 3: Workload-Oriented Scale-Out & Multi-Raft Consensus** (Longer term, multi-consensus partitioning, Raft learner mesh, 50x+ cluster scale).

---

## 1. Phased Implementation Roadmap

```
2026 Q1 - Q2                    2026 Q3 - Q4                    2027 Q1+
[ PHASE 1: In-Process Opts ]   [ PHASE 2: Component Redesign ]  [ PHASE 3: Workload Scale-Out ]
───────────────────────────     ─────────────────────────────    ───────────────────────────────
• Speculative Existence Elision • Lock-Free RCU Transit Core    • Multi-Raft Storage Partitioning
• Sharded Zero-Copy Phys Cache  • Bounded Ring-Buffer Auditing  • Raft Learner Read-Replica Mesh
• Lock-Free AEAD Term Keyring   • In-Process PKCS#11 Pooling    • Stateless Crypto Worker Nodes
• In-Memory Token Cache         • Barrier & Raft Batch Put/Get  • Privacy Vault LSM Token Engine
• Transit Parallel Batch Crypto • Time-Wheel Lease Management   • Workload Ingress Smart Router
```

### 1.1 Phase 1: Immediate In-Process Critical Path Optimizations
**Timeline**: Months 1–3
**Target Areas**: Request pipeline, storage cache, token lookup, transit batching.
**Deliverables**:
1. **Speculative Route Existence Check Elision**:
   - Add `BackendHandlesExistence` interface in `sdk/logical/backend.go`.
   - Update `internal/vault/request_handling.go:478-508` to skip `RouteExistenceCheck()` when supported.
   - Update KV v2, Transit, and PKI backends to handle atomic upsert.
2. **Sharded Zero-Copy Physical Cache (`sdk/physical/cache.go`)**:
   - Partition `c.lru` into a 64-way sharded array using FNV-1a key hashing.
   - Refactor `cache.Get()` to return immutable byte slices, removing `copy(cacheEntry.Value, entry.Value)`.
3. **Lock-Free AEAD Term Keyring Dispatch (`internal/vault/barrier/aes_gcm.go`)**:
   - Replace `b.cache map[uint32]cipher.AEAD` and `b.cacheLock sync.RWMutex` with an `atomic.Pointer[[]cipher.AEAD]`.
   - Implement copy-on-write during keyring term rotation.
4. **In-Memory Service Token Cache (`internal/vault/token_store.go`)**:
   - Implement a sharded lock-free LRU cache for active `TokenEntry` structures.
   - Wire instantaneous eviction into `TokenStore.Revoke()`.
5. **Parallel Transit Batching (`internal/builtin/logical/transit/path_encrypt.go`)**:
   - Replace the sequential `for` loop over `batchInputItems` with a chunked worker dispatcher using `runtime.GOMAXPROCS(0)`.

**Expected Milestone Metric**: 4x throughput increase on KV reads (>140,000 RPS), 2.5x increase on KV writes (~9,000 IOPS), 8x increase on Transit batch crypto.

---

### 1.2 Phase 2: Component-Level Modular Redesign
**Timeline**: Months 4–8
**Target Areas**: Audit logging, transit lock-free RCU, multi-key storage barrier, HSM session management.
**Deliverables**:
1. **Lock-Free Bounded Ring-Buffer Audit Pipeline**:
   - Implement LMAX disruptor ring buffer in `internal/vault/audit_broker.go`.
   - Implement dedicated background committer goroutines with vectorized OS `writev` batch flushes.
   - Implement Little's Law adaptive backpressure (HTTP 429) when buffers reach 90% capacity.
2. **Lock-Free RCU Transit Policy Core (`sdk/helper/keysutil/`)**:
   - Decompose `keysutil.Policy` into an immutable `PolicySnapshot` wrapped in `atomic.Pointer`.
   - Refactor `Encrypt`, `Decrypt`, `Sign`, and `HMAC` to execute lock-free without mutex acquisition.
3. **Multi-Key Barrier & Raft Batching**:
   - Introduce `PutBatch` and `GetBatch` to `physical.Backend` and `SecurityBarrier`.
   - Implement batch apply handling in `internal/physical/raft/fsm.go:731` and PebbleDB backend.
4. **In-Process PKCS#11 Adaptive Session Pooling (`internal/vault/external_keys/`)**:
   - Add CGO-enabled PKCS#11 provider with a lock-free session pool of authenticated `CK_SESSION_HANDLE` slots.
5. **Hierarchical Time-Wheel Expiration Manager (`internal/vault/expiration.go`)**:
   - Replace monolithic `pendingLock` and `sync.Map` with hashed second/minute time-wheel buckets.

**Expected Milestone Metric**: Sub-millisecond p99 latency (<0.6 ms read, <2.5 ms write), Transit throughput exceeding 450,000 ops/sec, HSM throughput reaching hardware maximum (8,000–25,000 ops/sec).

---

### 1.3 Phase 3: Workload-Oriented Scale-Out & Multi-Raft Partitioning
**Timeline**: Months 9–14
**Target Areas**: Distributed consensus, horizontal scale, privacy vault engine, stateless workers.
**Deliverables**:
1. **Raft Follower Lease Reads & Non-Voting Learners**:
   - Implement Raft leader leases allowing standby nodes to serve linearizable reads locally.
   - Add support for non-voting Raft learner nodes across remote regions and edge clusters.
2. **Multi-Raft Consensus Partitioning**:
   - Partition OpenBao's physical keyspace across multiple independent Raft consensus groups.
   - Distribute partition leadership across cluster nodes, enabling horizontal write scaling.
3. **Dedicated Stateless Cryptographic Workers**:
   - Introduce a stateless transit/privacy worker node profile that synchronizes key snapshots over mTLS and executes compute purely in memory.
4. **Enterprise Privacy Vault LSM Engine**:
   - Direct LSM-tree storage pipeline for random tokenization mappings and blind index search.

**Expected Milestone Metric**: >1,000,000 reads/sec, >100,000 writes/sec cluster-wide, >5,000,000 stateless crypto ops/sec.

---

## 2. Benchmarking Harness & Verification Methodologies

To empirically validate performance improvements without regressing security or stability, OpenBao requires a standardized, reproducible benchmarking harness.

### 2.1 Benchmark Test Matrix
The benchmark harness simulates realistic enterprise production conditions across four primary dimensions:

| Test Suite | Workload Profile | Target Metrics | Saturation Condition |
| :--- | :--- | :--- | :--- |
| **Bench-KV-Read** | 100% KV v2 Reads (1 KB payload, 10,000 distinct keys) | RPS, p50, p95, p99, p99.9 latency, GC pause time | 100 to 2,000 concurrent client connections |
| **Bench-KV-Write** | 100% KV v2 Writes (1 KB payload, sequential & random) | IOPS, Raft commit latency, FSM apply duration | Sustained 10,000 writes/sec to Raft storage |
| **Bench-Transit-Crypto** | Symmetric (AES-256-GCM) & Asymmetric (RSA-4096, P-256) | Ops/sec, CPU core scaling (1, 8, 16, 32, 64 cores) | Single-key contention & 1,000-item batch inputs |
| **Bench-PKCS11-HSM** | External Key Signing & Decryption via SoftHSM / Luna HSM | Hardware ops/sec, session checkout wait time | 10 to 200 concurrent worker threads |
| **Bench-Noisy-Neighbor** | 100% CPU Transit batch load running simultaneously with KV read | KV read p99 latency degradation delta | Transit saturated at 100% host CPU cores |
| **Bench-Lease-Churn** | 500,000 leases with 1-second to 60-second TTLs | Lease creation RPS, eviction worker CPU, lock wait | Burst eviction of 50,000 expired leases |

### 2.2 Profiling & Instrumentation Toolchain
Each benchmark run must capture:
- **Go Execution Profiles**: CPU profile, heap allocation profile, mutex contention profile (`go tool pprof http://localhost:8200/debug/pprof/profile`).
- **Trace Analysis**: Goroutine scheduling delays and GC stop-the-world pauses (`go tool trace`).
- **Operating System Telemetry**: CPU context switches, L1/L2/L3 cache misses (via Linux `perf stat`), disk I/O queue depth, and memory bandwidth (via `vmstat` / `iostat`).
- **OpenBao Internal Telemetry**:
  - `vault.core.handle_request`
  - `vault.barrier.get` / `vault.barrier.put`
  - `vault.audit.log_request`
  - `vault.raft-storage.fsm.apply`
  - `vault.expire.job_manager`

---

## 3. Risk Management & Rollback Strategies

Architectural modifications to a zero-trust cryptographic system carry inherent risks. The implementation strategy employs strict defensive guardrails:

### 3.1 Failure Modes & Mitigation Table

| Risk / Failure Mode | Likelihood | Impact | Architectural Mitigation Protocol |
| :--- | :--- | :--- | :--- |
| **Audit Log Data Loss during Node Crash** | Low | Critical | Bounded ring buffer supports configurable `audit_sync_ack = true` for compliance tiers; zero-data-loss guarantee maintained via backpressure. |
| **Stale Reads during Raft Leader Lease Clock Drift** | Very Low | High | Leader lease duration strictly bounded to 250 ms with hardware monotonic clock validation; fallback to quorum read if drift exceeds 50 ms. |
| **Token Revocation Inconsistency** | Low | Critical | In-memory token cache purges synchronously before revocation storage commit; standby nodes receive push invalidation vectors via cluster gossip/gRPC. |
| **CGO PKCS#11 Vendor Driver Crash** | Medium | High | In-process CGO mode is optional; driver sandbox isolates initialization; out-of-process gRPC plugin remains default fallback. |
| **Memory Pressure from Unbounded Request Queues** | Medium | High | Adaptive Little's Law admission controller sheds traffic with RFC-compliant HTTP 429 when queue depth or memory reaches 85%. |

### 3.2 Zero-Downtime Rolling Upgrade Compatibility
All changes across Phases 1 and 2 maintain strict protocol and on-disk backward compatibility:
- **Storage Serialization**: The physical storage schema and AES-256-GCM barrier envelope format remain completely unchanged.
- **Raft Wire Protocol**: Raft log entries maintain protobuf backward compatibility. Mixed-version clusters (vCurrent and vNext) can coexist seamlessly during rolling upgrades.
- **Feature Flags**: Every major component redesign is guarded by an explicit configuration toggle:
  ```hcl
  performance {
    speculative_existence_elision = true
    sharded_storage_cache         = true
    sharded_storage_cache_size    = 524288
    lockfree_transit_rcu          = true
    audit_ring_buffer_size        = 65536
    hsm_session_pooling           = true
  }
  ```
- **Instant Rollback**: If an issue arises in production, operators can disable any optimization dynamically via HCL configuration reload (`SIGHUP` / `bao reload`) without node restarts or data migration.
