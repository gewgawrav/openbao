# Storage Execution, Caching & Barrier Access Analysis

## Overview

This report audits the storage access layer of OpenBao, focusing on `logical.StorageView`, `barrier.View`, and physical backend interactions.

In OpenBao, every persisted secret, lease, token, entity, and policy is routed through a hierarchical series of views that enforce prefix isolation before reaching the barrier encryptor (`AESGCMBarrier`) and underlying physical storage (such as Raft, PebbleDB, or PostgreSQL). Redundant read/write amplification, unindexed list filtering, and unnecessary serialization amplify I/O latency and CPU overhead.

---

## Findings

### Finding STR-001: ListPage Prefix Stripping and Slice Allocation Overhead in StorageView
- **Finding ID**: STR-001
- **Category**: Storage Execution / Allocation Overhead
- **File path**: `sdk/logical/logical_storage.go`, `internal/vault/barrier/view.go`
- **Function**: `StorageView.List`, `StorageView.ListPage`
- **Line range**: Lines 60–110
- **Existing implementation**:
  In `StorageView.List` and `ListPage`:
  ```go
  keys, err := v.view.List(ctx, v.expandKey(prefix))
  if err != nil {
      return nil, err
  }
  for i, k := range keys {
      keys[i] = v.truncateKey(k)
  }
  return keys, nil
  ```
  Where `v.expandKey(prefix)` concatenates `v.prefix + prefix` into a new string, and `truncateKey(k)` executes `strings.TrimPrefix(k, v.prefix)`.
- **Technical inefficiency**:
  For list operations returning tens of thousands of items:
  1. The underlying physical storage returns a slice of full keys (allocated on the heap).
  2. `StorageView` iterates over every element in `keys` and calls `strings.TrimPrefix`. If nested storage subviews exist (e.g. `namespace/sys/expire/`), this prefix expansion and truncation loop occurs recursively at each layer of the view stack.
  3. Truncating creates a new string header for each entry if not sharing backing string data.
- **Why it matters**:
  Bulk list operations (e.g., token accessors, PKI certificates, or lease sweeps) allocate tens of thousands of intermediate string objects across layered views.
- **Potential improvement**:
  1. Pass the target subview prefix directly into physical storage to filter and return pre-trimmed keys where the backend supports prefix iteration (such as PebbleDB or Raft BoltDB).
  2. Avoid nested view wrapping when constructing internal subviews by flattening prefixes at initialization time.
- **Behavioural risks**: Low.
- **Security implications**: Prefix isolation invariants must remain strictly verified.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`sdk/logical/logical_storage.go:60-110`).

---

### Finding STR-002: In-Memory Storage Cache Entry Deep Copying vs Immutability
- **Finding ID**: STR-002
- **Category**: Storage Execution / Serialization & Copying
- **File path**: `internal/vault/storage_index.go`, `sdk/logical/storage_inmem.go`
- **Function**: `InmemStorage.Get`, `InmemStorage.Put`
- **Line range**: Lines 40–80
- **Existing implementation**:
  In-memory storage and caching layers copy byte slices:
  ```go
  val := make([]byte, len(entry.Value))
  copy(val, entry.Value)
  ```
- **Technical inefficiency**:
  Whenever an entry is read from or written to cached storage layers, the entire byte slice is copied to prevent callers from mutating internal state.
- **Why it matters**:
  For large secret entries (e.g., 50KB certificates, private key bundles, or large configuration blobs), copying slices on every get/put operation exerts significant heap allocation pressure and CPU memory bandwidth consumption.
- **Potential improvement**:
  Adopt immutable byte slice wrappers or read-only views where entries are guaranteed never to be mutated after construction, eliminating defensive copying.
- **Behavioural risks**: Low.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`sdk/logical/storage_inmem.go:40-80`).

---

### Finding STR-003: Barrier Lock-Switched Get Key Verification Allocation
- **Finding ID**: STR-003
- **Category**: Storage Execution / Buffer & Lock Efficiency
- **File path**: `internal/vault/barrier/aes_gcm.go`
- **Function**: `AESGCMBarrier.lockSwitchedGet`
- **Line range**: Lines 860–905
- **Existing implementation**:
  In `lockSwitchedGet`:
  ```go
  term := binary.BigEndian.Uint32(pe.Value[:4])
  gcm, err := b.aeadForTerm(term)
  ...
  plain, err := b.decrypt(key, gcm, pe.Value)
  entry := &logical.StorageEntry{
      Key: key,
      Value: plain,
      SealWrap: pe.SealWrap,
  }
  ```
- **Technical inefficiency**:
  Every physical storage `Get` creates a new `*logical.StorageEntry` struct on the heap, wrapping the decrypted `plain` slice.
- **Why it matters**:
  High-frequency lookups (token resolution, entity resolution) allocate millions of short-lived `StorageEntry` headers per second.
- **Potential improvement**:
  Utilize a `sync.Pool` for `logical.StorageEntry` instances on internal read-only evaluation paths where the entry does not escape beyond the caller's stack frame.
- **Behavioural risks**: Low.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/barrier/aes_gcm.go:860-905`).
