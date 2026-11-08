# Detailed Performance Audit Findings :robot:

This document enumerates the performance engineering findings identified through repository-local static analysis, source code tracing, and benchmark execution.

---

## Finding FINDING-PERF-01: I/O and Storage Write Amplification in TokenStore Service Token Creation

- **Classification:** `BENCHMARK-CONFIRMED` / `SOURCE-CONFIRMED`
- **File Path:** `internal/vault/token_store.go`
- **Function/Method Name:** `TokenStore.handleCreateCommon`, `TokenStore.create`, `TokenStore.createAccessor`, `TokenStore.storeCommon`
- **Line Numbers:** `internal/vault/token_store.go:1200-1620`, `2828-3170`

### Relevant Code Behaviour
During standard service token creation (`handleCreateCommon` -> `create`), the following sequence occurs:
1. `createAccessor` creates a random base62 accessor, derives a salt (`ts.SaltID`), marshals an `accessorEntry` via `jsonutil.EncodeJSON`, and executes `ts.accessorView(tokenNS).Put(ctx, le)` (lines 1205-1250).
2. If `persistToken` is true, `create` calls `storeCommon(ctx, entry, true)` (line 1342).
3. `storeCommon` (lines 1540-1615) calculates the primary salted ID (`ts.SaltID`), marshals the entry to JSON (`json.Marshal`), and:
   - If the token has a parent (`entry.Parent != ""`), it performs a parent lookup `ts.Lookup`, derives the parent salt ID, constructs a parent index entry path `parentSaltedID + "/" + saltedID`, and executes `ts.parentView(parentNS).Put(ctx, le)` (lines 1555-1595).
   - Writes the primary token entry: `ts.idView(tokenNS).Put(ctx, le)` (lines 1600-1615).

### Technical Explanation
Creating a single child service token issues at minimum:
1. 1 read: `ts.Lookup` for the parent token (barrier decryption + storage read).
2. 1 write: Accessor index (`accessor/` view) via barrier encryption + storage Put.
3. 1 write: Parent index (`parent/` view) via barrier encryption + storage Put.
4. 1 write: Primary token storage (`id/` view) via barrier encryption + storage Put.

Every Put operation traverses the core barrier, performing AES-GCM encryption and invoking the physical backend (e.g. Raft FSM apply or database transaction). In a storage backend like Raft or remote RDBMS, this causes 3 sequential transactional fsyncs/network round-trips per token creation.

### Multi-Dimensional Performance Impact
- **Latency:** High amplification on synchronous token creation path (accumulates multiple barrier encrypt calls and 3 sequential storage Puts).
- **Throughput:** Constrains max token creation rate to 1/3 of raw backend IOPs capability.
- **CPU:** Repetitive JSON marshaling and AES-GCM encryption across 3 storage entries per token.
- **Memory:** Generates 386 allocations and ~38.5 KB per token creation operation.
- **Storage Operations:** 3:1 physical storage write amplification ratio for every service token created.
- **Network Operations:** In clustered/Raft configurations, results in 3 separate Raft log replication rounds unless batched at transport.
- **Concurrency:** Storage barrier serialization bottleneck under concurrent login bursts.
- **Scalability:** Degrades horizontally as cluster size increases due to Raft commit overhead on each Put.

### Proposed Improvement
Implement transactional batching or bundled atomic multi-put for token writes when backed by a transactional physical backend (`physical.Transactional`). Combine the primary ID, accessor index, and parent index commits into a single atomic storage transaction.

### Confidence Level
`HIGH` (Confirmed by benchmark `BenchmarkTokenStore_HandleCreateCommon`: 213,242 ns/op, 386 allocs/op, 38,490 B/op).

### Production / Runtime Unknowns & Validation Recommendation
- **Unknown:** Actual disk fsync latency on production block storage (EBS, NVMe) and Raft FSM commit latency under network jitter.
- **Future Validation Recommendation:** Benchmark token issuance under clustered Raft topologies with varying disk IOPs and commit batch sizes.

---

## Finding FINDING-PERF-02: Sequential Synchronous Iteration in Transit Batch Encryption and Decryption

- **Classification:** `BENCHMARK-CONFIRMED` / `SOURCE-CONFIRMED`
- **File Path:** `internal/builtin/logical/transit/path_encrypt.go`, `internal/builtin/logical/transit/path_decrypt.go`
- **Function/Method Name:** `backend.pathEncryptWrite`, `backend.pathDecryptWrite`
- **Line Numbers:** `internal/builtin/logical/transit/path_encrypt.go:300-475`, `internal/builtin/logical/transit/path_decrypt.go:100-240`

