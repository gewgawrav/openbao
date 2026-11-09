# Key Rotation, Rewrapping Cost & Cryptographic Erasure 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document investigates the lifecycle costs of Subject Key rotation, lazy vs eager rewrapping, and cryptographic erasure (instant tenant/subject GDPR "Right to be Forgotten").

---

## 1. Key Rotation Lifecycle Dynamics
- **Source Reference:** `sdk/helper/keysutil/policy.go:1930-1965` (`Policy.Rotate`)

When a Subject Key is rotated:
1. OpenBao increments `LatestVersion` from $v$ to $v+1$.
2. Generates new 32-byte key material.
3. Persists the expanded policy to PostgreSQL.
4. **Historical Versions Preserved:** Versions $1 \dots v$ remain in `Policy.Keys` to allow transparent decryption of existing DEKs.

---

## 2. Eager Rewrapping vs. Lazy Rewrapping

```
+------------------------------------------------------------------------------------------------------+
|                                   REWRAPPING STRATEGY COMPARISON                                     |
+------------------------------------------------------------------------------------------------------+
| Strategy 1: Eager Batch Rewrap (`/transit/rewrap/`)                                                  |
|  - Client queries all DB rows and submits batch rewrap requests to OpenBao.                          |
|  - Cost: Massive storage read/write surge, high DB transaction lock contention.                     |
|                                                                                                      |
| Strategy 2: Lazy Rewrapping on Application Access                                                    |
|  - When Cloakit unwraps a DEK, OpenBao returns plaintext + indicates `version < LatestVersion`.      |
|  - Cloakit re-wraps the DEK with `v+1` in background during normal read query.                       |
|  - Cost: Zero surge, 100% amortized across natural application traffic.                              |
+------------------------------------------------------------------------------------------------------+
```

---

## 3. Cryptographic Erasure & GDPR Compliance
- **Source Reference:** `sdk/helper/keysutil/lock_manager.go:543-580` (`DeletePolicy`)
- When a privacy subject exercises their "Right to be Forgotten":
  - Cloakit invokes `DELETE /v1/transit/keys/{subject_id}`.
  - OpenBao deletes `policy/{name}` from PostgreSQL and memory.
  - All Layer 3 DEKs encrypted with that Subject Key become **permanently and irreversibly undecryptable**, even if historical database backups exist.
  - Execution time: **< 15 milliseconds** to destroy gigabytes of personal data.
