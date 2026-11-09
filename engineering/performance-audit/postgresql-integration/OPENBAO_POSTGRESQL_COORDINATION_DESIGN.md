# OpenBao–PostgreSQL Coordination Design 🤖

## Executive Summary
Historically, OpenBao and PostgreSQL have been tuned independently without runtime awareness of each other's operational limits. This report designs a holistic coordination architecture uniting Go connection pools, PostgreSQL server capacity, health metrics, and adaptive backpressure.

---

## 1. Unified Sizing Formula

To prevent connection exhaustion and CPU thrashing:

$$	ext{PostgreSQL } 	ext{max\_connections} \ge (	ext{Nodes} 	imes 	ext{max\_parallel}) + 	ext{superuser\_reserved} + 	ext{admin\_headroom}$$

### Recommended Allocation for Cloakit (3 OpenBao Nodes, 8-Core PostgreSQL)
- OpenBao Node 1: `max_parallel = 30`, `max_idle_connections = 30`
- OpenBao Node 2: `max_parallel = 30`, `max_idle_connections = 30`
- OpenBao Node 3: `max_parallel = 30`, `max_idle_connections = 30`
- Total OpenBao pool size: `90` connections.
- Superuser & replication: `15` connections.
- Headroom (PgBouncer/Admin): `45` connections.
- Total PostgreSQL `max_connections`: **150**.

---

## 2. Runtime Coordination Enhancements

### 1. Connection Health & Lifetime Management
OpenBao should expose configuration parameters in `storage "postgresql"`:
```hcl
storage "postgresql" {
  connection_url              = "postgres://..."
  max_parallel                = 30
  max_idle_connections        = 30
  max_connection_lifetime     = "30m"
  max_connection_idle_time    = "5m"
}
```
Mapped to `db.SetConnMaxLifetime(30 * time.Minute)` and `db.SetConnMaxIdleTime(5 * time.Minute)`. This eliminates silent connection drops across NAT gateways and load balancers. [SOURCE-CONFIRMED / CONFIRMED CONFIGURATION GAP]

### 2. Export Pool Telemetry Metrics
Instrument `db.Stats()` into OpenBao's telemetry sink (`metrics.MetricSink`):
- `postgres.pool.open_connections` (`db.Stats().OpenConnections`)
- `postgres.pool.in_use_connections` (`db.Stats().InUse`)
- `postgres.pool.idle_connections` (`db.Stats().Idle`)
- `postgres.pool.wait_count` (`db.Stats().WaitCount`)
- `postgres.pool.wait_duration_ms` (`db.Stats().WaitDuration`)

This gives operators immediate observability into connection pool saturation.
