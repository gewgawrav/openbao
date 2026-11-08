# OpenBao Resource Contention Map

## 1. System Overview: Shared Resources Architecture

In OpenBao, all workload profiles share underlying runtime and hardware resources, as well as core internal synchronization primitives. The diagram below illustrates how logical workloads converge onto shared locks, storage backends, runtime resources, and background subsystems:

```
+----------------------------------------------------------------------------------------------------+
|                                      INCOMING API WORKLOADS                                        |
|  [Secret Read]   [Secret Write]   [Transit Crypto]   [PKI Issuance]   [Auth / Token]   [Sys Ops]   |
+-------+-----------------+----------------+-----------------+-----------------+-------------+-------+
        |                 |                |                 |                 |             |
        v                 v                v                 v                 v             v
+----------------------------------------------------------------------------------------------------+
|                                    1. TRANSPORT & ADMISSION LAYER                                  |
|   - HTTP Listener & Connection Pool (MaxRequestSizeHandler)                                        |
|   - RateLimitQuota Enforcers (in-memory sliding window limiter per mount/namespace)               |
+----------------------------------------------------------------------------------------------------+
        |
        v
+----------------------------------------------------------------------------------------------------+
|                                    2. CORE SYNCHRONIZATION PRIMITIVES                              |
|   - Core.stateLock (sync.RWMutex): Shared RLock on every request; Exclusive on HA/lifecycle        |
|   - Identity MemDB (memdb.MemDB): Entity/group lookups and mutations                               |
|   - PolicyStore (TwoQueueCache): LRU read lock and policy compilation                              |
+----------------------------------------------------------------------------------------------------+
        |
        v
+----------------------------------------------------------------------------------------------------+
|                                    3. AUDIT PIPELINE (BLOCKING)                                    |
|   - AuditBroker.RWMutex: Serializes backend iteration                                              |
|   - Audit Backends (File / Syslog / Socket): Synchronous disk/socket I/O                           |
+----------------------------------------------------------------------------------------------------+
        |
        v
+----------------------------------------------------------------------------------------------------+
|                                    4. SECURITY BARRIER LAYER                                       |
|   - AESGCMBarrier.l (sync.RWMutex): Active keyring read lock; Exclusive on Key Rotation            |
|   - Barrier AEAD Ciphers: CPU-bound symmetric encryption/decryption                                |
+----------------------------------------------------------------------------------------------------+
        |
        v
+----------------------------------------------------------------------------------------------------+
|                                    5. PHYSICAL STORAGE LAYER (RAFT)                                |
|   - Raft Consensus Pipeline (Leader append log, replication RPCs)                                  |
|   - FSM.l (sync.RWMutex): Read lock on snapshot streaming; Exclusive on snapshot restore           |
|   - BoltDB (vault.db): Single-writer lock (tx.Lock), mmap reads, dirty page flush, fdatasync       |
+----------------------------------------------------------------------------------------------------+
        ^
        | Concurrent Storage Operations
+----------------------------------------------------------------------------------------------------+
|                                    6. BACKGROUND SUBSYSTEMS                                        |
|   - ExpirationManager: pendingLock, lease timers, FairShare dispatcher (200 workers)               |
|   - Raft Snapshotter: fsm.writeTo cursor scan over BoltDB data bucket                              |
|   - PKI Tidy Routine: Background sweep of expired certificates and revocation lists                |
+----------------------------------------------------------------------------------------------------+
```

---

## 2. Shared Locks and Synchronization Primitives

The following table provides an exhaustive map of OpenBao's shared synchronization primitives, their locations, lock granularity, holding durations, and cross-workload contention risks:

