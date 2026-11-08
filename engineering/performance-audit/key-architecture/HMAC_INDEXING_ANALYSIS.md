# 3. HMAC & KEYED IDENTIFIER ARCHITECTURE 🤖

## Executive Summary

Keyed cryptographic identifiers (HMACs) provide a deterministic bridge between external subject identities (e.g., user IDs, account numbers, biometric UUIDs) and internal OpenBao Subject Keys. By using an HMAC, Cloakit can generate the storage key for a Subject Key deterministically in memory, avoiding the operational bottleneck of querying an external database mapping table on every DEK wrap or unwrap request.

However, HMAC identifiers are **pseudonymous, not encrypted**. They inherently leak equality and access patterns. This report delivers an exhaustive cryptographic analysis of HMAC-based deterministic indexing, evaluates collision risks, specifies domain separation standards, assesses tenant scoping, models key rotation strategies, and compares three core identifier architectures.

---

## 1. Cryptographic Analysis of Deterministic HMAC Identifiers

### Mathematical Construction
Let $\mathcal{T}$ denote the tenant identifier, $\mathcal{S}$ denote the subject identifier, and $K_{\text{index}}$ denote a 256-bit secret cryptographic indexing key. A naive deterministic identifier is formulated as:
$$\text{ID} = \text{HMAC-SHA-256}(K_{\text{index}}, \mathcal{T} \parallel \mathcal{S})$$

### Cryptographic Properties:
1. **Collision Resistance**:
   - SHA-256 provides a 256-bit output space. Under the Birthday Bound, the probability of a hash collision among $N$ subjects is approximately:
     $$P(\text{collision}) \approx \frac{N^2}{2^{257}}$$
   - For $N = 10,000,000$ ($10^7$) subjects:
     $$P \approx \frac{10^{14}}{2 \times 10^{77}} \approx 5 \times 10^{-64}$$
   - This risk is cryptographically infinitesimal, completely ruling out accidental collisions.
2. **Preimage Resistance & Pseudorandomness**:
   - HMAC-SHA-256 behaves as a Pseudo-Random Function (PRF). Given $\text{ID}$, an adversary without $K_{\text{index}}$ cannot determine $\mathcal{S}$ with advantage greater than guessing at random.
3. **Canonicalisation Vulnerability**:
   - If concatenation is un-delimited ($\mathcal{T} \parallel \mathcal{S}$), ambiguous inputs yield identical identifiers (e.g., $\mathcal{T}=\text{"abc"}, \mathcal{S}=\text{"def"}$ vs $\mathcal{T}=\text{"ab"}, \mathcal{S}=\text{"cdef"}$).
   - **Remedy**: Mandatory length-prefixed encoding or unambiguous delimiter formatting:
     $$\text{Payload} = \text{Len}(\mathcal{T}) \parallel \mathcal{T} \parallel \text{Len}(\mathcal{S}) \parallel \mathcal{S}$$

---

## 2. Threat Modeling: Information Leakage & Attack Surfaces

While HMAC identifiers prevent plaintext exposure of subject identifiers, they exhibit distinct security characteristics that must be accounted for:

### 2.1 Equality Leakage (Frequency Analysis)
- **Mechanism**: Because HMAC is deterministic, $\text{HMAC}(K, \mathcal{S})$ always evaluates to the exact same hex string for a given subject.
- **Risk**: An attacker who compromises read access to OpenBao's storage backend (e.g., Raft logs or storage dumps) cannot invert the HMAC to see the user ID, but CAN observe access frequency:
  - If Subject Key `a4f1...` is queried 50,000 times a day while `9b2c...` is queried twice, the attacker infers that `a4f1...` represents an active "power user" or high-value account.
- **Mitigation**: Traffic shaping or salt versioning across major epochs. However, for a high-performance vault, equality leakage is generally an acceptable trade-off provided the indexing key is strictly secured.

