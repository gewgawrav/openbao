# 7. KEY VERSIONING & ROTATION PERFORMANCE 🤖

## Executive Summary

Key versioning and rotation are essential lifecycle requirements for cryptographic hygiene and compliance (e.g., PCI-DSS, HIPAA, GDPR). In Cloakit, Subject Keys rotate periodically (e.g., annually or upon security events), while DEKs wrapped under older Subject Key versions must remain decryptable.

This report evaluates OpenBao's key versioning architecture, analyzes the archive storage mechanism (`archive/:name`), investigates the performance impact of concurrent key rotation on active DEK processing, and specifies zero-downtime rotation optimizations.

---

## 1. Key Versioning Representation in OpenBao

In OpenBao, key versioning is split across two structures:
1. **Active Key Versions (`Policy.Keys`, `sdk/helper/keysutil/policy.go:470`)**:
   - A map (`keyEntryMap`) containing all active key versions between `MinDecryptionVersion` and `LatestVersion`.
   - Loaded into memory as part of the primary `Policy` struct.
2. **Archived Key Versions (`archivedKeys`, `sdk/helper/keysutil/policy.go:580-605`)**:
   - Old versions that fall below `MinDecryptionVersion` are offloaded to a separate storage entry: `archive/{name}`.
   - Purpose: Prevents the primary policy JSON blob from expanding indefinitely.

### Rotation Mechanics (`Policy.Rotate`, `sdk/helper/keysutil/policy.go:720-760`):
When a key is rotated:
1. Acquire exclusive lock on policy (`p.l.Lock()`) and shard (`lock.Lock()`).
2. Generate new key bytes: `newKey := make([]byte, keyLen)`.
3. Increment `LatestVersion`: $V_{\text{new}} = V_{\text{latest}} + 1$.
4. Insert new `KeyEntry` into `p.Keys[strconv.Itoa(V_{new})]`.
5. Check if old versions need archiving (`p.handleArchiving`).
6. Serialize and persist updated `Policy` to `policy/{name}` in barrier storage.
7. If archiving triggered, persist updated archive to `archive/{name}`.
8. Store updated policy in `lm.cache`.
9. Release locks.

---

## 2. Concurrency Impact: Rotation vs. Active DEK Processing

Under Cloakit's workload, thousands of DEK wrap/unwrap operations may occur concurrently with a key rotation:

```
Thread 1 (Active DEK Wrap):
  1. Acquire p.l.RLock()
  2. Read p.LatestVersion (e.g., v1)
  3. Execute AES-GCM Encrypt with v1
  4. Release p.l.RUnlock()

Thread 2 (Key Rotation Worker):
  1. Acquire p.l.Lock()  <─── BLOCKS all incoming DEK wraps on this key!
  2. Generate new key (v2)
  3. Barrier Storage Write (Disk I/O + AEAD encryption: 2ms - 10ms)
  4. Update p.LatestVersion = 2
  5. Release p.l.Unlock()
```

### Critical Bottleneck:
- `p.l.Lock()` is held across the **entire synchronous disk write** (`p.Persist(ctx, storage)`).
- If disk I/O stalls for 15 milliseconds, **all DEK wrapping and unwrapping operations on that Subject Key are blocked for 15ms**.
- This introduces severe tail latency spikes in P99 and P99.9 latencies.

---

## 3. The Archive Load Trap (`LoadArchive`)

When a client attempts to decrypt ciphertext created with an archived key version:
- OpenBao detects that the version is missing from `p.Keys`.
- If `MinDecryptionVersion` was lowered or archive retrieval is allowed, OpenBao calls `p.LoadArchive(ctx, storage)` (`policy.go:585`).
- `LoadArchive` executes a **synchronous barrier read** to `archive/{name}` and parses a large JSON array.
- This creates massive latency jitter on legacy ciphertext reads.

---

## 4. Key Versioning & Rotation Audit Findings

```
--------------------------------------------------------------------------------
Finding ID:               FINDING-VER-01
Component:                sdk/helper/keysutil
File Path:                sdk/helper/keysutil/policy.go
Function or Structure:    Policy.Rotate / Policy.Persist
Line Reference:           sdk/helper/keysutil/policy.go:720-760, 850-890
Classification:           SOURCE-CONFIRMED
Existing Behaviour:       Policy.Rotate holds the exclusive write lock (p.l.Lock()) while executing p.Persist(ctx, storage), which performs synchronous barrier encryption and physical storage I/O.
Identified Inefficiency:  Holding the in-memory policy mutex during disk I/O blocks all concurrent readers (DEK wrapping and unwrapping) for that Subject Key, causing multi-millisecond tail latency spikes.
Proposed Improvement:     Implement Read-Copy-Update (RCU) or Copy-On-Write (COW) for Policy updates:
                          1. Prepare new Policy instance with updated version in a private copy.
                          2. Persist to storage FIRST.
                          3. Only after storage write succeeds, acquire p.l.Lock() for microsecond atomic pointer swap.
Expected Latency Impact:  Reduces reader lock wait time during rotation from ~5ms to < 100 nanoseconds.
Expected Throughput:      Completely eliminates P99/P99.9 latency spikes during background key rotation.
Memory Impact:            Negligible (temporary clone of Policy struct during rotation).
Storage Impact:           None.
Security Implications:    Strictly preserves write atomicity; failed disk writes discard new key before it becomes visible to readers.
Tenant Isolation:         Ensures rotating one tenant's key does not block readers.
Key Rotation:             Directly improves rotation concurrency.
Implementation Complexity: Medium.
Confidence Level:         HIGH.
Validation Strategy:      Run 100 concurrent readers executing DEK unwraps while simultaneously triggering 50 key rotations on the same key; measure P99 latency.
--------------------------------------------------------------------------------
```

---

## 5. Architectural Recommendation for Version Management

1. **Decouple Storage I/O from Reader Mutex**: Adopt Copy-On-Write policy rotation so DEK operations continue uninterrupted during key rollover writes.
2. **Version Cache Slices**: Store key versions in an indexed slice `[]*KeyVersionEntry` rather than a map, enabling lock-free atomic reads of `LatestVersion`.
3. **Lazy Version Trimming**: Avoid frequent synchronous rewrites to `archive/{name}` by consolidating historical versions into a compact append-only version log.
