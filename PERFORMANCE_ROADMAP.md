# OpenBao Performance Engineering Roadmap

This document outlines the phased, risk-prioritized engineering roadmap to transform OpenBao into an ultra-high-throughput, predictable-latency security platform capable of supporting enterprise workloads.

---

## Phased Implementation Structure

The roadmap is structured into 6 sequential, value-oriented phases:
1. **Phase 1: Profiling and Baseline Measurement**
2. **Phase 2: Low-Risk Allocations and Computational Optimization**
3. **Phase 3: Storage and Cryptographic Optimization**
4. **Phase 4: Concurrency, Throughput and Lock Elimination**
5. **Phase 5: High Availability and Distributed Scalability**
6. **Phase 6: Continuous Performance Engineering and CI/CD Integration**

---

## Detailed Phase Breakdown

### Phase 1: Profiling and Baseline Measurement
*Objective: Establish reproducible benchmark baselines and profile CPU, memory, and lock contention across all critical paths.*

- **Task 1.1: Automated Benchmark Suite Initialization**
  - **Priority**: P0 (Immediate)
  - **Findings**: Cross-cutting (PERF-001 through PERF-020)
  - **Expected Benefit**: Precise data-driven baselines; eliminated guesswork.
  - **Implementation Complexity**: Low
  - **Dependencies**: None
  - **Security Risks**: None
  - **Testing Requirements**: Standard Go test execution on isolated hardware.
  - **Acceptance Criteria**: Baseline latency (p50, p95, p99) and ops/sec captured for Transit batch, Raft writes, and Auth tokens.

- **Task 1.2: End-to-End Tracing & Telemetry Instrumentation**
  - **Priority**: P1
  - **Findings**: PERF-003, PERF-005, PERF-012
  - **Expected Benefit**: Granular sub-microsecond span visibility in OpenTelemetry / pprof.
  - **Implementation Complexity**: Low
  - **Dependencies**: Task 1.1
  - **Security Risks**: Redact sensitive cryptographic material and auth tokens from trace spans.
  - **Testing Requirements**: Trace export unit tests.
  - **Acceptance Criteria**: Verified span propagation through core dispatch, barrier, and storage layers.

---

### Phase 2: Low-Risk Allocations and Computational Optimization
*Objective: Eliminate avoidable heap allocations, string copies, and redundant deserializations without altering system interfaces.*

- **Task 2.1: Barrier AES-GCM Buffer Pooling (`sync.Pool`)**
  - **Priority**: P0
  - **Findings**: PERF-006
  - **Expected Benefit**: 25-35% reduction in heap allocations during storage writes.
  - **Implementation Complexity**: Low
  - **Dependencies**: Phase 1
  - **Security Risks**: Memory security; pooled buffers must be strictly overwritten or wiped to prevent cross-tenant secret leakage.
  - **Testing Requirements**: Barrier unit tests with race detector and sanitization validation.
  - **Acceptance Criteria**: `BenchmarkClearView` shows zero additional byte allocations per entry.

- **Task 2.2: Policy ACL Path Radix Indexing**
  - **Priority**: P1
  - **Findings**: PERF-017
  - **Expected Benefit**: 40% reduction in policy evaluation CPU cycles for policy-dense tokens.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Phase 1
  - **Security Risks**: Critical; ACL access decisions must remain 100% equivalent to existing glob matching.
  - **Testing Requirements**: Exhaustive differential fuzz testing comparing existing `MatchesPath` against radix matcher.
  - **Acceptance Criteria**: Verified zero regressions across the complete test suite in `internal/vault/policy/`.

- **Task 2.3: Optimized JSON Serialization in Wrapped Responses**
  - **Priority**: P2
  - **Findings**: PERF-013, PERF-018
  - **Expected Benefit**: 10-15% latency reduction on large wrapped response handling.
  - **Implementation Complexity**: Low
  - **Dependencies**: Phase 1
  - **Security Risks**: None.
  - **Testing Requirements**: Test HTTP response wrapping and unwrapping endpoints.
  - **Acceptance Criteria**: Passing `internal/http/sys_wrapping_test.go`.

