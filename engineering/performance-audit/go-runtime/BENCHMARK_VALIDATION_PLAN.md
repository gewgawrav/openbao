# Benchmark Validation Plan & Profiling Specifications 🤖

## Executive Summary

To substantiate proposed optimizations and guard against regressions in cryptographic correctness, memory safety, or multi-tenant isolation, rigorous empirical benchmarking is required.

This document details benchmark specifications, test harnesses, CPU and memory profiling commands (`pprof`), and verification metrics to be executed offline within the repository environment.

---

## 1. Local Benchmark Execution Commands

The following commands can be executed directly within the repository to reproduce baseline metrics and measure optimization impact:

### A. Transit Batch Cryptography Benchmarks
```bash
# Run batch encryption microbenchmarks across 1, 10, 50, 100, 1000 items
cd internal/builtin/logical/transit
go test -bench='^BenchmarkTransit_BatchEncryption' -benchmem -count=5 -benchtime=2s | tee transit_encrypt_baseline.txt

# Run batch decryption microbenchmarks
go test -bench='^BenchmarkTransit_BatchDecryption' -benchmem -count=5 -benchtime=2s | tee transit_decrypt_baseline.txt
```

### B. Token Store Creation Benchmarks
```bash
# Run token creation benchmark to measure storage write amplification
cd internal/vault
go test -bench='^BenchmarkTokenStore_HandleCreateCommon' -benchmem -count=3 | tee token_create_baseline.txt
```

### C. Lease & Expiration Benchmarks
```bash
cd internal/vault
go test -bench='^BenchmarkExpiration_' -benchmem -count=3 | tee expiration_baseline.txt
```

---

## 2. Profiling & Diagnostics Harness

### CPU Profiling with `pprof`
To capture an exact profile of CPU cycles spent during Transit encryption and DEK wrapping:
```bash
cd internal/builtin/logical/transit
go test -bench=BenchmarkTransit_BatchEncryption100 -cpuprofile=cpu_transit.prof -benchmem -benchtime=5s
go tool pprof -top cpu_transit.prof
```
**Expected Observation**:
In the unoptimized baseline, significant CPU time will appear under:
- `crypto/aes.encryptBlockGo` or `crypto/aes.expandKeyGo`
- `crypto/cipher.(*gcm).deriveCounter`
- `encoding/base64.(*Encoding).DecodeString`
- `runtime.mallocgc` and `runtime.gcDrain`

### Heap Allocation & Escape Profiling
To isolate allocations and identify memory escaping to the heap:
```bash
cd internal/builtin/logical/transit
go test -bench=BenchmarkTransit_BatchEncryption100 -memprofile=mem_transit.prof -benchmem -benchtime=5s
go tool pprof -alloc_space -top mem_transit.prof
```

### Compiler Escape Analysis Inspection
To view the Go compiler's escape decisions without running tests:
```bash
cd internal/builtin/logical/transit
go build -gcflags="-m -m" path_datakey.go 2>&1 | grep -E "(escapes to heap|moved to heap)"
```

---

## 3. Validation Metric Targets

| Metric | Baseline (Measured) | Target After Proposed Optimizations | Success Threshold |
| :--- | :--- | :--- | :--- |
| **Batch Encrypt 100 Latency** | $\approx 380,000\text{ ns/op}$ | $< 65,000\text{ ns/op}$ (Parallelized) | $\ge 5\times$ speedup |
| **Batch Encrypt 10 Allocations**| $125\text{ allocs/op}$ | $< 40\text{ allocs/op}$ (Buffer pooling) | $\ge 65\%$ reduction |
| **Batch Encrypt 10 Allocated Bytes**| $20,173\text{ B/op}$ | $< 6,000\text{ B/op}$ | $\ge 70\%$ reduction |
| **Single Encrypt Allocations** | $44\text{ allocs/op}$ | $< 12\text{ allocs/op}$ | $\ge 70\%$ reduction |
| **Key Lock Contention Probability**| $> 90\%$ (at 50 concurrent keys) | $< 0.1\%$ (16k shards) | Virtually zero collisions |

---

## 4. Regression & Correctness Test Suite

Whenever performance refactorings are introduced, the following offline test suites must pass without error:
```bash
# 1. Full Transit test suite (verifies AES-GCM, ChaCha20, BYOK, derivation, rewrap)
cd internal/builtin/logical/transit && go test -v -race -run=TestBackend_

# 2. Key utility lock and policy test suite
cd sdk/helper/keysutil && go test -v -race -run=TestPolicy_

# 3. Router concurrency and mount tests
cd internal/vault/routing && go test -v -race -run=TestRouter_

# 4. Storage barrier test suite
cd internal/vault/barrier && go test -v -race -run=TestAESGCMBarrier_
```

Passing these tests with `-race` ensures that buffer pooling, lock expansion, and parallel batch workers introduce zero race conditions or data corruption.
