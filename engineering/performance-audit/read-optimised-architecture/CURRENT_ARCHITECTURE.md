# CURRENT_ARCHITECTURE.md: OpenBao Request Lifecycle & Layer Architecture Audit 🤖

## Executive Summary

This report delivers a rigorous architectural audit of the OpenBao repository (targeting commit `HEAD` of branch `gewgawrav/openbao`), establishing the exact request lifecycle, key hierarchy, caching behavior, and physical storage execution path. This audit evaluates OpenBao functioning as a **Layer 2 Subject Key management and cryptographic processing service** within the Cloakit Privacy Vault ecosystem.

In Cloakit's workload model:
- **Workload Profile**: 95% reads (DEK unwrapping / decryption), 5% writes (Subject Key creation / rotation, initial DEK generation / encryption).
- **Scale**: High Subject Key cardinality ($10^4$ to $10^7$ keys), high concurrency ($10^3$ to $10^5$ req/sec).
- **Latency Target**: Predictable sub-millisecond to low-millisecond P95/P99 latency with minimal GC overhead.

All findings are classified strictly by evidence level:
- `SOURCE-CONFIRMED`: Verified via direct inspection of source code and line numbers.
- `STATIC-ANALYSIS-CONFIRMED`: Verified via code compilation, AST/call-graph inspection, and syntax patterns.
- `TEST-CONFIRMED`: Validated by executing unit or integration tests in the test suite.
- `BENCHMARK-CONFIRMED`: Validated using existing benchmark suites in tree.
- `INFERRED`: Architecturally deduced from structural and concurrency semantics.
- `UNKNOWN`: Insufficient evidence in local repository to verify conclusively.

---

## 1. Key Hierarchy and Architectural Boundary (Cloakit vs OpenBao)

The cryptographic hierarchy spanning Cloakit and OpenBao comprises three distinct layers:

```
+-----------------------------------------------------------------------------------+
| LAYER 1: Cloud KMS / HSM Client KEK (Key Encryption Key)                          |
| - Location: AWS KMS, GCP KMS, Azure Key Vault, or PKCS#11 Hardware HSM             |
| - Boundary: External to OpenBao barrier (or used via Transit / BYOK external keys)|
+-----------------------------------------------------------------------------------+
                                         |
                                         v
+-----------------------------------------------------------------------------------+
| LAYER 2: Subject Keys (OpenBao Transit Backend)                                    |
| - Location: OpenBao Transit Mount (`sys/mounts/transit` -> `logical/transit`)     |
| - Representation: `keysutil.Policy` (`sdk/helper/keysutil/policy.go`)            |
| - Storage: Barrier-encrypted (`policy/{key_name}`) -> Physical Key-Value Store   |
| - In-Memory Cache: `keysutil.LockManager` (`TransitSyncMap` or `TransitLRU`)     |
+-----------------------------------------------------------------------------------+
                                         |
                                         v
+-----------------------------------------------------------------------------------+
| LAYER 3: Data Encryption Keys (DEKs)                                              |
| - Generation/Wrapping: OpenBao Transit (`/transit/datakey/wrapped/{key_name}`)    |
| - Unwrapping: OpenBao Transit (`/transit/decrypt/{key_name}`)                     |
| - Storage: Encrypted application records + Wrapped DEKs stored in PostgreSQL      |
+-----------------------------------------------------------------------------------+
```

### Key Lifecycle Contracts
1. **DEK Wrapping (Write / Setup Path - 5%)**:
   - Application calls OpenBao `/transit/datakey/wrapped/:name` or `/transit/encrypt/:name`.
   - OpenBao Transit loads the Subject Key (`Policy`), generates 256-bit high-entropy DEK via `crypto/rand`, encrypts it under the Subject Key's active version, and returns the ciphertext to the caller.
   - Cloakit persists the wrapped DEK in PostgreSQL alongside the record ciphertext.
2. **DEK Unwrapping (Read Path - 95%)**:
   - Application fetches wrapped DEK from PostgreSQL and dispatches `/transit/decrypt/:name` with `ciphertext=vault:v1:...`.
   - OpenBao Transit retrieves Subject Key (`Policy`), locates key version 1, initializes cipher (`AES-256-GCM` or `ChaCha20-Poly1305`), decrypts the DEK, and returns the plaintext DEK over TLS.
   - Application unwraps data in-memory without storing plaintext DEK.

