# OpenBao Performance & Benchmark Plan

This document establishes the official benchmarking methodology, workload profiles, toolchains, and acceptance criteria to validate performance improvements across the OpenBao system.

---

## 1. Benchmarking Principles & Rules of Engagement

1. **Isolation**: All benchmarks must execute on dedicated bare-metal or pinned cloud instances (e.g., c6i.4xlarge / c7i.4xlarge) with disabled CPU frequency scaling (performance governor).
2. **Reproducibility**: Environment parameters (OS version, kernel version, Go toolchain version, CPU model, memory clock, storage type) must be recorded with every benchmark run.
3. **Evidence-Based Evaluation**:
   - `CONFIRMED` optimizations require statistically rigorous before-and-after runs using `benchstat` (minimum 10 iterations: `go test -bench ... -count=10`).
   - Claimed improvements must demonstrate non-overlapping 95% confidence intervals or p < 0.01.
4. **Safety & Zero Code Compromise**: Performance testing must not weaken encryption parameters, skip HMAC validations, or bypass security barriers.

---

## 2. Tooling and Observability Stack

| Tool | Purpose | Usage / Invocation |
|---|---|---|
| **Go Testing Engine** | Microbenchmarks & memory allocations | `go test -bench=. -benchmem -count=10` |
| **pprof** | CPU, heap, and block profiling | `go test -cpuprofile=cpu.pprof -memprofile=mem.pprof` |
| **benchstat** | Statistical comparison of benchmark deltas | `benchstat baseline.txt optimized.txt` |
| **Vegeta / k6** | HTTP macro-benchmarking & load generation | Distributed load generators targeting HTTP API |
| **Prometheus / OpenTelemetry** | Real-time metric scraping | Gauge and counter observation at 1s scrape intervals |
| **bcc / bpftrace** | eBPF kernel I/O and fsync tracing | `bpftrace -e 'kprobe:vfs_fsync { ... }'` |

---

## 3. Benchmark Scenarios

### Scenario A: Core API Throughput & Latency
- **Workload Profiles**:
  - **Read-Heavy**: 95% KV v2 secret reads (`GET /v1/secret/data/app`), 5% writes.
  - **Write-Heavy**: 90% KV v2 secret writes (`POST /v1/secret/data/app`), 10% reads.
  - **Mixed Enterprise**: 70% reads, 20% writes, 10% deletes/lists.
- **Concurrency Levels**: 10, 50, 100, 500, 1,000 concurrent client connections (HTTP/1.1 and HTTP/2).
- **Target Metrics**:
  - Sustained requests per second (RPS).
  - Latency percentiles: p50, p95, p99, p99.9.
  - Connection reuse efficiency and TCP socket recycling.
- **Acceptance Criteria**:
  - Read-heavy p99 latency < 5ms under 500 concurrent connections.
  - Zero dropped connections or 5xx errors under load.

---

### Scenario B: Transit Secrets Engine Operations
- **Workload Profiles**:
  - **Single Operations**: `POST /v1/transit/encrypt/:key` and `POST /v1/transit/decrypt/:key`.
  - **Batch Operations**: Batches of 1, 10, 50, 100, 500, 1,000, and 5,000 items in `batch_input`.
  - **Sign & Verify**: Ed25519 and RSA-4096 signature creation and verification.
  - **Payload Sizes**: 32 B, 256 B, 4 KB, 64 KB, 1 MB.
  - **Cipher Suites**: `aes256-gcm96`, `chacha20-poly1305`, `rsa-2048`, `ed25519`.
- **Existing Test Coverage**:
  - `internal/builtin/logical/transit/path_encrypt_bench_test.go` (`BenchmarkTransit_BatchEncryption*`)
  - `internal/builtin/logical/transit/path_decrypt_bench_test.go` (`BenchmarkTransit_BatchDecryption*`)
- **Acceptance Criteria**:
  - Batch encryption of 1,000 items (aes256-gcm) drops from ~25ms to < 2.5ms on a 16-core system.
  - Linear scaling with core count up to 32 cores.

---

### Scenario C: Storage Subsystem Throughput & Latency
- **Workload Profiles**:
  - Pure barrier reads (`AESGCMBarrier.Get`) and barrier writes (`AESGCMBarrier.Put`).
  - Cache hit scenarios (100% cache hits) vs cache miss scenarios (100% miss).
  - Storage contention under 100 parallel write workers targeting random vs identical keys.
