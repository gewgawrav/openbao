# Request Execution Graph & Lifecycle Trace Audit 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document provides an exhaustive, multi-layer architectural trace of OpenBao request lifecycles relevant to the Cloakit Privacy Vault workload (95% reads, 5% writes). OpenBao operates as a Layer 2 Subject Key management and cryptographic processing engine beneath Layer 1 Cloud KMS/HSM KEKs and above Layer 3 PostgreSQL DEKs.

Every logical Cloakit operation traverses up to 9 distinct architectural layers within OpenBao:
1. **Network Ingress & TLS Termination** (`net/http`, `crypto/tls`)
2. **Core Ingress & Context Initialization** (`internal/vault/core.go:HandleRequest`)
3. **Authentication & Token Verification** (`internal/vault/token_store.go:LookupToken`)
4. **Identity & ACL Policy Authorization** (`internal/vault/policy_store.go`, `internal/vault/acl.go`)
5. **Route Dispatch & Mount Table Matching** (`internal/vault/router.go:Route`)
6. **Transit Engine Coordination & Key Concurrency** (`internal/builtin/logical/transit/backend.go`, `sdk/helper/keysutil/lock_manager.go`)
7. **Physical Barrier Decryption / Caching** (`internal/vault/barrier/aes_gcm.go`, `sdk/helper/keysutil/transit_lru.go`, `sdk/helper/keysutil/transit_syncmap.go`)
8. **Cryptographic Primitives Execution** (`sdk/helper/keysutil/policy.go`, `crypto/aes`, `crypto/cipher`)
9. **Audit Broker & Network Egress Serialization** (`internal/vault/audit_broker.go`, `json.Marshal`)

This trace establishes the baseline call graph and sequence for:
- **Operation 1:** Subject Key Lookup
- **Operation 2:** DEK Wrapping (`/transit/encrypt/:name`)
- **Operation 3:** DEK Unwrapping (`/transit/decrypt/:name`)
- **Operation 4:** Key Creation (`/transit/keys/:name` POST)
- **Operation 5:** Key Rotation (`/transit/keys/:name/rotate` POST)
- **Operation 6:** Key Revocation / Deletion (`/transit/keys/:name` DELETE)

---

## 1. Architectural Execution Layer Map

```
+-----------------------------------------------------------------------------------------------------------------------+
|                                        OPENBAO 9-LAYER REQUEST EXECUTION PIPELINE                                     |
+-----------------------------------------------------------------------------------------------------------------------+
| Layer 1: Network Ingress & TLS Parsing      | HTTP/1.1 or HTTP/2 listener, TLS handshake/session decryption, epoll  |
| Layer 2: Core Ingress & Context Init        | Request UUID generation, namespace extraction, deadline handling       |
| Layer 3: Authentication & Token Lookup      | Token salt hashing, TokenStore lookup, lease expiration check          |
| Layer 4: Authorization & Policy Evaluation  | ACL path rule matching, capability evaluation (read/create/update)    |
| Layer 5: Route Matching & Dispatch         | Trie/prefix match across mount table, barrier view delegation          |
| Layer 6: Transit Backend & Lock Management  | LockManager acquisition (RWMutex/shard lock), LRU/SyncMap policy cache|
| Layer 7: Barrier Storage Decryption (Miss)  | Storage Get, AES-256-GCM barrier unwrap, JSON policy deserialization   |
| Layer 8: Cryptographic Execution            | KeyEntry derivation, AES-GCM 96-bit nonce generation, AEAD seal/open  |
| Layer 9: Audit & Response Egress            | HMAC redaction, audit broker fanout, JSON serialization, TLS socket out|
+-----------------------------------------------------------------------------------------------------------------------+
```

---

## 2. Operation 1: Subject Key Lookup Trace
- **Classification:** `SOURCE-CONFIRMED`
- **Source References:** `internal/vault/core.go:120-250`, `sdk/helper/keysutil/lock_manager.go:296-330`, `sdk/helper/keysutil/transit_syncmap.go:14-38`, `internal/builtin/logical/transit/path_keys.go:80-140`

### Step-by-Step Call Flow
1. **Network Ingress:** Socket accepts HTTP GET `/v1/transit/keys/{subject_id}`.
2. **Core Dispatch:** `Core.HandleRequest` extracts `X-Vault-Token`, generates request ID.
3. **Authentication:** `TokenStore.LookupToken` queries barrier or local token cache (`id/` path).
4. **ACL Authorization:** `Core.fetchACLToken` evaluates token policies against `transit/keys/{subject_id}` capability `read`.
5. **Mount Router:** Mount table matches `transit/` prefix and routes to transit `*backend`.
6. **Key Retrieval:** `b.lm.GetPolicy(ctx, req, rand)` is invoked.
7. **Cache Check:**
   - In `TransitLRU` mode: `TransitLRU.Get(name)` acquires `sync.Mutex`, checking `TwoQueueCache`.
   - In `TransitSyncMap` mode: `TransitSyncMap.Load(name)` performs lock-free pointer dereference.