---

## 2. Complete Request Lifecycle Walkthrough

```
[ HTTP Client / Cloakit Engine ]
              |
              | (1) HTTP/1.1 or HTTP/2 POST /v1/transit/decrypt/:name
              v
[ internal/http/handler.go & logical.go ]
   - Handler dispatch, CORS check, context cancellation propagation
   - Logical request parse: path extraction, JSON unmarshal
              |
              v
[ internal/vault/request_handling.go: HandleRequest ]
   - State Lock check (`c.stateLock.RLock()`)
   - Namespace resolution (`ResolveNamespaceFromRequest`)
   - Authentication check (`CheckToken` / Inline Auth):
       * Batch Token: Fast path, zero storage reads (decrypted locally via barrier AEAD)
       * Service Token: Read lock on `ts.tokenLocks`, storage lookup (`id/{salted_id}`),
         barrier decryption, JSON unmarshal. If use-count > 0: durable write!
   - Audit Broker dispatch (`LogRequest`):
       * HMAC calculation on non-whitelisted request payload fields
       * Synchronous write lock across registered audit sinks
              |
              v
[ internal/vault/routing: Router.Route ]
   - Mount prefix match (`MatchingMount`, `MatchingBackend`)
   - Dispatch to `internal/builtin/logical/transit/backend.go`
              |
              v
[ internal/builtin/logical/transit: pathDecrypt ]
   - Parameter parsing & batch item iteration (`batch_input`)
   - Subject Key Resolution via `b.GetPolicy(ctx, PolicyRequest{...})`:
       * `b.configMutex.RLock()` -> checks `b.cacheSizeChanged`
       * `b.lm.GetPolicyWithLockType(..., exclusive=false)`:
           - Fast-path: `lm.cache.Load(req.Name)`
               * Hit: acquires `p.Lock(exclusive=false)` (`RWMutex.RLock()`)
               * Miss: acquires 256-shard lock (`locksutil.LockForKey(lm.keyLocks, name)`)
                       storage fetch (`LoadPolicy` -> `req.Storage.Get("policy/" + name)`)
                       barrier decryption -> JSON decode into `keysutil.Policy`
                       stores into `lm.cache` -> policy read lock acquired
              |
              v
[ sdk/helper/keysutil/policy.go: DecryptWithFactory / SymmetricDecryptRaw ]
   - Parse ciphertext version header (`vault:v{ver}:{base64}`)
   - Extract key version from `p.Keys[strconv.Itoa(ver)]`
   - Key derivation check (`p.Derived`): HKDF-SHA256 if derived; else raw key
   - Cipher allocation & execution:
       * `aes.NewCipher(encKey)` -> allocates cipher block
       * `cipher.NewGCM(aesCipher)` -> allocates GCM cipher context
       * `aead.Open(nil, nonce, trueCT, additionalData)` -> verifies tag & decrypts
              |
              v
[ internal/vault/request_handling.go: Post-Processing & Audit ]
   - Audit Broker dispatch (`LogResponse`): HMAC audit hashing + sink I/O
   - Response formatting & serialization (`logical.LogicalResponseToHTTPResponse`)
              |
              v
[ internal/http/logical.go: respondLogical / respondOk ]
   - JSON encoding into HTTP response body (`jsonutil.EncodeJSON`)
   - Write headers (`Content-Type: application/json`) and payload
```

---

## 3. Storage Layer Architecture (Logical to Physical)

When a Subject Key is missed in `keysutil.LockManager` cache or during initial boot/upsert, the request traverses the full storage stack:

