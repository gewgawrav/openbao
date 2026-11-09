# Redundant Execution & Wasteful Processing Analysis 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This audit pinpoints specific, redundant execution steps, unnecessary transformations, and repeated lookups in OpenBao's critical path for the Cloakit Privacy Vault workload.

By analyzing the Go source code line by line, we identify that significant CPU cycles and memory allocations are spent executing work that yields no security value or could be precalculated and reused.

---

## 1. Top Redundant Operations in Critical Path

### 1.1 Repetitive `aes.NewCipher` and `cipher.NewGCM` Instantiations
- **Classification:** `SOURCE-CONFIRMED`
- **Location:** `sdk/helper/keysutil/policy.go:1160-1200`, `policy.go:1220-1250`
- **Mechanism:** On every encrypt, decrypt, or datakey request, OpenBao retrieves the raw 32-byte key from `Policy.Keys[ver].Key` and calls `aes.NewCipher(encKey)` followed by `cipher.NewGCM(cipher)`.
- **Waste Factor:** Key material for a given version is immutable. Creating a new AES cipher expands the AES round keys (14 rounds for AES-256) and allocates the GCM struct on the heap on every single cryptographic request.
- **Redundancy:** 100% redundant. The `cipher.AEAD` instance could be precalculated at key load/rotation time and cached directly on `KeyEntry`.

### 1.2 Reflection Overhead in `mapstructure.Decode`
- **Classification:** `SOURCE-CONFIRMED`
- **Location:** `internal/builtin/logical/transit/path_decrypt.go:100-115`, `path_encrypt.go:298-320`
- **Mechanism:** OpenBao's `framework.FieldData` uses `mapstructure` to parse request parameters from generic maps into transit fields:
  ```go
  d.Get("ciphertext").(string)
  d.Get("context").(string)
  ```
- **Waste Factor:** Standard Go JSON decoding already maps incoming fields. Passing them through `map[string]any` and extracting them via type assertions and reflection allocates dozens of interface objects.

### 1.3 Double Base64 Roundtrips
- **Classification:** `SOURCE-CONFIRMED`
- **Location:** `sdk/helper/keysutil/policy.go:1130-1160`, `policy.go:1270-1285`
- **Mechanism:**
  1. Client sends base64 ciphertext in JSON string.
  2. OpenBao decodes base64 into `[]byte` for decryption.
  3. Decrypted DEK (32 bytes) is converted to base64 string: `base64.StdEncoding.EncodeToString(plain)`.
  4. Response is marshaled to JSON.
  5. Cloakit client receives JSON, decodes base64 string back into binary DEK bytes.
- **Waste Factor:** In a trusted VPC environment or microservice mesh where Cloakit communicates with OpenBao, base64 encoding 32-byte DEKs adds CPU overhead, intermediate string allocations, and 33% payload expansion.

### 1.4 Redundant Prefix and Mount Route Matching
- **Classification:** `SOURCE-CONFIRMED`
- **Location:** `internal/vault/router.go:130-185`
- **Mechanism:** OpenBao routes requests by scanning its radix tree/mount table for matching prefix (`transit/`).
- **Waste Factor:** For microservices dedicated strictly to encryption/decryption (Cloakit privacy proxy), the mount point does not change. Every request executes radix search and RLock acquisition on the router.

### 1.5 LRU Mutex Contention in Read-Heavy Scenarios
- **Classification:** `SOURCE-CONFIRMED`
- **Location:** `sdk/helper/keysutil/transit_lru.go:30-55`, `github.com/hashicorp/golang-lru/v2`
- **Mechanism:** OpenBao defaults to `TransitLRU` backed by `TwoQueueCache`. `TwoQueueCache.Get` acquires an exclusive lock (`c.lock.Lock()`) to update the hit list and frequency queue.
- **Waste Factor:** In a 95% read workload, multiple concurrent requests for the same or different Subject Keys serialize on this single mutex.
- **Alternative:** `TransitSyncMap` (`sdk/helper/keysutil/transit_syncmap.go`) eliminates this lock via Go's concurrent `sync.Map`, but is only enabled when cache size is set to 0 (unbounded).

---

## 2. Waste Impact Summary

```
+------------------------------------------------------------------------------------------------------+
| Operation Phase        | Current Implementation            | Redundant Waste Factor                 |
+------------------------------------------------------------------------------------------------------+
| Key Cipher Init        | Recomputed every unwrap/wrap      | 14 AES round expansions + GCM alloc     |
| Parameter Parsing      | Generic map + mapstructure        | Reflection, heap allocations            |
| Data Encoding          | Double base64 string encode/decode| String allocs + 33% wire bloat         |
| Cache Synchronization  | TwoQueueCache exclusive mutex     | Lock contention on read hits            |
| Audit Logging          | Synchronous JSON format + HMAC    | Blocking I/O on critical response path |
+------------------------------------------------------------------------------------------------------+
```
