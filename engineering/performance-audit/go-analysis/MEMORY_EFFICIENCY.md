# Go Memory Efficiency & Allocation Profiling

## Overview

This report documents Go-specific memory management patterns, escape analysis diagnostics, slice growth characteristics, interface boxing, and temporary buffer allocations within OpenBao. High-throughput cryptographic daemons demand minimal garbage collection (GC) pressure to maintain low, predictable latency percentiles (p99/p99.9).

Compiler diagnostics using `-gcflags="-m"` demonstrate that critical operations on the core request path, barrier encryption path, and audit serialization path frequently cause heap escapes and memory copying that can be substantially reduced or eliminated through buffer reuse, preallocation, and structure reuse.

---

## Findings

### Finding MEM-001: Double JSON Roundtrip Serialization in Audit Hashing
- **Finding ID**: MEM-001
- **Category**: Go Memory Efficiency / Serialization
- **File path**: `internal/audit/hashstructure.go`
- **Function**: `getUnmarshaledCopy`
- **Line range**: Lines 60–75
- **Existing implementation**:
  ```go
  func getUnmarshaledCopy(data any) (map[string]any, error) {
      marshaledData, err := json.Marshal(data)
      if err != nil {
          return nil, err
      }
      unmarshaledCopy := map[string]any{}
      if err := json.Unmarshal(marshaledData, &unmarshaledCopy); err != nil {
          return nil, err
      }
      return unmarshaledCopy, nil
  }
  ```
  Called by `HashRequest` (line 120) and `HashResponse` (line 170) whenever `req.Data` or `resp.Data` is non-nil during audit logging.
- **Technical inefficiency**:
  To clone the request or response payload prior to HMAC obfuscation, OpenBao marshals `data` to a JSON byte slice via `json.Marshal`, and then unmarshals that JSON byte slice back into a newly allocated `map[string]any`.
- **Why it matters**:
  On every single audited write or read response, this allocates:
  1. The serialized JSON byte slice (often kilobytes or megabytes in size).
  2. New map headers, map hash buckets, and boxed `any` interface values for every leaf element.
  3. Intermediate reflection structures inside standard `encoding/json`.
  This creates massive GC churn and dominates the CPU time spent in `AuditBroker.LogRequest` and `LogResponse`.
- **Potential improvement**:
  Implement a direct deep-copy function for `map[string]any` or perform the HMAC hashing in-place while streaming directly into the audit formatter encoder, bypassing intermediate JSON roundtrips entirely.
- **Behavioural risks**: Low.
- **Security implications**: None; data obfuscation rules remain identical.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/audit/hashstructure.go:60-75`), STATIC-ANALYSIS-CONFIRMED (heap escapes of `marshaledData` and `unmarshaledCopy`).

---

### Finding MEM-002: ReflectWalk Interface Boxing and Reflection Overhead in HashStructure
- **Finding ID**: MEM-002
- **Category**: Go Memory Efficiency / Interface Boxing & Reflection
- **File path**: `internal/audit/hashstructure.go`
- **Function**: `HashStructure`, `hashWalker`
- **Line range**: Lines 218–340
- **Existing implementation**:
  `HashStructure` invokes `reflectwalk.Walk(data, walker)`. The walker maintains slices of `reflect.Value`, `reflectwalk.Location`, and map keys (`key []string`, `loc []reflectwalk.Location`, `cs []reflect.Value`).
- **Technical inefficiency**:
  Walking arbitrary Go data structures using `reflectwalk` incurs interface boxing on every visited primitive, struct field, map element, and slice member. Each reflection inspection forces values to escape to the heap, creating tens to hundreds of short-lived `reflect.Value` instances and dynamic type conversions per request.
- **Why it matters**:
  Audit logging is synchronous on critical requests (unless configured asynchronously). Interface boxing and reflection walk add substantial latency overhead and heap allocations.
- **Potential improvement**:
  Provide optimized fast-path traversals for standard payload shapes (`map[string]any`, `map[string]string`, `[]string`, primitive types) avoiding `reflectwalk` entirely unless dealing with custom unregistered struct types.
- **Behavioural risks**: Low.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/audit/hashstructure.go:218-340`).

---

### Finding MEM-003: Metrics Label Allocation per AEAD Encryption
- **Finding ID**: MEM-003
- **Category**: Go Memory Efficiency / Heap Allocation
- **File path**: `internal/vault/barrier/aes_gcm.go`
- **Function**: `termLabel`
- **Line range**: Lines 1052–1060
- **Existing implementation**:
  ```go
  func termLabel(term uint32) []metrics.Label {
      return []metrics.Label{
          {
              Name:  "term",
              Value: strconv.FormatUint(uint64(term), 10),
          },
      }
  }
  ```
  Called in `encryptTracked` at line 1195:
  `metrics.IncrCounterWithLabels(barrierEncryptsMetric, 1, termLabel(term))`
