# Implementation Recommendations & Minimum Secure Work Analysis 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document synthesizes the recommendations for Prompt 1 and answers the mandated Final Question regarding the absolute minimum secure amount of work OpenBao must perform for Cloakit cryptographic operations.

---

## 1. Concrete Implementation Roadmap

1. **Step 1: Cache Precomputed `cipher.AEAD` Instances**
   - Update `sdk/helper/keysutil/policy.go` to construct `cipher.AEAD` during policy deserialization and key creation/rotation.
   - Guard with zeroization on eviction.

2. **Step 2: Default to `TransitSyncMap` for Cloakit Workloads**
   - Configure transit engine cache size to 0 or introduce a read-optimized sharded cache (`sync.Map` per namespace) to bypass `TwoQueueCache` lock contention.

3. **Step 3: Introduce High-Efficiency Protobuf / Binary API**
   - Provide an optional gRPC or raw binary transport for DEK wrapping/unwrapping that accepts binary IV, AAD, and ciphertext, bypassing base64 roundtrips.

4. **Step 4: Asynchronous Batched Audit Logging**
   - Ensure audit brokers process transit response records asynchronously through a ring buffer to avoid blocking the cryptographic return path.

---

## 2. Final Question Answer

### What is the minimum secure amount of work OpenBao must perform for each Subject Key lookup, DEK wrap and DEK unwrap?

To maintain 100% of OpenBao's security, authorization, tenant isolation, and auditability guarantees, the **minimum secure work** consists of:

### 1. Subject Key Lookup:
1. **1 Authentication Check:** Validate caller token against cached TokenStore entry (HMAC salt lookup in memory).
2. **1 Authorization Check:** Evaluate token policies against key path (`transit/keys/{name}`) for capability `read`.
3. **1 Lock-Free Cache Lookup:** Retrieve `Policy` pointer from thread-safe memory map (`sync.Map`).
4. **1 Sanitization & Serialization:** Filter out private key entries and serialize public metadata to response.
- **Minimum Operations:** 0 storage I/O, 0 barrier decryptions, 0 DB round trips, 0 mutex locks.

### 2. DEK Wrap (`encrypt`):
1. **1 Auth & ACL Check:** Verify caller token has `update` permission for `transit/encrypt/{name}`.
2. **1 Lock-Free Cache Lookup:** Retrieve precomputed key entry and pre-instantiated `cipher.AEAD`.
3. **1 CSPRNG Read:** Generate 12 cryptographically secure random bytes for the 96-bit AES-GCM nonce.
4. **1 Hardware AEAD Seal:** Execute AES-256-GCM hardware instruction (`gcmAesData` / `AES-NI`) over DEK plaintext and AAD context.
5. **1 Audit Event Emission:** Queue an asynchronous audit record (redacting plaintext DEK).
- **Minimum Operations:** 0 storage writes, 0 cipher re-instantiations, 0 base64 conversions (if binary/gRPC), 1 hardware GCM seal.

### 3. DEK Unwrap (`decrypt`):
1. **1 Auth & ACL Check:** Verify caller token has `update` permission for `transit/decrypt/{name}`.
2. **1 Lock-Free Cache Lookup:** Retrieve precomputed key entry for specified version.
3. **1 Key Version & Revocation Check:** Verify `MinDecryptionVersion <= ver <= LatestVersion` and not soft-deleted.
4. **1 Hardware AEAD Open:** Execute AES-256-GCM hardware instruction verifying 16-byte GMAC tag and decrypting 32-byte DEK.
5. **1 Audit Event Emission:** Queue an asynchronous audit record.
- **Minimum Operations:** 0 storage reads, 0 DB transactions, 0 barrier calls, 0 cipher allocations, 1 hardware GCM open.
