# Invalidation, Revocation & Critical Edge Case Audit 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document investigates critical operational edge cases where caching could compromise security or cause consistency bugs in the Cloakit Privacy Vault.

---

## 1. Critical Failure Scenarios

### 1.1 Scenario 1: Subject Key Rotated While Cached
- **Behavior:** OpenBao's transit ciphertext formats embed the key version (`vault:v{ver}:{base64}`).
- **Impact:** Decryption requests specify version `ver`. Even if the active policy is rotated to `v+1`, decryption of existing DEKs using `v` remains valid unless `MinDecryptionVersion` is raised.
- **Risk:** Encryption of new DEKs on a stale instance will continue using `v` instead of `v+1`.

### 1.2 Scenario 2: Subject Key Revoked While Cached
- **Behavior:** Caller sets `min_decryption_version` or deletes the key.
- **Impact:** A stale read node will continue decrypting DEKs using the revoked key version until cache eviction.
- **Security Violation:** **Breach of Cryptographic Revocation Guarantees.** Revoked keys must immediately reject decryption attempts.

### 1.3 Scenario 3: Tenant Deletion & Cross-Tenant Leakage
- **Mechanism:** In OpenBao, transit key paths are scoped to namespaces: `sys/namespaces/{id}/transit/keys/{name}`.
- **Cache Key Construction:** `sdk/helper/keysutil/` is mounted per backend instance; cache keys are relative to the mount point (`policy.Name`).
- **Guarantee:** Because each namespace/mount instantiates an independent `backend` and `LockManager`, cross-tenant key leakage in memory is **impossible by design**.

### 1.4 Scenario 4: PostgreSQL Replication Lag
- **Mechanism:** Standby reader queries a PostgreSQL streaming read replica.
- **Failure:** If cache miss occurs on Node B while replication lag is 200 ms, Node B reads stale or non-existent data from PostgreSQL, potentially caching a "not found" state or older key version.
