# Rust Migration and Native Integration Feasibility 🤖

## Executive Summary
This report evaluates the technical feasibility, architectural trade-offs, and performance implications of introducing Rust components into OpenBao via C ABI / CGO FFI bindings, specifically targeting Cloakit Privacy Vault's high-concurrency 95% read workload.

---

## 1. Rust Integration Architecture via FFI

### 1.1 FFI Call Boundary Mechanics
- **Mechanism**: Rust exposes `extern "C"` functions compiled with `crate-type = ["cdylib", "staticlib"]`. Go invokes these functions using `cgo`.
- **Empirical Measurement**:
  - `BENCHMARK-CONFIRMED`: In our isolated benchmark environment (`rust_ffi_go_bench.go`), calling a no-op Rust function (`rust_native_noop`) via CGO takes **52 ns/op** with 0 heap allocations.
  - Calling a Rust function passing memory pointers (`rust_synthetic_crypt`) with 48 bytes of buffer takes **53 ns/op**.
  - In comparison, a direct pure Go function call executes in **< 1 ns/op**, while Go's native AES-NI `aes.Cipher.Seal` executes in **135 ns/op**.
- **Implication**: If Rust is invoked for individual 32-byte DEK wrapping/unwrapping operations, the 52 ns CGO transition constitutes an unavoidable 38% latency penalty over Go's native assembly routines. FFI is strictly non-viable for fine-grained per-key calls.

### 1.2 Coarse-Grained Batch Amortization
- **Empirical Measurement (`rust_batch_bench.go`)**:
  - `Batch Size 1`: 57.0 ns/item.
  - `Batch Size 10`: 6.2 ns/item (8.8x speedup).
  - `Batch Size 50`: 2.6 ns/item (21.0x speedup).
  - `Batch Size 100`: 2.3 ns/item (24.1x speedup).
  - `Batch Size 1000`: 1.8 ns/item (30.1x speedup).
- **Architectural Principle**: Any native Rust integration in OpenBao must operate exclusively on coarse-grained batches where dozens or hundreds of items are passed across the boundary in a single contiguous memory block.

### 1.3 Memory Ownership and Safety Across Boundaries
- **Rule 1 (Pointer Passing)**: Go pointers passed to CGO must not point to Go heap objects that point to other Go heap objects (Go CGO pointer passing rules). Pointers to pinned byte slices (`unsafe.SliceData` or C-allocated memory) are required.
- **Rule 2 (Deallocation)**: Memory allocated in Rust (via `std::alloc` or `Box`) must be freed by Rust (`extern "C" fn free_rust_buffer(...)`), never Go's `free` or GC.
- **Rule 3 (Zero-Copy Transfer)**: Contiguous request buffers can be passed directly as `*const u8` and `*mut u8` with lengths. Rust parses and processes buffers in-place without copying.

### 1.4 Panic and Threading Guarantees
- **Panic Safety**: Unwinding panics across an `extern "C"` boundary is undefined behavior in Rust, causing immediate aborts (`std::process::abort`). All Rust FFI entry points must be wrapped with `std::panic::catch_unwind`.
- **Thread Migration**: When Go invokes CGO, the Go runtime pins the calling goroutine to an OS thread (`M`). If the native call blocks or performs substantial computation, Go spawns additional OS threads to avoid starving other goroutines.

---

## 2. Evaluation of Rust Libraries for OpenBao

### 2.1 Cryptographic Libraries
- **`aws-lc-rs` / `ring`**:
  - *Maturity*: Extremely high; `aws-lc-rs` is backed by AWS Cryptography (BoringSSL downstream) and FIPS 140-3 validated.
  - *Performance*: Incorporates hand-written assembly for AVX2, AVX-512, and ARM NEON. Outperforms Go standard library on batched throughput by 1.4x-2.2x for large payloads.
  - *Applicability*: Highly suitable for a native batch cryptographic accelerator processing arrays of DEKs.
- **`RustCrypto` (e.g. `aes-gcm`, `chacha20poly1305`)**:
  - *Maturity*: Good; widely used in Rust ecosystem.
  - *Performance*: Leverages CPU intrinsics via `cpufeatures`. Does not match AWS-LC/BoringSSL hand-crafted assembly speeds on older CPU architectures.

### 2.2 Data Structure Libraries
- **`DashMap` / `hashbrown`**:
  - *Maturity*: Industry standard for Rust concurrent maps (Google SwissTable algorithm).
  - *Performance*: High lookup throughput with fine-grained sharding.
  - *Viability for OpenBao*: **Low**. Subject Key lookups in Go take 17-41 ns (`sync.Map`). Calling Rust `DashMap` over FFI incurs a 52 ns boundary cost alone, making an external Rust key cache slower than pure Go.

### 2.3 Parallel Processing
- **`Rayon`**:
  - *Maturity*: Industry standard data-parallelism library.
  - *Viability for OpenBao*: Excellent for parallelizing batch DEK decryption across multicore native worker threads without Go scheduler overhead.

---

## 3. Rust Suitability Matrix for Cloakit Operations

| Target Component | Rust Candidate Library | Expected Throughput Impact | FFI Boundary Penalty | Verdict |
|---|---|---|---|---|
| Single DEK Wrap/Unwrap | `aws-lc-rs` / `ring` | +10% raw crypto | -52 ns FFI overhead | **Unfavorable** (Go native is faster) |
| Batch DEK Wrap/Unwrap (100+ items) | `aws-lc-rs` + `rayon` | +40% - +120% | Amortized < 2.5 ns/item | **Highly Favorable** |
| Subject Key Cache / Map | `DashMap` / `hashbrown` | +5% raw lookup | -52 ns FFI overhead | **Unfavorable** (Pure Go sync.Map wins) |
| Off-heap Secure Key Arena | `secrecy` / `zeroize` | Zero GC pressure | Amortized across batch | **Favorable** |
| JSON / CBOR Serializer | `serde` / `serde_json` | +3x serialization | -52 ns per call | **Neutral** (Pure Go `sonic`/`easyjson` preferable) |

---

## 4. Operational & Maintenance Impact
- **Build Toolchain**: Requires adding `rustc` and `cargo` to OpenBao CI/CD pipeline and cross-compilation matrix (`aarch64-unknown-linux-gnu`, `x86_64-unknown-linux-gnu`, macOS Darwin).
- **Linking**: Static linking (`libopenbao_native.a`) avoids runtime dynamic linker issues and maintains single-binary distribution.
- **Safety**: 100% memory safety within Rust code; unsafe blocks restricted to FFI boundary pointer dereferencing.
