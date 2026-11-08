# 6. DEK OPERATION OPTIMISATION 🤖

## Executive Summary

Data Encryption Key (DEK) wrapping and unwrapping constitute the critical operational core of Cloakit's privacy vault. In Cloakit, sensitive application payloads are encrypted locally with symmetric DEKs. The DEKs are then wrapped (encrypted) by OpenBao using Layer 2 Subject Keys. During record retrieval, the wrapped DEK is unwrapped (decrypted) by OpenBao to allow application decryption.

This report conducts an end-to-end execution trace of OpenBao's DEK operations (`datakey`, `encrypt`, `decrypt`, `rewrap`), identifies CPU, memory, and synchronization bottlenecks, and specifies concrete architectural optimizations for batching, zero-allocation buffers, and bounded concurrency.

---

## 1. End-to-End Execution Trace of DEK Operations

### 1.1 DEK Generation & Wrapping (`/transit/datakey/plaintext/:name`)
**Source Trace**: `internal/builtin/logical/transit/path_datakey.go:82-165`

```
1. Parse Request Parameters:
   - name: Subject Key identifier.
   - bits: Key length (128, 256, 512).
   - context: Optional base64-encoded KDF derivation context.
2. Retrieve Subject Key:
   - Call b.GetPolicy(ctx, PolicyRequest{Name: name}).
   - Lock policy (p.l.RLock()).
3. Generate Ephemeral DEK:
   - Allocate newKey := make([]byte, 32).
   - Read cryptographically secure entropy: rand.Read(newKey).
4. Wrap (Encrypt) DEK:
   - Base64-encode plaintext DEK: base64.StdEncoding.EncodeToString(newKey).
   - Call p.EncryptWithFactory(ver, context, nil, base64Plaintext, factories...).
   - Call p.safeGetKeyEntry(ver) -> strconv.Itoa(ver) map lookup.
   - If derived, run HKDF-SHA256 to derive encryption subkey.
   - Instantiate cipher.NewGCM / chacha20poly1305.New.
   - Generate 96-bit random nonce (rand.Read).
   - Compute AEAD ciphertext: gcm.Seal(nil, nonce, plaintext, aad).
   - Format version template string: "vault:v1:" + base64.Encode(nonce + ciphertext).
5. Format Response:
   - Return plaintext DEK (base64 encoded) + wrapped ciphertext DEK.
6. Release Policy Lock (p.Unlock()).
```

### 1.2 DEK Unwrapping (`/transit/decrypt/:name`)
**Source Trace**: `internal/builtin/logical/transit/path_decrypt.go:120-220`

```
1. Parse Input:
   - Single item or batch_input slice.
   - Decode ciphertext string: verify "vault:v1:" prefix.
2. Retrieve Subject Key:
   - Call b.GetPolicy(ctx, PolicyRequest{Name: name}).
   - Lock policy (p.l.RLock()).
3. Parse Ciphertext Envelope:
   - String split on delimiter: extract version string ("1") and base64 body.
   - Parse integer version: strconv.Atoi(splitVer[0]).
   - Base64 decode raw ciphertext: base64.StdEncoding.DecodeString(splitVer[1]).
4. Decrypt DEK:
   - Extract nonce (first 12 bytes) and ciphertext/tag.
   - Call p.safeGetKeyEntry(ver).
   - Instantiate cipher.NewGCM with key bytes.
   - Decrypt: gcm.Open(nil, nonce, ciphertextWithTag, aad).
5. Base64 Encode Output Plaintext:
   - Convert decrypted DEK bytes to base64 string.
6. Format HTTP Response:
   - Populate response dictionary.
7. Release Policy Lock (p.Unlock()).
```

---

## 2. Inefficiencies in the Current Execution Path

### 2.1 The Redundant Base64 Encoding Overhead
Throughout both wrapping and unwrapping, binary cryptographic keys undergo repeated base64 encoding and decoding:
- **Wrap Path**:
  $\text{Raw 32-byte DEK} \xrightarrow{\text{base64.Encode}} \text{String} \xrightarrow{\text{base64.Decode}} \text{Bytes} \xrightarrow{\text{AES-GCM}} \text{Ciphertext} \xrightarrow{\text{base64.Encode}} \text{Vault String}$
- **Unwrap Path**:
  $\text{Vault String} \xrightarrow{\text{base64.Decode}} \text{Ciphertext} \xrightarrow{\text{AES-GCM}} \text{Plaintext Bytes} \xrightarrow{\text{base64.Encode}} \text{Response String}$
- **Performance Tax**: Base64 encoding expands payload size by 33% and triggers multiple heap allocations for string buffers. For a 32-byte DEK, OpenBao allocates at least 4 temporary strings and byte slices per operation.

### 2.2 Serialized Execution of Batch Requests
In `path_encrypt.go:400-440` and `path_decrypt.go:170-205`:
```go
successesInBatch := false
for i, item := range batchInputItems {
    // Process items ONE BY ONE sequentially
    plaintext, err := p.DecryptWithFactory(...)
    batchResponseItems[i].Plaintext = plaintext
}
```
- OpenBao processes batch items **serially on a single goroutine**.
- If a client submits a batch of 500 DEK unwrap requests, the entire batch runs on a single CPU core, completely ignoring available multi-core parallel processing capacity.
- Batch processing latency scales strictly linearly: $T_{\text{total}} = N \times T_{\text{item}}$.

