# PostgreSQL Physical Replication & Replica Lag Hazards 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report analyzes the interaction between OpenBao multi-instance readers and PostgreSQL physical streaming replication.

---

## 1. Streaming Replication Mechanics

In a production scale-out architecture:
- OpenBao Primary (Active) writes to PostgreSQL Primary.
- OpenBao Read Nodes connect to PostgreSQL Streaming Read Replicas.

```
+------------------------------------+               +------------------------------------+
|       OpenBao Active Node          |               |       OpenBao Read Node            |
+-----------------+------------------+               +-----------------+------------------+
                  | (Writes)                                           | (Reads)
                  v                                                    v
+-----------------+------------------+  Async WAL    +-----------------+------------------+
|        PostgreSQL Primary          |-------------->|    PostgreSQL Read Replica         |
+------------------------------------+  (10-500ms)   +------------------------------------+
```

---

## 2. Critical Stale-Read Hazards for Cryptographic Keys

### 2.1 The Key Rotation Race Condition
1. **Time $T_0$:** Cloakit rotates Subject Key `sk-user-99` via OpenBao Active Node.
2. **Time $T_1$:** PostgreSQL Primary commits the update and sends WAL record to Read Replica.
3. **Time $T_2$ ($T_1 + 50\text{ms}$):** Cloakit receives HTTP 200 OK for rotation and immediately sends data to be encrypted by a worker connected to OpenBao Read Node.
4. **Time $T_3$ ($T_1 + 60\text{ms}$):** PostgreSQL Read Replica has **NOT** applied the WAL record due to replication lag.
5. **Outcome:** OpenBao Read Node experiences a cache miss, queries the Read Replica, and fetches the **old (pre-rotation) key**.
6. **Violation:** Causes broken ciphertext versioning or silent failure of encryption rotation mandates.

### 2.2 Replication Lag Measurement
- **Source Reference:** `internal/physical/postgresql/index.go:30-45`
OpenBao exposes hooks to check replication index: `AppliedReplicationIndex(ctx)` querying PostgreSQL LSN (`pg_last_wal_replay_lsn()`).
