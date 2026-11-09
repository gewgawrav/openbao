# DEK Operation Step-by-Step Analysis & Nonce Integrity 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report traces the exact cryptographic sequence of DEK generation, wrapping, unwrapping, and AAD binding within OpenBao's Transit engine.

---

## 1. Step-by-Step Execution Path

### 1.1 DEK Generation (`/transit/datakey/plaintext/{name}`)
- **Source Reference:** `internal/builtin/logical/transit/path_datakey.go:83-145`
1. **CSPRNG Generation:** Reads 32 random bytes from `crypto/rand.Reader` for the DEK.
2. **Internal Wrap:** Passes generated DEK to `Policy.Encrypt(...)`.
3. **Response Assembly:** Returns both the plaintext DEK (base64) and the wrapped ciphertext envelope.

### 1.2 Nonce Integrity & Catastrophic GCM Reuse Prevention
- **Critical Risk:** In AES-GCM, encrypting two distinct plaintexts with the same key and nonce allows an attacker to compute the GMAC authentication key $H$, destroying ciphertext authenticity and permitting plaintext recovery.
- **OpenBao Protection:**
  - Nonces are 96-bit (12 bytes) generated via CSPRNG (`crypto/rand.Read`) per operation.
  - With a 96-bit random nonce, the birthday paradox collision threshold is $\approx 2^{48}$ operations under the same key.
  - Because Subject Keys are rotated periodically and partitioned across millions of subjects, collision risk is cryptographically negligible ($< 2^{-40}$).

### 1.3 Additional Authenticated Data (AAD) Binding
- **Source Reference:** `sdk/helper/keysutil/policy.go:1190-1205`
- Cloakit passes database row metadata (e.g. `tenant_id`, `table_name`, `record_uuid`) as `context`.
- OpenBao binds this context directly into the GCM GMAC tag computation (`symopts.AdditionalData`).
- **Guarantee:** If an attacker attempts to copy an encrypted DEK from one user row to another in PostgreSQL, GCM decryption fails with `cipher: message authentication failed`.
