# OpenBao Workload Isolation & Architectural Recommendations

## Executive Overview

This document presents concrete, production-grade architectural and algorithmic recommendations to eliminate workload interference and resource starvation in OpenBao. All recommendations are designed to **strictly preserve OpenBao's security, auditability, and data consistency guarantees**.

---

## 1. Asynchronous Audit Logging with Non-Blocking Backpressure Isolation

### Problem
OpenBao's `AuditBroker.LogRequest` and `LogResponse` (`internal/vault/audit_broker.go`) operate synchronously on the critical path of every authenticated request. An I/O stall in a single audit backend completely halts foreground request processing system-wide.

### Recommended Architecture & Implementation Blueprint
1. **Bounded Lock-Free Ring Buffer per Audit Device**:
   - Decouple the request handling path from audit write I/O using a bounded ring buffer (e.g., Disruptor pattern or buffered channel with pre-allocated slots).
   - The HTTP handler deposits the serialized audit event into the buffer in sub-microsecond time and proceeds with request execution.
2. **Dedicated Audit Flushing Worker Pool**:
   - Background worker goroutines drain each device's buffer and perform the blocking I/O (disk `write()`, network socket flush).
3. **Preserving Security Guarantees & Safety Proof**:
   - **Fail-Closed Semantics**: If the buffer fills beyond a high-water mark (e.g., 90%), apply adaptive backpressure by throttling incoming requests.
   - If the buffer becomes 100% full, the broker blocks incoming requests until space is cleared or fails the request with an audit failure error, ensuring that no un-audited request is ever committed.
   - For ultra-high security modes, provide an opt-in `sync_mode: true` toggle per audit backend, while defaulting to buffered asynchronous execution for performance-critical production clusters.

### Concrete Implementation Blueprint
```go
// Proposed bounded ring-buffer audit pipeline
type BufferedAuditBackend struct {
    backend      audit.Backend
    eventRing    chan *auditEvent
    dropPolicy   DropPolicy // FailClosed (default) or Sample
    highWater    int64
    maxBuffer    int64
    activeCount  atomic.Int64
}

func (b *BufferedAuditBackend) Enqueue(ctx context.Context, in *logical.LogInput) error {
    cur := b.activeCount.Add(1)
    if cur > b.maxBuffer {
        b.activeCount.Add(-1)
        return ErrAuditBufferFull // Triggers 500 error, preserving strict audit guarantee
    }
    b.eventRing <- &auditEvent{input: in, ctx: ctx}
    return nil
}
```

---

## 2. Dedicated Worker Pools & Priority-Aware Execution

### Problem
OpenBao uses the default Go runtime scheduler for all operations. CPU-intensive operations (Transit batch encryption, 4096-bit RSA PKI key generation, bcrypt/argon2 hashing) monopolize Go scheduler threads (`GOMAXPROCS`), starving lightweight KV reads and token validations.

### Recommended Architecture
1. **Cryptographic Worker Pool**:
   - Create a dedicated, bounded worker pool for heavy cryptographic operations (`TransitBatchWorkerPool`, `PKICryptoPool`).
   - Limit the pool size to a fraction of total CPU cores (e.g., `max(1, GOMAXPROCS/2)`), guaranteeing that at least 50% of CPU capacity remains reserved for latency-sensitive API requests, TLS handshakes, and Raft consensus heartbeats.
2. **Background Task Concurrency Caps**:
   - Throttle the `ExpirationManager` FairShare workers (`internal/vault/expiration.go:numExpirationWorkersDefault = 200`).
   - Introduce dynamic concurrency scaling: during periods of high foreground API write traffic, throttle background lease revocation workers to a low baseline (e.g., 10–20 workers), expanding up to 200 workers only when storage write queues are idle.

---

## 3. Cost-Weighted & Multi-Dimensional Admission Control

### Problem
OpenBao's existing `RateLimitQuota` (`internal/vault/quotas/quotas_rate_limit.go`) only tracks uniform request rates (requests per second) per namespace or mount. It does not account for request payload size, batch item counts, or computational cost.

### Recommended Architecture
1. **Cost-Weighted Token Bucket**:
   - Assign computational weight factors to different request operations:
     - Secret read (KV v2): `Cost = 1`
     - Secret write (KV v2): `Cost = 5`
     - Transit symmetric encrypt (single): `Cost = 2`
     - Transit batch encrypt (N items): `Cost = 2 * N`
     - PKI ECDSA cert issue: `Cost = 15`
     - PKI RSA-4096 cert issue: `Cost = 80`
   - Quotas decrement tokens based on calculated cost, preventing tenants from saturating CPU or storage under the guise of low request counts.
2. **Batch Item & Payload Size Enforcers**:
   - Enforce hard limits on `batch_input` array lengths (e.g., maximum 250 items per transit batch request). Requests exceeding the limit are rejected with HTTP 400 (`logical.ErrInvalidRequest`).
   - Implement streaming or incremental decoding to avoid multi-megabyte transient heap allocations.