| Lock / Primitive | Source Location | Lock Mode | Critical Section Operations | Contention & Contending Workloads |
| :--- | :--- | :--- | :--- | :--- |
| **`Core.stateLock`** | `internal/vault/core.go` | `sync.RWMutex` | **RLock**: Held during entire `HandleRequest` execution.<br>**Lock**: Held during Seal, Unseal, Rekey, Namespace CRUD, Active context cancellation. | **High Impact**: When `Lock()` is acquired, all active requests are suspended and all new incoming API requests block immediately. |
| **`AESGCMBarrier.l`** | `internal/vault/barrier/aes_gcm.go` | `sync.RWMutex` | **RLock**: Fetch active term & AEAD cipher for `Put`/`Get`.<br>**Lock**: Barrier Key Rotation (`Rotate`), Root Key Rotation (`RotateRootKey`). | **Total Storage Halt**: `Rotate` acquires exclusive lock and writes new keyring. While held, all KV reads/writes, token lookups, and lease registrations block. |
| **`AuditBroker.RWMutex`** | `internal/vault/audit_broker.go` | `sync.RWMutex` | **RLock**: Synchronously iterating through registered audit backends and calling `LogRequest`/`LogResponse`.<br>**Lock**: Register/Deregister audit devices. | **Head-of-Line Blocking**: Held for the duration of external I/O. If a disk or network socket stalls, `RLock` duration spikes, blocking all audit calls. |
| **`keysutil.Policy.l`** | `sdk/helper/keysutil/policy.go` | `sync.RWMutex` | **RLock**: Transit encrypt, decrypt, sign, verify, datakey generation.<br>**Lock**: Key rotation, policy config updates, key deletion. | **Batch Contention**: A batch encrypt call holding `RLock` over thousands of items delays key rotation or configuration updates for that transit key. |
| **`ExpirationManager.pendingLock`** | `internal/vault/expiration.go` | `locking.RWMutex` | **Lock/RLock**: Registering new leases, updating lease timers, calculating metrics, fetching counts. | **Revocation Storms**: During high-frequency lease creation and expiration, `pendingLock` experiences rapid lock/unlock churn. |
| **`FSM.l`** | `internal/physical/raft/fsm.go` | `sync.RWMutex` | **RLock**: Snapshot creation (`writeTo`) scanning BoltDB.<br>**Lock**: Snapshot restore (`Restore`), noop restore flag updates. | **Storage Freeze**: During snapshot restore on a follower/recovering node, all local `Put`, `Get`, and `Delete` calls block completely. |
| **`BoltDB.metalock / tx.lock`** | `go.etcd.io/bbolt` (via Raft FSM) | `sync.Mutex` | **Update Lock**: Serializes all physical storage writes (`Put`, `Delete`).<br>**View**: Concurrent mmap page reads. | **Write Serializer**: Secret creation, token storage, PKI cert storage, and lease revocations all queue behind BoltDB's single write transaction. |
| **`IdentityStore.groupLock`** | `internal/vault/identity/store_structs.go` | `sync.RWMutex` | **Lock**: Modifying entity groups and aliases.<br>**RLock**: Group policy resolution. | Contention between dynamic group membership updates and token authentication policy derivation. |

---

## 3. Physical & Runtime Shared Resources

### A. CPU Core Scheduling (`GOMAXPROCS`)
- **Shared Pool**: All goroutines (HTTP requests, cryptographic engines, background lease workers, Raft consensus heartbeats) compete for the same OS threads.
- **Contention Hotspots**:
  - RSA 2048/4096-bit key generation and X.509 signing in PKI.
  - Large-batch AES-GCM and ChaCha20-Poly1305 operations in Transit.
  - PBKDF2 / Bcrypt / Argon2 password hashing during authentication.
- **Blast Radius**: Latency-sensitive operations (e.g., KV reads, token validation) suffer Go runtime scheduler delays (`schedlat`).

### B. Go Heap Memory & Garbage Collection (GC)
- **Shared Heap**: Memory allocations for request unmarshaling, batch structures, and MemDB state exist on a single Go runtime heap.
- **Contention Hotspots**:
  - Unmarshaling multi-megabyte Transit batch payloads.
  - Buffering Raft snapshots (50,000 protobuf messages per batch).
  - High-cardinality identity entities in `memdb.MemDB`.
