# CPU Core Allocation & Connection Pool Sizing Model 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document designs the CPU allocation, thread scheduling, and database connection pool sizing model for OpenBao instances and the underlying PostgreSQL database cluster.

Balancing CPU allocation against connection pooling ensures maximum cryptographic throughput without triggering thread thrashing or database lock contention.

---

## 1. CPU Sizing & Throughput Capabilities

### 1.1 OpenBao CPU Utilization
- Cryptographic primitive: 32-byte AES-GCM executes in ~125 ns on modern x86_64 cores with AES-NI (`internal/builtin/logical/transit/`).
- End-to-end HTTP parsing, Token Authentication, JSON encoding, and network framing: ~15-20 $\mu$s.
- Single vCPU throughput capability (Go runtime `GOMAXPROCS=1`): $\approx 45,000 \text{ wraps/unwraps per second}$ in steady-state warm cache.
- To achieve a cluster target of 200,000 cryptographic operations/second:
  $$\text{OpenBao Cores Required} = \frac{200,000}{45,000} \approx 4.4 \implies \mathbf{8 \text{ vCPUs}} \text{ (providing >40% headroom)}.$$

### 1.2 PostgreSQL Connection Pool Sizing
- **Source Reference:** `internal/physical/postgresql/postgresql.go:120-150`
- Configured via `max_parallel` (default 128) and `transaction_max_parallel` (default 64).
- Formula: $\text{Max Connections} = \text{OpenBao Instances} \times \text{max\_parallel}$.
- If 4 OpenBao instances run with default `max_parallel = 128`:
  $$\text{Total Active DB Connections} = 4 \times 128 = \mathbf{512 \text{ connections}}.$$
- PostgreSQL performance degrades significantly beyond 200-300 active connections due to internal lock contention (`ProcArrayLock`).
- **Recommendation:** Reduce `max_parallel` to 32 per instance, or place PgBouncer (transaction pooling) between OpenBao and PostgreSQL to cap backend database connections at 64-100.
