# Snapshot Lifecycle, Compaction & Unbounded Disk Growth Audit 🤖

## Executive Summary
This report analyzes OpenBao's snapshotting architecture (`internal/physical/raft/snapshot.go`), Raft log truncation (`startLogTruncationWorker`), physical compaction limitations in bbolt, and mechanisms of disk growth. We investigate historical bugs, fixed regressions, and conditions leading to unbounded disk expansion.

---

## 1. Snapshot Architecture & Implementation

### 1.1 Snapshot Creation Pipeline
- **Source**: `internal/physical/raft/snapshot.go:110-135` and `internal/physical/raft/fsm.go:997-1004`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao implements `BoltSnapshotStore` (`snapshot.go:49`).
- When a snapshot is initiated:
  1. Raft calls `fsm.Snapshot()`.
  2. Inside `fsm.writeTo` (`fsm.go:946-995`), OpenBao starts a read-only transaction on `vault.db`: `tx, err := f.db.Begin(false)`.
  3. Writes the database directly to the snapshot sink: `tx.WriteTo(sink)` (`fsm.go:961`).
  4. Packaging: Compressed into an encrypted or sealed tar archive containing `db` (`vault.db`), `meta.json`, and SHA-256 verification hashes (`snapshot/archive.go:119-150`).

### 1.2 Snapshot Storage & Cleanup (`ReapSnapshots`)
- **Source**: `internal/physical/raft/snapshot.go:277-310`.
- **Classification**: `SOURCE-CONFIRMED`.
- Snapshots are written to `${path}/raft/snapshots/<term>-<index>-<timestamp>.tmp`.
- On completion, `ReapSnapshots` scans the directory and removes older snapshot directories, retaining only the latest completed snapshot.

---

## 2. Raft Log Truncation vs bbolt State Compaction

### 2.1 Raft Log Truncation Worker
- **Source**: `internal/physical/raft/raft.go:840-848, 1921-1965`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao intentionally **disables HashiCorp Raft's native snapshot-driven log truncation**:
  ```go
  config.SnapshotThreshold = math.MaxUint64
  config.SnapshotInterval = math.MaxInt64 / 2
  config.TrailingLogs = math.MaxUint64
  ```
- **Rationale**: HashiCorp Raft native truncation resets on restarts, which prevented logs from truncating if restart occurred before `snapshot_threshold` was reached (`raft.go:840-845`).
- **Dedicated Worker (`truncateLog`)**:
  - Runs in background every `snapshotInterval` (default 120s) plus jitter (`raft.go:1928-1939`).
  - Checks: `entryCount := applied.Index - minLog + 1`.
  - If `entryCount > b.snapshotThreshold + b.trailingLogs`:
    - Calls `b.logStore.DeleteRange(minLog, applied.Index - b.trailingLogs)` (`raft.go:1960`).
    - Removes old Raft logs from `raft.db`.

### 2.2 The Zero-Compaction Reality of bbolt
- **Source**: `internal/physical/raft/fsm.go` and `go.mod` (`go.etcd.io/bbolt`).
- **Classification**: `SOURCE-CONFIRMED`.
- **Crucial Finding**: `b.logStore.DeleteRange()` in `raft.db` and key deletes in `vault.db` **DO NOT release physical disk space back to the filesystem!**
- bbolt simply marks the freed pages in its internal freelist map.
- The filesystem size (`stat -c %s raft.db` or `vault.db`) **never shrinks**.
- OpenBao has **no online defragmentation or compaction mechanism** for either `raft.db` or `vault.db`.

---

## 3. Disproportionate Disk Growth Dynamics & Failure Modes

1. **High-Churn Keys (Frequent DEK Rotations / Updates)**:
   - Repeated overwrites of keys cause continuous page allocations and copy-on-write page splits.
   - While free pages are reused, memory fragmentation can leave pages partially filled (~50% fill ratio), causing the file to double in physical size relative to live data.
2. **Leaked Read Transactions**:
   - If a client or background job leaks a read transaction (`bolt.Tx`), the freelist cannot reuse any pages freed by subsequent writes.
   - `raft.db` and `vault.db` will allocate net-new pages at the end of the file for every subsequent write, expanding until the host filesystem runs out of disk space (`ENOSPC`).
3. **Recovery After Deletes**:
   - To reclaim physical disk space from the operating system, an operator must perform an offline compaction:
     `bbolt compact -o vault_compacted.db vault.db`
     or take a snapshot, delete the data directory, and perform a snapshot restore.
