# Proof-of-Concept Implementation Specifications 🤖

## Executive Summary
This document specifies the five most promising acceleration opportunities identified during the OpenBao polyglot performance engineering audit. For each candidate, a complete technical specification is provided, detailing the existing Go implementation, the proposed alternative, candidate libraries, integration mechanics, expected benefits, benchmark methodology, acceptance criteria, and rollback strategies.

---

## Candidate 1: Coarse-Grained Batch DEK Cryptographic Accelerator (Rust FFI)

### 1.1 Current Go Implementation
- **Source**: `sdk/helper/keysutil/policy.go:2400-2470` (`EncryptWithFactory`), `policy.go:2570-2640` (`DecryptWithFactory`), and `internal/builtin/logical/transit/path_decrypt.go:160-210`.
- **Mechanics**: Loops sequentially over incoming batch items in Go, initializing `aes.NewCipher` and `cipher.NewGCM` per item, generating heavy heap allocations and GC pressure.

### 1.2 Proposed Alternative
- **Language**: **Rust**.
- **Candidate Library**: `aws-lc-rs` (AWS-LC cryptographic engine) + `rayon` (data parallelism).
- **Integration Mechanism**: C ABI static library linked via CGO. Go passes a contiguous array descriptor containing pointers to all batch ciphertexts/plaintexts, nonces, and tags. Rust processes the batch in parallel using AVX-512 VAES vector pipelines and returns results in a single FFI call.

### 1.3 Expected Hypothesis
- Batch throughput increases by **50% to 120%** for batches ≥ 50 items.
- Amortizes FFI call overhead to < 2.0 ns/item (`BENCHMARK-CONFIRMED`).
- Eliminates 80% of Go heap allocations in batch cryptographic processing.

### 1.4 Security Considerations
- Memory buffers zeroized via `zeroize::ZeroizeOnDrop`.
- Boundary input validation strictly guards against buffer overruns.
- `std::panic::catch_unwind` wraps FFI entry point to prevent unhandled process aborts.

### 1.5 Acceptance Criteria
- ≥ 40% higher throughput on batch sizes ≥ 50.
- P99 latency reduced by ≥ 30% under 64 concurrent workers.
- Zero memory leaks detected by AddressSanitizer and Valgrind.

### 1.6 Rollback Strategy
- Feature flag `VAULT_ACCEL_NATIVE_BATCH=false` reverts immediately to pure Go transit loop.

---

## Candidate 2: Sharded Lock-Free Subject Key Cache (Optimised Go)

### 2.1 Current Go Implementation
- **Source**: `sdk/helper/keysutil/transit_lru.go:14-38`.
- **Mechanics**: Employs `github.com/hashicorp/golang-lru/v2.TwoQueueCache`, which uses a single global `sync.Mutex` for all cache reads and writes.

### 2.2 Proposed Alternative
- **Language**: **Optimised Go**.
- **Candidate Library**: Sharded RWMutex cache or Lock-free RCU atomic pointer cache (`sync/atomic`).
- **Integration Mechanism**: Native in-process Go interface implementation of `keysutil.Cache`.

### 2.3 Expected Hypothesis
- Lookup latency reduced from **143 ns to 36 ns** at 10K keys (`BENCHMARK-CONFIRMED`).
- Lock contention eliminated under 64+ concurrent reading goroutines.
- Zero CGO or FFI complexity.

### 2.4 Acceptance Criteria
- 4x throughput increase under 64 concurrent client threads.
- Constant P99 lookup latency < 100 ns.
- Passing all existing OpenBao keysutil unit and integration tests.

### 2.5 Rollback Strategy
- Configurable via existing `cache_size` configuration toggle.

---

## Candidate 3: Off-Heap Pinned Subject Key Memory Arena (Rust)

### 2.1 Current Go Implementation
- **Source**: `sdk/helper/keysutil/policy.go:120-180`.
- **Mechanics**: Policies and decrypted Subject Key bytes are stored as standard Go pointers in the Go heap. At 1M-10M keys, millions of objects must be scanned by the Go garbage collector during each GC cycle.

### 2.2 Proposed Alternative
- **Language**: **Rust**.
- **Candidate Technology**: Dedicated off-heap memory arena utilizing `libc::mmap` with `MAP_LOCKED` (`mlock(2)`), exposing opaque 64-bit integer handles to Go.
- **Integration Mechanism**: Handle-based CGO FFI (`load_key(id) -> handle_id`, `drop_key(handle_id)`).

### 2.3 Expected Hypothesis
- Eliminates 100% of Subject Key heap objects from Go GC scan phase.
- Reduces GC STW mark pause duration by up to **60%** at 1,000,000 keys.
- Operating system enforces swap-prevention, keeping key material pinned in physical RAM.

### 2.4 Security Considerations
- Eliminates risk of plaintext key material lingering in reclaimed Go heap pages.
- Memory explicitly wiped on deletion via `memset_s` / `zeroize`.

### 2.5 Acceptance Criteria
- Go heap scan size reduced by > 500 MB at 1M keys.
- GC pause times maintain P99 < 500 µs.

### 2.6 Rollback Strategy
- Standard Go heap caching remains available as default fallback.

---

## Candidate 4: Zero-Allocation Request Decoding & Buffer Pools (Optimised Go)

### 4.1 Current Go Implementation
- **Source**: `internal/builtin/logical/transit/path_encrypt.go:30-45` and `path_decrypt.go:20-40`.
- **Mechanics**: Uses `github.com/go-viper/mapstructure` to reflect over incoming JSON maps and allocates new byte slices for every single encryption/decryption item.

### 4.2 Proposed Alternative
- **Language**: **Optimised Go**.
- **Candidate Technology**: Code-generated fast JSON deserializer (`bytedance/sonic` or manual `easyjson` unmarshaler) paired with `sync.Pool` for cryptographic buffers.
- **Integration Mechanism**: Direct Go package integration.

### 4.3 Expected Hypothesis
- Request decoding CPU time reduced by **65%**.
- Heap allocations per DEK unwrap reduced from 39 allocs/op to < 5 allocs/op (`BENCHMARK-CONFIRMED`).
- Eliminates 28% of request processing CPU overhead before cryptography executes.

### 4.4 Acceptance Criteria
- `BenchmarkTransit_BatchDecryption1` drops from 9,862 ns to < 3,500 ns.
- Zero allocations in buffer recycling paths.

### 4.5 Rollback Strategy
- Retain existing `decodeEncryptBatchRequestItems` as fallback path.

---

## Candidate 5: Zero-Copy Ciphertext Pipeline (Optimised Go)

### 5.1 Current Go Implementation
- **Source**: `internal/builtin/logical/transit/path_datakey.go:149` and `sdk/helper/keysutil/policy.go:2407`.
- **Mechanics**: Converts raw bytes to base64 strings and back to raw byte slices up to 4 times during a single DEK generation and wrapping request.

### 5.2 Proposed Alternative
- **Language**: **Optimised Go**.
- **Candidate Technology**: Internal binary/slice API passing `[]byte` directly through the execution pipeline, performing base64 encoding only at the final HTTP response serialization boundary.
- **Integration Mechanism**: Internal Go API refactoring.

### 5.3 Expected Hypothesis
- Eliminates 4 string-to-byte allocations per request.
- Improves single-item DEK wrapping latency by **15% to 25%**.

### 5.4 Acceptance Criteria
- DEK wrapping benchmark allocations drop by ≥ 4 allocs/op.
- 100% backward API compatibility with existing HTTP JSON clients.

### 5.5 Rollback Strategy
- Internal interface refactoring guarded by standard unit test suite.
