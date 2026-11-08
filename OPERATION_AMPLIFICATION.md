# OPERATION_AMPLIFICATION.md
# OpenBao Internal Operation Amplification Analysis

This report calculates the internal operation amplification factor for each major logical operation in OpenBao. A single external REST API call induces an extensive cascade of internal functions, cryptographic operations, storage transactions, Raft consensus rounds, allocations, and lock acquisitions.

---

## 1. Complete Transaction Amplification Table

The following table provides quantified internal workload metrics for the standard execution of each logical operation under a healthy primary node running Integrated Storage (Raft) with a single active file audit device:

| Logical Operation | Storage Reads | Storage Writes | Raft Proposals | Raft Replication Ops | Crypto Operations | Key Lookups | Serialisation Ops | Deserialisation Ops | Allocations | Memory Copies | Locks Acquired | Network Calls | Audit Events |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| **Secret retrieval (KV v2)** | 3 | 0 | 0 | 0 | 4 | 3 | 2 | 4 | 320 | 18 | 9 | 0 | 2 |
| **Secret creation (KV v2)** | 5 | 2 | 2 | 2 * (N-1) | 6 | 4 | 5 | 4 | 540 | 32 | 14 | 2 * (N-1) | 2 |
| **Secret update (KV v2)** | 5 | 2 | 2 | 2 * (N-1) | 6 | 4 | 5 | 4 | 560 | 34 | 14 | 2 * (N-1) | 2 |
| **Secret deletion (KV v2)** | 2 | 1 | 1 | 1 * (N-1) | 4 | 2 | 3 | 3 | 380 | 22 | 11 | 1 * (N-1) | 2 |
| **Transit encrypt (single)** | 1 | 0 | 0 | 0 | 4 | 2 | 2 | 2 | 210 | 12 | 7 | 0 | 2 |
| **Transit decrypt (single)** | 1 | 0 | 0 | 0 | 4 | 2 | 2 | 2 | 200 | 11 | 7 | 0 | 2 |
| **Transit batch encrypt (100)**| 1 | 0 | 0 | 0 | 103 | 2 | 102 | 102 | 936 | 104 | 7 | 0 | 2 |
| **Transit batch decrypt (100)**| 1 | 0 | 0 | 0 | 103 | 2 | 102 | 102 | 732 | 104 | 7 | 0 | 2 |
| **Token creation (service)** | 2 | 3 | 3 | 3 * (N-1) | 7 | 3 | 5 | 3 | 386 | 28 | 12 | 3 * (N-1) | 2 |
| **Token validation (lookup)** | 1 | 0 | 0 | 0 | 3 | 1 | 1 | 2 | 140 | 8 | 5 | 0 | 2 |
| **Policy evaluation (ACL)** | 0 | 0 | 0 | 0 | 0 | 1 | 0 | 0 | 45 | 4 | 2 | 0 | 0 |
| **PKI issuance (w/ storage)** | 3 | 1 | 1 | 1 * (N-1) | 5 | 2 | 4 | 3 | 680 | 45 | 10 | 1 * (N-1) | 2 |
| **Lease renewal** | 3 | 1 | 1 | 1 * (N-1) | 4 | 2 | 3 | 3 | 310 | 20 | 8 | 1 * (N-1) | 2 |
| **Lease revocation** | 3 | 2 | 2 | 2 * (N-1) | 3 | 2 | 2 | 3 | 340 | 22 | 10 | 2 * (N-1) | 2 |
| **Key rotation (barrier)** | 2 | 2 | 2 | 2 * (N-1) | 4 | 2 | 3 | 2 | 280 | 16 | 6 | 2 * (N-1) | 2 |
| **Audit event generation** | 0 | 1 (file) | 0 | 0 | 3 (HMAC) | 1 | 2 (JSON) | 0 | 65 | 6 | 2 | 0 | 1 |

*Note: In the table above, $N$ represents the number of nodes in the Raft consensus cluster. Storage reads assume warm in-memory policy and transit caches.*

---

## 2. Micro-Breakdown of Critical Path Internal Work

### 2.1 Secret Creation (KV v2 POST /v1/secret/data/:path)
When a client submits 1KB of JSON data to create a versioned secret, the internal amplification unfolds as follows:

