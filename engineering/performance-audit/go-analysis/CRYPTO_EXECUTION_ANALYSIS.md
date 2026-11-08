# Cryptographic Execution, Hashing & Cipher Performance Analysis

## Overview

This report examines cryptographic operations across OpenBao, focusing on barrier envelope encryption (AES-256-GCM), Shamir secret sharing, HMAC calculations for audit log masking, and key serialization routines.

Because cryptographic primitives are computationally intensive and operate directly on sensitive in-memory material, optimizations must maintain strict constant-time properties, side-channel resistance, and zeroization guarantees.

---

## Findings

### Finding CRY-001: math/big.Int Allocations in Shamir Fisher-Yates Shuffle
- **Finding ID**: CRY-001
- **Category**: Cryptographic Execution / Allocation Overhead
- **File path**: `sdk/helper/shamir/shamir.go`
- **Function**: `shuffledXCoordinates`
- **Line range**: Lines 60–85
- **Existing implementation**:
  In `shuffledXCoordinates`:
  ```go
  var result []uint8
  for i := 1; i < 256; i++ {
      result = append(result, uint8(i))
  }
  for i := len(result) - 1; i > 0; i-- {
      jI, err := rand.Int(rand.Reader, big.NewInt(int64(i)))
      if err != nil {
          return nil, err
      }
      j := int(jI.Int64())
      result[i], result[j] = result[j], result[i]
  }
  ```
- **Technical inefficiency**:
  Inside the shuffle loop running 254 iterations:
  1. `big.NewInt(int64(i))` is called on every iteration, allocating a `big.Int` struct and backing slice header on the heap.
  2. `rand.Int(rand.Reader, ...)` allocates and returns a new `*big.Int` on every iteration.
  3. `result` slice starts as nil and reallocates repeatedly during the initial loop.
  This produces over 500 heap allocations per Shamir split operation solely to shuffle 255 bytes.
- **Why it matters**:
  Unseal operations, rekey operations, and root token generation run through Shamir splitting. Allocation churn in cryptographic code paths risks GC latency and fragments heap memory holding sensitive shares.
- **Potential improvement**:
  1. Preallocate `result` with `make([]uint8, 0, 255)`.
  2. Preallocate a single reusable `big.Int` limit or, even better, sample a single random byte and reject values `>= 256 - (256 % i)` to achieve a bias-free uniform distribution without `math/big`.
- **Behavioural risks**: None.
- **Security implications**: The distribution must remain strictly uniform to avoid bias in polynomial evaluation coordinates.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`sdk/helper/shamir/shamir.go:60-85`), STATIC-ANALYSIS-CONFIRMED (heap escapes of `big.NewInt`).

---

### Finding CRY-002: Repeated Polynomial Allocations in Shamir Secret Splitting
- **Finding ID**: CRY-002
- **Category**: Cryptographic Execution / Memory Allocations
- **File path**: `sdk/helper/shamir/shamir.go`
- **Function**: `Split`, `makePolynomial`
- **Line range**: Lines 45–60
- **Existing implementation**:
  ```go
  for idx, val := range secret {
      p, err := makePolynomial(val, uint8(threshold-1))
      ...
  }
  ```
  Where `makePolynomial` executes:
  ```go
  p := polynomial{
      coefficients: make([]byte, degree+1),
  }
  ```
- **Technical inefficiency**:
  For every single byte of `secret` (e.g., 32 bytes for an AES-256 key or 64 bytes for master keys), `makePolynomial` allocates a new `[]byte` slice for `p.coefficients`. For a 32-byte key with threshold 5, this performs 32 distinct heap allocations.
- **Why it matters**:
  Each slice of coefficients escapes to the heap and contains secret ephemeral polynomial intercepts that must subsequently be collected by the garbage collector rather than being safely stack-allocated or immediately zeroized.
- **Potential improvement**:
  Reuse a single `coefficients` buffer across iterations, zeroizing its contents after each byte is evaluated across all shares.
- **Behavioural risks**: Low.
- **Security implications**: Positive: reduces unzeroized ephemeral secret fragments scattered across the Go heap.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`sdk/helper/shamir/shamir.go:45-60`).

---

### Finding CRY-003: String and Slice Allocation in Barrier AES-GCM Encrypt
- **Finding ID**: CRY-003
- **Category**: Cryptographic Execution / Buffer Allocation
- **File path**: `internal/vault/barrier/aes_gcm.go`
- **Function**: `AESGCMBarrier.encrypt`
- **Line range**: Lines 1015–1045
- **Existing implementation**:
  ```go
  switch b.currentAESGCMVersionByte {
  case AESGCMVersion1:
      out = gcm.Seal(out, nil, plain, nil)
  case AESGCMVersion2:
      aad := []byte(nil)
      if path != "" {
          aad = []byte(path)
      }
      out = gcm.Seal(out, nil, plain, aad)
  }
  ```
- **Technical inefficiency**:
  `aad = []byte(path)` copies the storage key path string into a heap-allocated byte slice for every barrier write. Under version 2, `path` serves as authenticated additional data (AAD). Converting the string to a byte slice creates an avoidable allocation per storage write.
- **Why it matters**:
  Physical storage writes bottleneck on allocation and cryptographic hashing speed.
- **Potential improvement**:
  Utilize Go 1.20+ `unsafe.Slice(unsafe.StringData(path), len(path))` to pass `aad` to `gcm.Seal` with zero allocations and zero memory copies.
- **Behavioural risks**: Low. `gcm.Seal` only reads `aad` without modifying it.
- **Security implications**: Safe; read-only access to immutable string memory.
- **Confidence level**: High
- **Validation methodology**: STATIC-ANALYSIS-CONFIRMED (`[]byte(path)` escapes to heap in `aes_gcm.go:1039`).

---

### Finding CRY-004: Repetitive HMAC Instance Construction in Audit Log Formatter
- **Finding ID**: CRY-004
- **Category**: Cryptographic Execution / Hashing
- **File path**: `sdk/helper/salt/salt.go`, `internal/audit/hashstructure.go`
- **Function**: `Salt.GetHMAC`, `HashString`
- **Line range**: `salt.go:146-152`
- **Existing implementation**:
  ```go
  func (s *Salt) GetHMAC(data string) string {
      hm := hmac.New(s.config.HMAC, []byte(s.salt))
      hm.Write([]byte(data))
      return hex.EncodeToString(hm.Sum(nil))
  }
  ```
- **Technical inefficiency**:
  `hmac.New` initializes an HMAC internal state by creating two distinct hash instances (inner and outer hash) and hashing `s.salt ^ ipad` and `s.salt ^ opad`. When `HashStructure` masks 20–50 sensitive fields in a large audit payload, `hmac.New` is invoked 20–50 times sequentially, repeating the exact same key expansion and pad XOR steps for every single field.
- **Why it matters**:
  Audit logging represents a major CPU consumer during high-rate write workloads. Key expansion inside `hmac.New` accounts for a substantial portion of the CPU time spent in audit formatting.
- **Potential improvement**:
  Precompute and cache the marshaled inner and outer hash states for the active salt using `encoding.BinaryMarshaler` (supported by `crypto/sha256`), or use a pooled/cloned HMAC state to eliminate key setup on every field.
- **Behavioural risks**: Medium; requires verification that crypto library state cloning behaves correctly.
- **Security implications**: None; mathematical output of HMAC remains identical.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`sdk/helper/salt/salt.go:146-152`).