3. **Adaptive Concurrency Limiting (Little's Law / Vegas)**:
   - Implement an admission controller at `internal/http/handler.go` that monitors moving average round-trip latency.
   - When P95 latency escalates beyond safe thresholds (signaling queue backup or storage saturation), the admission controller dynamically rejects lower-priority requests with HTTP 429 ("Too Many Requests"), protecting active transactions from cascading failure.

---

## 4. Fine-Grained Locking & Read-Copy-Update (RCU) Barrier State

### Problem
The security barrier (`internal/vault/barrier/aes_gcm.go`) uses a single `sync.RWMutex` (`b.l`). Barrier key rotation (`Rotate`) takes an exclusive write lock, halting all reads and writes across the entire system.

### Recommended Architecture
1. **Atomic Pointer for Active AEAD Keyring**:
   - Replace the mutex-protected active keyring lookup with an `atomic.Pointer[Keyring]`.
   - Read operations (`Get`) and write operations (`Put`) atomically load the active keyring pointer without acquiring a mutex lock.
2. **Non-Blocking Key Rotation**:
   - When rotating the barrier key:
     1. Construct the new `Keyring` in memory with the incremented term.
     2. Persist the new keyring to physical storage under a localized storage lock.
     3. Atomically swap the active keyring pointer (`b.activeKeyring.Store(newKeyring)`).
   - This eliminates the global write lock on `b.l`, allowing key rotation to complete with zero disruption to concurrent reads and writes.

---

## 5. Storage Write Isolation & Raft Snapshot Optimizations

### Problem
BoltDB operates with a single global write transaction lock. Long-running Raft FSM snapshots (`internal/physical/raft/fsm.go`) hold open read transactions (`db.View`) across full cursor iterations, preventing page reclamation and causing write degradation.

### Recommended Architecture
1. **PebbleDB as Preferred Storage Engine**:
   - OpenBao already includes preliminary support for PebbleDB (`internal/physical/pebbledb/`). Transitioning to an LSM-tree storage engine decouples write pipelines (WAL append + memtable write) from background compaction and read operations, eliminating BoltDB's single-writer bottleneck.
2. **Incremental & Out-of-Band Raft Snapshotting**:
   - Instead of holding a long-running BoltDB read transaction during snapshot streaming:
     - Use checkpointing or copy-on-write filesystem snapshots (or PebbleDB checkpoints) to take instantaneous, zero-overhead storage snapshots.
     - Stream snapshot chunks from the static checkpoint in a background goroutine, completely isolating snapshot I/O from live transactions.
3. **PKI `no_store` Default for High-Throughput Issuance**:
   - Encourage and document `no_store=true` as the default for short-lived PKI certificates (e.g., service mesh, mTLS), avoiding unnecessary write amplification in BoltDB.

---

## 6. Multi-Tenant Fairness via Hierarchical Queuing

### Problem
High-volume tenants can monopolize storage write queues and CPU cores, starving low-volume tenants across the cluster.

### Recommended Architecture
1. **Per-Namespace FairShare Storage Dispatcher**:
   - Route state-modifying requests through a namespace-aware priority queue before proposing to Raft.
   - Ensure round-robin or deficit round-robin dispatching across active namespaces, guaranteeing that Tenant A cannot exhaust storage write bandwidth to the detriment of Tenant B.
2. **Namespace Concurrency Bounds**:
   - Configure maximum concurrent in-flight requests per namespace (`max_in_flight_requests`).
   - If a tenant exhausts their concurrent slot allocation, subsequent requests wait in a short bounded queue or receive HTTP 429, preventing runaway scripts or rogue workloads from degrading neighboring tenants.

---

## 7. Phased Implementation Roadmap

To ensure smooth adoption without destabilizing existing deployments, we recommend a 3-phase rollout:

### Phase 1: Transport & Quotas (Low Risk, High Impact)
- Add request batch size limits (`batch_input` max items = 250) in Transit.
- Implement cost-weighted RateLimitQuotas.
- Introduce per-namespace `max_in_flight_requests` concurrency limits.

### Phase 2: Core Isolation & Audit Pipeline (Medium Risk, High Architecture Value)
- Implement bounded ring-buffer asynchronous audit logging with fail-closed backpressure.
- Partition CPU threads by establishing a bounded worker pool for heavy cryptographic operations.
- Transition `AESGCMBarrier` to atomic pointer RCU reads to eliminate key rotation read/write stalls.

### Phase 3: Storage Engine & Snapshot Modernization (Strategic Enterprise Upgrade)
- Transition production storage backend from BoltDB to PebbleDB (LSM-tree).
- Implement out-of-band snapshotting using point-in-time filesystem/LSM checkpoints.
- Integrate namespace-aware fair scheduling into Raft proposal dispatching.

---

## Summary of Recommendations & Implementation Impact

| Subsystem | Recommended Change | Impact on Interference | Security & Consistency Impact |
| :--- | :--- | :--- | :--- |
| **Audit Logging** | Bounded asynchronous ring buffer with backpressure high-water mark | Eliminates audit head-of-line blocking; stabilizes P99 API latency | **Zero security loss**: strictly fails-closed if buffer reaches 100% capacity |
| **Crypto & PKI** | Dedicated CPU worker pools with core reservation | Prevents CPU starvation for KV reads, TLS, and Raft consensus | **Identical crypto guarantees**: keys generated with same CSPRNG |
| **Quotas & Admission** | Cost-weighted token buckets & max batch item limits | Eliminates tenant abuse via massive batch payloads | **Fully transparent**: preserves REST API semantics, adds HTTP 429 on abuse |
| **Security Barrier** | `atomic.Pointer` for active keyring (RCU pattern) | Eliminates total system freeze during barrier key rotation | **Strictly preserved**: key terms and AEAD ciphers remain cryptographically isolated |
| **Storage Engine** | PebbleDB transition & copy-on-write snapshot checkpoints | Eliminates single-writer contention and snapshot write stalls | **Strict ACID compliance**: WAL ensures durability and Raft guarantees consensus |
| **Background Tasks** | Dynamic throttling of lease revocation workers during API peaks | Prevents background storms from degrading foreground user traffic | **Lease integrity maintained**: expired leases are revoked with fair priority |
