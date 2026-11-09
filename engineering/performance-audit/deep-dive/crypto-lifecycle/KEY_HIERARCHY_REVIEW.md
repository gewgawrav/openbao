# Key Hierarchy, Cryptographic Separation & Architecture Review 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document reviews Cloakit Privacy Vault's 3-layer cryptographic key hierarchy and its mapping to OpenBao's Transit engine and PostgreSQL backend.

In Cloakit's operational model:
- **Layer 1 (Master KEK):** Resides in Cloud KMS (AWS KMS, GCP Cloud KMS) or Hardware Security Module (HSM). Protects OpenBao's root barrier keyring.
- **Layer 2 (Subject Keys):** Managed inside OpenBao as Transit keys (`internal/builtin/logical/transit/`). Each privacy subject (user, organization, or data entity) is assigned a distinct Subject Key.
- **Layer 3 (DEKs - Data Encryption Keys):** Ephemeral 256-bit symmetric keys generated to encrypt individual columns/rows in customer databases. OpenBao wraps and unwraps Layer 3 DEKs using Layer 2 Subject Keys.

---

## 1. Key Hierarchy Structural Mapping

```
+------------------------------------------------------------------------------------------------------+
|                                CLOAKIT 3-LAYER KEY HIERARCHY ARCHITECTURE                            |
+------------------------------------------------------------------------------------------------------+
| Layer 1: Cloud KMS / HSM Root KEK                                                                    |
|  - Manages OpenBao Root Barrier Keyring                                                              |
|  - Encrypts OpenBao Master Barrier Key                                                              |
|                                                  |                                                    |
|                                                  v                                                    |
| Layer 2: OpenBao Transit Engine (Subject Keys)                                                        |
|  - Keys: AES-256-GCM / HKDF-SHA256 (`keysutil.Policy`)                                               |
|  - Cardinality: 10,000 to 10,000,000 Subject Keys                                                    |
|  - Storage: Sealed with Barrier Key, persisted in PostgreSQL (`vault_kv_store`)                      |
|  - Caching: In-memory `TransitSyncMap` on OpenBao nodes                                              |
|                                                  |                                                    |
|                                                  v (Wrap / Unwrap via Transit API)                    |
| Layer 3: Application Data Encryption Keys (DEKs)                                                     |
|  - Symmetric 256-bit AES keys stored in customer PostgreSQL database (encrypted)                     |
|  - Decrypted DEK kept in application process memory only for duration of query execution              |
+------------------------------------------------------------------------------------------------------+
```

---

## 2. Security Invariants Preserved
1. **Cryptographic Separation:** Compromising a single Subject Key exposes only that specific subject's data; other subjects remain completely unreadable.
2. **Key Derivation Isolation:** When using derived keys (`derived=true`), derivation contexts (e.g. tenant UUID + column ID) ensure identical plaintexts encrypt to distinct ciphertexts even across the same subject.
3. **Hardware Root of Trust:** No OpenBao operator or PostgreSQL administrator can decrypt Subject Keys without authorization from the Layer 1 Cloud KMS.