```
+---------------------------------------------------------------------------------------+
| 1. LOGICAL STORAGE VIEW (`internal/vault/barrier/view.go`)                            |
|    - Path scoping: prepends mount prefix (e.g., `logical/{uuid}/policy/{name}`)       |
|    - Sanitizes keys and enforces namespace view constraints                          |
+---------------------------------------------------------------------------------------+
                                           |
                                           v
+---------------------------------------------------------------------------------------+
| 2. SECURITY BARRIER (`internal/vault/barrier/aes_gcm.go: AESGCMBarrier`)              |
|    - Concurrency: `b.l.RLock()` protects keyring and seal state                       |
|    - Cryptographic operations:                                                        |
|        * Read: `backend.Get(ctx, key)` -> extracts 4-byte term -> looks up cached    |
|          term AEAD (`b.cacheLock.RLock()`) -> `aead.Open(nil, nonce, ct, [term+key])`|
|        * Write: term AEAD -> `aead.Seal(...)` -> creates `physical.Entry`             |
+---------------------------------------------------------------------------------------+
                                           |
                                           v
+---------------------------------------------------------------------------------------+
| 3. PHYSICAL CACHE LAYER (`sdk/physical/cache.go: Cache`)                              |
|    - Default size: 131,072 entries (`DefaultCacheSize = 128 * 1024`)                  |
|    - Cache eviction algorithm: 2Q Cache (`lru.TwoQueueCache[string, *Entry]`)         |
|    - Sharded concurrency control: 256 sharded RWMutexes (`locksutil.LockCount = 256`)|
|    - Concurrency hash function: `Blake2b256Hash(key)[0]`                              |
+---------------------------------------------------------------------------------------+
                                           |
                                           v
+---------------------------------------------------------------------------------------+
| 4. PHYSICAL STORAGE BACKEND                                                           |
|    - Integrated Storage (Raft / BoltDB):                                             |
|        * Read: read-only bbolt transaction (`tx.Bucket().Get()`)                      |
|        * Write: Raft log proposal -> cluster quorum consensus -> FSM Apply ->         |
|          writable bbolt transaction (`tx.Bucket().Put()`) -> disk sync (`fdatasync`)  |
|    - PebbleDB (`internal/physical/pebbledb`):                                         |
|        * Read: Pebble snapshot read (`db.Get()`) via LSM block cache / memtable      |
|        * Write: WriteBatch commit (`db.Apply()`) -> WAL sync                          |
|    - PostgreSQL (`internal/physical/postgresql`):                                     |
|        * Read: `SELECT value FROM vault_kv WHERE path = $1`                           |
|        * Write: `INSERT INTO ... ON CONFLICT DO UPDATE`                               |
+---------------------------------------------------------------------------------------+
```

---

## 4. Operation Classification: Read-Only vs. Durable Writes

To optimize for the 95:5 Cloakit workload, we must strictly separate operations that are purely computational/in-memory reads from those that trigger durable storage writes:

| Endpoint / Operation | Classification | Storage Operations (authoritative) | Physical Write Amplification |
| :--- | :--- | :--- | :--- |
| `POST /v1/transit/decrypt/:name` | **Read-Only** | 0 storage reads (if `Policy` cached). 1 barrier read + 1 physical read on miss. | Zero disk writes. |
| `POST /v1/transit/datakey/wrapped/:name` | **Read-Only** | 0 storage reads (if cached). Encrypts freshly generated random bytes in memory. | Zero disk writes. |
| `POST /v1/transit/datakey/plaintext/:name` | **Read-Only** | 0 storage reads (if cached). Returns raw bytes + encrypted bytes. | Zero disk writes. |
| `POST /v1/transit/keys/:name` (Create) | **Durable Write** | Upserts key metadata, generates term 1 key, commits to `policy/{name}`. | 1 Raft log + 1 FSM bbolt write + barrier encryption. |
| `POST /v1/transit/keys/:name/rotate` | **Durable Write** | Generates version $N+1$, archives old keys if configured, writes to storage. | 1 Raft log + 1 FSM bbolt write. |
| `Token Authentication (Service Token)` | **Durable Write (if use count > 0)** | Reads token from `id/{salt}`. If `NumUses > 0`, updates token entry on every single read! | 1 Raft log entry + bbolt write per request! |
| `Token Authentication (Batch Token)` | **Read-Only** | Decrypted statelessly using Barrier Root Key. Zero storage reads or writes. | Zero disk writes. |
| `Audit Logging (File / Syslog / Socket)` | **Durable I/O Write** | Sinks write JSON audit record for every request and response synchronously. | Disk or pipe I/O write on every call. |

