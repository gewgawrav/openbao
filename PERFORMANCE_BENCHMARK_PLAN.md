# OpenBao Performance Benchmark Plan & Coverage Analysis :robot:

## Objectives

This document evaluates the existing benchmark suite in OpenBao, identifies missing coverage gaps across mission-critical subsystems, provides exact commands for executing safe targeted benchmarks, and outlines synthetic benchmark designs for ongoing performance regression prevention.

---

## Current Benchmark Coverage & Execution Results

Safe microbenchmarks were executed directly in the repository environment. All results are captured below with exact metrics.

### 1. Transit Secrets Engine (`internal/builtin/logical/transit/`)
- **Benchmark Command**:
  ```bash
  go test -run=^$ -bench=BenchmarkTransit_Batch -benchmem -benchtime=50x ./internal/builtin/logical/transit/
  ```
- **Results**:
  - `BenchmarkTransit_BatchDecryption1`: 11,087 ns/op, 3,565 B/op, 39 allocs/op
  - `BenchmarkTransit_BatchDecryption10`: 37,369 ns/op, 17,954 B/op, 102 allocs/op
  - `BenchmarkTransit_BatchDecryption50`: 167,269 ns/op, 83,462 B/op, 382 allocs/op
  - `BenchmarkTransit_BatchDecryption100`: 219,147 ns/op, 163,679 B/op, 732 allocs/op
  - `BenchmarkTransit_BatchDecryption1000`: 2,374,423 ns/op, 1,615,830 B/op, 7,935 allocs/op
  - `BenchmarkTransit_BatchDecryption10000`: 19,160,869 ns/op, 16,113,395 B/op, 79,943 allocs/op
  - `BenchmarkTransit_BatchEncryption1`: 13,647 ns/op, 5,066 B/op, 46 allocs/op
  - `BenchmarkTransit_BatchEncryption10`: 42,742 ns/op, 21,045 B/op, 127 allocs/op
  - `BenchmarkTransit_BatchEncryption50`: 203,355 ns/op, 93,517 B/op, 487 allocs/op
  - `BenchmarkTransit_BatchEncryption100`: 479,760 ns/op, 183,850 B/op, 938 allocs/op
  - `BenchmarkTransit_BatchEncryption1000`: 3,375,746 ns/op, 1,795,642 B/op, 9,941 allocs/op
  - `BenchmarkTransit_BatchEncryption10000`: 35,171,681 ns/op, 17,883,395 B/op, 99,950 allocs/op

### 2. Certutil Certificate Operations (`sdk/helper/certutil/`)
- **Benchmark Command**:
  ```bash
  (cd sdk && go test -run=^$ -bench=BenchmarkCertBundleParsing -benchmem -benchtime=50x ./helper/certutil/)
  ```
- **Results**:
  - `BenchmarkCertBundleParsing`: 6,955,456 ns/op, 384,112 B/op, 2,814 allocs/op

### 3. Keysutil Cryptographic Primitives (`sdk/helper/keysutil/`)
- **Benchmark Command**:
  ```bash
  (cd sdk && go test -run=^$ -bench=. -benchmem -benchtime=50x ./helper/keysutil/)
  ```
- **Results**:
  - `BenchmarkSymmetric`: 4,338 ns/op, 3,002 B/op, 12 allocs/op
  - `BenchmarkEncrytedKeyStorage_Put`: 27,046 ns/op, 11,837 B/op, 103 allocs/op
  - `BenchmarkEncrytedKeyStorage_List`: 8,567,167 ns/op, 1,796,631 B/op, 6,676 allocs/op

### 4. StoragePacker Bucket Compaction (`internal/helper/storagepacker/`)
- **Benchmark Command**:
  ```bash
  go test -run=^$ -bench=BenchmarkStoragePacker -benchmem -benchtime=50x ./internal/helper/storagepacker/
  ```
- **Results**:
  - `BenchmarkStoragePacker`: 452,136 ns/op, 757,090 B/op, 117 allocs/op

### 5. Audit Logging File Backend (`internal/builtin/audit/file/`)
- **Benchmark Command**:
  ```bash
  go test -run=^$ -bench=BenchmarkAuditFile_request -benchmem -benchtime=50x ./internal/builtin/audit/file/
  ```
- **Results**:
  - `BenchmarkAuditFile_request`: 13,147 ns/op, 5,550 B/op, 36 allocs/op

### 6. Audited Headers Configuration (`internal/vault/`)
- **Benchmark Command**:
  ```bash
  go test -run=^$ -bench=BenchmarkAuditedHeaderConfig_ApplyConfig -benchmem -benchtime=50x ./internal/vault/
  ```
- **Results**:
  - `BenchmarkAuditedHeaderConfig_ApplyConfig`: 1,468 ns/op, 96 B/op, 4 allocs/op

### 7. Namespace Store Resolution (`internal/vault/`)
- **Benchmark Command**:
  ```bash
  go test -run=^$ -bench=BenchmarkNamespaceStore -benchmem -benchtime=50x ./internal/vault/
  ```
- **Results**:
  - `GetNamespace`: 1,621 ns/op, 160 B/op, 3 allocs/op
  - `GetNamespaceByAccessor`: 1,823 ns/op, 160 B/op, 3 allocs/op
  - `GetNamespaceByPath`: 8,764 ns/op, 296 B/op, 6 allocs/op
  - `ModifyNamespaceByPath`: 118,552 ns/op, 4,196 B/op, 50 allocs/op
  - `ListNamespaces_non-recursive`: 6,732 ns/op, 647 B/op, 11 allocs/op
  - `ListNamespaces_recursive`: 32,010 ns/op, 4,583 B/op, 65 allocs/op
  - `ResolveNamespaceFromRequest`: 3,924 ns/op, 458 B/op, 8 allocs/op