- **Existing Test Coverage**:
  - `internal/physical/crosstest/cross_test.go`
  - `internal/vault/barrier/aes_gcm_test.go`
- **Acceptance Criteria**:
  - Barrier memory allocation: 0 B/op heap allocation on steady-state writes using buffer pooling.
  - Cache contention: < 5% CPU time spent in mutex wait in `sdk/physical/cache.go`.

---

### Scenario D: Integrated Storage / Raft Consensus
- **Cluster Topologies**:
  - Single-node OpenBao instance.
  - 3-node HA Raft cluster.
  - 5-node HA Raft cluster.
- **Workload Profiles**:
  - Linear write load to quantify consensus commit pipeline.
  - Raft leader failover under sustained write pressure (measuring election and recovery time).
  - Follower replication lag measurement during 10,000 writes/sec bursts.
- **Existing Test Coverage**:
  - `internal/physical/raft/raft_test.go` (`BenchmarkDB_Puts`, `BenchmarkDB_Snapshot`)
  - `internal/vault/external_tests/raft/raft_test.go` (`BenchmarkRaft_SingleNode`)
- **Acceptance Criteria**:
  - Single-node Raft write throughput > 12,000 ops/sec with group commit batching.
  - 3-node cluster write throughput > 8,000 ops/sec over 1Gbps / <1ms network.
  - Leader failover election and barrier re-activation completes within < 2 seconds.

---

### Scenario E: Authentication, Tokens and Policy Evaluation
- **Workload Profiles**:
  - High-volume Service Token creation (`POST /v1/auth/token/create`).
  - Batch Token creation and validation (`TokenTypeBatch`).
  - High-concurrency AppRole logins (`POST /v1/auth/approle/login`).
  - Policy evaluation throughput with varying token policy counts (1, 10, 50, 100 policies).
- **Existing Test Coverage**:
  - `internal/vault/token_store_create_bench_test.go` (`BenchmarkTokenStore_HandleCreateCommon`)
  - `internal/vault/token_store_test.go` (`BenchmarkTokenStore_RevokeTree`)
- **Acceptance Criteria**:
  - Batch Token issuance throughput > 30,000 tokens/sec.
  - Service Token login latency reduced by > 50% via atomic lease persistence.
  - Policy evaluation latency < 10 microseconds for tokens with 50 rules.

---

### Scenario F: HSM & PKCS#11 Integration
- **Workload Profiles**:
  - Comparison of software cryptography baseline against PKCS#11 KMS plugin.
  - Concurrent sign and unseal operations across 1 to 64 parallel threads.
  - Evaluation of session pooling and thread contention on PKCS#11 slot handles.
- **Failure Mode Profiles**:
  - PKCS#11 session exhaustion simulation (`CKR_DEVICE_MEMORY` / `CKR_MAX_SESSIONS`).
  - Graceful queuing and backpressure without server panic or connection termination.
- **Acceptance Criteria**:
  - Zero session leaks over 100,000 operations.
  - Bounded concurrency queuing preventing HSM device saturation.

---

### Scenario G: Long-Running Stability & Sustained Traffic
- **Workload Profile**:
  - 24-hour continuous load test at 70% peak capacity (e.g., 5,000 req/sec mixed traffic).
  - Background lease lifecycle: 500,000 active leases with continuous expiration and renewal.
- **Observed Metrics**:
  - Heap memory stability (checking for memory leaks).
  - Goroutine count stability (checking for dangling goroutines).
  - GC pause frequency and duration (`runtime.ReadMemStats`).
  - BoltDB / PebbleDB disk file growth and compaction efficiency.
- **Acceptance Criteria**:
  - Zero continuous memory growth; heap residency returns to baseline post-traffic.
  - Maximum p99 latency degradation < 15% after 24 hours of sustained execution.

---

## 4. Benchmark Execution Command Reference

```bash
# 1. Run Transit batch encryption microbenchmarks
go test -v ./internal/builtin/logical/transit/ -bench=BenchmarkTransit_Batch -benchmem -count=10 > transit_bench.txt

# 2. Run Raft storage write microbenchmarks
go test -v ./internal/physical/raft/ -bench=BenchmarkDB_Puts -benchmem -count=10 > raft_bench.txt

# 3. Run Token store creation microbenchmarks
go test -v ./internal/vault/ -bench=BenchmarkTokenStore_HandleCreateCommon -benchmem -count=10 > token_bench.txt

# 4. Statistically compare benchmark changes
benchstat baseline.txt optimized.txt
```

---
