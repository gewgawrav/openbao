# Vacuum, Bloat & Write Amplification Analysis 🤖

## Executive Summary
This report analyzes how OpenBao's storage write patterns affect PostgreSQL table and index bloat, dead tuple accumulation, WAL volume, and autovacuum maintenance under Cloakit's 95:5 privacy vault workload.

---

## 1. Dead Tuple Generation Dynamics

In Cloakit's workload:
- 95% of operations are read-only (`Get`) for Subject Keys and DEK wrapping/unwrapping.
- 5% of operations are writes:
  1. Creation of new Subject Keys (`Put` -> `INSERT`).
  2. Rotation of Subject Keys (`Put` -> `ON CONFLICT UPDATE`).
  3. Key revocation or expiry (`Delete` -> `DELETE`).
  4. HA active lock renewals (`UPDATE openbao_ha_locks`).

### HA Locks Bloat
- In `internal/physical/postgresql/postgresql.go:668`, `periodicallyRenewLock` renews the leader lock every **5 seconds**:
  ```sql
  UPDATE openbao_ha_locks
  SET (ha_identity, ha_key, ha_value, valid_until) = ($1, $2, $3, NOW() + $4 * INTERVAL '1 seconds')
  WHERE (ha_identity = $1 AND ha_key = $2)
  ```
- This single row is updated 12 times per minute, 720 times per hour, 17,280 times per day!
- Without aggressive autovacuuming, `openbao_ha_locks` accumulates dead tuples rapidly.
- Because `openbao_ha_locks` has only 1 row, PostgreSQL autovacuum with default scale factor `0.20` + threshold `50` will only vacuum after 50 dead tuples (~4 minutes). Setting `autovacuum_vacuum_threshold = 10` on `openbao_ha_locks` ensures continuous cleaning. [SOURCE-CONFIRMED / STATIC-ANALYSIS-CONFIRMED]

---

## 2. Table and Index Bloat Diagnostics

To inspect table and index bloat on the live OpenBao database, operators should execute:
```sql
SELECT
    schemaname,
    relname,
    n_live_tup,
    n_dead_tup,
    round(100.0 * n_dead_tup / nullif(n_live_tup + n_dead_tup, 0), 2) AS dead_tuple_pct,
    last_vacuum,
    last_autovacuum
FROM pg_stat_user_tables
WHERE relname IN ('openbao_kv_store', 'openbao_ha_locks');
```

And for index usage:
```sql
SELECT
    relname,
    indexrelname,
    idx_scan,
    idx_tup_read,
    idx_tup_fetch
FROM pg_stat_user_indexes
WHERE relname IN ('openbao_kv_store', 'openbao_ha_locks');
```

---

## 3. Recommended Maintenance Configuration

Apply table-level overrides on `openbao_ha_locks`:
```sql
ALTER TABLE openbao_ha_locks SET (
    autovacuum_vacuum_threshold = 20,
    autovacuum_vacuum_scale_factor = 0.0,
    autovacuum_vacuum_cost_delay = 0
);
```
This guarantees that `openbao_ha_locks` never exceeds a few disk pages despite thousands of lock updates daily.
