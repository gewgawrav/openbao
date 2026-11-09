# Request Queueing Points, PermitPool & Concurrency Architecture 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document analyzes the discrete queueing stages, concurrency bounds, and synchronization bottlenecks within OpenBao when processing Cloakit Privacy Vault requests.

Under high load, an incoming request traverses four cascading queueing checkpoints before cryptographic completion. If any downstream queue fills, upstream requests accumulate, leading to thread and memory exhaustion.

---

## 1. Cascading Queueing Points

```
+------------------------------------------------------------------------------------------------------+
|                                   OPENBAO CASCADING QUEUEING PIPELINE                                |
+------------------------------------------------------------------------------------------------------+
| Queue 1: HTTP Ingress Queue       | net/http listener backlog (SOMAXCONN, default 128-4096)           |
|                                   | Unbounded goroutine spawn per active TCP socket                   |
|                                                  |                                                    |
| Queue 2: Storage PermitPool       | physical.PermitPool (`max_parallel`, default 128)                  |
|                                   | Buffered channel acting as concurrency semaphore                  |
|                                                  |                                                    |
| Queue 3: Database Connection Pool | database/sql connection pool (`db.SetMaxOpenConns(max_parallel)`) |
|                                   | Mutex-guarded connection request queue                            |
|                                                  |                                                    |
| Queue 4: LockManager Shard Locks  | 256 hashed sync.RWMutex instances in LockManager                   |
|                                   | Goroutines block in runtime sema wait queue on key write          |
+------------------------------------------------------------------------------------------------------+
```

---

## 2. Source-Level Queueing Mechanics

### 2.1 The PermitPool Concurrency Limiter
- **Source Reference:** `sdk/physical/permit_pool.go:10-50`, `internal/physical/postgresql/postgresql.go:100-140`
```go
type PermitPool struct {
    sem chan struct{}
}
func (p *PermitPool) Acquire() {
    p.sem <- struct{}{}
}
func (p *PermitPool) Release() {
    <-p.sem
}
```
- OpenBao bounds total concurrent physical storage operations via `PermitPool`.
- Default: `max_parallel = 128`, `transaction_max_parallel = 64`.
- **Behavior under load:** If 500 requests experience cache misses simultaneously, 128 acquire permits while 372 goroutines block waiting on channel send `p.sem <- struct{}{}`.

### 2.2 PostgreSQL Connection Pool (`sql.DB`)
- **Source Reference:** `internal/physical/postgresql/postgresql.go:145-165`
```go
db.SetMaxOpenConns(maxParInt)
```
- OpenBao sets PostgreSQL `MaxOpenConns` strictly equal to `max_parallel`.
- If `max_parallel = 128`, Go will open up to 128 physical TCP sockets to PostgreSQL.