### 2.3 Repeated Cipher and Key Derivation Initialization
In `Policy.SymmetricDecryptRaw` and `Policy.SymmetricEncryptRaw` (`sdk/helper/keysutil/policy.go:1140-1175`):
- For every batch item, OpenBao calls `aes.NewCipher(encKey)` followed by `cipher.NewGCM(cipher)`.
- Creating `cipher.Block` expands the AES key schedule (10 rounds for AES-128, 14 rounds for AES-256) into a temporary structure on the heap.
- In a batch of 500 items using the same key version, the AES key schedule is computed and discarded **500 times redundantly**.

---

## 3. DEK Operation Audit Findings

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-DEK-01
Component:                internal/builtin/logical/transit
File Path:                internal/builtin/logical/transit/path_decrypt.go
Function or Structure:    pathDecryptWrite / batch loop
Line Reference:           internal/builtin/logical/transit/path_decrypt.go:167-210
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       batchInputItems are processed in a single-threaded sequential for-loop.
Identified Inefficiency:  Batch unwrap requests execute sequentially on one CPU core, failing to exploit multi-core parallelism. Large batches experience high latency and degrade P99 response times.
Proposed Improvement:     Parallelize batch DEK wrapping and unwrapping using a worker pool (e.g., errgroup / bounded worker goroutines) when batch size > threshold (e.g., N >= 8).
Expected Latency Impact:  Reduces batch processing latency by 70% - 85% on multi-core systems (e.g., 16+ vCPUs).
Expected Throughput:      4x - 6x increase in batch DEK unwrapping throughput.
Memory Impact:            Negligible (temporary synchronization primitives).
Storage Impact:           None.
Security Implications:    Each worker operates with independent nonces; zero cryptographic compromise.
Tenant Isolation:         None.
Key Rotation:             None.
Implementation Complexity: Low.
Confidence Level:         HIGH.
Validation Strategy:      Benchmark batch unwrap of 100 items with serial loop vs 8-worker errgroup.
--------------------------------------------------------------------------------
```

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-DEK-02
Component:                sdk/helper/keysutil
File Path:                sdk/helper/keysutil/policy.go
Function or Structure:    Policy.SymmetricDecryptRaw / Policy.SymmetricEncryptRaw
Line Reference:           sdk/helper/keysutil/policy.go:1140-1175, 1220-1250
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       Every encryption/decryption call instantiates a new aes.NewCipher and cipher.NewGCM instance from raw key bytes.
Identified Inefficiency:  Redundant AES key schedule computation (14 rounds of AES key expansion) on every operation. Discards cipher.AEAD immediately after single block execution.
Proposed Improvement:     Cache the initialized cipher.AEAD instance inside KeyEntry or an atomic field within Policy. During batch operations, reuse the active cipher.AEAD instance across all batch items sharing the same key version.
Expected Latency Impact:  Reduces CPU cycle cost per DEK operation by 20% - 30%.
Expected Throughput:      25% - 40% higher ops/sec on single-core microbenchmarks.
Memory Impact:            Saves thousands of temporary cipher allocations per second.
Storage Impact:           None.
Security Implications:    AES-GCM instances are safe for concurrent Seal/Open calls provided nonces are unique (guaranteed by crypto/rand).
Tenant Isolation:         None.
Key Rotation:             On key rotation, the old version AEAD remains immutable while the new version AEAD is initialized.
Implementation Complexity: Medium.
Confidence Level:         HIGH.
Validation Strategy:      Micro-benchmark AES-GCM Seal/Open with new AEAD per call vs cached AEAD.
--------------------------------------------------------------------------------
```

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-DEK-03
Component:                internal/builtin/logical/transit
File Path:                internal/builtin/logical/transit/path_datakey.go
Function or Structure:    pathDatakeyWrite
Line Reference:           internal/builtin/logical/transit/path_datakey.go:142-155
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       pathDatakeyWrite base64-encodes the newly generated random DEK, then passes the base64 string to p.EncryptWithFactory, which immediately base64-decodes it back to raw bytes.
Identified Inefficiency:  Useless double-conversion: []byte -> base64 string -> []byte -> ciphertext.
Proposed Improvement:     Directly invoke internal raw encryption function p.EncryptRaw(ver, context, nil, newKey) without intermediate base64 string round-trips.
Expected Latency Impact:  5% - 8% latency reduction per datakey call.
Expected Throughput:      10% reduction in memory allocations per datakey operation.
Memory Impact:            Eliminates 2 temporary heap allocations per request.
Storage Impact:           None.
Security Implications:    None.
Tenant Isolation:         None.
Key Rotation:             None.
Implementation Complexity: Low.
Confidence Level:         HIGH.
Validation Strategy:      Compare allocs/op and ns/op of pathDatakeyWrite before and after refactoring.
--------------------------------------------------------------------------------
```

---

## 4. Architectural Target for DEK Processing

For Cloakit, the target DEK pipeline achieves:
1. **Zero-Allocation Execution**: Using `sync.Pool` for 32-byte key buffers and 12-byte nonces.
2. **Cached AEAD Primitives**: Maintaining persistent `cipher.AEAD` instances per key version.
3. **Parallel Batch Unwrapping**: Spreading batch inputs across CPU cores to achieve sub-millisecond batch latencies.
