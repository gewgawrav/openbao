# Synchronous vs Asynchronous Database Execution Analysis 🤖

## Executive Summary
This report analyzes every operation in OpenBao's PostgreSQL physical backend and classifies its execution model, evaluating durability, blocking semantics, safety, and correctness for Cloakit Privacy Vault's cryptographic workload.

---

## 1. Classification Matrix of PostgreSQL Operations

| Operation | Calling Function | Execution Model | Blocks Caller? | Durability Required? | Isolation Level | Can Run Concurrently? | Can be Batched? | Async Safe? |
|---|---|---|---|---|---|---|---|---|
| Read Key | `Get` (`postgresql.go:434`) | Synchronous | Yes | N/A (Read) | Read Committed (default) | Yes | Yes (Multi-get) | No |
| Write Key | `Put` (`postgresql.go:417`) | Synchronous | Yes | Yes (WAL sync) | Read Committed + Fenced | Yes (different keys) | Yes (Multi-insert) | No |
| Delete Key | `Delete` (`postgresql.go:456`) | Synchronous | Yes | Yes (WAL sync) | Read Committed + Fenced | Yes (different keys) | Yes (Multi-delete) | No |
| List Keys | `List` / `ListPage` (`postgresql.go:475, 508`) | Synchronous | Yes | N/A (Read) | Read Committed | Yes | No | No |
| Begin Tx | `BeginTx` (`transaction.go:76`) | Synchronous | Yes | N/A | Repeatable Read | Yes (up to txn limit) | No | No |
| Commit Tx | `Commit` (`transaction.go:214`) | Synchronous | Yes | Yes (WAL sync) | Repeatable Read | Yes | Yes (Group Commit) | No |
| HA Lock Renew | `periodicallyRenewLock` (`postgresql.go:668`) | Asynchronous Background Ticker | No (Background) | No (Time-bound lease) | Read Committed | Yes | No | Yes (Already async) |
| HA Lock Acquire | `tryToLock` (`postgresql.go:645`) | Asynchronous Background Goroutine | Yes (until acquired/stopped) | Yes (WAL sync) | Read Committed | No (Exclusive) | No | No |
| Replication LSN | `AppliedReplicationIndex` (`index.go:27`) | Synchronous | Yes | N/A (Read) | Read Committed | Yes | No | No |

---

## 2. Durability & Correctness Guarantees for Cryptographic Data

### Subject Key Creation and Rotation
- Cloakit Subject Keys are the authoritative root for client data privacy.
- If key creation or rotation is acknowledged to the caller before WAL flush (`synchronous_commit = off` or async fire-and-forget write):
  - A crash or database failover immediately destroys or rolls back the newly generated Subject Key.
  - Any ciphertext encrypted under the lost key becomes permanently irrecoverable!
- **Strict Rule**: State-changing operations on authoritative key material (`sys/`, `transit/keys/`) **MUST remain synchronous and durable**. Asynchronous execution is strictly rejected for key creation, rotation, and revocation. [SOURCE-CONFIRMED]

### DEK Wrapping and Unwrapping
- In Cloakit's architecture, Layer 2 Subject Keys wrap/unwrap ephemeral Layer 3 Data Encryption Keys (DEKs).
- Wrapping/unwrapping itself is an in-memory cryptographic operation utilizing the Subject Key.
- It performs **0 storage writes** (read-only verification of Subject Key).
- If the Subject Key is cached in OpenBao's 2Q cache, DEK wrapping/unwrapping incurs **0 database queries** and **0 blocking I/O calls**. [SOURCE-CONFIRMED]

---

## 3. Safe Asynchronous & Batching Opportunities

1. **Replication Index Tracking**:
   - `AppliedReplicationIndex` (`index.go:27`) queries `pg_current_wal_lsn()` on every replication index check.
   - This can be cached or batched with a micro-window (e.g. 5ms) without violating replication ordering.
2. **Audit Logging & Telemetry**:
   - Metric calls (`metrics.MeasureSince`) are already non-blocking.
   - Non-authoritative access logs can be routed to buffered asynchronous channels.
3. **Multi-key Batch Retrieval**:
   - OpenBao's `physical.Backend` lacks a `GetBatch(ctx, []string)` method. Adding multi-key retrieval via `WHERE (path, key) IN (...)` can batch bulk Subject Key lookups into a single network round trip.
