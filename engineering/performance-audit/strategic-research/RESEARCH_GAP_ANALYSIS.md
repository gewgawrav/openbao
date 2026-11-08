# Research Gap Analysis: Strategic Performance & Architecture Discovery 🤖

## 1. Executive Summary

This Research Gap Analysis establishes the formal analytical foundation for the strategic architectural evaluation of OpenBao (`github.com/openbao/openbao/v2` and `sdk/v2`) within the context of the **Cloakit Privacy Vault**.

In the Cloakit deployment architecture, OpenBao functions as the critical **Layer 2 Subject Key management and cryptographic processing engine**. It operates beneath Layer 1 (Cloud KMS / HSM Client Key Encryption Keys - KEKs) and directly brokers, wraps, and unwraps Layer 3 Data Encryption Keys (DEKs) that protect sensitive customer records stored in PostgreSQL.

Prior audits in this repository (`engineering/performance-audit/` and branches covering Go runtime mechanics, lock contention, and key data structures) successfully explored point-level code characteristics, microbenchmarks, and local bottlenecks (such as `TokenStore` write amplification and transit LRU eviction behavior). However, significant strategic, systemic, and architectural unknowns remain unaddressed. This report delineates what has been definitively proven, exposes the architectural research gaps, and synthesizes the methodology required to navigate enterprise-grade scalability, multitenancy, fault tolerance, and cost efficiency.

---

## 2. Evidence Taxonomy Standard

To prevent speculative assertions and ensure engineering defensibility, every finding, evaluation, and recommendation across this strategic research initiative is classified according to the following strict evidence taxonomy:

| Classification | Definition | Application Criteria |
| :--- | :--- | :--- |
| **`SOURCE-CONFIRMED`** | Directly verified via inspection of OpenBao Go source code, struct layouts, control flows, and algorithms. | Must cite exact source file path, struct/function name, and line numbers. |
| **`STATIC-ANALYSIS-CONFIRMED`** | Verified via static tooling, AST inspection, compiler escape analysis (`-gcflags="-m"`), or dependency graph analysis. | Must provide reproducible compiler or static analysis tool invocation details. |
| **`TEST-CONFIRMED`** | Verified by running repository-local unit or integration test suites (`go test`). | Must cite the test function name and passing execution trace. |
| **`BENCHMARK-CONFIRMED`** | Verified via offline, repository-local Go benchmark harnesses (`go test -bench`). | Must provide exact throughput (`ns/op`), allocation count (`allocs/op`), and memory volume (`B/op`). |
| **`INFERRED`** | Architecturally deduced from system dynamics, queuing models, distributed consensus behavior, or mathematical scaling models. | Must articulate explicit assumptions, constraints, and analytical derivations. |
| **`UNKNOWN`** | Factors that cannot be measured or proven offline without external hardware (HSMs), production networks, live RDBMS, or customer telemetry. | Must state why it cannot be resolved offline and outline the required empirical validation test. |

---

## 3. Review of Prior Audits & Established Baseline

A rigorous review of completed audits across the repository reveals several foundational baselines:

1. **TokenStore Write Amplification (`SOURCE-CONFIRMED` / `BENCHMARK-CONFIRMED`):**
   - Source: `internal/vault/token_store.go:1200-1620`
   - Established Finding: Service token creation triggers 3 sequential physical storage writes (accessor entry, parent index entry, and primary ID entry), leading to a 3:1 physical storage write amplification ratio and substantial latency degradation under high-concurrency login bursts.
2. **Transit Engine LRU Global Lock Bottleneck (`SOURCE-CONFIRMED`):**
   - Source: `sdk/helper/keysutil/transit_lru.go:10-30`, `sdk/helper/keysutil/lock_manager.go:68-135`
   - Established Finding: `TransitLRU` wraps `golang-lru/v2/TwoQueueCache`, which synchronizes every cache `Get` and `Add` operation with a single internal `sync.RWMutex`. Under high concurrent read throughput (e.g., 50,000 req/s across 64 CPU cores), this single lock becomes a severe CPU cache line bouncing and goroutine scheduling bottleneck.
3. **Lock Striping Limitations (`SOURCE-CONFIRMED` / `BENCHMARK-CONFIRMED`):**
   - Source: `sdk/helper/locksutil/sharded.go:13-45`
   - Established Finding: OpenBao uses a static 256-shard lock array (`LockCount = 256`) hashed via BLAKE2b-256 (`cryptoutil.Blake2b256Hash(key)[0]`). For high Subject Key cardinalities ($N > 10,000$), the birthday paradox dictates widespread lock collisions across completely unrelated keys, inducing false serializations.
