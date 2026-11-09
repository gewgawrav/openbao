# Alternative Embedded Engines & Storage Architecture Evaluation 🤖

## Executive Summary
This report evaluates alternative storage engines and architectural paradigms to address the bottlenecks identified in OpenBao's current Integrated Storage (Raft + bbolt). We rigorously compare the existing Raft+bbolt architecture against Pebble, RocksDB, BadgerDB, LMDB, and separated in-memory materialized views.

Every option is evaluated for read/write latency potential, throughput, memory consumption, write amplification, recovery complexity, and compatibility with OpenBao's `physical.Backend` and `physical.Transactional` interfaces.

---

## 1. Engine Comparison Matrix

| Storage Engine | Engine Architecture | Read Latency (p99) | Write Latency / Throughput | Concurrency Model | Write Amplification Factor | Crash Recovery Complexity | Compatibility with OpenBao |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Existing bbolt** | B+ Tree (mmap) | **Ultra-Low (<10µs)** for cache hits | **Low (~500 w/s)** (dual fsync, single writer) | Multi-reader, **Single-writer** | High on random writes (page splits) | Low (WAL replay, auto-freelist rebuild) | Native / Default |
| **CockroachDB Pebble** | LSM-tree (Go native) | Low (~50-100µs) (bloom filters + sst block cache) | **High (>20,000 w/s)** (append-only WAL + memtable) | Concurrent readers & writers | Low to Medium (tiered compaction) | Fast (LSM WAL replay) | High (Go native, clean key-value API) |
| **Facebook RocksDB** | LSM-tree (C++) | Low (~50-100µs) | **Ultra-High (>50,000 w/s)** | Concurrent multi-threaded | Low to Medium | Fast (WAL replay) | Low (Requires CGo, complex cross-compilation) |
| **Dgraph BadgerDB** | LSM-tree + WiscKey (Go) | Low-Medium | **High** for large values (key-value separation) | Concurrent multi-threaded | Low for large values; higher for small | Medium (vlog garbage collection) | High (Pure Go) |
| **LMDB** | B+ Tree (mmap, C) | **Ultra-Low (<5µs)** | Medium | Multi-reader, Single-writer | High on random updates | Ultra-Fast (no log needed) | Medium (Requires CGo) |
| **In-Memory Materialized View** | RCU / Concurrent Map | **Sub-Microsecond (<500ns)** | Async update from Raft FSM | Non-blocking read lock | Zero disk overhead for reads | Rebuilt on startup from snapshot/WAL | High (Transparent internal layer) |

---

## 2. In-Depth Architectural Evaluation

### 2.1 Optimizing Existing Raft + bbolt (Recommended Immediate Path)
- **Modifications**:
  1. Increase `permitPool` capacity for read operations from 128 to 1024 or make it configurable via `max_parallel`.
  2. Implement an in-memory hot-key cache in Transit for Subject Keys to eliminate storage-level reads.
  3. Increase `BatchApplyCh` pipeline depth and tune `max_append_entries` (e.g. 256) to maximize batch sizes.
- **Pros**: Zero migration risk, retains proven durability, no data format changes.
- **Cons**: Write path remains bounded by bbolt's single-writer mutex and dual fsyncs.

### 2.2 Pebble (Best Long-Term Distributed Backend)
- **Suitability**: Pebble is CockroachDB's high-performance LSM-tree engine implemented in pure Go.
- **Benefits**:
  - Replaces bbolt's single-writer lock with concurrent memtable writes.
  - Slashes write amplification by writing sequentially to memtables and SSTables.
  - Eliminates the 100GB sparse mmap allocation.
- **Challenges**:
  - Point read latency for cold keys is slightly higher than bbolt's direct mmap pointer dereference ($50\mu	ext{s}$ vs $5\mu	ext{s}$). Requires tuned block cache.

### 2.3 Separate Read-Optimized Materialized View
- **Concept**: Raft log consensus persists state into `raft.db`, while the application state (`vault.db`) maintains a concurrent lockless in-memory cache (e.g. `sync.Map` or Radix tree) of hot Subject Key metadata.
- **Benefits**: Read path achieves sub-microsecond latency without touching bbolt or acquiring permit pools.