### 8. Expiration Manager Lease Restoration (`internal/vault/`)
- **Benchmark Command**:
  ```bash
  go test -run=^$ -bench=BenchmarkExpiration_Restore_InMem -benchmem -benchtime=10x -timeout=60s ./internal/vault/
  ```
- **Results**:
  - `BenchmarkExpiration_Restore_InMem` (100,000 leases): 3,540,352,255 ns/op (~3.54s), 511,486,398 B/op (~511MB allocated), 6,703,441 allocs/op

---

## Missing Benchmark Coverage Gaps

The audit identified critical gaps where existing tests only assert correctness without measuring throughput, latency distribution, or memory allocations.

### Gap 1: Core Policy ACL Evaluation & Compilation
- **Deficiency**: `internal/vault/policy/` has zero Go microbenchmarks for `NewACL`, `AllowOperation`, or `Capabilities`. As a result, the performance impact of creating radix trees per request went undetected.
- **Proposed Benchmark**:
  ```go
  func BenchmarkACLEvaluation(b *testing.B) {
      policies := createTestPolicies(5, 50) // 5 policies, 50 paths each
      ctx := namespace.ContextWithNamespace(context.Background(), namespace.RootNamespace)
      b.ResetTimer()
      b.ReportAllocs()
      for i := 0; i < b.N; i++ {
          acl, _ := NewACL(ctx, policies)
          _ = acl.AllowOperation(ctx, &logical.Request{Path: "secret/data/foo", Operation: logical.ReadOperation}, false)
      }
  }
  ```

### Gap 2: AES-GCM Security Barrier Read/Write
- **Deficiency**: `internal/vault/barrier/` lacks microbenchmarks measuring barrier encryption, decryption, term AEAD caching, and allocation profiles for various payload sizes (64B, 1KB, 64KB, 1MB).
- **Proposed Benchmark**:
  ```go
  func BenchmarkBarrier_GetPut(b *testing.B) {
      backend := inmem.NewInmem(nil, nil)
      bar := NewAESGCMBarrier(backend, nil)
      bar.Initialize(context.Background(), key, nil)
      bar.Unseal(context.Background(), key)
      entry := &logical.StorageEntry{Key: "test", Value: make([]byte, 1024)}
      b.ResetTimer()
      b.ReportAllocs()
      b.RunParallel(func(pb *testing.PB) {
          for pb.Next() {
              _ = bar.Put(context.Background(), entry)
              _, _ = bar.Get(context.Background(), "test")
          }
      })
  }
  ```

### Gap 3: PKI Certificate Generation & Issuance
- **Deficiency**: PKI tests do not benchmark certificate issuance throughput (`pathIssueSignCert`) comparing RSA 2048, RSA 4096, ECDSA P-256, and Ed25519, nor do they benchmark `no_store=true` vs `no_store=false`.
- **Proposed Benchmark**:
  ```go
  func BenchmarkPKI_IssueCert_ECDSA(b *testing.B) {
      // Setup PKI backend and CA issuer with ECDSA P-256
      b.ResetTimer()
      b.ReportAllocs()
      for i := 0; i < b.N; i++ {
          _, _ = bEnd.HandleRequest(ctx, &logical.Request{
              Path: "issue/test-role",
              Operation: logical.UpdateOperation,
              Data: map[string]any{"common_name": "test.example.com"},
          })
      }
  }
  ```

### Gap 4: Concurrent Token Creation & Revocation
- **Deficiency**: `BenchmarkTokenStore_HandleCreateCommon` is single-threaded and does not stress concurrent contention across `tokenLocks` and `ExpirationManager.pendingLock`.
- **Proposed Benchmark**:
  Add `BenchmarkTokenStore_Create_Parallel` using `b.RunParallel` across 8, 16, and 32 worker goroutines.

### Gap 5: Raft Snapshot Creation Under Write Load
- **Deficiency**: No benchmark measures Raft snapshot generation latency while active writes are being committed to BoltDB.
- **Proposed Benchmark**:
  Add `BenchmarkRaft_Snapshot_ConcurrentWrites` in `internal/physical/raft/`.

---

## Continuous Performance Regression Plan

1. **Automated CI Microbenchmark Tracking**:
   - Integrate `benchstat` into GitHub Actions to compare pull requests against `main` for critical packages: `internal/vault/barrier`, `internal/builtin/logical/transit`, `internal/vault/policy`, and `internal/vault`.
   - Fail CI if memory allocations (`allocs/op`) or latency (`ns/op`) regress by more than 5%.

2. **Nightly Macro / Load Testing Pipeline**:
   - Deploy standalone 3-node OpenBao Raft clusters in an isolated test environment.
   - Run standard automated load profiles using tools like `ghz` (for gRPC) or `wrk2` / `vegeta` (for HTTP):
     - 10,000 req/sec KV v2 reads (cache hit rate > 99%).
     - 2,000 req/sec Token creations (evaluating Raft write latency).
     - 5,000 req/sec Transit batch encryption.
     - 500 req/sec PKI certificate issuances.
   - Record P50, P90, P95, P99, and P99.9 latencies.