### Critical Source Confirmation: The Service Token Use-Count Hazard
`SOURCE-CONFIRMED`: In `internal/vault/token_store.go` lines 1625–1685 (`UseToken`):
```go
if te.NumUses == 0 {
    return te, nil
}
lock := locksutil.LockForKey(ts.tokenLocks, te.ID)
lock.Lock()
defer lock.Unlock()
...
err = ts.store(ctx, te)
```
If Cloakit clients use Service Tokens with finite `num_uses` (or use tokens that track use counts), **every read operation (DEK unwrap) becomes a durable storage write**, completely destroying read performance. Cloakit **must** either use Batch Tokens or Service Tokens with `num_uses = 0` (unlimited).

---

## 5. Overhead and Bottleneck Identification

Through line-by-line inspection, six critical friction points have been identified in the current read path:

1. **Lock Sharding Allocation Bug in `locksutil`** (`SOURCE-CONFIRMED: sdk/helper/locksutil/sharded.go:53`):
   Every time `LockIndexForKey` is called, it executes:
   ```go
   return uint8(cryptoutil.Blake2b256Hash(key)[0])
   ```
   In `sdk/helper/cryptoutil/cryptoutil.go:8-14`:
   ```go
   func Blake2b256Hash(key string) []byte {
       hf, _ := blake2b.New256(nil)
       hf.Write([]byte(key))
       return hf.Sum(nil)
   }
   ```
   **Every lock acquisition allocates a new BLAKE2b hash state, allocates a `[]byte(key)` copy, and allocates a 32-byte digest slice**, solely to extract 8 bits of entropy. Under 50,000 req/sec, this generates 150,000 unnecessary heap allocations per second just for sharded mutex selection.

2. **Per-Operation Cipher Context Allocation** (`SOURCE-CONFIRMED: sdk/helper/keysutil/policy.go:2325-2345`):
   In `SymmetricDecryptRaw`:
   ```go
   aesCipher, err := aes.NewCipher(encKey)
   gcm, err := cipher.NewGCM(aesCipher)
   ```
   For every single DEK unwrapped, OpenBao allocates an `aesCipher` structure, expands the AES round keys, and initializes Galois Counter Mode hash subkeys (`ghash`). Neither the `cipher.Block` nor the `cipher.AEAD` context is cached on `Policy.Keys[ver]`.

3. **Global Config RWMutex Contention** (`SOURCE-CONFIRMED: internal/builtin/logical/transit/backend.go:153-178`):
   Every call to `b.GetPolicy` acquires `b.configMutex.RLock()`. Under high concurrency across dozens of goroutines on a high-core server (e.g. 64 vCPUs), `sync.RWMutex.RLock()` suffers reader cache-line bouncing (`readerCount` atomic increments on the same cache line).

4. **Policy-Level RWMutex Lock Contention** (`SOURCE-CONFIRMED: sdk/helper/keysutil/lock_manager.go:322`):
   When a key is loaded from cache:
   ```go
   p.Lock(exclusive)
   ```
   `p.Lock(false)` takes an `RLock()` on `Policy.l`. If many concurrent requests are unwrapping DEKs under the *same* Subject Key, they all bounce on `Policy.l.readerCount`.

5. **JSON Serialization / Deserialization on Key Loading** (`SOURCE-CONFIRMED: sdk/helper/keysutil/policy.go:403`):
   When a policy misses the cache, it is loaded from storage and decoded via `jsonutil.DecodeJSON(entry.Value, p)`. For large policies with many rotated keys, JSON parsing involves massive reflection, string parsing, and multiple map allocations (`p.Keys = make(map[string]KeyEntry)`).

6. **Base64 Encoding Overhead** (`SOURCE-CONFIRMED: internal/builtin/logical/transit/path_decrypt.go:140`, `sdk/helper/keysutil/policy.go:1145`):
   Ciphertexts and plaintexts are converted back and forth: string -> base64 decode -> byte slice -> AEAD decrypt -> byte slice -> base64 encode -> string -> JSON response.

---

## 6. Summary Evaluation for Cloakit

The existing OpenBao Transit architecture is functionally complete and cryptographically sound, but is architected as a general-purpose secrets engine rather than a specialized, high-throughput microsecond cryptographic primitive. For Cloakit's 95% read workload:
- Cache hits in Transit bypass Raft and barrier decryption completely.
- However, hot-path lock bouncing, cipher context re-allocation, Blake2b lock hashing, and JSON/base64 conversions impose significant throughput ceilings and latency jitter at high Subject Key cardinalities.

Subsequent reports quantify these factors and design high-performance drop-in architectural mitigations.
