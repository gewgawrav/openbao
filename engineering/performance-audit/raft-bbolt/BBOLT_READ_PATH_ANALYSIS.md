# bbolt Internals & Read Path Performance Audit 🤖

## Executive Summary
This report analyzes bbolt internals, lookup complexity, page cache behavior, and concurrency scaling within OpenBao's Integrated Storage implementation. We evaluate the performance of frequent Subject Key lookups across cardinalities of 10K, 100K, 1M, and 10M records under Cloakit Privacy Vault's 95% read / 5% write workload.

---

## 1. Bucket and Key Layout in `vault.db`

### 1.1 Bucket Structure
- **Source**: `internal/physical/raft/fsm.go:49-51, 226-235`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao employs a strictly flat bucket model inside `vault.db`:
  - `data` bucket: Houses 100% of physical storage entries.
  - `config` bucket: Houses Raft consensus and node suffrage metadata.
- Rather than leveraging nested bbolt sub-buckets (e.g. `tx.Bucket("transit").Bucket("keys")`), OpenBao stores full path keys as raw byte slices in the single `data` bucket:
  - Example: `sys/mounts`, `transit/keys/subject-key-001`, `auth/token/id/...`

### 1.2 B-Tree Traversal Complexity
- bbolt implements a B+ tree with page sizes equal to the OS page size (4,096 bytes).
- A B+ tree node page stores key-pointer elements. For average key sizes of ~64 bytes and 16-byte page element metadata, a branch page holds approximately:
  $$B pprox rac{4096 - 16}{80} pprox 50 	ext{ keys per branch page}$$
- Given fanout $B pprox 50$, the tree depth $D$ scales logarithmically with entry count $N$:
  $$D pprox \lceil \log_{50}(N) ceil$$

| Key Cardinality ($N$) | Tree Depth ($D$) | Page Reads per Lookup | Binary Searches per Page | Total Key Comparisons |
| :--- | :--- | :--- | :--- | :--- |
| **10,000** | 3 levels | 3 pages | $\log_2(50) pprox 6$ | ~18 comparisons |
| **100,000** | 3-4 levels | 3-4 pages | $\log_2(50) pprox 6$ | ~24 comparisons |
| **1,000,000** | 4 levels | 4 pages | $\log_2(50) pprox 6$ | ~24-28 comparisons |
| **10,000,000** | 4-5 levels | 5 pages | $\log_2(50) pprox 6$ | ~30 comparisons |

---

## 2. Page Reads, Memory Mapping (mmap) & Zero-Copy Semantics

### 2.1 Mmap Behavior & Page Cache
- **Source**: `internal/physical/raft/raft.go:2179-2212` and `internal/physical/raft/bolt_linux.go:19-40`.
- **Classification**: `SOURCE-CONFIRMED`.
- On 64-bit platforms, OpenBao opens `vault.db` with `InitialMmapSize = 100GB` (`vars_64bit.go:8`).
- On Linux, if system RAM exceeds database file size (`bolt_linux.go:35`), it requests `unix.MAP_POPULATE`, faulting all database pages into the OS page cache upfront.
- **Cache Hit Dynamics**:
  - When pages reside in OS page cache (or RAM), B-tree navigation incurs **zero disk I/O syscalls** (`read()` / `pread()`). The CPU traverses virtual memory pointers directly.
- **Cache Miss Dynamics (Cold Cache)**:
  - If database size exceeds RAM or after a restart, page traversal generates hard page faults (`SIGBUS`/major page faults), causing synchronous 4KB disk reads.

### 2.2 Memory Copy Overhead (`valCopy`)
- **Source**: `internal/physical/raft/fsm.go:517-537`.
- **Classification**: `SOURCE-CONFIRMED`.
- Inside `f.Get`:
  ```go
  err := f.db.View(func(tx *bolt.Tx) error {
      value := tx.Bucket(dataBucketName).Get([]byte(path))
      if value != nil {
          found = true
          valCopy = make([]byte, len(value))
          copy(valCopy, value)
      }
      return nil
  })
  ```
- **Analysis**: bbolt mmap memory is valid only during the lifetime of the read-only transaction (`*bolt.Tx`). Because `f.Get` must return a `*physical.Entry` that survives transaction termination, it **must allocate a new heap byte slice and copy the bytes**.
- For 100,000 reads/sec with 1KB values, this generates $100	ext{ MB/sec}$ of continuous Go heap allocations, triggering garbage collector pacing and CPU cycles.

---

## 3. Read Concurrency & Reader/Writer Contention

### 3.1 Reader Concurrency (`db.View`)
- In bbolt, read-only transactions (`db.View` / `db.Begin(false)`) do **not** take a global write lock. Multiple goroutines can read concurrently using shared memory mapping.
- However, OpenBao wraps reads with `b.permitPool.Acquire()` (`raft.go:1724`), capping concurrent physical backend operations at **128 parallel goroutines** (`physical.DefaultParallelOperations`).

### 3.2 Reader Retention & Freelist Starvation
- When a bbolt write transaction modifies pages, it places old pages onto the freelist.
- These freed pages **cannot be reused** until all read-only transactions started prior to that write have closed!
- If a long-running read transaction (such as a paginated `ListPage` or a slow backup) remains open, subsequent write transactions cannot reclaim free pages. The database file grows uncontrollably on disk.

---

## 4. Subject Key Lookup Scaling Evaluation (10K to 10M)

1. **At 10K - 100K Keys**:
   - `vault.db` is small (~50MB - 500MB). Fits entirely in RAM.
   - Lookups take 3 page traversals (<1 microsecond in memory).
2. **At 1M Keys**:
   - `vault.db` is ~3GB - 6GB. Tree depth is 4 levels.
   - In-memory lookups remain fast (~1-2 microseconds).
3. **At 10M Keys**:
   - `vault.db` is ~30GB - 60GB. Tree depth reaches 4-5 levels.
   - If RAM is constrained (e.g., 16GB container), leaf pages drop out of the OS page cache. Lookups degrade from microseconds to milliseconds due to disk I/O page faults.

### Recommendation on Custom In-Memory Index
- **Verdict**: Retain the existing flat bucket layout, but **do not build a separate in-memory disk index**.
- Transit engine's existing `TransitLRU` / `TransitSyncMap` (`sdk/helper/keysutil/`) already provides an effective in-memory cache for Subject Keys. Adding an intermediate index would duplicate memory overhead without improving mmap read performance.