8. **Cache Hit Path (95% workload):** Policy struct returned directly. Read lock held briefly.
9. **Cache Miss Path (5% cold start):**
   - Calls `getPolicyFromStorage(ctx, storage, name)`.
   - Executes `storage.Get(ctx, "policy/" + name)`.
   - `AESGCMBarrier.Decrypt` performs AES-256-GCM unwrap of policy blob.
   - `json.Unmarshal` deserializes into `keysutil.Policy`.
   - Stores in cache via `TransitLRU.Put` or `TransitSyncMap.Store`.
10. **Response Serialization:** Sanitizes private key material (unless exportable); serializes public metadata (latest version, min decryption version, key type) into JSON response.
11. **Audit Logging:** If audit devices enabled, logs request and response metadata.

---

## 3. Operation 2: DEK Wrapping Trace (`/transit/encrypt/{name}`)
- **Classification:** `SOURCE-CONFIRMED`
- **Source References:** `internal/builtin/logical/transit/path_encrypt.go:298-460`, `sdk/helper/keysutil/policy.go:1098-1150`, `sdk/helper/keysutil/lock_manager.go:308-335`

### Step-by-Step Call Flow
1. **HTTP Ingress:** POST `/v1/transit/encrypt/{subject_id}` with JSON body `{"plaintext": "<base64_dek>", "context": "<base64_aad>"}`.
2. **Ingress Unmarshaling:** Go `json.NewDecoder` unmarshals payload into `map[string]interface{}`.
3. **Framework Field Parsing:** `framework.FieldData` runs reflection-based validation via `mapstructure`.
4. **Authentication & ACL:** Validates token and checks `update` or `create` permission on `transit/encrypt/{subject_id}`.
5. **Lock Acquisition:** `b.lm.GetPolicyWithLockType(ctx, req, rand, false)` acquires `RLock` on key shard lock.
6. **Cache Resolution:** Retrieves `Policy` pointer from LRU or SyncMap.
7. **Key Version Resolution:** If version unspecified, resolves to `Policy.LatestVersion`.
8. **Cryptographic Wrapping:**
   - Calls `Policy.Encrypt(ver, context, nonce, value)`.
   - Decodes base64 plaintext DEK (32 bytes).
   - Generates 12-byte random IV (`crypto/rand.Read`).
   - Retrieves key bytes for version (`Policy.GetKey`).
   - If derived key enabled, derives subkey via HKDF-SHA256 (`Policy.DeriveKey`).
   - Instantiates AES block cipher: `cipher, err := aes.NewCipher(encKey)`.
   - Instantiates GCM mode: `gcm, err := cipher.NewGCM(cipher)`.
   - Encrypts and computes 16-byte GMAC tag: `gcm.Seal(nil, iv, plaintext, additionalData)`.
   - Prepends IV to ciphertext.
   - Formats ciphertext as `vault:v{ver}:{base64(iv+ciphertext+tag)}`.
9. **Lock Release:** Releases `RUnlock` on shard lock.
10. **Response Serialization:** Formats JSON envelope `{"data": {"ciphertext": "vault:v1:..."}}`.
11. **Audit Logging:** Logs request path and non-confidential context (plaintext is redacted).

---

## 4. Operation 3: DEK Unwrapping Trace (`/transit/decrypt/{name}`)
- **Classification:** `SOURCE-CONFIRMED`
- **Source References:** `internal/builtin/logical/transit/path_decrypt.go:100-240`, `sdk/helper/keysutil/policy.go:1106-1260`, `sdk/helper/keysutil/lock_manager.go:308-335`

### Step-by-Step Call Flow
1. **HTTP Ingress:** POST `/v1/transit/decrypt/{subject_id}` with JSON body `{"ciphertext": "vault:v1:...", "context": "<base64_aad>"}`.
2. **Payload Parsing:** JSON unmarshaled to generic map; `mapstructure` copies fields.
3. **Auth & ACL Check:** Evaluates token against `transit/decrypt/{subject_id}` capability `update`.
4. **Policy Resolution:** `b.lm.GetPolicyWithLockType(ctx, req, rand, false)` acquires `RLock` and reads policy from memory cache.
5. **Ciphertext Parsing:**
   - Checks prefix `vault:v`.
   - Parses integer version string `1`.
   - Verifies version bounds: `ver >= MinDecryptionVersion` and `ver <= LatestVersion`.
   - Base64 decodes ciphertext payload into byte slice.