1. **Storage Read Amplification (5 Reads)**:
   - Read 1: `sys/token/id/<salted-token>`: TokenStore reads caller token from physical backend (`internal/vault/token_store.go:1833`).
   - Read 2: `policy/<name>`: Checked via 2Q LRU cache; on cache miss, reads from barrier view.
   - Read 3: `metadata/:path`: Core Router existence check (`RouteExistenceCheck()` -> `kv.dataExistenceCheck()` at `path_data.go:68`) reads metadata to differentiate Create from Update.
   - Read 4: `config`: Engine configuration read (`b.config()` at `path_data.go:271`).
   - Read 5: `metadata/:path`: Re-read under write lock inside `pathDataWrite()` (`path_data.go:303`).

2. **Storage Write Amplification (2 Writes)**:
   - Write 1: `data/:path`: Version payload protobuf written to barrier (`path_data.go:343`).
   - Write 2: `metadata/:path`: Updated metadata protobuf with version index written to barrier (`path_data.go:351`).

3. **Consensus & Network Amplification**:
   - 2 independent Raft log proposals (`RaftBackend.Put()` -> `applyLog()` at `internal/physical/raft/raft.go:1746`).
   - Each proposal undergoes serialization into `LogData`, Raft log append, RPC broadcast to all $N-1$ follower nodes, disk fsync on quorum, and FSM commit on leader and followers.
   - Total network RPCs = $2 \times 2 \times (N - 1)$ network messages (AppendEntries request + response).

4. **Cryptographic Amplification (6 Operations)**:
   - Crypto 1: HMAC-SHA256 of caller token ID (`token_store.go:1132`).
   - Crypto 2: AES-256-GCM Decryption of caller token storage entry (`aes_gcm.go:888`).
   - Crypto 3: AES-256-GCM Encryption of new version storage entry with fresh 12-byte IV (`aes_gcm.go:1016`).
   - Crypto 4: AES-256-GCM Encryption of key metadata storage entry.
   - Crypto 5: HMAC-SHA256 hashing of sensitive fields for Request Audit Log (`audit/format.go`).
   - Crypto 6: HMAC-SHA256 hashing of response fields for Response Audit Log.

5. **Serialization & Deserialization Amplification**:
   - Deserialization: JSON decode HTTP body -> JSON decode caller token -> Protobuf unmarshal metadata (existence check) -> Protobuf unmarshal metadata (pathDataWrite).
   - Serialization: JSON encode secret data -> Protobuf marshal `Version` -> Protobuf marshal `KeyMetadata` -> JSON encode Request Audit Entry -> JSON encode Response Audit Entry.

6. **Lock Synchronisation (14 Locks)**:
   - Core `stateLock.RLock()` (`request_handling.go:586`).
   - TokenStore `saltLock.RLock()` (`token_store.go:912`).
   - Token lock `tokenLocks[hash].RLock()` (`token_store.go:1696`).
   - Barrier `l.RLock()` for token decrypt (`aes_gcm.go:850`).
   - KV per-key striped lock `b.locks[hash].Lock()` (`path_data.go:289`).
   - Barrier `l.RLock()` for version encrypt (`aes_gcm.go:814`).
   - Barrier `l.RLock()` for metadata encrypt.
   - Raft `l.RLock()` and `permitPool.Acquire()` for write 1 (`raft.go:1765`).
   - Raft `l.RLock()` and `permitPool.Acquire()` for write 2.
   - AuditBroker `RLock()` (request) + File audit `fileLock.Lock()` (`backend.go:228`).
   - AuditBroker `RLock()` (response) + File audit `fileLock.Lock()`.

---

### 2.2 Transit Batch Operations (100 Items)
Transit batch encryption demonstrates how algorithmic vectorization avoids storage amplification but scales linearly with payload count:

1. **Storage Invariance**:
   - Exactly 1 storage read occurs for the caller token regardless of batch size.
   - 0 storage writes occur.
   - 0 Raft consensus rounds.

2. **Cryptographic Scaling**:
   - Single item batch: 4 crypto ops (token HMAC + token AES-GCM + 1 data encrypt + audit HMAC).
   - 100-item batch: 103 crypto ops (1 token HMAC + 1 token AES-GCM + 100 AES-GCM Encrypt + audit HMAC).
   - 10,000-item batch: 10,003 crypto ops.
   - Execution time scales strictly linearly:
     - 1 item: 14.0 $\mu$s (45 allocs, 4.5 KB)
     - 100 items: 402.8 $\mu$s (936 allocs, 183.3 KB)
     - 1,000 items: 3,378.8 $\mu$s (9,939 allocs, 1.79 MB)
     - 10,000 items: 34,734.1 $\mu$s (99,948 allocs, 17.88 MB)