- **Blast Radius**: Increased heap allocation triggers Go's concurrent garbage collector. Under high allocation rates, user goroutines are conscripted into **GC Mark Assist**, stealing 25%+ of CPU cycles from API request processing.

### C. BoltDB & Disk Storage I/O
- **Shared Storage Media**: The embedded `vault.db` file handles all transactional reads and writes.
- **Contention Hotspots**:
  - Raft write pipeline issuing `fdatasync` per consensus commit.
  - High-volume PKI issuance storing raw X.509 certificates.
  - FairShare lease revocation workers issuing batch storage deletes.
  - Raft snapshotting streaming database pages to disk.
- **Blast Radius**: Disk I/O saturation (I/O wait) degrades write throughput and increases latency for all stateful endpoints.

### D. Audit Logging I/O Sinks
- **Shared File Descriptors & Network Sockets**: Audit logs write to local disk files, syslog daemons, or TCP/UDP sockets.
- **Contention Hotspots**: High request throughput generating bursts of formatted JSON audit entries.
- **Blast Radius**: Operating system write buffer saturation or disk write stalls cause `AuditBroker.LogRequest` to block, halting request processing across all mounts.

---

## 4. Contention Points Mapped Against the 12 Workload Profiles

The matrix below maps each workload profile to the shared resources it consumes, its primary contention points, and its operational failure modes:

| Workload Profile | Key Shared Resources Consumed | Contention Points & Contending Workloads | Primary Failure / Degradation Mode |
| :--- | :--- | :--- | :--- |
| **1. Secret Retrieval** | - `Core.stateLock` (RLock)<br>- `AESGCMBarrier.l` (RLock)<br>- BoltDB (mmap read) | - Contends with Barrier Key Rotation (`Rotate`)<br>- Contends with BoltDB page cache evictions caused by heavy writes | Read latency P99 spikes from <1ms to >20ms; timeout under lock contention. |
| **2. Secret Writes** | - `AESGCMBarrier.l` (RLock)<br>- Raft commit log<br>- BoltDB write transaction | - Contends with concurrent PKI issuance, dynamic lease creations, and background lease revocations | Write queue stalls; BoltDB `fdatasync` backlog; increased memory footprint. |
| **3. Transit Crypto** | - CPU cores (`GOMAXPROCS`)<br>- `keysutil.Policy.l` (RLock) | - Contends with PKI and Auth for CPU time<br>- Contends with Transit key rotation for policy lock | Scheduler latency increases; concurrent API requests delayed. |
| **4. Batch Crypto** | - CPU cores<br>- Heap memory (allocations)<br>- `keysutil.Policy.l` (RLock) | - Monopolizes single goroutine CPU time<br>- Triggers GC Mark Assist across other goroutines | Micro-pauses and tail latency spikes across unrelated tenant requests. |
| **5. PKI Issuance** | - CPU cores (RSA/ECDSA)<br>- BoltDB write lock (`Put` certs)<br>- `ExpirationManager` | - Contends with Secret Writes for BoltDB write lock<br>- Contends with CPU for token auth and reads | PKI issuance P99 degrades to >1s; foreground KV writes stall. |
| **6. Authentication** | - CPU (password hashing)<br>- `TokenStore` (write lock)<br>- Identity `memdb.Txn` | - Contends with CPU-intensive crypto<br>- Serializes storage writes for new token creation | Auth login timeouts (HTTP 504); connection pool exhaustion. |
| **7. Token Validation** | - `TokenStore` memory map<br>- BoltDB write lock (if `NumUses > 0`) | - When limited-use tokens are used, contends directly with write pipeline | High-frequency token checks convert into disk writes, collapsing throughput. |
| **8. Policy Eval** | - `policy.Store` 2Q Cache<br>- Identity `memdb.Txn` (RLock) | - Contends for cache memory during policy updates<br>- Contends with Identity store updates | Marginal latency increase during rapid policy churn. |
| **9. Lease Expiration** | - `fairshare.Dispatcher`<br>- BoltDB write lock (deletes)<br>- `pendingLock` | - 200 background workers contend with user writes for storage and barrier | API write latency degrades during bulk lease expirations. |
| **10. Key Rotation** | - `AESGCMBarrier.l` (Lock)<br>- BoltDB write transaction | - Exclusively blocks ALL reads and writes across the entire system | Total system stall (HTTP latency spikes, client timeouts) during rotation. |
| **11. Audit Logging** | - `AuditBroker.RWMutex`<br>- Host disk / network I/O | - Any slow audit device blocks all API requests system-wide | Catastrophic failure: entire vault stops accepting requests if audit sinks stall. |
| **12. Raft Snapshot** | - `FSM.l` (RLock)<br>- BoltDB read transaction<br>- Disk / Network bandwidth | - Holds long-lived BoltDB read txn, blocking page reclamation<br>- Satures disk I/O | Memory usage surges; storage write latency spikes during snapshot transfer. |

