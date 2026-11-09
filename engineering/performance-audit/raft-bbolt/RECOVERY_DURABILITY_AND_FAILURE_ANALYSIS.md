# Recovery, Durability & Failure Mode Analysis 🤖

## Executive Summary
This report analyzes durability guarantees, crash recovery semantics, WAL replay behavior, and failure modes across power loss, network partitions, and node crashes.

---

## 1. Durability Guarantees & Fsync Semantics

- **Source**: `internal/physical/raft/raft.go:2179-2212` and `github.com/hashicorp/raft-boltdb/v2`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao Integrated Storage provides **strict enterprise durability**:
  1. `raft.db` commits logs with synchronous disk flushes (`fdatasync`).
  2. `vault.db` commits FSM applications with synchronous disk flushes.
  3. Power failure at any instant guarantees zero uncommitted state corruption because bbolt uses atomic meta-page pointer toggling.

---

## 2. Crash Recovery & Startup Replay Pipeline

### 2.1 Startup Replay Path
- **Source**: `internal/physical/raft/raft.go:453-479, 880` and `fsm.go:198-269`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao sets `config.NoSnapshotRestoreOnStart = true` (`raft.go:880`).
- **Startup Sequence**:
  1. `NewFSM` opens `vault.db`, reads `latestIndexKey` from bucket `config`, and sets `f.latestIndex`.
  2. HashiCorp Raft opens `raft.db` and determines `lastLogIndex`.
  3. If `f.latestIndex < lastLogIndex`: Raft replays unapplied logs from `raft.db` sequentially through `FSM.Apply` until `f.latestIndex == lastLogIndex`.
  4. Node transitions to follower and participates in cluster elections.

### 2.2 Recovery Replay Time (RTO)
- If a node crashes when `raft.db` contains 50,000 un-truncated logs beyond `latestIndex`:
  - Replaying 50,000 logs through `fsm.db.Update` can take **30 to 120 seconds**, delaying node availability.
  - Mitigated by OpenBao's background log truncation worker keeping trailing logs bounded (`trailing_logs` default 10,240).