4. **Data Key Generation Double-Encoding Overhead (`SOURCE-CONFIRMED`):**
   - Source: `internal/builtin/logical/transit/path_datakey.go:140-185`
   - Established Finding: `pathDatakeyWrite` generates raw random bytes, base64-encodes them into a string, passes that string to `p.EncryptWithFactory`, which internally base64-decodes it back to raw bytes for encryption, and then base64-encodes the ciphertext for JSON transmission. This creates unnecessary heap churn and GC pressure.

---

## 4. Systematic Strategic Research Gaps

Despite the deep micro-optimizations identified in prior passes, the following 14 strategic areas represent major engineering gaps that must be thoroughly addressed to guarantee enterprise readiness for Cloakit Privacy Vault:

```
+---------------------------------------------------------------------------------------------------+
|                                  STRATEGIC RESEARCH GAPS MATRIX                                   |
+---------------------------------------------------------------------------------------------------+
| Gap Area                     | Current State in OpenBao Core        | Strategic Cloakit Need      |
+------------------------------+--------------------------------------+-----------------------------+
| 1. Workload Characterisation | Generic secret engine assumptions    | 95% Read / 5% Write Vault   |
| 2. End-to-End Latency        | Point microbenchmarks only           | Sub-millisecond P99 Budget  |
| 3. Protocol & Serialization  | HTTP/1.1 REST + JSON                 | gRPC, Protobuf, Pipelining  |
| 4. Cryptographic Lifecycle   | On-demand key derivation per request | Pre-computed session ciphers|
| 5. Multi-Tenant Contention   | Shared lock arrays and cache         | Tenant isolation & quotas   |
| 6. CQRS & Read/Write Split   | Monolithic active leader routing     | Read standbys & event WAL   |
| 7. Crash Recovery & DR       | Synchronous Raft snapshot restore    | Instant recovery, no stalls |
| 8. Concurrency & Security    | Standard mutexes, quota limits       | Side-channel & DoS defense  |
| 9. Capacity Planning         | Ad-hoc memory sizing                 | Exact RAM model up to 10M+  |
| 10. Upstream & Dependencies  | Generic standard library             | Go 1.24+ crypto & runtime   |
| 11. Observability Overhead   | High-overhead stats instrumentation  | Zero-alloc eBPF / otel      |
| 12. Deployment Topology      | Centralized 3/5 node Raft cluster    | Co-located Agent vs Sidecar |
| 13. Resource / Cost Model    | Untracked vCPU/RAM operational cost  | Multi-cloud cost curve      |
| 14. API Backward Comp        | Rigid Vault v1 API compatibility     | Extensible zero-break API   |
+---------------------------------------------------------------------------------------------------+
```

### Gap 1: Workload Characterisation & Operation Frequency
- **Existing Limitation:** Existing benchmarks test synthetic single-key loops (`BenchmarkTransit_BatchEncryption1`).
- **Research Gap:** No empirical model exists for Cloakit's operational frequency: 95% reads (Subject Key lookup + DEK unwrapping) versus 5% writes (new Subject Key onboarding + DEK wrapping + key rotation). There is no Zipfian key access distribution model reflecting skewed real-world tenant activity.

### Gap 2: End-to-End Latency Decomposition
- **Existing Limitation:** Prior work measured isolated transit function execution times.
- **Research Gap:** No microsecond-level latency waterfall exists decomposing the complete lifecycle: TLS handshake -> HTTP/2 parsing -> Client Token authentication -> ACL path check -> Core request routing -> Barrier decryption -> Transit policy lock acquisition -> Cryptographic execution (AES-256-GCM / HKDF) -> Audit serialization -> HTTP response encoding.

### Gap 3: API Protocol Modernization, Batching & Transport
- **Existing Limitation:** OpenBao relies heavily on HTTP/1.1 REST endpoints transmitting base64-encoded strings wrapped in verbose JSON dictionaries.
- **Research Gap:** The cost of JSON reflection (`mapstructure.Decode`), string base64 conversions, and TCP connection establishment has not been contrasted against high-efficiency binary transport (gRPC / Protocol Buffers, HTTP/2 multiplexing, HTTP/3 QUIC). Batch operations in `path_encrypt.go` process items sequentially inside a single thread.

