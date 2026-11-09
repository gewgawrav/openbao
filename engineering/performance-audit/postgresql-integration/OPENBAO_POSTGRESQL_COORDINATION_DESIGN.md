# OpenBao–PostgreSQL Coordination Design 🤖

## Executive Summary

This priority report addresses the architectural decoupling between OpenBao and PostgreSQL. Traditionally, OpenBao and PostgreSQL are deployed and tuned as independent silos without mutual awareness of capacity, resulting in uncoordinated queuing, resource starvation, and latency spikes. We present an end-to-end coordinated system design integrating pool metrics, runtime tuning, adaptive backpressure, and load harmonization tailored to Cloakit Privacy Vault's 95:5 workload.

Evidence classifications: `SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`.

---

## 1. The Independence Dilemma

In the existing codebase (`internal/physical/postgresql/postgresql.go`):
- OpenBao reads static parameters at startup (`max_parallel`, `transaction_max_parallel`, `max_idle_connections`).
- It has no feedback mechanism to observe PostgreSQL's connection pool saturation, query wait queues, replication lag, or CPU saturation.
- If PostgreSQL experiences transient I/O throttling during an uncoordinated WAL checkpoint, OpenBao continues firing up to `max_parallel` concurrent queries until connection timeouts or context deadlines fail.
- Conversely, PostgreSQL has no mechanism to communicate client-side concurrency pressure back to OpenBao's admission controllers.

---

## 2. Coordinated Capacity & Limit Matrix

To operate as a unified, coordinated engine, parameters across OpenBao, PgBouncer, and PostgreSQL must be harmonized based on physical hardware capacity:

| Infrastructure Tier | Control Knob | Uncoordinated Default | Coordinated Value (16-Core DB Host) | Harmonization Mechanism |
|---|---|---|---|---|
| **OpenBao Core** | HTTP In-flight Requests | Unbounded | Bounded (e.g., 2,000) | `listener { http { max_request_duration = "10s" } }` |
| **OpenBao Storage**| `max_parallel` | 128 | 40 | Sets `db.SetMaxOpenConns(40)` |
| **OpenBao Storage**| `max_idle_connections`| Unset (2) | 40 | Eliminates connection allocation churn |
| **OpenBao Storage**| `transaction_max_parallel` | 64 | 20 | Guards connection headroom for HA and reads |
| **Connection Proxy**| PgBouncer `default_pool_size`| N/A (Direct) | 32 | Matches PostgreSQL optimal execution concurrency |
| **PostgreSQL Server**| `max_connections` | 100 | 200 | Accommodates administrative and proxy connections |
| **PostgreSQL Server**| `work_mem` | 4 MB | 16 MB | Prevents on-disk sorting for directory listings |
| **PostgreSQL Server**| `shared_buffers` | 128 MB | 16 GB (25% RAM) | Ensures 100% memory residency for Subject Keys |

---

## 3. Dynamic Telemetry & Pool Observability Architecture

### 3.1 Missing Go `sql.DBStats` Telemetry (`SOURCE-CONFIRMED`)
Go's standard library `database/sql` provides rich real-time pool metrics via `db.Stats()`:
- `MaxOpenConnections`: Limit of open connections.
- `OpenConnections`: Established connections (in-use + idle).
- `InUse`: Connections currently executing queries.
- `Idle`: Connections sitting idle in pool.
- `WaitCount`: Total queries blocked waiting for a connection.
- `WaitDuration`: Total cumulative blocking wait time.
- `MaxIdleClosed`: Connections closed due to `SetMaxIdleConns`.
- `MaxLifetimeClosed`: Connections closed due to `SetConnMaxLifetime`.

**Current Gap**: OpenBao does **not** export `db.Stats()` to its metrics sink (`SOURCE-CONFIRMED`: lines 414, 436, 452 only track `MeasureSince` per query). Operators cannot determine if request latency is caused by PostgreSQL query execution or client-side connection acquisition stalls!

### 3.2 Proposed Telemetry Collector
Implement a lightweight background metrics reporter in `internal/physical/postgresql/postgresql.go`:
```go
func (m *PostgreSQLBackend) startPoolMetricsCollector(interval time.Duration, stopCh <-chan struct{}) {
    ticker := time.NewTicker(interval)
    go func() {
        defer ticker.Stop()
        for {
            select {
            case <-stopCh:
                return
            case <-ticker.C:
                stats := m.client.Stats()
                metrics.SetGauge([]string{"postgres", "pool", "open"}, float32(stats.OpenConnections))
                metrics.SetGauge([]string{"postgres", "pool", "in_use"}, float32(stats.InUse))
                metrics.SetGauge([]string{"postgres", "pool", "idle"}, float32(stats.Idle))
                metrics.IncrCounter([]string{"postgres", "pool", "wait_count"}, float32(stats.WaitCount))
                metrics.IncrCounter([]string{"postgres", "pool", "wait_duration_ms"}, float32(stats.WaitDuration.Milliseconds()))
            }
        }
    }()
}
```

---

## 4. Adaptive Concurrency & Backpressure Design

When PostgreSQL encounters resource exhaustion (e.g. storage I/O spike, lock wait, or vacuum storm):
1. **Saturation Detection**:
   When `stats.InUse == maxParInt` and `stats.WaitDuration` increases beyond 50ms, the storage backend signals saturation.
2. **Graceful Backpressure**:
   OpenBao can apply client-side HTTP backpressure (HTTP 429 / 503 with `Retry-After: 1`) before request queues cascade into PostgreSQL server collapse.
3. **Fencing Check Decoupling**:
   By caching active fence lease validity locally, 100% of write-path verification queries to `openbao_ha_locks` are eliminated, preserving critical database I/O during heavy loads.

---

## 5. Architectural Locus of Optimizations

| Optimization Component | Belongs in OpenBao Core | Belongs in PostgreSQL Backend | Belongs in Deployment / Proxy | Belongs in Cloakit Application |
|---|---|---|---|---|
| `db.Stats()` Telemetry Export | No | **Yes** (`postgresql.go`) | No | No |
| `max_idle_connections` Auto-alignment | No | **Yes** (`postgresql.go`) | No | No |
| Connection Lifetime Management | No | **Yes** (`postgresql.go`) | No | No |
| PgBouncer Transaction Mode | No | No | **Yes** (Kubernetes Sidecar / VM) | No |
| Server Memory & WAL Tuning | No | No | **Yes** (`postgresql.conf`) | No |
| Batch Key Retrieval API | **Yes** (`physical.Backend`) | **Yes** (Multi-row `SELECT`) | No | No |
| Subject Key Metadata Index | No | No | No | **Yes** (Application DB) |
| In-Memory Policy Caching | **Yes** (`TransitLRU`) | No | No | No |

---

## 6. Audit Verdict

OpenBao and PostgreSQL can achieve optimal coordination without radical core redesign. By exposing `sql.DBStats`, aligning `max_idle_connections` with `max_parallel`, deploying PgBouncer in transaction pooling mode, and caching fence lease validity, the combined system operates with predictable P99 latency and robust backpressure resilience.