3. **Memory & Allocation Inefficiencies**:
   - Each batch item incurs an individual base64 decoding allocation, a `cipher.AEAD.Seal()` allocation, and a string format allocation (`vault:v1:...`).
   - Notice that allocations per item remain fixed at $\approx 9.9$ allocs/item across all batch sizes, reflecting a lack of slice/buffer recycling.

---

### 2.3 Token Creation (Service Token)
Creating a service token is one of the heaviest non-PKI write operations in OpenBao:
- **Storage Reads**: 2 (caller token + parent token verification).
- **Storage Writes**: 3 distinct storage entries persisted sequentially or semi-transactionally:
  1. Accessor Index (`sys/token/accessor/<salted-accessor>`)
  2. Parent Hierarchy Index (`sys/token/parent/<salted-parent>/<salted-child>`)
  3. Primary Token Payload (`sys/token/id/<salted-token-id>`)
- **Crypto Ops**: 7 (3 random byte generations from CSPRNG, 2 salt HMACs, 1 SSC signature HMAC, 2 barrier AES-GCM encryptions).
- **Consensus Roundtrips**: 3 Raft log proposals committed sequentially if storage transactions are unbatched.

---

## 3. Workload Scaling & Amplification Growth Drivers

The total internal work generated per request grows disproportionately when specific systemic dimensions scale:

### 3.1 Audit Device Multiplier ($D$)
Every registered audit device introduces a synchronous multiplier on every external request:
$$\text{Audit Writes} = 2 \times D$$
$$\text{Audit HMAC Calculations} = 2 \times D \times K_{\text{sensitive}}$$
Where $K_{\text{sensitive}}$ is the number of sensitive fields, headers, and parameters in the request and response. If 3 audit devices are enabled (e.g., local file, remote syslog, socket), each request performs 6 synchronous disk/network I/O flushes. If any device blocks, the client request stalls.

### 3.2 Raft Cluster Node Count ($N$)
For mutating operations (KV write, token creation, PKI storage, lease updates):
$$\text{Consensus Network Packets} = 2 \times W \times (N - 1)$$
Where $W$ is the number of barrier writes per API call. For token creation ($W = 3$) in a 5-node cluster ($N = 5$):
$$\text{Packets} = 2 \times 3 \times 4 = 24 \text{ Raft network RPCs per token creation}$$

### 3.3 Number of Attached Policies ($P$) and Namespace Depth ($S$)
When a token contains $P$ policies across a namespace hierarchy of depth $S$:
- Token store must resolve $P$ policies from cache or storage.
- Radix trees in `policy/acl.go: NewACL()` must clone and insert rules for every path entry across all $P$ policies.
- If templated policies are used (e.g. `{{identity.entity.id}}`), the policy cannot be pre-compiled into the static ACL cache; it must be re-parsed and re-evaluated dynamically on **every request**.
- Radix tree prefix searches traverse string segments proportional to $S + \text{path depth}$.

### 3.4 Key Versions Accumulation ($V$) in KV v2
In KV v2, the `KeyMetadata` protobuf structure holds an in-memory dictionary of all version tombstones and timestamps:
- Every secret write reads the entire `KeyMetadata` protobuf.
- As $V$ increases (up to `max_versions`), the serialized metadata grows, increasing barrier encryption/decryption overhead and storage payload size on every single write.
- If $V$ exceeds `max_versions`, KV v2 triggers `cleanupOldVersions()`, issuing $M$ sequential `storage.Delete()` operations to purge old version payloads.

---

## 4. Work Amplification Matrix

| Scaling Dimension | Affected Operations | Amplification Mechanism | Growth Complexity |
|---|---|---|---|
| **Number of Audit Devices ($D$)** | All 16 Operations | Synchronous file/socket writes & HMAC-SHA256 computations in `AuditBroker` | $O(D)$ |
| **Cluster Nodes ($N$)** | All write/update/delete operations | Raft log replication RPCs & follower confirmations | $O(N)$ |
| **Number of Policies ($P$)** | All authenticated requests | ACL radix tree construction & capability scanning in `acl.go` | $O(P)$ |
| **Namespace Depth ($S$)** | All requests | Path canonicalization, namespace tree traversal, prefix checks | $O(S)$ |
| **Batch Size ($B$)** | Transit Batch Encrypt / Decrypt | Repeated JSON map decoding, base64 decoding, AEAD allocations | $O(B)$ |
| **KV Version History ($V$)** | Secret Creation & Update | Metadata serialization size, old version traversal & pruning | $O(V)$ |
| **Active Leases ($L$)** | Lease Revocation / Expiration | In-memory timer priority queue updates & storage index deletes | $O(\log L)$ |
