# Benchmark Measurements and Methodology :robot:

## 1. Methodology & Environment

All benchmarks were executed directly within the local repository sandbox using the Go standard testing framework (`go test -bench=... -benchmem`). No external services, production traffic, or remote endpoints were involved.

### Hardware & Runtime Specifications
- **Architecture:** `x86_64` (linux/amd64)
- **CPU:** Intel(R) Xeon(R) Processor @ 2.30GHz
- **Go Version:** `go version go1.27.0 linux/amd64`
- **Compiler:** Standard Go compiler (`gc`)

---

## 2. Benchmark Suite 1: Transit Batch Encryption

**Command:**
```bash
go test -bench=BenchmarkTransit_BatchEncryption -benchmem -benchtime=50x -run=^$ ./internal/builtin/logical/transit/
```

### Execution Results
| Benchmark Name | Batch Size ($N$) | Latency (ns/op) | Latency (µs or ms) | Memory (B/op) | Allocs/op |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `BenchmarkTransit_BatchEncryption1` | 1 | 15,983 ns/op | 15.98 µs | 5,066 B/op | 46 allocs/op |
| `BenchmarkTransit_BatchEncryption10` | 10 | 49,470 ns/op | 49.47 µs | 21,045 B/op | 127 allocs/op |
| `BenchmarkTransit_BatchEncryption50` | 50 | 244,563 ns/op | 244.56 µs | 93,563 B/op | 487 allocs/op |
| `BenchmarkTransit_BatchEncryption100` | 100 | 416,390 ns/op | 416.39 µs | 182,302 B/op | 937 allocs/op |
| `BenchmarkTransit_BatchEncryption1000` | 1,000 | 3,401,838 ns/op | 3.40 ms | 1,795,643 B/op | 9,941 allocs/op |
| `BenchmarkTransit_BatchEncryption10000`| 10,000 | 35,302,659 ns/op | 35.30 ms | 17,882,640 B/op | 99,950 allocs/op |

### Analysis
1. **Linear Scaling:** Latency scales directly linearly with batch size ($R^2 > 0.99$), confirming the single-threaded sequential execution of `p.EncryptWithFactory`.
2. **Allocation Growth:** Allocations scale at ~10 allocations per item, reaching ~100,000 allocations and ~17.9 MB per batch request for $N=10,000$.

---

## 3. Benchmark Suite 2: Transit Batch Decryption

**Command:**
```bash
go test -bench=BenchmarkTransit_BatchDecryption -benchmem -benchtime=50x -run=^$ ./internal/builtin/logical/transit/
```

### Execution Results
| Benchmark Name | Batch Size ($N$) | Latency (ns/op) | Latency (µs or ms) | Memory (B/op) | Allocs/op |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `BenchmarkTransit_BatchDecryption1` | 1 | 11,762 ns/op | 11.76 µs | 3,565 B/op | 39 allocs/op |
| `BenchmarkTransit_BatchDecryption10` | 10 | 27,613 ns/op | 27.61 µs | 17,954 B/op | 102 allocs/op |
| `BenchmarkTransit_BatchDecryption50` | 50 | 155,802 ns/op | 155.80 µs | 84,213 B/op | 382 allocs/op |
| `BenchmarkTransit_BatchDecryption100` | 100 | 234,404 ns/op | 234.40 µs | 164,430 B/op | 732 allocs/op |
| `BenchmarkTransit_BatchDecryption1000` | 1,000 | 1,944,355 ns/op | 1.94 ms | 1,615,082 B/op | 7,935 allocs/op |
| `BenchmarkTransit_BatchDecryption10000`| 10,000 | 18,997,415 ns/op | 19.00 ms | 16,115,656 B/op | 79,943 allocs/op |

### Analysis
Decryption exhibits identical linear scaling characteristics to encryption (~1.9 µs per item, 8 allocations per item).

---

## 4. Benchmark Suite 3: TokenStore Service Token Creation

**Command:**
```bash
go test -bench=BenchmarkTokenStore_HandleCreateCommon -benchmem -benchtime=50x -run=^$ ./internal/vault/
```

### Execution Results
- **Latency:** `213,242 ns/op` (~0.213 ms per token creation)
- **Memory Consumption:** `38,490 B/op` (~38.5 KB per token)
- **Heap Allocations:** `386 allocs/op`

### Analysis
Even in an in-memory test core harness (`TestCoreUnsealed`), creating a single standard service token requires 386 distinct memory allocations and ~38.5 KB of heap churn, driven by accessor generation, multiple salt derivations, barrier AES-GCM wrapping, and three separate storage view writes.

---

## 5. Microbenchmarks: Subsystem Components

### A. Key Hashing for Lock Sharding (`LockIndexForKey`)
Comparison between current `cryptoutil.Blake2b256Hash` and direct `blake2b.Sum256`:
- **Current `Blake2b256Hash`:** `431 ns/op`, `3 allocs/op`, `464 B/op`
- **Direct `blake2b.Sum256`:** `252 ns/op`, `1 allocs/op`, `48 B/op`
- **Improvement Potential:** 41.5% reduction in latency and 89.7% reduction in memory churn.

### B. Deep Map Cloning via JSON Round-Trip (`getUnmarshaledCopy`)
Benchmark of JSON marshal/unmarshal cycle used by audit logging for a sample 4-key nested map:
- **Latency:** `9,233 ns/op` (~9.23 µs)
- **Memory Consumption:** `1,425 B/op`
- **Heap Allocations:** `45 allocs/op`
- **Impact:** In high-throughput audit setups (e.g., 20,000 req/sec), this serialization cycle alone accounts for ~900,000 heap allocations per second.