### 2.2 Enumeration & Dictionary Attacks
- **Mechanism**: If an attacker steals $K_{\text{index}}$ or has access to an unauthenticated HMAC oracle, and the subject space is small (e.g., sequential 8-digit account numbers or SSNs), the attacker can precompute HMACs for all possible subjects in seconds.
- **Mitigation**:
  1. Use high-entropy UUIDs for $\mathcal{S}$ rather than raw PII.
  2. Implement strict domain separation and keep $K_{\text{index}}$ stored inside OpenBao's encrypted barrier.

### 2.3 Replay and Re-identification across Tenants
- **Mechanism**: If a single global $K_{\text{index}}$ is shared across all tenants, a subject present in Tenant A and Tenant B could produce correlated signatures if tenant prefixing is omitted.
- **Mitigation**: Strict **Tenant-Scoped Derivation**.

---

## 3. Cryptographic Specification: Tenant-Scoped Domain-Separated HMAC

To eliminate cross-tenant correlation and ensure absolute cryptographic separation, the indexing key MUST be derived hierarchically using HKDF (RFC 5869):

```
                   Master Vault Indexing Root (K_root)
                                  │
                                  ▼  HKDF-Expand(K_root, "cloak:tenant-index:" || TenantID)
                    Tenant-Specific Indexing Key (K_tenant)
                                  │
                                  ▼  HMAC-SHA-256(K_tenant, DomainSep || SubjectID)
                     Deterministic Subject Key Identifier
```

### Formal Specification:
1. **Tenant Key Derivation**:
   $$K_{\text{tenant}} = \text{HKDF-Expand}(K_{\text{root}}, \text{"cloak:tenant-key-derivation:"} \parallel \text{TenantID}, 32)$$
2. **Subject Identifier Construction**:
   $$\text{SubjectKeyName} = \text{HexEncode}(\text{HMAC-SHA-256}(K_{\text{tenant}}, \text{"cloak:v1:subject:"} \parallel \text{SubjectID}))$$
3. **Storage Path in OpenBao**:
   $$\text{Path} = \text{"policy/tenant-" } \parallel \text{TenantID} \parallel \text{"/sub-" } \parallel \text{SubjectKeyName}$$

### Architectural Advantages:
- **Tenant Isolation**: Compromise of Tenant A's derived key $K_{\text{tenantA}}$ reveals zero information regarding Tenant B's subjects.
- **Independent Rotation**: Tenant A can rotate its indexing key without triggering re-indexing for Tenant B.
- **Cryptographic Separation**: Indexing keys are strictly separated from encryption keys (Client KEK and Subject Keys).

---

## 4. Key Rotation & Migration Strategies for HMAC Identifiers

When an indexing key is rotated (e.g., following a security incident or regular cryptographic hygiene), deterministic identifiers change. Managing this transition without downtime is critical.

### The Dual-Epoch Lookaside Migration Algorithm

```
                 Incoming Request (TenantID, SubjectID)
                                  │
                                  ▼
           Derive Active Identifier (Epoch N): ID_new
                                  │
                                  ▼
                    Check Cache / Storage for ID_new
                                  │
                   ┌──────────────┴──────────────┐
                   ▼                             ▼
              [ FOUND ]                    [ NOT FOUND ]
                   │                             │
                   │                             ▼
                   │              Derive Previous Identifier (Epoch N-1): ID_old
                   │                             │
                   │                             ▼
                   │               Check Storage for ID_old
                   │                             │
                   │              ┌──────────────┴──────────────┐
                   │              ▼                             ▼
                   │         [ FOUND ]                    [ NOT FOUND ]
                   │              │                             │
                   │              ▼                             ▼
                   │     1. Read Subject Key            Key Does Not Exist
                   │     2. Persist copy under ID_new   (Trigger Subject Key
                   │     3. Delete or tombstone ID_old   Onboarding)
                   │     4. Update cache with ID_new
                   │              │
                   ▼              ▼
               Execute DEK Operation (Wrap/Unwrap)
```

- **Dual-Epoch Window**: OpenBao supports querying the current epoch key version $N$ and falling back to epoch $N-1$.
- **Lazy Migration**: Upon first access during epoch $N$, the Subject Key is re-keyed to the new storage path `policy/{ID_new}` and the old entry is pruned.
- **Zero Downtime**: High-frequency keys are migrated in real-time with zero disruption to ongoing DEK processing.

