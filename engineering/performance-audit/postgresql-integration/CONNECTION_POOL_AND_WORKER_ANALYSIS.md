# Connection Pool & Worker Management Analysis 🤖

## Executive Summary
This report analyzes connection management in OpenBao's PostgreSQL backend, examining `database/sql` interaction, connection pool sizing, connection lifecycle parameters, worker scheduling, and interaction with external proxies like PgBouncer.

---

## 1. Pool Parameters and Go `database/sql` Dynamics

### Code Configuration (`internal/physical/postgresql/postgresql.go`)
```go
// Line 171
db.SetMaxOpenConns(maxParInt)

// Line 173-174
if maxIdleConnsIsSet {
    db.SetMaxIdleConns(maxIdleConns)
}
```
[SOURCE-CONFIRMED]

### Defaults vs Production Reality
1. `max_parallel` (`maxParInt`):
   - Default: `128` (`physical.DefaultParallelOperations`).
   - If an operator deploys 5 OpenBao instances with default configuration, the combined peak connection demand is `5 * 128 = 640` connections.
   - Default PostgreSQL `max_connections` is typically `100`. This leads directly to `FATAL: remaining connection slots are reserved for non-replication superuser connections` or `FATAL: sorry, too many clients already`. [SOURCE-CONFIRMED / INFERRED]
2. `max_idle_connections`:
   - Default: **Unset** in OpenBao.
   - When unset, Go's `database/sql` applies `defaultMaxIdleConns = 2`.
   - **Severe Flaw**: Under high burst concurrency (e.g., 100 concurrent Cloakit Subject Key lookups), `sql.DB` opens up to 100 connections. When the burst finishes, 98 connections are immediately closed and destroyed! On the next request burst, 98 new TCP handshakes + TLS negotiations + PostgreSQL backend processes must be spawned. [SOURCE-CONFIRMED / CONFIRMED CONFIGURATION GAP]
3. `conn_max_lifetime` & `conn_max_idle_time`:
   - Both are completely omitted from OpenBao configuration.
   - Go keeps idle connections alive indefinitely.
   - If a cloud load balancer (e.g. AWS Network Load Balancer with 350s idle timeout) or firewall drops silent idle TCP sessions, OpenBao's pooled connection dies silently. The next query using that connection encounters an I/O timeout or broken pipe (`read: connection reset by peer`), requiring retries and spiking P99 latency. [SOURCE-CONFIRMED / CONFIRMED CONFIGURATION GAP]

---

## 2. Multi-Tier Concurrency & Limit Alignment

The concurrency chain consists of 6 distinct layers:
```
[ Cloakit Client Concurrency ]
              │
              ▼
[ OpenBao HTTP Listener Concurrency ]
              │
              ▼
[ OpenBao txnPermitPool (Default: 64) ]  <-- Only for transactions
              │
              ▼
[ Go sql.DB Pool (max_parallel, Default: 128) ]
              │
              ▼
[ (Optional) PgBouncer Pool (pool_size, max_client_conn) ]
              │
              ▼
[ PostgreSQL max_connections (PostgreSQL Server) ]
```

### Resource Starvation & Deadlock Risk
In `postgresql.go:146-149`:
```go
if txnMaxParInt >= maxParInt && maxParInt >= 3 {
    // Leave one for lock renewal.
    txnMaxParInt = maxParInt - 1
}
```
OpenBao enforces that transactional operations cannot consume all connections in `sql.DB`, reserving at least 1 connection for HA lock renewal (`periodicallyRenewLock`).
However:
- If non-transactional operations (`Get`, `Put`, `List`) saturate all `max_parallel` connections, the HA lock renewal goroutine (`periodicallyRenewLock` calling `writeItem` with timeout `10s`) will wait for a connection in Go's `sql.DB` free queue.
- If wait time exceeds the renewal deadline (`PostgreSQLLockTTLSeconds * 2/3 = 10s`), the node loses leader status and terminates leader operations! [SOURCE-CONFIRMED / INFERRED]

---

## 3. PgBouncer: Session vs Transaction Pooling

When pairing OpenBao with PgBouncer:
- **Session Pooling**: Fully compatible, but retains connections on the server per client session, reducing scaling effectiveness.
- **Transaction Pooling**:
  - OpenBao uses `Jackc/pgx/v5/stdlib`. When `pgx` issues prepared statements, transaction pooling can conflict if server-side prepared statements are enabled (`statement_cache_mode = describe` or unnamed statements).
  - OpenBao's transactional backend opens explicit transactions (`BEGIN ... COMMIT / ROLLBACK`), which are strictly supported by PgBouncer transaction pooling.
  - However, HA locks use session-independent timestamps (`NOW() + interval`) and explicit keys, making them compatible with transaction pooling.
  - **Recommendation**: Deploy PgBouncer in **transaction mode** with `max_prepared_statements = 0` (or `pgx` parameter `default_query_exec_mode=exec` or `simple_protocol`).

---

## 4. Coordinated Sizing Recommendations for Cloakit (95:5)

For a Cloakit cluster (3 OpenBao nodes, 8 vCPU PostgreSQL):
- `max_parallel`: Set to `30` per node (`3 * 30 = 90` max active connections).
- `max_idle_connections`: Set equal to `max_parallel` (`30`). Eliminates connection thrashing under burst reads.
- `transaction_max_parallel`: Set to `10` per node (Cloakit is 95% reads; write transactions are infrequent).
- Connection Lifetimes:
  - Add `max_connection_lifetime = "30m"`
  - Add `max_connection_idle_time = "5m"`
- PostgreSQL `max_connections`: Set to `150` (`90` OpenBao conns + `20` superuser/maintenance + `40` margin).