### Relevant Code Behaviour
In `pathEncryptWrite`, when a caller supplies `batch_input` containing $N$ items:
1. The entire `batch_input` is decoded into `batchInputItems` slice (lines 300-330).
2. A single policy read lock is acquired: `p, _, err := b.GetPolicy(...)` (lines 410-425).
3. The function iterates sequentially over all items:
   ```go
   for i, item := range batchInputItems {
       // ...
       ciphertext, err := p.EncryptWithFactory(item.KeyVersion, item.DecodedContext, nil, item.Plaintext, factories...)
       // ...
   }
   ```
4. Each cryptographic operation (`p.EncryptWithFactory`) runs synchronously on the calling goroutine.

### Technical Explanation
While holding `p.Unlock()` via deferred unlock across the entire function scope, CPU-intensive cryptographic symmetric cipher computations (AES-GCM, ChaCha20-Poly1305) execute in a single-threaded loop on one core. Multi-core parallelism is unutilized for large batches ($N=1000$ to $10,000$). Furthermore, holding the policy lock for the duration of the long sequential loop blocks conflicting policy operations (e.g., rotation, key trimming, config changes).

### Multi-Dimensional Performance Impact
- **Latency:** Scales linearly $O(N)$ with batch size. For $N=10,000$, latency reaches ~34-35 ms per batch request.
- **Throughput:** Restricts batch throughput to the single-core CPU frequency instead of leveraging multicore host parallelism.
- **CPU:** Underutilizes multi-core CPUs during large batch operations; single CPU core maxes out at 100% while others sit idle.
- **Memory:** Allocations scale linearly (for $N=10,000$: 99,950 allocs/op and ~17.8 MB heap allocation).
- **Concurrency:** Prolonged hold of the policy read lock degrades concurrency when key rotations or updates are attempted.
- **Scalability:** Horizontal scaling of large batches requires spinning up multiple independent client requests rather than fully saturating node CPUs with single batch requests.

### Proposed Improvement
Introduce worker pool parallelism (e.g. `sync.WaitGroup` or bounded chunked concurrency) for batch sizes exceeding a threshold (e.g. $N > 32$), chunking the work across `runtime.GOMAXPROCS` worker goroutines while reusing buffers.

### Confidence Level
`HIGH` (Confirmed by benchmarks `BenchmarkTransit_BatchEncryption` and `BenchmarkTransit_BatchDecryption` where latency grows from 15.9 µs ($N=1$) to 35.3 ms ($N=10,000$)).

### Production / Runtime Unknowns & Validation Recommendation
- **Unknown:** Overhead of goroutine scheduling and channel dispatch on small batch sizes ($N < 10$).
- **Future Validation Recommendation:** Profile batch encryption with chunked worker pools across varying GOMAXPROCS and payload sizes to identify the crossover threshold where concurrency overhead is amortized.

---

## Finding FINDING-PERF-03: Excessive Heap Allocations in Blake2b Key Sharding Hash Function

- **Classification:** `BENCHMARK-CONFIRMED` / `SOURCE-CONFIRMED`
- **File Path:** `sdk/helper/cryptoutil/cryptoutil.go`, `sdk/helper/locksutil/sharded.go`
- **Function/Method Name:** `cryptoutil.Blake2b256Hash`, `locksutil.LockIndexForKey`
- **Line Numbers:** `sdk/helper/cryptoutil/cryptoutil.go:8-14`, `sdk/helper/locksutil/sharded.go:50-60`

### Relevant Code Behaviour
`locksutil.LockIndexForKey` computes the lock shard index (0 to 255) for any lock key:
```go
func LockIndexForKey(key string) uint8 {
    return uint8(cryptoutil.Blake2b256Hash(key)[0])
}
```
`cryptoutil.Blake2b256Hash` is implemented as:
```go
func Blake2b256Hash(key string) []byte {
    hf, _ := blake2b.New256(nil)
    hf.Write([]byte(key))
    return hf.Sum(nil)
}
```

### Technical Explanation
Every time `LockIndexForKey` is called:
1. `blake2b.New256(nil)` allocates a new `digest` struct on the heap.
2. `[]byte(key)` allocates a byte slice copy of the input string.
3. `hf.Sum(nil)` allocates a 32-byte slice for the result.
4. `LockIndexForKey` extracts only the first byte `[0]` and discards the entire 32-byte slice, hashing context, and string copy.