- **Technical inefficiency**:
  `termLabel` is called on every storage write or barrier encryption. It calls `strconv.FormatUint`, allocating a new string on the heap, and then allocates a slice containing a `metrics.Label` struct.
- **Why it matters**:
  Keyring terms are monotonically increasing uint32 values that rarely change (typically term 1, 2, or 3 over years). Generating a new string and a new slice header for every single physical barrier encryption wastes allocations on the storage hot path.
- **Potential improvement**:
  Cache `[]metrics.Label` slices per term in an array or map alongside the cached AEAD instances in `AESGCMBarrier.cache`.
- **Behavioural risks**: None.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/barrier/aes_gcm.go:1052-1060`, line 1195), STATIC-ANALYSIS-CONFIRMED (`strconv.FormatUint` and slice escape to heap).

---

### Finding MEM-004: Decrypt Output Slice Reallocation in AESGCMBarrier
- **Finding ID**: MEM-004
- **Category**: Go Memory Efficiency / Buffer Allocation
- **File path**: `internal/vault/barrier/aes_gcm.go`
- **Function**: `AESGCMBarrier.decrypt`
- **Line range**: Lines 1062–1095
- **Existing implementation**:
  ```go
  out := []byte{}
  switch cipher[4] {
  case AESGCMVersion1:
      return gcm.Open(out, nil, raw, nil)
  case AESGCMVersion2:
      aad := []byte(nil)
      if path != "" {
          aad = []byte(path)
      }
      return gcm.Open(out, nil, raw, aad)
  ```
- **Technical inefficiency**:
  `out` is initialized as a zero-capacity slice `[]byte{}`. When passed to `gcm.Open(out, nil, raw, aad)`, standard Go `cipher.AEAD.Open` sees that `cap(out) < len(raw) - gcm.Overhead()` and allocates a completely new slice from the heap to hold the decrypted plaintext. Additionally, `aad := []byte(path)` copies the string `path` into a newly allocated heap byte slice.
- **Why it matters**:
  Every barrier decryption (every storage read) incurs two heap allocations: one for the AAD byte slice and one for the newly allocated decrypted plaintext.
- **Potential improvement**:
  1. Use unsafe string-to-byte conversion (`unsafe.Slice(unsafe.StringData(path), len(path))`) or direct constant slice for `aad` to avoid allocating `[]byte(path)`.
  2. Consider using a `sync.Pool` of preallocated plaintext buffers or preallocating `out` with exact capacity `len(raw) - gcm.Overhead()`.
- **Behavioural risks**: Low. Decrypted entries are passed to caller, so buffer reuse requires caller life-cycle management or copy semantics.
- **Security implications**: Memory must be zeroized if pooled buffers are reused for secret plaintexts.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/barrier/aes_gcm.go:1072-1088`), STATIC-ANALYSIS-CONFIRMED (`[]byte(path)` escapes to heap).

---

### Finding MEM-005: Intermediate String Concatenation and Byte Conversions in Salt Operations
- **Finding ID**: MEM-005
- **Category**: Go Memory Efficiency / String and Byte Conversions
- **File path**: `sdk/helper/salt/salt.go`
- **Function**: `SaltID`, `Salt.GetHMAC`
- **Line range**: Lines 146–152, 172–176
- **Existing implementation**:
  ```go
  func (s *Salt) GetHMAC(data string) string {
      hm := hmac.New(s.config.HMAC, []byte(s.salt))
      hm.Write([]byte(data))
      return hex.EncodeToString(hm.Sum(nil))
  }

  func SaltID(salt, id string, hash HashFunc) string {
      comb := salt + id
      hashVal := hash([]byte(comb))
      return hex.EncodeToString(hashVal)
  }
  ```
- **Technical inefficiency**:
  - `[]byte(s.salt)` copies the salt string into a heap slice on every single HMAC call.
  - `[]byte(data)` copies the data string into a heap slice.
  - In `SaltID`, `salt + id` allocates a concatenated string, `[]byte(comb)` copies it to a byte slice, `hashVal` is allocated, and `hex.EncodeToString` allocates a final hex string.
- **Why it matters**:
  Compiler escape analysis confirms:
  `helper/salt/salt.go:147:40: ([]byte)(s.salt) escapes to heap`
  `helper/salt/salt.go:148:18: ([]byte)(data) escapes to heap`
  `helper/salt/salt.go:174:25: ([]byte)(comb) escapes to heap`
  Every token hashing and audit HMAC operation triggers multiple intermediate allocations.
- **Potential improvement**:
  1. Cache `[]byte(s.salt)` inside `Salt` struct as `saltBytes []byte` so it does not need to be converted per call.
  2. Implement an `io.Writer`-based hash feed (`h.Write(saltBytes); h.Write(idBytes)`) instead of string concatenation `salt + id`.
- **Behavioural risks**: None.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: STATIC-ANALYSIS-CONFIRMED (via `go test -gcflags="-m" ./sdk/helper/salt/...`).