6. **Cryptographic Unwrapping:**
   - Calls `Policy.DecryptWithFactory`.
   - Retrieves key bytes for specified version (`Policy.GetKey(context, ver, 32)`).
   - Separates 12-byte nonce from ciphertext payload.
   - Instantiates `aes.NewCipher(encKey)` and `cipher.NewGCM(cipher)`.
   - Computes GHASH and decrypts: `gcm.Open(nil, nonce, ciphertextTag, aad)`.
   - Base64 encodes decrypted plaintext DEK.
7. **Lock Release:** `RUnlock` released.
8. **Egress Response:** Serializes JSON `{"data": {"plaintext": "<base64_dek>"}}`.

---

## 5. Operation 4: Key Creation Trace (`/transit/keys/{name}`)
- **Classification:** `SOURCE-CONFIRMED`
- **Source References:** `internal/builtin/logical/transit/path_keys.go:150-250`, `sdk/helper/keysutil/lock_manager.go:302-340`, `sdk/helper/keysutil/policy.go:1930-1980`

### Step-by-Step Call Flow
1. **HTTP Ingress:** POST `/v1/transit/keys/{subject_id}` with parameters (type: `aes256-gcm96`, derived: false, exportable: false).
2. **Auth & ACL:** Token authorized for `create` or `update` on `transit/keys/{subject_id}`.
3. **Exclusive Lock:** `b.lm.GetPolicyExclusive(ctx, req, rand)` acquires exclusive `Lock` for key name.
4. **Existence Check:** Verifies policy does not exist in memory or storage.
5. **Key Generation:**
   - Initializes new `Policy` struct.
   - Generates 32 random bytes via `crypto/rand.Read`.
   - Creates `KeyEntry` at version 1.
6. **Storage Persistence:**
   - Calls `Policy.Persist(ctx, storage)`.
   - `Policy.Serialize` encodes policy struct to JSON.
   - `AESGCMBarrier.Encrypt` seals plaintext JSON with active barrier key.
   - Barrier invokes physical backend: `PostgreSQLBackend.Put(ctx, entry)`.
   - Executes SQL `INSERT INTO vault_kv_store ... ON CONFLICT DO UPDATE`.
7. **Cache Population:** Saves new `Policy` into `TransitLRU` or `TransitSyncMap`.
8. **Lock Release:** Releases exclusive lock.

---

## 6. Operation 5: Key Rotation Trace (`/transit/keys/{name}/rotate`)
- **Classification:** `SOURCE-CONFIRMED`
- **Source References:** `internal/builtin/logical/transit/path_keys.go:280-340`, `sdk/helper/keysutil/policy.go:1930-1965`

### Step-by-Step Call Flow
1. **HTTP Ingress:** POST `/v1/transit/keys/{subject_id}/rotate`.
2. **Auth & ACL:** Validates `update` permission.
3. **Exclusive Lock:** Acquires exclusive write lock on key.
4. **Policy Fetch:** Loads policy from cache or storage.
5. **Rotation Logic:**
   - Increments `Policy.LatestVersion` ($N \to N+1$).
   - Generates new 32-byte key material for version $N+1$.
   - Appends to `Policy.Keys` map.
6. **Storage Write:**
   - Barrier encryption of expanded policy JSON.
   - PostgreSQL `Put` execution (updating existing row).
7. **Cache Update:** Replaces cached policy pointer with updated version.
8. **Lock Release:** Unlocks key.

---

## 7. Operation 6: Key Revocation / Deletion Trace (`/transit/keys/{name}`)
- **Classification:** `SOURCE-CONFIRMED`
- **Source References:** `sdk/helper/keysutil/lock_manager.go:543-580`, `internal/builtin/logical/transit/path_keys.go:350-410`

### Step-by-Step Call Flow
1. **HTTP Ingress:** DELETE `/v1/transit/keys/{subject_id}`.
2. **Auth & ACL:** Validates `delete` capability.
3. **Exclusive Lock:** Acquires exclusive lock.
4. **Policy Validation:** Checks `deletion_allowed` flag on policy.
5. **Physical Deletion:**
   - Calls `storage.Delete(ctx, "policy/" + name)`.
   - PostgreSQL executes `DELETE FROM vault_kv_store WHERE path = $1 AND key = $2`.
   - Deletes archived keys path if archiving was enabled (`archive/` view).
6. **Cache Eviction:** Removes policy from `TransitLRU` / `TransitSyncMap`.
7. **Lock Manager Cleanup:** Cleans up lock entries.
8. **Lock Release:** Releases lock.
