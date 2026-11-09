# Raft Write-Path Performance & Write Amplification Audit 🤖

## Executive Summary
This report analyzes the complete execution path of write operations (`Put`, `Delete`, and transactional `Commit`) in OpenBao's Integrated Storage engine. Every write operation undergoes a multi-stage pipeline encompassing protobuf marshaling, chunking, Raft consensus, dual-database disk persistence (`raft.db` and `vault.db`), and state verification.

We trace one logical operation from the physical API to durable completion, quantify operation and write amplification, analyze pipeline backpressure, and identify key bottlenecks.

---

## 1. End-to-End Write Path Trace (Put / Delete)

### Stage 1: API Entry & Concurrency Gating
- **Source**: `internal/physical/raft/raft.go:1746-1774`.
- **Classification**: `SOURCE-CONFIRMED`.
- **Trace**:
  1. `b.permitPool.Acquire()` (`raft.go:1766`): Acquires a permit from the 128-capacity pool (`physical.DefaultParallelOperations`). If 128 concurrent physical operations are active, incoming writes block on channel acquisition.
  2. Input validation: Verifies `len(entry.Key) <= bolt.MaxKeySize` (32,768 bytes, `raft.go:1748`).
  3. Context cancellation check: `ctx.Err()`.
  4. Wraps the operation into protobuf structure `LogData` (`raft.go:1756-1764`):
     ```go
     command := &LogData{
         Operations: []*LogOperation{
             { OpType: putOp, Key: entry.Key, Value: entry.Value },
         },
     }
     ```
  5. Acquires backend read lock `b.l.RLock()` (`raft.go:1769`) and invokes `b.applyLog(ctx, command)`.

### Stage 2: Log Marshaling, Chunking & Sizing
- **Source**: `internal/physical/raft/raft.go:1822-1863`.
- **Classification**: `SOURCE-CONFIRMED`.
- **Trace**:
  1. Computes `lowestActiveIndex` from `fastTxnTracker.lowestActiveIndex()` and caps with `raft.AppliedIndex()` (`raft.go:1830-1837`).
  2. Marshals `LogData` into bytes via `proto.Marshal(command)` (`raft.go:1840`).
  3. Validates entry size against `b.maxEntrySize` (default: 2x chunk size = 1,048,576 bytes = 1MB; `raft.go:85, 489-497`). If transactional, validates against `b.maxTransactionSize` (default: 8MB; `raft.go:86, 499-507`).
  4. **Chunking Threshold Evaluation**:
     - Chunking size constant: `raftchunking.ChunkSize = 512 * 1024` (512KB).
     - If `len(commandBytes) <= 512KB`: Direct invocation via `applyFuture = b.raft.Apply(commandBytes, 0)` (`raft.go:1858`).
     - If `len(commandBytes) > 512KB`: Fragmented across multiple Raft entries via `raftchunking.ChunkingApply(...)` (`raft.go:1861`). Each chunk creates an independent Raft log entry.

### Stage 3: Raft Consensus Pipeline & Local Append
- **Source**: `github.com/hashicorp/raft/raft.go` and `internal/physical/raft/raft.go:453-471`.
- **Classification**: `STATIC-ANALYSIS-CONFIRMED`.
- **Trace**:
  1. `raft.Apply` creates a `raft.Log` entry with `Type: LogCommand`.
  2. **Leader Log Persistence**: Log is written to `b.logStore` (which is `raft.LogCache` backed by `raftboltdb.BboltStore` on `raft/raft.db`).
  3. `raftboltdb` commits the log entry into the `logs` bucket:
     - Encodes key as 8-byte big-endian `uint64(Index)`.
     - Fsync behavior: `raftboltdb` executes `tx.Commit()` on `raft.db`. This issues an `fdatasync()` / `fsync()` to ensure the log is durable on the leader's disk.
  4. **Replication Over Network**: The leader's replication pipelining sends RPC `AppendEntries` to all active followers concurrently over the TLS `streamLayer`.

### Stage 4: Quorum Commitment & Follower Acknowledgement
- **Source**: `github.com/hashicorp/raft`.
- **Classification**: `STATIC-ANALYSIS-CONFIRMED`.
- **Trace**:
  1. Followers receive `AppendEntries`, write entries to their local `raft.db`, issue `fsync()`, and reply with success.
  2. For a cluster of $N=3$ voting nodes, quorum requires $Q = \lfloor 3/2 floor + 1 = 2$ nodes (leader + 1 follower). For $N=5$, $Q = 3$.
  3. Once quorum acknowledgments are received, the leader advances its `commitIndex`.