### Gap 4: Cryptographic Hierarchy & Session Key Lifecycle
- **Existing Limitation:** Every `Encrypt` and `Decrypt` invocation in Transit reconstructs cipher instances or performs HKDF derivation dynamically.
- **Research Gap:** OpenBao lacks persistent AEAD cipher instance caching (`cipher.AEAD`) in `KeyEntry`. For derived keys, HKDF-SHA256 is evaluated repeatedly even when context parameters repeat across batched records.

### Gap 5: High-Cardinality Multi-Tenancy & Noisy-Neighbor Dynamics
- **Existing Limitation:** Namespaces exist as administrative boundaries, but underlying resources (in-memory LRU cache, lock stripes, quota evaluators) are globally shared.
- **Research Gap:** What happens when Tenant A submits 50,000 DEK unwrap requests while Tenant B attempts to rotate Subject Keys? Global cache eviction causes cold-storage lookup storms for Tenant B, while lock collisions in `locksutil` stall Tenant A.

### Gap 6: CQRS & Read/Write Consensus Decoupling
- **Existing Limitation:** In standard Vault/OpenBao HA, all write and read requests are either forwarded to the active leader or served locally via performance standbys with complex invalidation mechanics.
- **Research Gap:** Since 95% of Cloakit's workload is read-only DEK unwrapping and Subject Key lookup, forcing all traffic through active node consensus creates an unnecessary scalability ceiling. OpenBao's standby read path (`internal/vault/ha.go:maybeBypassGRPCRequirement`) and invalidation pipeline (`internal/vault/grpc_invalidation.go`) have not been evaluated for zero-staleness read scaling.

### Gap 7: Crash Consistency, Snapshotting & Degradation Dynamics
- **Existing Limitation:** Failure modes are usually evaluated as binary up/down states.
- **Research Gap:** What occurs during slow physical disk writes (e.g. AWS EBS IOPS throttling), Raft log compaction stalls, or abrupt crash recovery under 100,000 inflight DEK operations? The impact of Raft snapshot transfers on read latency spikes (tail latency blowout) remains unquantified.

### Gap 8: Security & Cryptographic Timing Under Resource Exhaustion
- **Existing Limitation:** Security reviews focus on RBAC and cryptographic correctness.
- **Research Gap:** The interplay between CPU saturation, memory exhaustion, and side-channel timing leaks or memory zeroization failure has not been explored. When Go GC pauses or memory allocation stalls occur, do cryptographic buffers linger in unzeroed heap space longer than permitted?

### Gap 9: Capacity Planning & Non-Linear Growth Forecasting
- **Existing Limitation:** Basic RAM recommendations (e.g. "16 GB of memory") are anecdotal.
- **Research Gap:** Cloakit requires an exact parametric memory and CPU model mapping:
  $$\text{RAM}_{\text{total}} = f(\text{Subject Keys}, \text{Versions}, \text{Cache Ratio}, \text{Active Tenants}, \text{Inflight Concurrency})$$
  Scaling from 100,000 to 10,000,000 Subject Keys requires rigorous data structure memory sizing.

### Gap 10: Upstream Alignment & Dependency Modernization
- **Existing Limitation:** Codebase references older third-party libraries (e.g., `golang.org/x/crypto/chacha20poly1305`, `golang-lru/v2`).
- **Research Gap:** Evaluation of upstream OpenBao 2.x architectural directions, Go 1.24+ runtime optimizations (Swiss Tables in runtime maps, Profile-Guided Optimization - PGO, allocation-free byte slice cloning, ChaCha8 / AES hardware SIMD assembly), and external database storage drivers.

---

## 5. Methodological Framework for Discovery

To bridge these gaps without violating offline repository constraints, the discovery methodology applies:

1. **Static AST & Escape Analysis Tracing:** Tracing allocation lifecycles and lock acquisition paths using Go tooling.
2. **Deterministic Algorithmic Modeling:** Constructing formal mathematical formulations for queueing delay, cache hit probability (Zipf-Mandelbrot distributions), lock contention probabilities, and memory consumption.
3. **Repository Benchmark Harness Execution:** Utilizing existing benchmarks in `internal/vault/` and `internal/builtin/logical/transit/` with varying parameters to establish empirical lower bounds.
4. **Architectural Dissection & Synthesis:** Deconstructing architectural boundaries between OpenBao Core and Cloakit Vault, ensuring that proposed evolutions maintain backward compatibility and enterprise security guarantees.

The subsequent 11 strategic research documents under `engineering/performance-audit/strategic-research/` deliver exhaustive, evidence-backed investigations into each of these critical domains.
