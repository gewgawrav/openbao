# OPENBAO — WORKLOAD-SPECIFIC PERFORMANCE OPTIMISATION MATRIX
**Document Version:** 1.0.0
**Classification:** Workload Engineering & Hot-Path Specialisation Guide
**Author:** Principal Distributed Systems Architect & Cryptographic Infrastructure Engineer

---

## 1. Overview & Workload Profiling Philosophy

A primary architectural deficiency in legacy OpenBao is the **uniform treatment of disparate workloads**. A high-throughput, in-memory cryptographic transform (such as a 64-byte Transit encryption) is processed using the exact same heavyweight execution pipeline, mount locks, and synchronous auditing as an enterprise root certificate authority generation or a multi-key Raft barrier write.

By profiling and isolating the 10 core OpenBao workloads, we establish dedicated fast paths, resource quotas, and latency expectations.

---

## 2. In-Depth Workload Execution Paths & Optimisation Strategies

### Workload 1: Secret Retrieval (KV v2 Read)
- **Characteristics**: Read-heavy (85-95% of total cluster volume), extreme low latency sensitivity (P99 < 1.5ms).
- **Current Execution Path**:
  1. Client sends `GET /v1/secret/data/app`.
  2. Parse JSON & validate token via storage barrier read.
  3. Pre-audit log written synchronously.
  4. Mount table `RLock` acquired.
  5. KV v2 backend fetches metadata from storage (`AES-GCM` decrypt).
  6. KV v2 backend fetches versioned data from storage (`AES-GCM` decrypt).
  7. Post-audit log written synchronously.
  8. JSON serialization & HTTP write.
- **Shared Bottlenecks**: Active Raft leader network saturation; lock contention on token store and mount tables.
- **Unique Bottlenecks**: Double storage barrier decryption per secret read (one for metadata index, one for payload).
- **Target Architecture**:
  - Standby local read execution via Raft Read-Index leases.
  - Coalesce metadata and data storage lookups into a single barrier read or single combined storage entry.
  - Generational in-memory token cache (L1) bypassing token barrier lookup.
- **Latency & Throughput Target**: P99: **< 1.0 ms** | Throughput: **> 45,000 RPS / cluster**.

---

### Workload 2: Secret Storage (KV v2 Write)
- **Characteristics**: Write-heavy, strictly linearised, durability-critical.
- **Current Execution Path**:
  1. Client sends `POST /v1/secret/data/app` with JSON payload.
  2. Token authentication & synchronous pre-audit.
  3. KV v2 acquires write lock for the path.
  4. Write metadata barrier entry + write data barrier entry.
  5. Two distinct Raft log entries proposed to consensus quorum.
  6. Synchronous disk `fsync()` on quorum nodes.
  7. Post-audit write & response return.
- **Shared Bottlenecks**: Raft consensus round-trip latency and disk I/O commit times.
- **Unique Bottlenecks**: Two sequential Raft log appends per versioned write.
- **Target Architecture**:
  - Atomic multi-put batching in the barrier: Single Raft log entry containing both metadata and secret version update.
  - Storage-layer Adaptive Group Commit (coalescing writes across concurrent threads within a 250µs window).
  - Background asynchronous audit emission.
- **Latency & Throughput Target**: P99: **< 3.5 ms** | Throughput: **> 18,000 RPS / cluster**.

---

### Workload 3: Transit Encryption
- **Characteristics**: CPU-intensive, 100% in-memory, zero storage persistence, high volume.
- **Current Execution Path**:
  1. Client sends `POST /v1/transit/encrypt/app-key` with base64 plaintext.
  2. Token authentication.
  3. Pre-audit logging.
  4. Key retrieved from backend cache (requires cache read lock).
  5. Generate 96-bit random IV via `crypto/rand`.
  6. `cipher.NewGCM` creates cipher instance (allocates memory).
  7. `gcm.Seal` performs encryption.
  8. Post-audit logging.
  9. Base64 encode & return JSON.
- **Shared Bottlenecks**: Synchronous audit logger; token verification overhead.
- **Unique Bottlenecks**: Heap allocations in `sync.Pool` absence; lock contention on key structure during key rotation checks.
- **Target Architecture**:
  - Fast-Path bypass: Route directly from HTTP router to Transit engine without acquiring core locks.
  - RCU (Read-Copy-Update) atomic pointer for key caching (lock-free reads).
  - `sync.Pool` pre-allocated byte slices for encryption buffers.
  - Vectorised AES-NI / AVX-512 assembly transforms.
