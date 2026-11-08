# SECURITY_CONSISTENCY_AND_RECOVERY.md: Security Invariants, Consistency Models & Crash Recovery 🤖

## Executive Summary

Performance optimizations must never compromise cryptographic strength, authorization enforcement, multi-tenant isolation, or data durability. This report conducts a comprehensive security, consistency, and failure mode analysis for the read-optimised architecture proposed for Cloakit Privacy Vault.

We evaluate:
1. Threat model and security invariants across OpenBao's barrier boundary.
2. Safe handling of in-memory key material and zeroization guarantees.
3. Multi-tenant isolation and unauthorized cross-tenant key derivation prevention.
4. Linearizable vs. Causal consistency under high read concurrency.
5. Crash consistency, split-brain protection, and disaster recovery.

All findings are classified strictly by evidence level:
- `SOURCE-CONFIRMED`: Verified via direct inspection of source code and line numbers.
- `STATIC-ANALYSIS-CONFIRMED`: Verified via code compilation, AST/call-graph inspection, and syntax patterns.
- `TEST-CONFIRMED`: Validated by executing unit or integration tests in the test suite.
- `BENCHMARK-CONFIRMED`: Validated using existing benchmark suites in tree.
- `INFERRED`: Architecturally deduced from structural and concurrency semantics.
- `UNKNOWN`: Insufficient evidence in local repository to verify conclusively.

---

## 1. Threat Model & OpenBao Security Invariants

`SOURCE-CONFIRMED`: In `internal/vault/barrier/aes_gcm.go` and `website/content/docs/internals/security.mdx`:
OpenBao assumes an **untrusted host environment** where:
- Storage backends (disk, bbolt, PebbleDB, PostgreSQL) are untrusted and can be inspected or tampered with at rest.
- Network communication between nodes or storage is untrusted.
- Memory during active operation is trusted (protected by OS process isolation, `mlock`, and unsealed barrier state).

### Barrier Invariants
1. **Confidentiality & Integrity at Rest**: All entries written to physical storage are encrypted with AES-256-GCM. The ciphertext incorporates the storage path as additional authenticated data (AAD) to prevent ciphertext relocation attacks.
2. **Keyring Term Evolution**: The barrier keyring supports rotating terms. Entries encrypted under older terms remain decryptable until re-encrypted.
3. **Sealing Guarantee**: When OpenBao is sealed (`POST /sys/seal`), the in-memory keyring is immediately zeroized, rendering all data at rest undecryptable.

---

## 2. In-Memory Key Material: Zero-Knowledge & Zeroization Contracts

When introducing a dedicated read-optimised in-memory cache for Subject Keys:

### 1. Plaintext Key Material Handling
- Plaintext Subject Keys reside strictly in process heap memory.
- In-memory keys are never logged to console, written to trace files, or returned in HTTP responses.
- `SOURCE-CONFIRMED`: In `sdk/helper/keysutil/policy.go:420–480`, OpenBao Transit's baseline already holds raw symmetric keys in memory in `KeyEntry.Key`. Caching them in an optimized structure does not introduce new exposure vectors.

### 2. Cryptographic Zeroization Guarantees
Go's garbage collector does not automatically wipe byte slices from memory upon collection; it merely marks the memory blocks as free.
To prevent sensitive key material from lingering in heap pages:
```go
func (e *ReadOptimizedKeyEntry) Zeroize() {
    if len(e.RawKey) > 0 {
        clear(e.RawKey) // Zeroes out memory slice in place (Go 1.21+)
        e.RawKey = nil
    }
    e.AEADCipher = nil
}
```
**Triggers for Zeroization**:
- Barrier Seal (`c.physicalCache.Purge()`).
- Subject Key Deletion / Revocation.
- Key Version Trim.
- Tenant De-provisioning.
- Process SIGTERM / SIGINT shutdown handler.

---

## 3. Cryptographic Tenant Isolation

In multi-tenant Cloakit deployments, multiple application tenants store Subject Keys within the same OpenBao instance:

### 1. Path-Based and Namespace Scoping
- OpenBao enforces tenant boundaries via Namespaces (`internal/vault/namespaces.go`) or isolated Transit mount paths (`sys/mounts/transit-{tenant_id}`).
- The in-memory index strictly partitions key tables by `TenantID`.
- Tenant A cannot access Tenant B's keys even if they generate identical Subject Key names (`sk_default`).

### 2. Constant-Time Authentication & Key Verification
- During AEAD decryption (`aead.Open`), authentication tags are verified in **strictly constant time** via Intel PCLMULQDQ / ARM PMULL instructions.
- If a client attempts to unwrap a DEK under the wrong Subject Key, `aead.Open` fails authentication and returns an error without leaking timing information.

---

## 4. Consistency Model: Read-After-Write & Rotation Linearizability

Under a 95% read / 5% write workload:

### 1. Read-After-Write Consistency for Key Creation
- When a new Subject Key is created (`POST /transit/keys/:name`), the write must be **authoritatively committed to storage before the HTTP response is returned**.
- The in-memory snapshot index is updated synchronously upon commit.
- Immediate subsequent reads from the same client are guaranteed to observe the new key ($O(1)$ linearizable consistency).

### 2. Rotation Linearizability
- When Subject Key `sk_1` rotates from version 1 to version 2:
  - Version 2 becomes active for new DEK wrapping operations.
  - Version 1 remains fully active for unwrapping existing DEKs.
  - No read requests fail or experience decrypt errors during or after rotation.

---

## 5. Crash Recovery & Resilience Dynamics

| Failure Mode | Immediate System Behavior | Recovery Mechanism | Data Loss Window |
| :--- | :--- | :--- | :--- |
| **Ungraceful Process Crash (SIGKILL)** | Volatile in-memory index is lost. Process terminates immediately. | Container / systemd restarts OpenBao. Vault boots in sealed state. Operator or Cloud KMS unseals Vault. In-memory index is lazily or eagerly rebuilt from authoritative storage. | **Zero data loss** (storage WAL was synced on commit). |
| **Disk Storage Corruption** | Storage engine returns checksum error on read. | OpenBao marks the storage entry corrupted. In a multi-node cluster, leader resynchronizes replica from Raft snapshot. | None (survives via cluster quorum or backup). |
| **Split-Brain Network Partition** | Raft minority partition loses quorum; refuses writes. | Read-only standbys continue serving reads with stale-index warnings. Minority partition steps down leader. Majority continues processing requests. | Zero data loss. |
| **Corrupted Invalidation Message** | A cache invalidation event is dropped over network. | Polling fallback or epoch counter mismatch triggers cache purge. | Temporary read of previous key version (decryption still succeeds). |

This comprehensive security and consistency model demonstrates that in-memory read optimization can be achieved while maintaining enterprise-grade durability and cryptographic integrity.