---

### Phase 3: Storage and Cryptographic Optimization
*Objective: Scale batch cryptographic operations and optimize Integrated Storage write serialization.*

- **Task 3.1: Transit Secrets Engine Concurrent Batch Execution**
  - **Priority**: P0 (Highest Impact)
  - **Findings**: PERF-001, PERF-002
  - **Expected Benefit**: 10x-30x throughput increase for Transit batch operations (`batch_input` >= 100).
  - **Implementation Complexity**: Low-Medium
  - **Dependencies**: Task 1.1
  - **Security Risks**: None; symmetric AEAD cryptography on independent batch items shares no state.
  - **Testing Requirements**: Full execution of `internal/builtin/logical/transit/` test suite with `-race`.
  - **Acceptance Criteria**: `BenchmarkTransit_BatchEncryption1000` execution time drops from ~30ms to <2ms on a 16-core machine.

- **Task 3.2: Integrated Storage Group Commit Proposal Coalescing**
  - **Priority**: P0
  - **Findings**: PERF-005, PERF-020
  - **Expected Benefit**: 4x-8x write throughput increase for concurrent Raft storage mutations.
  - **Implementation Complexity**: High
  - **Dependencies**: Phase 1
  - **Security Risks**: Raft linearizability and fsync crash durability must be preserved.
  - **Testing Requirements**: Jepsen / fault-injection partition tests and crash-recovery verification.
  - **Acceptance Criteria**: Write throughput reaches >15,000 writes/sec on NVMe storage under 250 concurrent clients.

- **Task 3.3: Storage Cache Partitioning and Sharded LRU**
  - **Priority**: P1
  - **Findings**: PERF-008
  - **Expected Benefit**: Eliminates global lock contention during cache misses; 3x improvement under 64-thread concurrent reads.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Phase 1
  - **Security Risks**: None.
  - **Testing Requirements**: Cache concurrency race tests in `sdk/physical/cache_test.go`.
  - **Acceptance Criteria**: Zero mutex wait time spikes during 10,000-key concurrent read storms.

---

### Phase 4: Concurrency, Throughput and Lock Elimination
*Objective: Remove lock serialization across the Transit engine, token store, and audit logging pipelines.*

- **Task 4.1: Lock-Free Transit Key Policy Lookups (Copy-on-Write)**
  - **Priority**: P0
  - **Findings**: PERF-014
  - **Expected Benefit**: Eliminates `p.RLock()` contention; linear scaling with CPU core count for single-key encryption.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Task 3.1
  - **Security Risks**: Atomic pointer updates must ensure readers never observe partially rotated key versions.
  - **Testing Requirements**: Concurrent key rotation and high-speed encryption test.
  - **Acceptance Criteria**: Zero lock wait time recorded in mutex profiling for `policy.Encrypt`.

- **Task 4.2: Pipelined Ring-Buffer File Audit Logging**
  - **Priority**: P0
  - **Findings**: PERF-003, PERF-004
  - **Expected Benefit**: Decouples API response latency from physical disk I/O; 4x-5x higher audited throughput.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Phase 1
  - **Security Risks**: Maintain fail-closed guarantees; if ring buffer fills or disk stalls, API must exert backpressure without dropping audit records.
  - **Testing Requirements**: Power-kill simulation and audit log truncation validation.
  - **Acceptance Criteria**: Audited API latency within 10% of unaudited API latency up to disk throughput limits.

- **Task 4.3: High-Performance Token Verification & Accessor Cache**
  - **Priority**: P1
  - **Findings**: PERF-009, PERF-012, PERF-015
  - **Expected Benefit**: Eliminates 80% of storage lookups for repetitive service token requests.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Phase 1
  - **Security Risks**: Token revocation must trigger instant invalidation across all cluster nodes.
  - **Testing Requirements**: Revocation latency tests and cross-node token invalidation tests.
  - **Acceptance Criteria**: Read requests using Service Tokens execute at >50,000 req/sec on leader.

