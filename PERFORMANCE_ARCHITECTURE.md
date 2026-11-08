# OpenBao Performance Architecture: Current vs. Proposed

This document analyzes the structural execution paths of OpenBao, maps out identified architectural bottlenecks, and defines the proposed high-performance, enterprise-grade architecture.

---

## 1. Current Architecture & Execution Bottlenecks

```
                                  CURRENT ARCHITECTURE PIPELINE

  [ HTTP Client Request ]
             │
             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 1. HTTP Listener & Transport (internal/http/handler.go)│
  │    - TLS Handshake & TCP Socket Ingress                │
  │    - Standby Node Forwarding Check                     │
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 2. Core Request Handling (internal/vault/request_*)    │
  │    - Namespace Resolution                              │
  │    - Longest-Prefix Mount Router Lookup                │
  │    - CheckToken: Barrier Read + JSON Deserialization   │ ◄─── BOTTLENECK: Storage Read &
  │    - ACL Policy Matching: Linear Glob Traversal        │      JSON Parsing per Request
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 3. Synchronous Audit Logging (internal/vault/audit_*)  │
  │    - Formatter & Salt HMAC Generation                  │ ◄─── BOTTLENECK: Blocks calling goroutine
  │    - Serial Iteration over Audit Backends              │      on fileLock & disk WriteTo
  │    - File Lock Contention in file/backend.go           │
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 4. Backend Execution (e.g. Transit, KV, PKI)          │
  │    - Transit Batch: Sequential Single-Threaded Loop    │ ◄─── BOTTLENECK: 1 Core utilized for
  │    - Key Policy Mutex Serialization                    │      entire 1,000-item batch
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 5. Cryptographic Barrier (internal/vault/barrier/)     │
  │    - AESGCMBarrier.Put / Get                           │
  │    - Non-pooled Buffer Allocations (make([]byte))      │ ◄─── BOTTLENECK: High GC pressure
  │    - Read-Lock Contention Across Physical I/O          │
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 6. Storage Layer & Integrated Storage (Raft)          │
  │    - TwoQueueCache Global Lock Contention              │
  │    - Discrete Unbatched Raft Proposals (applyLog)      │ ◄─── BOTTLENECK: Single fsync per write;
  │    - bbolt Global DB Update Mutex                      │      1k-3k writes/sec disk cap
  └────────────────────────────────────────────────────────┘
```

---

## 2. Proposed Optimized Architecture

```
                                  PROPOSED OPTIMIZED ARCHITECTURE

  [ HTTP Client Request ]
             │
             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 1. Zero-Allocation Transport Pipeline                  │
  │    - Flat Request Context Structures                   │
  │    - Local Read Handling on Standby Nodes (v2.5+)      │
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 2. Optimized Core Dispatch                             │
  │    - Radix-Tree Precompiled Policy ACL Evaluator       │
  │    - Ephemeral Token Verification Cache (5s TTL)       │ ◄─── OPTIMIZATION: Bypasses 80% of
  │    - Native Batch Token Stateless Validation           │      barrier reads for active sessions
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 3. Pipelined Non-Blocking Audit Subsystem              │
  │    - High-Performance Ring Buffer / Worker Flusher     │ ◄─── OPTIMIZATION: Zero API thread blocking;
  │    - Batched File I/O with Fail-Closed Backpressure    │      bounded disk write coalescing
  │    - Pooled Audit Formatter Buffers                    │
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 4. Parallelized Backend Execution Engine               │
  │    - Transit Batch Parallel Dispatch (GOMAXPROCS)      │ ◄─── OPTIMIZATION: 10x-30x speedup via
  │    - Lock-Free Key Policy Access (Copy-on-Write)       │      concurrent worker fan-out
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 5. Pooled Cryptographic Barrier                        │
  │    - sync.Pool Buffer Recycler for AEAD Sealing        │ ◄─── OPTIMIZATION: 0 allocs/op during steady
  │    - Atomic Keyring Term Checks (Narrow Lock Scope)    │      state encryption & decryption
  └──────────────────────────┬─────────────────────────────┘
                             │
                             ▼
  ┌────────────────────────────────────────────────────────┐
  │ 6. High-Throughput Storage & Consensus Subsystem       │
  │    - Partitioned Sharded Physical Cache                │
  │    - Adaptive Group Commit Proposal Pipeline           │ ◄─── OPTIMIZATION: Coalesces 50-200 writes
  │    - PebbleDB Concurrent LSM Storage Integration       │      into single Raft fsync commit
  └────────────────────────────────────────────────────────┘
```

---

## 3. Critical Path Bottleneck Mitigation Breakdown

### 3.1 Transit Secrets Engine Parallelization
- **Current Flow**:
  1. Client sends 1,000 items in `batch_input`.
  2. Single goroutine loops through indices 0 to 999.
  3. Total duration = 1,000 * 30µs = ~30,000µs (30ms).
- **Optimized Flow**:
  1. Client sends 1,000 items in `batch_input`.
  2. Handler partitions items into chunks across `runtime.GOMAXPROCS(0)` worker goroutines.
  3. All CPU cores execute AES-NI instructions concurrently.
  4. Total duration on 16 cores = ~2ms (15x latency reduction).

### 3.2 Raft Consensus Group Commits
- **Current Flow**:
  1. 100 concurrent requests each call `RaftBackend.Put`.
  2. 100 separate `raft.Apply` proposals are serialized.
  3. 100 separate disk `fsync` calls are executed sequentially by `bbolt`.
  4. Total duration = 100 * 2ms = 200ms.
- **Optimized Flow**:
  1. 100 concurrent requests push write operations into a thread-safe batching channel.
  2. A dedicated proposal loop drains the channel (up to 256 operations or 2ms max delay).
  3. All operations are packed into a single multi-op `LogData` proposal.
  4. 1 single `raft.Apply` and 1 single `fsync` commits all 100 writes.
  5. Total duration = 2-3ms (70x write throughput increase).

### 3.3 Audit Logging Pipelining
- **Current Flow**:
  1. Calling API goroutine formats JSON audit log.
  2. Calling goroutine acquires `b.fileLock.Lock()`.
  3. Calling goroutine calls `writer.WriteTo` blocking on kernel I/O flush.
  4. Mutex is held while other API threads queue up.
- **Optimized Flow**:
  1. Calling goroutine writes formatted entry to an in-memory lock-free ring buffer.
  2. Background worker consumes ring buffer in bulk and executes large 64KB vector writes to disk.
  3. Calling goroutine returns response immediately, protected by watermark backpressure to preserve fail-closed guarantees.

---
