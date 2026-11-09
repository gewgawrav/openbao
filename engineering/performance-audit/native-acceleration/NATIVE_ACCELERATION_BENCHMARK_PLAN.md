# Native Acceleration Benchmark Plan 🤖

## Executive Summary
This document establishes the rigorous benchmarking protocol to evaluate candidate native acceleration modules against OpenBao's pure Go implementation under the Cloakit Privacy Vault workload (95% read / 5% write).

---

## 1. Benchmarking Scope and Workload Matrix

### 1.1 Target Test Operations
1. **Subject Key Lookup**: Evaluating lookup speed across varying Subject Key set sizes.
2. **DEK Wrapping (Encryption)**: Generating and encrypting 32-byte Data Encryption Keys.
3. **DEK Unwrapping (Decryption)**: Decrypting 32-byte Data Encryption Keys with AEAD tag authentication.
4. **Key Rotation & Re-wrapping**: Rotating Subject Keys and batch rewrapping legacy ciphertexts.
5. **Memory & GC Pressure**: Measuring heap allocations, bytes allocated, and GC mark pause duration under continuous load.

### 1.2 Test Cardinality & Scale
- **Key Set Cardinality**: 10,000 keys (10K), 100,000 keys (100K), 1,000,000 keys (1M), and 10,000,000 keys (10M).
- **Concurrency Profiles**: 1, 8, 16, 64, 128, and 256 concurrent client worker goroutines.
- **Cache Conditions**:
  - Cold cache (empty memory, forcing physical storage reads via PebbleDB).
  - Warm cache (all active Subject Keys preloaded in memory).
- **Read / Write Ratios**:
  - Pure read (100:0) - high-volume unwrapping.
  - Standard Cloakit target (95:5) - 95% unwrapping/lookup, 5% wrapping/rotation.
  - Write heavy (50:50) - bulk key provisioning stress test.
- **Batch Granularity**: 1 (individual item), 10, 50, 100, 500, and 1,000 items per request.

---

## 2. Metrics and Telemetry Collection

| Metric Category | Target Measurement | Collection Tool / Instrumentation | Acceptance Target |
|---|---|---|---|
| **Throughput** | Requests / sec; Ops / sec | `ghz`, `vegeta`, Go `testing.B` | > 50,000 batch ops/sec |
| **Latency Profile** | p50, p95, p99, p99.9 latency (microseconds) | HDR Histogram, OpenTelemetry | p99 < 2.0 ms under load |
| **Memory Allocation** | B/op, allocs/op | Go runtime `testing.AllocsPerOp` | 0 allocs in crypto hot path |
| **GC Pacer Activity** | GC mark time, STW pauses, % CPU in GC | `GODEBUG=gctrace=1`, Go Execution Tracer | GC CPU overhead < 5% |
| **FFI Boundary Cost** | Per-item transition latency | Synthetic CGO / Rust harness | < 2.5 ns / item amortized |
| **Lock Contention** | Mutex wait time, futex sleep events | `go tool pprof --mutex` | 0 contention on key reads |
| **CPU Instruction Eff.**| Instructions per cycle (IPC), cache misses | Linux `perf stat -e instructions,cycles,cache-misses` | IPC > 2.0 |

---

## 3. Benchmark Execution Methodology

### Step 1: Isolation & Environment Standardization
- CPU governor pinned to `performance` mode (`cpupower frequency-set -g performance`).
- Benchmarks executed on pinned CPU cores via `taskset -c 2-7`.
- Background daemons and periodic cron jobs disabled.

### Step 2: Pure Go Baseline Measurement
- Execute Transit benchmark suites:
  ```bash
  go test -bench=BenchmarkTransit_Batch -benchmem -run=^$ ./internal/builtin/logical/transit/
  ```
- Collect CPU, memory, and block profiles:
  ```bash
  go test -bench=BenchmarkTransit_BatchDecryption100 -cpuprofile=cpu.pprof -memprofile=mem.pprof ./internal/builtin/logical/transit/
  ```

### Step 3: Candidate Native Module Measurement
- Compile candidate Rust static library (`cargo build --release`).
- Link into synthetic test harness using identical batch payload schemas and keys.
- Execute identical benchmark parameters across identical key cardinalities.

### Step 4: Side-by-Side Equivalence Verification
- Verify that both implementations use identical algorithms (AES-256-GCM), 12-byte nonces, 16-byte authentication tags, and identical error-checking behaviors.
- Ensure no security features (e.g. constant-time verification) are disabled in the native code to inflate benchmark results.

---

## 4. Benchmark Acceptance Criteria
To warrant adoption of any native component into OpenBao:
1. **Batch Throughput**: Must demonstrate a **≥ 40% improvement in operations/second** for batch sizes ≥ 50 items.
2. **P99 Tail Latency**: Must demonstrate a **≥ 30% reduction in P99 latency** under high concurrency (128+ goroutines).
3. **Memory Allocation**: Must reduce Go heap allocations in the cryptographic path by **≥ 80%**.
4. **No Regression on Single Calls**: If a single-item request is made, native execution must fall back to pure Go to prevent FFI latency degradation.