This introduces 3 heap allocations totaling 464 bytes on every single key-lock lookup. Because `locksutil` is on the critical hot path for virtually all transit operations, token revocations, barrier views, and approle operations, this generates substantial garbage collection churn.

Using Go's direct `blake2b.Sum256([]byte(key))` allocates only 48 bytes (1 allocation for the byte slice) or 0 allocations if an internal fast non-allocating hash is used.

### Multi-Dimensional Performance Impact
- **Latency:** ~431 ns per lock lookup vs ~252 ns for direct `Sum256` (71% latency overhead).
- **Throughput:** Reduces maximum lock acquisition throughput under heavy concurrent load.
- **CPU:** Excessive memory allocator and garbage collector cycles during lock acquisition bursts.
- **Memory:** 464 bytes allocated per lock index computation across all lock-guarded subsystems.
- **Concurrency:** Elevated GC pressure causes stop-the-world pauses affecting all concurrent routines.

### Proposed Improvement
Replace `blake2b.New256` in `Blake2b256Hash` with `blake2b.Sum256` or optimize `LockIndexForKey` to use a stack-allocated digest or zero-allocation hashing algorithm (e.g. xxHash or siphash) for lock distribution.

### Confidence Level
`HIGH` (Confirmed by benchmark `lock_bench2.go`: Current Blake2b256Hash requires 431 ns/op, 3 allocs/op, 464 B/op vs 252 ns/op, 1 alloc/op, 48 B/op for `blake2b.Sum256`).

### Production / Runtime Unknowns & Validation Recommendation
- **Unknown:** Total percentage of GC CPU time attributable solely to `LockIndexForKey` across a full multi-tenant production cluster.
- **Future Validation Recommendation:** Capture CPU and memory allocation profiles (`pprof`) during high-concurrency read/write benchmarks.

---

## Finding FINDING-PERF-04: JSON Marshal/Unmarshal Serialization Cycle for Deep Map Copying in Audit Logging