---

## 5. Architectural Comparison: Identifier Paradigms

We evaluate three comprehensive designs for Cloakit:

| Design Dimension | Design A: Pure Random UUID | Design B: Deterministic HMAC (Tenant-Scoped) | Design C: Hybrid Random ID + HMAC Index |
| :--- | :--- | :--- | :--- |
| **Lookup Latency** | High (Requires external DB lookup: $2\text{--}5\text{ ms}$) | **Ultra-Low ($< 10\ \mu\text{s}$ CPU derivation)** | **Ultra-Low ($< 50\ \mu\text{s}$ memory index)** |
| **Storage Overhead** | Baseline (1 record in OpenBao + 1 row in PG) | **Lowest (Zero external DB records)** | Moderate (+1 index entry in OpenBao) |
| **External DB Load** | High ($O(1)$ query per wrap/unwrap) | **Zero (Stateless derivation)** | **Zero (Stateless derivation)** |
| **Privacy / Secrecy** | High (Pure random tokens) | Medium (Pseudonymous; leaks equality) | Medium (Pseudonymous index pointer) |
| **Enumeration Risk** | Zero | Prevented by 256-bit PRF key | Prevented by 256-bit PRF key |
| **Key Rotation Overhead**| Heavy (Database table row updates) | Dual-epoch lazy migration | Index-only re-pointing |
| **Operational Complexity**| High (Coordinating OpenBao & PG) | **Low (Self-contained in OpenBao/Cloakit)**| Medium |
| **Suitability for Cloakit**| Suboptimal for $> 10,000\text{ req/sec}$ | **Recommended for Maximum Performance** | Recommended if Equality Leakage Prohibited |

---

## 6. Audit Findings on HMAC Processing

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-HMAC-01
Component:                internal/builtin/logical/transit
File Path:                internal/builtin/logical/transit/path_hmac.go
Function or Structure:    pathHMACWrite / Policy.HMACKey
Line Reference:           internal/builtin/logical/transit/path_hmac.go:120-165, sdk/helper/keysutil/policy.go:1280-1300
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       OpenBao provides an HMAC endpoint (/transit/hmac/:name) that retrieves the named policy, reads KeyEntry.HMACKey, and computes the HMAC of input data.
Identified Inefficiency:  Using OpenBao's /transit/hmac endpoint over HTTP to derive deterministic Subject Key identifiers introduces an extra network round-trip and barrier lock acquisition per lookup.
Proposed Improvement:     Cloakit client library or Vault plugin should derive tenant-scoped HMAC identifiers locally in-process using standard crypto/hmac with an ephemeral or cached tenant indexing key, or use an OpenBao native in-memory index table that performs the PRF without HTTP overhead.
Expected Latency Impact:  Eliminates 1.5ms - 3ms HTTP round-trip per Subject Key lookup.
Expected Throughput:      Unlocks 10x higher lookup throughput by performing PRF hashing client-side or in-core.
Memory Impact:            Negligible.
Storage Impact:           Zero.
Security Implications:    Tenant indexing key material must be protected via mTLS / secure memory enclaves if evaluated client-side.
Tenant Isolation:         Absolute; mathematically bounded by HKDF tenant derivation.
Key Rotation:             Standard dual-epoch rotation.
Implementation Complexity: Low.
Confidence Level:         HIGH.
Validation Strategy:      Compare local in-process HMAC-SHA-256 latency (sub-microsecond) vs HTTP /transit/hmac call (multi-millisecond).
--------------------------------------------------------------------------------
```

---

## 7. Recommendation

For Cloakit's privacy vault:
1. Adopt **Design B (Tenant-Scoped Deterministic HMAC)**.
2. Formulate identifiers as:
   $$\text{SubjectKeyName} = \text{Hex}(\text{HMAC-SHA-256}(K_{\text{tenant}}, \text{"cloak-subject:"} \parallel \text{subject\_id}))$$
3. Hold $K_{\text{root}}$ inside OpenBao's secure barrier, deriving $K_{\text{tenant}}$ dynamically.
4. Never reuse Subject Key cryptographic material for indexing.
