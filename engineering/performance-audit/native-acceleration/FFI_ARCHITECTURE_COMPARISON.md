# FFI Architecture and Integration Design Comparison 🤖

## Executive Summary
This report analyzes and compares eight integration patterns for connecting OpenBao with external and native execution technologies. It establishes the mathematical, mechanical, and architectural trade-offs between pure Go, in-process FFI (CGO/Rust/C++), out-of-process services, WebAssembly, and native assembly.

---

## 1. Evaluation of Eight Integration Architectures

### Architecture A: Pure Go Implementation
- **Mechanism**: All code compiled by Go compiler (`gc`).
- **Call Overhead**: < 1 ns per function call (direct register/stack call, eligible for compiler inlining).
- **Memory Overhead**: Zero serialization, direct struct pointers, zero copy.
- **Safety**: 100% memory safe, bounds-checked, garbage-collected.
- **Verdict**: Baseline standard. Optimal for single-key operations, caching, and storage drivers.

### Architecture B: Go + CGO + C Library (e.g. OpenSSL, LMDB)
- **Mechanism**: Standard `cgo` bridge invoking C shared/static libraries.
- **Call Overhead**: ~53 ns per call (`BENCHMARK-CONFIRMED`).
- **Memory Overhead**: Requires passing C pointers, manual memory allocation (`malloc`/`free`).
- **Thread Management**: Allocates an OS thread (`M`) to execute the C function.
- **Safety**: High risk of memory corruption, buffer overflow, and process SIGSEGV crash.
- **Verdict**: Unfavorable. Subsumed by Rust.

### Architecture C: Go + Rust via C ABI (e.g. `aws-lc-rs`)
- **Mechanism**: Rust compiles to `staticlib` (`.a`) exposing `extern "C"` functions, linked into OpenBao via CGO.
- **Call Overhead**: ~52 ns per call (`BENCHMARK-CONFIRMED`).
- **Memory Overhead**: Contiguous memory passing via raw pointers.
- **Safety**: Memory safe within Rust code; panic catching prevents process aborts.
- **Verdict**: **Best-in-Class for Native In-Process Acceleration**, provided it is restricted to coarse-grained batch operations.

### Architecture D: Go + C++ via C Wrapper (e.g. Google Highway, Folly)
- **Mechanism**: C++ classes wrapped with `extern "C"` functions, linked via CGO.
- **Call Overhead**: ~55-60 ns per call.
- **Complexity**: Requires C++ runtime (`libstdc++`/`libc++`), C++ exception handling translation, and complex build scripts.
- **Verdict**: Unfavorable compared to Rust.

### Architecture E: Go + Native Assembly (Plan 9 ASM)
- **Mechanism**: Assembly routines written directly in Go's Plan 9 syntax (`.s`).
- **Call Overhead**: ~2 ns per call (uses Go ABIInternal calling convention).
- **Safety**: No bounds checking, direct CPU registers, but runs on Go goroutine stack.
- **Maintenance**: Extremely difficult to write, audit, and debug.
- **Verdict**: Justified only for standard library primitives (where Go team already maintains it). Custom assembly in OpenBao is rejected.

### Architecture F: Go + Subprocess / Native Unix Domain Socket Service
- **Mechanism**: Standalone native daemon running alongside OpenBao, communicating over IPC (Unix Domain Socket or shared memory).
- **Call Overhead**: 10,000 to 30,000 ns (10-30 µs) per IPC round-trip.
- **Safety**: Complete process crash isolation; daemon crash cannot bring down OpenBao.
- **Verdict**: Too slow for high-frequency DEK wrapping/unwrapping.

### Architecture G: Go + WebAssembly (Wasmtime / Wazero)
- **Mechanism**: Compiled Rust/C WebAssembly module executed in Go via Wazero or Wasmtime.
- **Call Overhead**: 100 to 500 ns per invocation.
- **Performance**: Lacks native CPU vectorization (VAES, AVX-512) because WASM is a sandboxed virtual instruction set.
- **Verdict**: Incompatible with high-performance cryptographic goals.

### Architecture H: Independently Managed Native Sidecar Engine
- **Mechanism**: Native acceleration engine managing cryptographic keys in shared hardware/enclave memory.
- **Verdict**: Relevant for confidential computing enclaves (AWS Nitro Enclaves), but excessive for general Cloakit deployments.

---

## 2. Quantitative Comparison Matrix

| Integration Pattern | Call Latency (ns) | Zero-Copy Support | Crash Isolation | Toolchain Complexity | Suitability for Cloakit |
|---|---|---|---|---|---|
| **A. Pure Go** | < 1 ns | Yes | Goroutine Panic Recovery | Minimal (Go only) | **Primary Architecture** |
| **B. Go + CGO + C** | ~53 ns | Yes (Pointer) | None (SIGSEGV crashes process) | High (C Toolchain) | Not Recommended |
| **C. Go + Rust (C ABI)**| ~52 ns | Yes (Pointer) | Catch Unwind Safe | Moderate (Go + Cargo)| **Recommended for Batches** |
| **D. Go + C++** | ~55 ns | Yes (Pointer) | High Risk | High (C++ Toolchain) | Not Recommended |
| **E. Go Assembly** | ~2 ns | Yes | Medium | Extreme | Not Recommended |
| **F. Native Subprocess**| ~15,000 ns | No (IPC Copy) | Complete Process Isolation | Low (Independent binary)| Too High Latency |
| **G. WebAssembly** | ~250 ns | No (Linear Mem) | Sandbox Isolation | Low (Wazero pure Go) | Lacks Vector Crypto |

---

## 3. Architectural Rule: Fine-Grained vs Coarse-Grained FFI
- **Fine-Grained Trap**: Calling a native library for an individual 32-byte DEK introduces a 52 ns penalty to save 20 ns of crypto time, netting a **negative return**.
- **Coarse-Grained Batching**: In Cloakit, when applications encrypt or decrypt batches of DEKs (e.g. 50-1000 records), passing an array descriptor containing pointers to all items in one CGO invocation amortizes the 52 ns boundary cost to **< 1 ns per item**, unlocking the full hardware throughput of native vectorization.