- **Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`
- **File Path:** `internal/audit/hashstructure.go`
- **Function/Method Name:** `getUnmarshaledCopy`
- **Line Numbers:** `internal/audit/hashstructure.go:73`, `121`, `164-174`

### Relevant Code Behaviour
In `HashRequest` and `HashResponse`, when audit logging processes request/response payloads:
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

### Technical Explanation
To deep-copy the `req.Data` and `resp.Data` maps so that sensitive fields can be hashed in place without mutating the original request/response structures, OpenBao marshals the entire map into a JSON byte slice and immediately unmarshals it back into a new generic `map[string]any`.

This round-trip JSON serialization cycle:
1. Traverses reflection structures twice.
2. Allocates intermediate JSON byte buffers.
3. Decodes all numbers as `float64` (requiring downstream fixups).
4. Creates dozens of heap allocations per request for even small dictionaries.

Benchmark simulation of `getUnmarshaledCopy` on a basic 4-key map requires **9,233 ns/op, 45 allocations/op, and 1,425 bytes/op**.

### Multi-Dimensional Performance Impact
- **Latency:** Adds ~10–50 µs of synchronous serialization latency per logged request/response.
- **Throughput:** Drops total audit pipeline throughput, directly throttling the main request execution path when audit logging is enabled (OpenBao rejects operations if audit sinks fail or block).
- **CPU:** Significant CPU time spent in JSON reflection and parsing.
- **Memory:** 45+ heap allocations per audit log entry; causes severe heap fragmentation under sustained traffic.
- **Concurrency:** Request goroutines are delayed waiting for audit formatting before returning responses to callers.

### Proposed Improvement
Replace JSON serialization round-tripping with a dedicated recursive map cloning function that directly duplicates maps, slices, and primitive scalar values without string conversions or JSON reflection.

### Confidence Level
`HIGH` (Established directly from source code and confirmed by isolated benchmark `audit_bench.go`).

### Production / Runtime Unknowns & Validation Recommendation
- **Unknown:** Cumulative audit latency when writing to high-latency file or syslog audit backends.
- **Future Validation Recommendation:** Run `vault` with audit logging enabled under `wrk` / `ghz` benchmarks and profile `getUnmarshaledCopy` CPU/heap allocation percentage.

---

## Finding FINDING-PERF-05: Raft Storage Batch Enqueue Bound to max_append_entries Default of 64

- **Classification:** `SOURCE-CONFIRMED`
- **File Path:** `internal/physical/raft/raft.go`
- **Function/Method Name:** `RaftBackend.applyConfigSettings`
- **Line Numbers:** `internal/physical/raft/raft.go:890-915`

### Relevant Code Behaviour
In `internal/physical/raft/raft.go`:
```go
maxAppendEntriesRaw, ok := b.conf["max_append_entries"]
maxAppendEntries := 64
if ok {
    // ...
}
config.MaxAppendEntries = maxAppendEntries
config.BatchApplyCh = true
```

### Technical Explanation
OpenBao enables `config.BatchApplyCh = true`, allowing the Raft library to batch concurrent commit entries into a single Raft log write. However, the default `MaxAppendEntries` is set to 64. Under high concurrent write loads (e.g., hundreds of concurrent token creations or secret writes), Raft batches truncate at 64 entries, forcing additional discrete Raft AppendEntries RPC rounds and BoltDB bbolt disk commits rather than maximizing batch pipeline depth.

### Multi-Dimensional Performance Impact
- **Throughput:** Limits peak write throughput on multi-core servers capable of handling higher write concurrency.
- **Latency:** Under write saturation, requests queue behind multiple Raft commit rounds rather than being absorbed into larger batch flushes.
- **Storage Operations:** More discrete BoltDB disk transactions and fsyncs are committed than necessary when concurrency exceeds 64.
- **Network Operations:** Generates more Raft RPC append packets between cluster nodes.
- **Scalability:** Bottlenecks cluster write scaling on Raft storage.

### Proposed Improvement
Evaluate increasing the default `max_append_entries` or provide dynamic batch sizing guidance in high-throughput documentation.

### Confidence Level
`MEDIUM` (Source-confirmed defaults; actual optimal batch size depends on underlying network latency and NVMe write speeds).

### Production / Runtime Unknowns & Validation Recommendation
- **Unknown:** The exact point of diminishing returns for `max_append_entries` across different cloud network fabrics (e.g. AWS ENA vs bare-metal 10GbE).
- **Future Validation Recommendation:** Benchmark Raft cluster write throughput at 64, 128, 256, and 512 `max_append_entries` under varying concurrency levels.

---

## Finding FINDING-PERF-06: External Hardware Security Module (HSM) Latency and Maximum Concurrency

- **Classification:** `UNKNOWN` / `INFERRED RISK`
- **File Path:** `sdk/helper/keysutil/policy.go`, `internal/vault/seal/`
- **Function/Method Name:** `Policy.EncryptWithFactory`, `ExternalKeyFactory`
- **Line Numbers:** `internal/builtin/logical/transit/path_encrypt.go:420-435`

### Relevant Code Behaviour
When Transit keys or barrier seals are configured with external KMS / PKCS#11 HSM mechanisms (`KeyType_ExternalKey`), cryptographic calls are delegated via `factories` to an external HSM driver.

### Technical Explanation
In an offline repository-only audit, hardware HSM device behavior cannot be observed or measured. HSMs typically exhibit:
- Strict limits on concurrent PKCS#11 sessions.
- Higher network or bus round-trip latency (1–20 ms per cryptographic operation).
- Fixed cryptographic operations per second (e.g., 500 to 5,000 ops/sec).

Because Transit batch encryption processes items sequentially, batch requests against an external HSM will compound HSM round-trip latency linearly ($O(N)$), potentially causing HTTP client timeouts on large batches.

### Multi-Dimensional Performance Impact
- **Latency:** Potentially multi-second request latencies for external HSM operations.
- **Throughput:** Capped by HSM hardware capacity and session concurrency limits.
- **Concurrency:** Thread pool exhaustion if client requests block waiting on HSM responses.
- **Scalability:** Cannot scale horizontally beyond physical HSM device limits without clustering HSM partitions.

### Proposed Improvement
Provide configuration options for concurrent asynchronous HSM batching and connection pooling, along with strict batch size validation for external key types.

### Confidence Level
`LOW` (Explicitly marked `UNKNOWN` because HSM hardware and live latency characteristics cannot be determined from repository contents alone).

### Production / Runtime Unknowns & Validation Recommendation
- **Unknown:** Production HSM vendor latency (e.g., AWS CloudHSM, Thales Luna, Utimaco), maximum sustainable concurrency, and PKCS#11 driver thread safety.
- **Future Validation Recommendation:** Execute load tests against staging environments equipped with actual target HSM hardware to quantify session latency and determine optimal concurrency bounds.
