# Performance Visibility Gaps & Telemetry Blind Spots 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report identifies critical blind spots in OpenBao's existing telemetry instrumentation that prevent engineers from diagnosing tail latency spikes and storage bottlenecks under production Cloakit workloads.

---

## 1. Major Observability Gaps

### 1.1 Unmeasured Storage Queue Wait Time
- **The Gap:** OpenBao measures `postgres.get` from inside `PostgreSQLBackend.Get`, which starts timing *after* the permit is acquired from `PermitPool`.
- **Blind Spot:** Time spent blocked in `PermitPool.Acquire()` waiting for a free permit is **completely invisible** in telemetry. A request might wait 500 ms in the permit queue and take 2 ms in the database; metrics will only record 2 ms, misleading operators into blaming application code.

### 1.2 Missing Lock Contention Metrics
- OpenBao does not record mutex wait durations for `LockManager` shard locks or `TwoQueueCache.lock`.
- Operators cannot distinguish between CPU saturation and lock serialization when P99 latency degrades under high concurrency.

### 1.3 Lack of PostgreSQL Connection Pool Health
- No gauges for `sql.DB.Stats().WaitDuration`, `InUse`, `Idle`, or `WaitCount`.
- Operators cannot determine whether PostgreSQL connection pool exhaustion is occurring or if connections are sitting idle.

### 1.4 Cryptographic Primitive Timing
- No metrics distinguishing AES-GCM execution time from parameter parsing, `mapstructure` decoding, or Base64 encoding/decoding.