### Stage 5: FSM Batch Application (`vault.db`)
- **Source**: `internal/physical/raft/fsm.go:731-929`.
- **Classification**: `SOURCE-CONFIRMED`.
- **Trace**:
  1. Raft background loop dispatches committed logs to `FSM.ApplyBatch(logs []*raft.Log)` (`fsm.go:731`).
  2. Logs are unmarshaled outside of database locks (`fsm.go:744-767`).
  3. Acquires FSM read lock `f.l.RLock()` (`fsm.go:786`).
  4. Starts a writable transaction on `vault.db`: `err = f.db.Update(func(tx *bolt.Tx) error { ... })` (`fsm.go:809`).
  5. Inside `f.db.Update`:
     - Acquires `bbolt`'s exclusive write mutex (`tx.db.metalock.Lock()`).
     - Obtains bucket `data`: `b := tx.Bucket(dataBucketName)`.
     - Writes key-value: `b.Put([]byte(op.Key), op.Value)` (`fsm.go:615`).
     - Updates `latestIndexKey` in bucket `config` with protobuf `IndexValue{Term, Index}` (`fsm.go:871`).
  6. bbolt commits: Writes modified dirty pages to disk, updates freelist, writes meta page, and executes `fsync()` on `vault.db`.
  7. Invalidation Hook: If `f.invalidateHook != nil`, notifies Vault's physical cache to invalidate modified keys (`fsm.go:889-904`).
  8. Unlocks FSM read lock and returns `FSMApplyResponse` to `applyFuture`.
  9. Leader's `applyLog` unpacks future response and returns `nil` to caller.

---

## 2. Operation and Write Amplification Analysis

| Level / Layer | Amplification Mechanism | Measured / Analyzed Impact | Evidence |
| :--- | :--- | :--- | :--- |
| **Logical API** | 1 Put of $S$ bytes | 1 Logical Operation | SOURCE-CONFIRMED (`raft.go:1746`) |
| **Protobuf Layer** | Encapsulated into `LogData` + `LogOperation` + tags | Overhead: ~15 to 40 bytes per entry | SOURCE-CONFIRMED (`types.proto:10-29`) |
| **Raft Log (`raft.db`)** | Stored in `logs` bucket with msgpack index metadata | Full payload ($S + 50$ bytes) + bbolt B-tree node overhead | SOURCE-CONFIRMED (`raft.go:453`) |
| **Disk Fsync #1** | Leader `raft.db` fsync per write batch | 1 fsync ($1-10$ ms on SSD/NVMe) | STATIC-ANALYSIS-CONFIRMED |
| **Network Replication** | Replicated to $N-1$ peers over network | Payload multiplied by $(N-1)$ network transfers | STATIC-ANALYSIS-CONFIRMED |
| **Follower Disk Fsync** | Each voter follower fsyncs `raft.db` | $(N-1)$ remote fsyncs | STATIC-ANALYSIS-CONFIRMED |
| **FSM Application (`vault.db`)** | Stored in `data` bucket, plus `latestIndex` in `config` | Full payload $S$ + page allocation (minimum 4096 bytes) | SOURCE-CONFIRMED (`fsm.go:811-871`) |
| **Disk Fsync #2** | Leader `vault.db` fsync per batch commit | 1 fsync ($1-10$ ms on SSD/NVMe) | STATIC-ANALYSIS-CONFIRMED |
| **Total Fsync Count** | Minimum fsyncs per committed write on leader | **2 Synchronous Fsyncs on Leader** (`raft.db` + `vault.db`) | SOURCE-CONFIRMED |

### 2.1 Dual-Fsync Write Amplification Bottleneck
Every single write incurs **two synchronous disk flushes** on the leader node:
1. First in `raft.db` during Raft log append.
2. Second in `vault.db` during `FSM.ApplyBatch`.

On an enterprise SSD with a 1 ms fsync latency, the absolute theoretical throughput cap for non-batched sequential writes is:
$$	ext{Max Throughput} = rac{1}{	ext{Fsync}_1 + 	ext{Fsync}_2} = rac{1}{0.001	ext{s} + 0.001	ext{s}} = 500	ext{ writes/sec}$$

### 2.2 Write Batching Optimization in OpenBao
OpenBao activates batching via `config.BatchApplyCh = true` (`internal/physical/raft/raft.go:902`) and sets `config.MaxAppendEntries = 64` (default, configurable up to 1024, `raft.go:883-897`).
- When write concurrency is high, HashiCorp Raft batches multiple log proposals into a single disk write and replication RPC.
- In `FSM.ApplyBatch` (`fsm.go:731-879`), multiple logs within the batch are executed inside a **single `f.db.Update` transaction**, collapsing the `vault.db` commit overhead into a single fsync.
- Under isolated low-concurrency workloads (e.g. concurrency = 1), batching cannot form, and latency equals the full dual-fsync sum ($2-15$ ms).

---

## 3. Backpressure, Pipeline & Concurrency Bounds

1. **Permit Pool Gating**:
   - `permitPool`: 128 permits (`physical.DefaultParallelOperations`). High write spikes exceeding 128 concurrent requests queue at the channel layer.
   - `txnPermitPool`: 64 permits (`physical.DefaultParallelTransactions`).
2. **bbolt Single-Writer Mutex**:
   - `bbolt` enforces strict serial writes via `db.metalock` and `db.mmaplock`.
   - Even if `permitPool` allows 128 threads and Raft pipelines entries, `vault.db` execution in `FSM.ApplyBatch` is strictly serialized.
3. **Log Cache Boundary**:
   - `raftLogCacheSize = 512` (`raft.go:80`). If followers lag by more than 512 entries, the leader must read historical logs directly from `raft.db` disk, incurring disk read contention against incoming writes.