---

### Phase 5: High Availability and Distributed Scalability
*Objective: Eliminate cluster leader write bottlenecks and optimize standby read-scaling pipelines.*

- **Task 5.1: Standby gRPC Invalidation Stream Coalescing**
  - **Priority**: P1
  - **Findings**: PERF-016
  - **Expected Benefit**: Prevents standby node starvation and cache-thrashing during high-volume leader write spikes.
  - **Implementation Complexity**: High
  - **Dependencies**: Phase 3
  - **Security Risks**: Standby nodes must never serve revoked secrets past acceptable replication lag bounds.
  - **Testing Requirements**: Multi-node replication lag tests under 20,000 writes/sec load.
  - **Acceptance Criteria**: Invalidation stream backlog remains bounded under 100ms.

- **Task 5.2: Expiration Manager Hierarchical Wheel Queuing**
  - **Priority**: P1
  - **Findings**: PERF-010, PERF-011
  - **Expected Benefit**: Supports 5,000,000+ active leases without memory bloat or revocation CPU storms.
  - **Implementation Complexity**: Medium-High
  - **Dependencies**: Phase 1
  - **Security Risks**: Timely revocation of credentials upon expiration must remain guaranteed.
  - **Testing Requirements**: Long-duration lease lifecycle tests with 1,000,000 concurrent leases.
  - **Acceptance Criteria**: Memory footprint per lease reduced by >50%; background revocation CPU consumption <5% total CPU.

- **Task 5.3: KMS / PKCS#11 Session Pool Optimization**
  - **Priority**: P2
  - **Findings**: PERF-019
  - **Expected Benefit**: Bounded HSM concurrency; elimination of session exhaustion crashes.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Phase 1
  - **Security Risks**: Non-exportable HSM boundary must remain inviolate.
  - **Testing Requirements**: Mock PKCS#11 concurrency stress test.
  - **Acceptance Criteria**: Predictable p99 latency without PKCS#11 `CKR_DEVICE_MEMORY` or session exhaustion errors.

---

### Phase 6: Continuous Performance Engineering and CI/CD Integration
*Objective: Institutionalize performance testing into every pull request to permanently prevent regressions.*

- **Task 6.1: Automated Benchmark Regression Testing in CI**
  - **Priority**: P0
  - **Findings**: All
  - **Expected Benefit**: Automatic detection of allocation, latency, or throughput regressions before merging code.
  - **Implementation Complexity**: Low-Medium
  - **Dependencies**: Phases 1-5
  - **Security Risks**: None.
  - **Testing Requirements**: GitHub Actions / CI workflow scripts.
  - **Acceptance Criteria**: CI automatically fails any PR that increases allocations or degrades throughput by >5% using `benchstat`.

- **Task 6.2: Nightly Macro-Workload Stress Testing**
  - **Priority**: P1
  - **Findings**: All
  - **Expected Benefit**: Continuous validation of multi-hour stability, leak prevention, and goroutine health.
  - **Implementation Complexity**: Medium
  - **Dependencies**: Task 6.1
  - **Security Risks**: None.
  - **Testing Requirements**: Automated nightly k6/Vegeta cluster harness.
  - **Acceptance Criteria**: Zero memory leaks or goroutine leaks detected across 12-hour sustained traffic runs.

---

## Roadmap Summary & Dependency Graph

```
[Phase 1: Baseline Profiling & Tracing]
         │
         ├──► [Phase 2: Low-Risk Allocations & Buffer Pools]
         │             │
         ├──► [Phase 3: Transit Parallelism & Raft Group Commits]
         │             │
         │             └──► [Phase 4: Lock Elimination & Audit Ring Buffers]
         │                           │
         └───────────────────────────┴──► [Phase 5: Standby Invalidation & Scalability]
                                                       │
                                                       └──► [Phase 6: CI/CD Continuous Benchmarks]
```

---
