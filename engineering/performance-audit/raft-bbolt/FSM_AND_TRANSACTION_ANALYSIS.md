# FSM & Transaction Implementation Deep Dive 🤖

## Executive Summary
This report analyzes OpenBao's Finite State Machine (FSM) implementation in `internal/physical/raft/fsm.go` and its transaction execution model in `internal/physical/raft/transaction.go`. We examine how transactions are coordinated across distributed consensus, how optimistic concurrency control (OCC) is implemented via `fsmTxnCommitIndexTracker`, transaction verification costs, and failure recovery.

---

## 1. FSM Architecture and ApplyBatch Pipeline

### 1.1 FSM Struct Definition
- **Source**: `internal/physical/raft/fsm.go:107-140`.
- **Classification**: `SOURCE-CONFIRMED`.
- **Key Fields**:
  - `db *bolt.DB`: Pointer to open `vault.db` database.
  - `l sync.RWMutex`: Read-write lock protecting `f.db` during snapshot restoration or database swapping.
  - `fastTxnTracker *fsmTxnCommitIndexTracker`: Tracker enabling lockless/fast application of transactions.
  - `latestIndex atomic.Uint64`: Last applied Raft log index.
  - `latestTerm atomic.Uint64`: Term of last applied Raft log.
  - `latestConfig atomic.Pointer[ConfigurationValue]`: Current active cluster configuration.

### 1.2 Batch Application Flow (`ApplyBatch`)
- **Source**: `internal/physical/raft/fsm.go:731-929`.
- **Classification**: `SOURCE-CONFIRMED`.
- When HashiCorp Raft commits a set of log entries, it invokes `f.ApplyBatch(logs []*raft.Log)`:
  1. **Phase 1: Concurrent Unmarshaling** (`fsm.go:741-767`): Parses protobuf `LogData` from `l.Data` across all logs before acquiring locks, preventing deserialization CPU overhead from blocking database writes.
  2. **Phase 2: Single bbolt Update Transaction** (`fsm.go:809-879`):
     - Executes `f.db.Update(func(tx *bolt.Tx) error { ... })`.
     - Iterates through all unmarshaled commands.
     - Differentiates non-transactional operations (`applyBatchNonTxOps`) from transactional operations (`applyBatchTxOps`).
     - Appends results into `entrySlices`.
     - Writes the latest log index to `configBucketName` with key `latestIndexKey` (`fsm.go:871`).
  3. **Phase 3: Post-Commit State Advancement & Cache Invalidation** (`fsm.go:885-915`):
     - Clears fast transaction tracker history up to `lowestActiveIndex` (`fsm.go:886`).
     - Calls `f.invalidateHook` to purge OpenBao's in-memory LRU/physical cache for all updated keys (`fsm.go:889-904`).
     - Updates `f.latestTerm` and `f.latestIndex` atomically (`fsm.go:908-909`).

---

## 2. Transaction Architecture & Optimistic Concurrency Control

OpenBao Integrated Storage implements ACID transactions on top of Raft using an **Optimistic Concurrency Control (OCC) model with read-set verification**.

### 2.1 Transaction Initiation (`BeginTx` / `newTransaction`)
- **Source**: `internal/physical/raft/transaction.go:193-270`.
- **Classification**: `SOURCE-CONFIRMED`.
- **Lifecycle**:
  1. Acquires a permit from `txnPermitPool` (capacity: 64, `physical.DefaultParallelTransactions`).
  2. Acquires FSM read lock: `b.fsm.l.RLock()`.
  3. Reads currently applied index: `index := b.AppliedIndex()`.
  4. Starts an underlying **read-only bbolt transaction**: `tx, err := b.fsm.db.Begin(false)` (`transaction.go:210`).
     - *Crucial Architectural Invariant*: bbolt does not support concurrent write transactions. Therefore, OpenBao creates an isolated read-only bbolt transaction for the duration of the OpenBao transaction!
  5. If writable, registers `b.fsm.fastTxnTracker.trackTransaction(index)`.
  6. Attaches a runtime finalizer (`runtime.AddCleanup`) to detect and log leaked transactions (`transaction.go:234-267`).

