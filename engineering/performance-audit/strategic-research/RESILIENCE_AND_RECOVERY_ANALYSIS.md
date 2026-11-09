# Resilience, Failure Injection & Recovery Analysis 🤖

## 1. Executive Summary

This report delivers a comprehensive resilience, failure injection, crash consistency, and disaster recovery analysis for OpenBao in support of the **Cloakit Privacy Vault**.

As the Layer 2 cryptographic engine protecting sensitive customer records, OpenBao must survive severe operational failures—including abrupt node crashes, network partitions, disk fsync stalls, slow physical storage backends, and Raft consensus failures—without data corruption, silent decryption failures, or prolonged service unavailability.

This analysis evaluates OpenBao's recovery mechanics, Raft snapshot transfer impacts on tail latency, degraded-mode operations, and defines chaos engineering injection scenarios.

---

## 2. Failure Modes & Systemic Impact Matrix

```
+---------------------------------------------------------------------------------------------------+
|                                   FAILURE TAXONOMY & IMPACT MATRIX                                |
+--------------------------+---------------------+-------------------+------------------------------+
| Failure Scenario         | Subsystem Affected  | Direct Impact     | Recovery Mechanism           |
+--------------------------+---------------------+-------------------+------------------------------+
| 1. Abrupt Process Crash  | Go Runtime / Memory | Ephemeral state   | Barrier unseal, WAL replay,  |
|    (SIGKILL / Kernel OOM)|                     | in cache lost     | Raft FSM state re-read       |
| 2. Storage Disk Slowdown | Physical Storage    | Massive P99 spike | Read cache shielding; writes |
|    (Cloud EBS IOPS limit)| / Raft BoltDB       | on cache misses   | block on sync fsync          |
| 3. Raft Leader Network   | HA Cluster Core /   | Leader election   | Term timeout; followers vote;|
|    Partition (Split-Brain| Forwarding RPC      | period (2-5s)     | active leader steps down     |
| 4. Raft Snapshot Sync    | Raft Transport / FSM| CPU & network sat;| Streaming snapshot chunking; |
|    (New node joining)    |                     | Tail latency spike| rate-limited snapshot transfer|
| 5. Barrier Decrypt Fail  | Security Barrier /  | Key inaccessible; | Storage integrity check;     |
|    (Corrupted ciphertext)| Physical Layer      | HTTP 500 error    | restore from backup/replica  |
| 6. Upstream KMS Outage   | Auto-Unseal / Seal  | Node cannot boot; | Local key caching during op; |
|    (Layer 1 Cloud KMS)   | Wrapper (Layer 1)   | runtime unaffected| alert on unseal stall        |
+--------------------------+---------------------+-------------------+------------------------------+
```

---

## 3. Crash Consistency & Storage Barrier Recovery

### Barrier Integrity & Write Atomicity
OpenBao enforces cryptographic crash consistency via the security barrier (`internal/vault/barrier/aes_gcm.go` and `internal/vault/physical/`).
1. Every write to physical storage is wrapped in an authenticated ciphertext:
   $$\text{StorageEntry} = \text{Key} \parallel \text{AES-256-GCM}_{\text{BarrierKey}}(\text{JSON}(\text{Data}))$$
2. Because AES-GCM includes an authenticated GMAC tag, any truncated or torn write resulting from an abrupt power loss or operating system crash fails cryptographic verification upon reboot:
   - `cipher: message authentication failed`
3. This cryptographic guarantee prevents **silent data corruption**. However, it means a torn write results in a hard read error (`ErrInvalidCiphertext`).
4. To prevent torn writes, OpenBao relies on the underlying storage engine's atomic guarantees:
   - In Integrated Storage (Raft): BoltDB / Pebble uses a Write-Ahead Log (WAL) and copy-on-write B-Tree page allocation. An uncommitted transaction is rolled back automatically during crash recovery.

---

## 4. High Availability, Consensus & Read-Replica Dynamics

### Standby Node Serving & Cache Invalidation
OpenBao provides built-in read scalability via Standby Nodes (`internal/vault/ha.go` and `internal/vault/grpc_invalidation.go`):

```
+---------------------------------------------------------------------------------------------------+
|                                    HA READ & INVALIDATION FLOW                                    |
+---------------------------------------------------------------------------------------------------+
|  [ ACTIVE LEADER ]                                                    [ STANDBY REPLICA ]        |
|  1. Writes new key / rotates key                                                                  |
|  2. Commits to Raft log                                                                           |
|  3. Dispatches gRPC Invalidation -----------------------------------> Receives Invalidation Msg   |
|     (`invalidationPeers.dispatcher`)                                  Evicts key from Transit LRU |
|                                                                       (`lm.InvalidatePolicy`)     |
|                                                                                                   |
|                                                                       Serves 95% Read Queries:    |
|                                                                       - Reads locally cached key  |
|                                                                       - Unwraps DEK instantly     |
+---------------------------------------------------------------------------------------------------+
```

