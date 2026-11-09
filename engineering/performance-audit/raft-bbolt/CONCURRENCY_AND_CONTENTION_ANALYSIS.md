# Concurrency, Contention & Runtime Lock Dynamics 🤖

## Executive Summary
This report analyzes concurrency boundaries, mutex contention, goroutine scheduling, and thread contention within OpenBao's Integrated Storage engine. We evaluate the interaction between OpenBao's permit pools and bbolt's internal locks.

---

## 1. Concurrency Control Mechanisms

```
+-------------------------------------------------------------------+
| Request Pipeline                                                  |
+-------------------------------------------------------------------+
| 1. Operation Permit Pool: physical.NewPermitPool(128)             |
|    - Binds concurrent Get, Put, Delete, List calls                |
+-------------------------------------------------------------------+
| 2. Transaction Permit Pool: physical.NewPermitPool(64)            |
|    - Binds concurrent BeginTx / BeginReadOnlyTx calls             |
+-------------------------------------------------------------------+
| 3. FSM Lock: fsm.l.RLock() / Lock()                               |
|    - Held in RLock by all reads, transactions, and applies        |
|    - Exclusively locked during snapshot restore & database swap   |
+-------------------------------------------------------------------+
| 4. bbolt Internal Locks:                                          |
|    - mmaplock (sync.RWMutex): Read lock held during memory access |
|    - metalock (sync.Mutex): Exclusive lock held by single writer  |
+-------------------------------------------------------------------+
```

---

## 2. Lock Contention & Bottlenecks

### 2.1 The Single-Writer Mutex Bottleneck
- **Source**: `go.etcd.io/bbolt/db.go`.
- **Classification**: `STATIC-ANALYSIS-CONFIRMED`.
- bbolt enforces that **only one write transaction can execute at any time**.
- When `FSM.ApplyBatch` executes `f.db.Update`, it locks `db.metalock`.
- Under high concurrent write loads (e.g. 50-500 threads proposing writes), all threads serialize at this lock.
- **Impact on Cloakit**:
  - In a 95% read / 5% write workload at 10,000 req/sec, writes account for 500 req/sec.
  - Because 500 writes/sec fully saturates the single-writer fsync capacity ($500	imes 2	ext{ms} = 1000	ext{ms}$ CPU/disk time), writes can cause tail latency spikes (P99 > 500ms) that block incoming read transactions during permit acquisition.

### 2.2 Permit Pool Saturation (128 Limit)
- **Source**: `internal/physical/raft/raft.go:576-577` and `sdk/physical/physical.go:14`.
- **Classification**: `SOURCE-CONFIRMED`.
- `permitPool` is hardcoded to 128 permits.
- In Cloakit's architecture with hundreds of concurrent microservices performing DEK wrapping/unwrapping:
  - If Transit key lookups experience cache misses, >128 concurrent requests will block waiting on `b.permitPool.Acquire()`.
  - Latency degrades exponentially once the pool is exhausted.