---

## 5. Contention Latency Amplification Models & Failure Chains

To understand how individual resource bottlenecks compound into cluster-wide outages, we model three classic failure chains observed in OpenBao shared deployments:

### Failure Chain 1: The Audit Backpressure Collapse
```
[Slow Audit Device (e.g. NFS / Remote Syslog Lag)]
                   │
                   ▼ (Synchronous write call blocks in AuditBroker.LogRequest)
[Goroutines Accumulate in switchedLockHandleRequest]
                   │
                   ▼ (c.stateLock.RLock held open indefinitely)
[HTTP Worker Pool Depleted & Connection Backlog Reaches max_request_size]
                   │
                   ▼ (Reverse Proxy / Load Balancer Connection Timeout: 504 Gateway Timeout)
[Clients Retry Simultaneously -> Thundering Herd -> Port / FD Exhaustion]
```
- **Amplification Factor**: $100\times$ to $\infty$. A 500ms disk latency spike on the audit device causes 100% of all subsequent requests to stall, resulting in total system unresponsiveness.

### Failure Chain 2: The Storage Single-Writer Cascade
```
[Bulk Dynamic PKI Issuance (certs/ write) + Bulk Lease Revocation (fairshare.Dispatcher)]
                   │
                   ▼ (Both issue barrier.Put / barrier.Delete)
[BoltDB tx.lock Saturation (Exclusive Write Lock Queue Backlog)]
                   │
                   ▼ (Raft commit channel blocked; fdatasync queues saturate NVMe controller)
[Foreground KV Secret Writes Experience 50ms - 500ms Wait]
                   │
                   ▼ (Clean OS Page Cache Pages Evicted to buffer dirty pages)
[KV Secret Reads Suffer Page Faults -> P99 Read Latency Spikes from 0.5ms to 25ms]
```
- **Amplification Factor**: $10\times$ to $50\times$. Background cleanup routines directly degrade user-facing read and write latencies.

### Failure Chain 3: The Cryptographic Preemption Starvation
```
[Tenant Submits Batch Transit Requests (10,000 items) + PKI 4096-bit RSA Generation]
                   │
                   ▼ (Tight crypto execution loops saturate 100% of GOMAXPROCS logical cores)
[Go Runtime Cooperative Preemption Incurring Context-Switch Delays]
                   │
                   ▼ (Goroutines for Lightweight Secret Reads wait in Go Run Queue: schedlat > 20ms)
[High Allocation Rate Triggers GC Mark Assist on Active Goroutines]
                   │
                   ▼ (Sub-millisecond Read SLA Collapses; Client P99 Latency > 100ms)
```
- **Amplification Factor**: $20\times$ to $100\times$. Purely in-memory CPU saturation destroys the performance of unrelated lightweight endpoints.