### Standby Bypass Mechanics (`SOURCE-CONFIRMED`):
In `internal/vault/ha.go:1035-1070` (`c.maybeBypassGRPCRequirement`):
- When a standby node is disconnected from the active leader's gRPC invalidation stream, OpenBao enters a defensive degraded mode.
- If storage is synchronized (e.g. Raft state machine is up to date), the standby can continue serving read traffic, but if leadership state is ambiguous, it must prevent stale reads.
- **Cloakit Workload Impact:** For 95% read operations (DEK unwrapping), reading an immediately stale Subject Key is generally safe *unless* the key was revoked or rotated. A key version is immutable once written. Therefore, read replicas can safely serve DEK unwraps with near-zero availability downtime even during transient leader disconnects.

---

## 5. Raft Snapshot Transfer Contention & Compaction Dynamics

Under Integrated Storage (Raft), the Raft log grows continuously with every write (token creation, key creation, key rotation).
1. When the log reaches `snapshot_threshold` (default 8,192 transactions), OpenBao takes a snapshot:
   - Streams the entire current state database to disk (`takesnapshot` metric).
   - Compacts older log entries.
2. When a new node joins or falls behind, the active leader streams a full database snapshot across the Raft TLS transport.
3. **Tail Latency Contention:** Streaming a 10 GB database snapshot saturates CPU and network egress interfaces, creating severe tail latency blowouts for concurrent DEK operations.
4. **Degradation Shielding:** Integrated Storage must configure bandwidth rate-limiting for snapshot transfers (`snapshot_threshold = 32768`, streaming chunks throttled to $\le 50\,\text{MB/s}$) to guarantee uninterrupted sub-millisecond cryptographic operations on the hotpath.

---

## 6. Chaos Engineering & Failure Injection Test Specifications

To validate Cloakit's operational resilience, we define five reproducible chaos engineering test harnesses:

### Test Scenario CHAOS-01: Storage Fsync Latency Spike
- **Objective:** Measure DEK unwrap latency degradation when physical disk I/O stalls.
- **Harness:** Inject simulated 100 ms disk write latency via `tc` network emulation or `dm-delay` on the underlying storage mount.
- **Expected Result:** Warm cache DEK operations continue at $< 1\,\text{ms}$ P99 latency. Cold cache misses degrade to 100+ ms. Lock striping should prevent hot keys from stalling behind cold keys.

### Test Scenario CHAOS-02: Abrupt Active Node Termination
- **Objective:** Quantify leader election duration and client retry behavior.
- **Harness:** Send `SIGKILL` to active OpenBao process under 20,000 req/s load.
- **Expected Result:** Standby node detects heartbeat loss within 1.5 seconds, assumes leadership, and restores read/write availability within 2.5 seconds total failover window.

### Test Scenario CHAOS-03: gRPC Invalidation Pipeline Severance
- **Objective:** Verify standby behavior during invalidation stream disconnection.
- **Harness:** Sever TCP port 8201 between active leader and standby node using `iptables -A INPUT -p tcp --dport 8201 -j DROP`.
- **Expected Result:** Standby logs warning, transitions to local storage invalidation polling, continues serving warm-cache DEK unwraps, and reconnects with exponential backoff.

### Test Scenario CHAOS-04: Extreme Memory Exhaustion (cgroup OOM Simulation)
- **Objective:** Verify process behavior under restricted memory cgroups.
- **Harness:** Confine OpenBao process to 2 GB cgroup memory limit while populating 500,000 Subject Keys.
- **Expected Result:** Transit LRU bounds cache retention at configured capacity; Go GC pacing keeps memory usage within 1.8 GB without kernel OOM killer invocation.

---

## 7. Strategic Resilience Recommendations

| Area | Current Behavior | Strategic Architecture Improvement |
| :--- | :--- | :--- |
| **Read Availability** | Standbys stall if leader disconnects | Allow Read-Only Standby Grace Mode for immutable key decrypts. |
| **Snapshot Throttling** | Uncapped Raft snapshot streaming | Throttle snapshot network bandwidth to protect cryptographic request bandwidth. |
| **Torn-Write Recovery** | Hard 500 error on corrupted entry | Automatic replica healing from Raft peers on barrier integrity failure. |
| **Cold Cache Storm** | Unbounded concurrent storage reads | Singleflight deduplication on key cache misses to prevent disk thrashing. |
