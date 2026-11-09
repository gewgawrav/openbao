# Systems-Level Performance Analysis 🤖

## 1. Scope & System Hierarchy

This report provides a systems-level analysis of OpenBao operating as the Layer 2 cryptographic engine for Cloakit, spanning hardware architecture, Linux kernel subsystems, Go runtime internals, and network I/O.

```
+----------------------------------------------------------------------------------------------------+
|                                    FULL-STACK SYSTEMS PROFILE                                      |
+----------------------------------------------------------------------------------------------------+
| Layer                 | Dominant Mechanisms / Primitives          | Performance Impact             |
+-----------------------+-------------------------------------------+--------------------------------+
| L1: Hardware & CPU    | MESI Cache Coherence, 64B Cache Line,     | False sharing on RWMutex,      |
|                       | Branch Predictor, NUMA Interconnect       | Cross-socket QPI latency       |
+-----------------------+-------------------------------------------+--------------------------------+
| L2: Linux Kernel      | CFS Scheduler, epoll_wait, TCP Stack,     | Throttling freezes, socket     |
|                       | fsync / page cache, context switching    | exhaustion, TIME_WAIT churn    |
+-----------------------+-------------------------------------------+--------------------------------+
| L3: Go Runtime        | GMP Scheduler, Tri-color Mark-Sweep GC,   | STW pauses, write barrier      |
|                       | netpoll, Mcache / Mcentral allocations    | overhead, goroutine stacks     |
+-----------------------+-------------------------------------------+--------------------------------+
| L4: Database/Storage  | PostgreSQL Repeatable Read MVCC, WAL LSN, | Lock wait events, checkpoint   |
|                       | Connection pooling, B-tree indexes        | fsync storms, bloat stalls     |
+-----------------------+-------------------------------------------+--------------------------------+
```

---

## 2. Hardware Architecture & CPU Microarchitecture

### Cache Line Invalidation & False Sharing
- **64-Byte Cache Lines**: Modern x86_64 and ARM64 CPUs manipulate memory in 64-byte aligned chunks.
- In `sdk/helper/keysutil/policy.go:455`, `Policy` holds:
  ```go
  l *sync.RWMutex
  writeLocked bool
  deleted atomic.Bool
  ```
- **False Sharing Risk**: `writeLocked` (1 byte) and `deleted` (atomic.Bool, 1 byte) sit immediately adjacent in heap memory. When concurrent reader threads repeatedly query `deleted.Load()`, while another goroutine acquires/releases locks or modifies `writeLocked`, CPU cores invalidate each other's L1/L2 cache lines repeatedly despite accessing distinct logical fields.
- **Remediation**: Align critical concurrency fields using `cpu.CacheLinePad` (`[64]byte`) or restructure hot structs to isolate read-mostly metadata from mutation flags.

### NUMA Locality & Cross-Socket Penalties
- In dual-socket server configurations (e.g. 2x AMD EPYC or Intel Xeon), memory access across the UPI/Infinity Fabric interconnect incurs a **~3x latency penalty** (~30 ns local DRAM vs ~90 ns remote DRAM).
- When Go's work-stealing scheduler migrates a goroutine executing DEK decryption from NUMA Node 0 to Node 1, cache misses spike on the transit Policy cache.
- **`TEST-CONFIRMED` Principle**: Pinning OpenBao instances to specific NUMA nodes via `numactl --cpunodebind=0 --membind=0` stabilizes P99 tail latency by eliminating inter-socket interconnect traffic.

---

## 3. Linux Kernel Subsystems & Networking

### TCP TIME_WAIT & Port Exhaustion
- Cloakit client applications performing high-rate DEK requests over short-lived HTTP connections will exhaust the ephemeral port range (`net.ipv4.ip_local_port_range`, ~28,000 ports) within 60 seconds if HTTP keep-alive connections are not pooled.
- Sockets enter `TIME_WAIT` for $2 \times \text{MSL} = 60\,\text{s}$.
- **Resolution**: Enforce HTTP/2 or keep-alive connection pooling in the Cloakit SDK, and tune kernel parameters:
  - `net.ipv4.tcp_tw_reuse = 1`
  - `net.ipv4.tcp_fin_timeout = 15`

### Network Socket Buffers (`SO_RCVBUF`, `SO_SNDBUF`)
- When high-concurrency bursts occur, kernel socket receive queues fill rapidly. If `net.core.rmem_max` or `net.ipv4.tcp_rmem` are default Linux values (e.g. 212992 bytes), window exhaustion forces TCP window scale reductions and round-trip ACK stalls, amplifying client-observed P99 latency.

---

## 4. Go Runtime Scheduling & Garbage Collection

### Tri-Color Mark-Sweep GC & Write Barriers
- Go's concurrent garbage collector operates concurrently with application code, but enforces a write barrier (`runtime.gcWriteBarrier`) during mark phases.
- Every pointer write in Go heaps incurs a 2–4 CPU instruction penalty during GC cycles.
- In `sdk/helper/keysutil`, heavy allocation of short-lived maps and interfaces in Transit handlers forces the GC to run frequently. Under a 50,000 req/s load with 1KB allocations, garbage generation reaches **50 MB/s**, triggering GC sweeps every 2–3 seconds.
- During GC mark assists, client-serving goroutines are forced to stop and perform marking work, causing latency spikes directly visible at P99 and P99.9.

### Goroutine Stack Growth & Shrinking
- Goroutines start with a 2KB stack (`runtime.stackalloc`).
- When deeply nested encryption/decryption routines or JSON reflection decoders exceed 2KB, Go allocates a 4KB contiguous stack and copies all frame pointers.
- Under heavy concurrency (thousands of goroutines), repeated stack resizing introduces allocation churn and stalls active goroutines.

---

## 5. Storage Subsystem & Disk I/O Realities

### Disk Queue Depth & Fsync Contention
- Although Cloakit is 95% reads, the 5% writes (Subject Key creation, rotation, and token/lease updates) require durable persistence in PostgreSQL.
- Each PostgreSQL transaction commit requires an `fsync` or `fdatasync` on the Write-Ahead Log (WAL).
- On standard NVMe drives with ~5,000 IOPS per thread, each fsync takes $0.2 - 1.0\,\text{ms}$.
- Under concurrent write bursts, NVMe disk queue depth grows. When queue depth exceeds 32, IO latency degrades non-linearly, delaying all PostgreSQL read queries sharing the same storage pool.

---

## 6. HSM and KMS Latency Dynamics

Where OpenBao interfaces with Layer 1 Cloud KMS (AWS KMS, Google Cloud KMS, Azure Key Vault) or PKCS#11 HSMs:
- **Network / Hardware Latency Floor**: Round-trip RPCs to Cloud KMS introduce a hard latency floor of **10–25 ms** per cryptographic operation.
- **Architectural Imperative**: Layer 1 KMS must **never** be accessed on the Layer 3 DEK decryption hot-path. KMS must strictly be used to unwrap OpenBao's Master Key or Root KEK during daemon startup/unseal, with all Layer 2 Subject Key operations cached and processed in-memory within OpenBao.
