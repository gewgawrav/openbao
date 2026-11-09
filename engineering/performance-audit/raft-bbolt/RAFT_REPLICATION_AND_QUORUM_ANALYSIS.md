# Raft Networking, Quorum Scaling & Multi-Node Cluster Dynamics 🤖

## Executive Summary
This report analyzes Raft consensus networking, replication pipelines, heartbeat timings, and quorum dynamics across 1, 3, and 5-node clusters and non-voter read replicas. We evaluate leader failover impact, network partition scenarios, and noisy-node blast radius under Cloakit's high-throughput requirements.

---

## 1. Replication Pipeline & Network Transport

### 1.1 Stream Layer & TLS Multiplexing
- **Source**: `internal/physical/raft/streamlayer.go:37-120` and `internal/physical/raft/raft.go:113-119`.
- **Classification**: `SOURCE-CONFIRMED`.
- Raft inter-node RPCs travel over the same network listener used by OpenBao's cluster port (default `:8201`), demultiplexed via TLS ALPN and header routing.
- The leader establishes pipelined replication connections to each follower, streaming `AppendEntries` RPCs asynchronously.

### 1.2 Heartbeat, Election & Multiplier Timing
- **Source**: `internal/physical/raft/raft.go:823-835`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao applies a default `performance_multiplier = 5` (`raft.go:824`):
  - `HeartbeatTimeout`: $1000	ext{ ms} 	imes 5 = 5.0	ext{ seconds}$
  - `ElectionTimeout`: $1000	ext{ ms} 	imes 5 = 5.0	ext{ seconds}$
  - `LeaderLeaseTimeout`: $500	ext{ ms} 	imes 5 = 2.5	ext{ seconds}$
- **Leader Failover Impact**:
  - If the active leader fails or crashes, followers detect loss of heartbeats after $5.0$ to $10.0$ seconds.
  - During this election window, **no writes can be accepted**, and client requests receive `500 Server Error` or connection timeouts until a new leader is elected and initialized.

---

## 2. Quorum Scaling Models: 1, 3, 5 Nodes & Non-Voters

$$	ext{Quorum Size } Q = \left\lfloor rac{N}{2} ightfloor + 1$$

| Cluster Topology | Voting Nodes ($N$) | Quorum ($Q$) | Fault Tolerance | Write Amplification Factor | Replicated Fsyncs per Write |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Single-Node** | 1 | 1 | 0 nodes | $1	imes$ | 2 fsyncs (local only) |
| **3-Node Cluster** | 3 | 2 | 1 node | $3	imes$ network, $2	imes$ disk quorum | Leader (2) + Follower (1) = 3 fsyncs |
| **5-Node Cluster** | 5 | 3 | 2 nodes | $5	imes$ network, $3	imes$ disk quorum | Leader (2) + 2 Followers (2) = 4 fsyncs |
| **3 Voters + 3 Non-Voters**| 3 voters | 2 | 1 node | $3	imes$ consensus, $3	imes$ replica stream| Consensus unaffected by non-voters |

### 2.1 The Non-Voter (Read Replica) Pattern
- **Source**: `internal/physical/raft/raft.go:550-565` and `raft_autopilot_delegate.go:34-45`.
- **Classification**: `SOURCE-CONFIRMED`.
- Non-voters (`retry_join_as_non_voter = true`) receive continuous log replication from the leader but **do not participate in quorum calculations or leader elections**.
- **Cloakit Workload Fit**:
  - Non-voter nodes can scale 95% read traffic horizontally across multiple data centers or AZs without increasing write quorum latency or election fragility.

---

## 3. Network Partitions & Noisy-Node Impact

1. **Quorum Loss**:
   - In a 3-node cluster, a partition isolating the leader into a minority causes write proposals to block and timeout (`raft: cannot commit log, no quorum`).
2. **Slow / Noisy Follower**:
   - Because HashiCorp Raft requires only a simple majority ($Q=2$ out of 3), a single slow follower (e.g. disk stalling on fsync) **does not stall write commits** as long as the other follower responds promptly.
   - However, the slow follower lags in log index; if the leader truncates logs before the follower catches up, the follower enters snapshot restore mode, transferring full `vault.db` over the network.