### 2.2 In-Transaction Read and Write Buffering
- **Reads (`Get`)**:
  - Checks if the key was already modified in the transaction buffer (`t.updates`). If found, returns the buffered value (`transaction.go:343-356`).
  - Otherwise, reads from the read-only bbolt transaction: `val := t.tx.Bucket(dataBucketName).Get([]byte(key))` (`transaction.go:368`).
  - Records the read in `t.reads` along with a SHA-384 hash of the value for conflict detection (`transaction.go:374-388`).
- **Writes (`Put`, `Delete`)**:
  - Writes are **not** sent to bbolt immediately.
  - Appended to memory map `t.updates[entry.Key] = &raftTxnUpdateRecord{...}` (`transaction.go:272-336`).

### 2.3 Transaction Commit (`Commit`)
- **Source**: `internal/physical/raft/transaction.go:520-640`.
- **Classification**: `SOURCE-CONFIRMED`.
- **Commit Pipeline**:
  1. Closes the underlying read-only bbolt transaction: `t.tx.Rollback()` (`transaction.go:560`).
  2. If no writes occurred (`!t.haveWritten`), returns immediately without proposing to Raft (`transaction.go:569-571`).
  3. Formulates a consolidated Raft log entry:
     - Header: `beginTxOp` containing transaction start index (`transaction.go:594-597`).
     - Read verifications: `verifyReadOp` for all read keys (`transaction.go:598-600`).
     - List verifications: `verifyListOp` for all listing prefixes (`transaction.go:601-607`).
     - Modifications: `putOp` and `deleteOp` for all buffered updates (`transaction.go:608-622`).
     - Sentinel: `commitTxOp` (`transaction.go:623-625`).
  4. Acquires standard permit: `t.b.permitPool.Acquire()`.
  5. Proposes consolidated log entry to Raft via `applyLog` (`transaction.go:636`).

---

## 3. Fast Transaction Verification (`fsmTxnCommitIndexTracker`)

- **Source**: `internal/physical/raft/transaction.go:693-970`.
- **Classification**: `SOURCE-CONFIRMED`.

When `FSM.ApplyBatch` executes a transaction log:
1. **Fast Path Check (`canFastWrite`)**:
   - If `latestAppliedIndex == txnStartIndex` (meaning no other Raft logs were committed between when the transaction started and when it was applied), verification is bypassed entirely (`transaction.go:912-914`).
   - Hits metric: `raft-storage.txn_fast_apply_read_hit`.
2. **Selective Bypass Check (`canFastWriteBypassRead`)**:
   - If `latestAppliedIndex > txnStartIndex`, checks `fastTxnTracker.hasModifiedEntry(txnStartIndex, commandIndex, key)` (`transaction.go:789-811`).
   - If no intermediate write touched `key`, read verification succeeds without reading bbolt!
3. **Slow Path Verification**:
   - If an intermediate write modified `key`, OpenBao executes `b.Get([]byte(op.Key))` from `vault.db` and verifies that the current hash matches the hash recorded when the transaction started (`transaction.go:957-960`).
   - If the hash differs, the transaction aborts with `physical.ErrTransactionCommitFailure`.

---

## 4. Bottlenecks and Failure Modes

1. **Transaction Size Limit**:
   - `maxTransactionSize = 8 * maxEntrySize = 8MB` (`raft.go:86, 177, 499-507`).
   - Large bulk operations exceeding 8MB are rejected upfront (`physical.ErrValueTooLarge`).
2. **Read-Set Amplification**:
   - Transactions that read many keys before writing pay a severe network and storage penalty: every read key and its hash are serialized into the Raft log, replicated over TLS, and written to `raft.db`.
3. **Optimistic Conflict Rate under High Contention**:
   - If concurrent transactions touch the same keys, OCC causes rollbacks (`ErrTransactionCommitFailure`), requiring caller application retries.
