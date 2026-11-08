# OpenBao Capacity Model & Sizing Guide

## 1. Capacity Mathematical Modeling

This document provides formal sizing equations and empirical coefficients for dimensioning OpenBao deployments based on expected request volumes, token counts, cryptographic load, and storage throughput.

---

## 2. Core Sizing Formulas

### Formula 1: CPU Dimensioning

Total CPU cores required ($C_{total}$) is given by:

$$C_{total} = \left( \sum_{i=1}^{N} \frac{R_i \times T_i}{1000} \right) \times (1 + \mu_{GC}) \times \frac{1}{\text{Target Utilization}}$$

Where:
- $R_i$: Operations per second for workload category $i$.
- $T_i$: CPU execution time per operation (in milliseconds).
- $\mu_{GC}$: Go GC CPU overhead fraction ($\approx 0.15$ to $0.25$ under heavy allocation).
- $\text{Target Utilization}$: Recommended peak target utilization ($\approx 0.60$ or 60%).

#### Empirical Execution Time Coefficients ($T_i$ on modern x86_64 @ 2.5+ GHz):

| Workload Category ($i$) | Time $T_i$ (ms) | Ops/sec per Core |
| :--- | :--- | :--- |
| Secret Read (KV Cache Hit) | 0.052 ms | ~19,200 |
| Secret Write (Uniform) | 0.028 ms | ~35,500 |
| Transit Encrypt (AES-256-GCM) | 0.024 ms | ~41,400 |
| Transit Decrypt (AES-256-GCM) | 0.021 ms | ~47,800 |
| Transit Sign (Ed25519) | 0.035 ms | ~28,500 |
| PKI Issuance (ECDSA P-256) | 0.241 ms | ~4,150 |
| PKI Issuance (RSA-2048) | 2.220 ms | ~450 |
| Userpass Auth (Bcrypt cost 10) | 22.140 ms | ~45 |
| Token Lookup / Self | 0.190 ms | ~5,260 |
| Policy ACL Evaluation | 0.354 ms | ~2,830 |
| Lease / Token Renewal | 0.298 ms | ~3,350 |

---

### Formula 2: Memory (RAM) Sizing

Total RAM required ($M_{total}$) comprises base overhead, active token cache, lease state, and runtime heap headroom:

$$M_{total} = M_{base} + (N_{tokens} \times S_{token}) + (N_{leases} \times S_{lease}) + (N_{secrets} \times S_{secret\_cache}) + M_{inflight} + M_{heap\_headroom}$$

Where:
- $M_{base}$: Base OpenBao binary and runtime footprint $\approx 256 \text{ MB}$.
- $N_{tokens}$: Total active service tokens. Average serialized entry size $S_{token} \approx 2.5 \text{ KB}$ (in-memory + index).
- $N_{leases}$: Active dynamic secrets and token leases. $S_{lease} \approx 1.5 \text{ KB}$ per entry in ExpirationManager.
- $N_{secrets}$: Number of secrets held in memory cache. $S_{secret\_cache} \approx \text{Average Payload Size} \times 1.4$.
- $M_{inflight}$: In-flight HTTP request buffers:
  $$M_{inflight} = \text{Concurrent Requests} \times (\text{Request Payload} + \text{Response Buffer} + 32\text{ KB goroutine stack})$$
- $M_{heap\_headroom}$: Go runtime heap factor (recommend at least $100\%$ headroom to avoid frequent aggressive GC cycles, $GOGC=100$).

#### Practical Sizing Example for 500,000 Active Tokens & Leases:
- Base: 256 MB
- Tokens ($500,000 \times 2.5 \text{ KB}$): 1,250 MB
- Leases ($500,000 \times 1.5 \text{ KB}$): 750 MB
- Secret cache (50,000 entries @ 2 KB): 140 MB
- In-flight buffers (500 concurrency @ 64 KB): 48 MB
- Subtotal: ~2.44 GB
- With 100% Go GC headroom ($2 \times$ multiplier): **~4.9 GB to 6 GB RAM minimum**.

---

### Formula 3: Storage IOPS & Raft Disk Bandwidth

For Raft Integrated Storage, every state modification produces a WAL entry and barrier commit:

$$\text{Required Write IOPS} = R_{write\_ops} \times W_{amplification}$$

Where:
- $R_{write\_ops}$: Total write operations per second (secret writes, token issues, token revocations, lease renewals).
- $W_{amplification}$: Write amplification factor of Raft and underlying storage engine:
  - OpenBao Raft WAL: 1 write per commit.
  - Storage engine (BoltDB/Pebble/bbolt): B-Tree or LSM rebalancing (typically $1.5$ to $3.0\times$ write amplification).
- **Disk Latency Constraint**:
  $$\text{Max Sustainable Write Throughput} = \frac{\text{Concurrent Raft Pipeline Slots}}{\text{Disk fsync latency (seconds)}}$$
  For single-writer non-pipelined commits with $1.0\text{ ms}$ disk sync latency:
  $$\text{Max Throughput} \approx 1,000 \text{ write ops/sec}$$
  With transaction batching / group committing:
  $$\text{Max Throughput} \approx 5,000 - 15,000 \text{ write ops/sec (SSD NVMe)}$$

---

## 3. Recommended Cluster Profiles

| Profile Tier | Intended Target Scale | Compute (vCPU) | Memory (RAM) | Storage Specification | Network |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Small (Dev/Test)** | < 1,000 ops/sec, < 25k tokens | 2 vCPU | 4 GB | Standard SSD (500 IOPS, < 5ms latency) | 1 Gbps |
| **Medium (Standard Prod)**| 1,000 - 10,000 ops/sec, < 200k tokens | 4 - 8 vCPU | 16 GB | Provisioned SSD (3,000 IOPS, < 1ms latency) | 5 - 10 Gbps |
| **Large (High Scale)** | 10,000 - 35,000 ops/sec, < 1M tokens | 16 - 32 vCPU | 32 - 64 GB | Direct NVMe PCIe (< 0.2ms fsync latency) | 10 - 25 Gbps |
| **Extreme Cryptographic** | > 50,000 Transit ops/sec | 32 - 64 vCPU | 64 GB | Direct NVMe SSD + Dedicated Network | 25+ Gbps |

---

## 4. Scaling Topology Guidelines

1. **Raft Cluster Size**:
   - Maintain 3 or 5 nodes (odd quorum). Never deploy 2 or 4 nodes as even numbers do not increase fault tolerance.
   - 3 nodes tolerate 1 failure; 5 nodes tolerate 2 failures.
2. **Read Scaling with Performance Standbys**:
   - For read-heavy clusters (>80% reads), enable Performance Standby nodes to handle local token lookups and secret reads directly without forwarding to the active leader.
3. **Partitioning with Namespaces**:
   - Isolate independent business units or environments into distinct namespaces. While sharing the storage barrier, namespaces reduce path trie lookup depth and allow granular quota enforcement.