- **Latency & Throughput Target**: P99: **< 0.25 ms** | Throughput: **> 90,000 ops/s / node**.

---

### Workload 4: Detokenisation / Decryption
- **Characteristics**: Symmetric decryption, high concurrency, extreme P99 tail sensitivity.
- **Current Execution Path**:
  1. Client sends ciphertext payload.
  2. Auth validation & audit.
  3. Key version extracted from ciphertext prefix.
  4. Lookup key version in memory.
  5. `AES-GCM` authentication tag verification and decryption.
  6. Return plaintext.
- **Shared Bottlenecks**: Audit latency; Go runtime GC pauses due to byte slice allocations.
- **Unique Bottlenecks**: Cache invalidation storms if key versions are updated dynamically.
- **Target Architecture**:
  - In-place slice decryption within pre-allocated buffer pools.
  - NUMA-aware worker pool dispatching.
- **Latency & Throughput Target**: P99: **< 0.3 ms** | Throughput: **> 75,000 ops/s / node**.

---

### Workload 5: PKI Certificate Issuance
- **Characteristics**: Hybrid compute & write: Asymmetric cryptography (RSA/ECDSA signing) plus storage persistence of certificate serial numbers.
- **Current Execution Path**:
  1. Client submits CSR (`POST /v1/pki/issue/role`).
  2. Token authentication & policy check.
  3. Role configuration read from barrier storage.
  4. CA private key accessed (in-memory or PKCS#11 HSM).
  5. Cryptographic signature generated over X.509 certificate.
  6. Certificate serial number, revocation info, and certificate body persisted to Raft barrier.
  7. Quorum disk fsync.
  8. Return signed certificate and chain.
- **Shared Bottlenecks**: Raft write serialization.
- **Unique Bottlenecks**: Asymmetric math latency (e.g. 2048-bit RSA sign takes ~1.5ms of pure CPU time; ECDSA P-256 takes ~0.2ms); serial storage write overhead.
- **Target Architecture**:
  - Dedicated PKI issuance worker pool preventing API starvation.
  - Support for **Ephemeral Issuance Mode** (`no_store=true`): If CRL/revocation tracking is managed via external short-lived cert models, omit the Raft storage write entirely, converting issuance into an in-memory compute operation.
  - Hardware crypto offloading for Root CA signing operations.
- **Latency & Throughput Target**: P99: **< 1.8 ms (ECDSA)** | Throughput: **> 8,000 certs/sec**.

---

### Workload 6: High-Frequency Authentication (AppRole / Kube / Token)
- **Characteristics**: High arrival frequency, bursts during container rolling restarts.
- **Current Execution Path**:
  1. Client sends role ID + secret ID / JWT token.
  2. Backend validates identity against identity provider or local hash.
  3. Token Store creates new token entity: generates UUID, creates lease, serializes metadata, writes barrier entry.
  4. Quorum consensus commit.
  5. Return token string.
- **Shared Bottlenecks**: Raft storage write per login.
- **Unique Bottlenecks**: Token storage proliferation. Under 10,000 logins/sec, Raft storage is overwhelmed with ephemeral token entries.
- **Target Architecture**:
  - Promote use of **Cryptographically Signed Batch Tokens** (Ed25519 or HMAC stateless tokens) for machine-to-machine microservices. Stateless tokens require **zero Raft storage writes** upon creation and validate via in-memory public key verification.
  - Group commit for standard persisted tokens.
- **Latency & Throughput Target**: Stateless: P99 **< 0.4 ms**, **> 50,000 logins/sec** | Stateful: P99 **< 3.5 ms**, **> 12,000 logins/sec**.

---

### Workload 7: Policy Evaluation
- **Characteristics**: Executed on 100% of incoming requests; CPU-bound pattern matching.
- **Current Execution Path**:
  1. Extract policy names from client token.
  2. For each policy, retrieve ACL structures.
  3. Perform string prefix matching, regex globbing, and path segment parsing against requested API endpoint.
  4. Evaluate capabilities bitmask (create, read, update, delete, list, sudo).
- **Shared Bottlenecks**: Invoked sequentially on the request thread.
- **Unique Bottlenecks**: Trie traversal overhead and dynamic regex compilation.
- **Target Architecture**:
  - Compiled Policy Bitmask / Deterministic Finite Automaton (DFA) path index.
  - Policy decision cache (L1) keyed by `(TokenHash, HTTPMethod, RequestPath)`. Evaluated in < 25 nanoseconds.
- **Latency & Throughput Target**: P99: **< 0.05 ms** | Throughput: **> 200,000 evaluations/sec**.

---

### Workload 8: Lease Renewal & Revocation
- **Characteristics**: Background timer management, write bursts during cluster restarts or mass expiries.
- **Current Execution Path**:
  1. Expiration manager maintains an in-memory priority queue (`expiration.go`).
  2. Timers fire when leases expire.
  3. Acquire barrier locks, invoke backend revocation handlers, delete barrier entries.
  4. Raft storage commit per revocation.
- **Shared Bottlenecks**: Raft write bandwidth shared with live client requests.
- **Unique Bottlenecks**: "Revocation Storms": When thousands of leases expire simultaneously, the expiration queue floods the Raft log, starving user API writes.
- **Target Architecture**:
  - Leveled hierarchical priority queues with adaptive rate smoothing.
  - Coalesced batch revocation: Collect up to 256 expired leases and execute revocation within a single Raft commit transaction.
  - Rate-limited background revocation workers with dynamic backoff when client API latency increases.
- **Throughput Target**: Sustained **> 15,000 revocations/sec** with zero impact on client API latency.

---

### Workload 9: Audit Logging
- **Characteristics**: 2x volume of total API requests; strict non-repudiation requirement.
- **Current Execution Path**:
  1. Synchronous serialization of request payload to JSON.
  2. Blocking POSIX `write()` and `fsync()` to all registered audit devices.
  3. Synchronous serialization of response payload to JSON.
  4. Blocking POSIX `write()` and `fsync()`.
- **Shared Bottlenecks**: Disk I/O, network sockets, OS context switching.
- **Unique Bottlenecks**: The slowest audit sink limits the speed of the entire cluster.
- **Target Architecture**:
  - Lock-free ring buffer (LMAX Disruptor pattern) with dedicated background writer thread.
  - Merged Single Audit Mode: Combine request and response audit data into a single comprehensive record emitted after request dispatch.
  - Micro-batched vectorised I/O (`writev`) to disk / remote collectors.
- **Throughput Target**: **> 120,000 audit events/sec** per node with zero request latency penalty.

---

### Workload 10: Cluster Replication & State Synchronisation
- **Characteristics**: Background data streaming, inter-node consensus.
- **Current Execution Path**:
  1. Raft leader logs state change to WAL.
  2. Leader transmits `AppendEntries` RPC to all follower nodes.
  3. Followers append to their WAL and send ACK.
  4. Leader updates commit index and sends commit notification.
- **Bottlenecks**: Network serialization overhead; lock contention on Raft state mutex.
- **Target Architecture**:
  - Enable gRPC compression & zero-copy network buffers for log streaming.
  - Dedicated physical network interface (NIC) bonding for Raft inter-node cluster traffic.
- **Throughput Target**: Sustained log replication > **40,000 entries/sec** with sub-millisecond replication lag.

---

## 3. Workload Optimization Matrix Summary

| Workload Profile | Dominant Resource | Primary Architectural Isolation Mechanism | Target P99 Latency |
|---|---|---|---|
| **Secret Retrieval** | Memory / Network | Standby Read-Index Replicas | 0.8 ms |
| **Secret Storage** | Disk I/O / Consensus | Adaptive Group Commit + Multi-Put Barrier | 3.5 ms |
| **Transit Encryption** | CPU (SIMD) | Fast-Path Router + sync.Pool Workers | 0.2 ms |
| **Detokenisation** | CPU / Memory | Vectorized AES-NI + NUMA Pools | 0.3 ms |
| **PKI Issuance** | CPU (Math) / Consensus | Ephemeral Issuance Mode + Dedicated Worker Pool | 1.8 ms |
| **Authentication** | Storage / Crypto | Stateless Batch Tokens (Ed25519) | 0.4 ms |
| **Policy Evaluation** | CPU Cache | Compiled Policy Bitmask + L1 Decision Cache | 0.05 ms |
| **Lease Expiration** | Storage (WAL) | Leveled Priority Queue + Batch Revocation | Background |
| **Audit Logging** | Disk I/O | Lock-Free Disruptor Buffer + Vectorized writev | < 0.01 ms |
| **Replication** | Network Bandwidth | Dedicated Cluster Network Interface + gRPC Buffers | 1.0 ms lag |
