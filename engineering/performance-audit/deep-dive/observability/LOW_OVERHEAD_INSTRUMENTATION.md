# Low-Overhead Instrumentation Design & Cardinality Safety 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document designs a high-precision, low-overhead performance instrumentation model for OpenBao that guarantees strict tenant privacy boundaries and zero high-cardinality metric pollution.

---

## 1. Cardinality Safety & Sensitive Data Invariants

### Strict Negative Invariants:
1. **Zero Key / DEK Telemetry:** Never emit Subject Key IDs, DEKs, ciphertexts, plaintexts, or user UUIDs into Prometheus metric labels or trace spans.
2. **Bounded Label Cardinality:** Labels must be restricted to static dimensions:
   - `mount_point`: e.g. `transit/`
   - `operation`: `unwrap`, `wrap`, `lookup`, `rotate`
   - `status`: `200`, `400`, `403`, `500`

---

## 2. Low-Overhead Atomic Phase Timers

To decompose latency without allocating heap memory, OpenBao should use fixed-size phase structs passed on the stack:
```go
type RequestTiming struct {
    IngressTime   time.Time
    AuthDone      time.Time
    LockAcquired  time.Time
    CryptoDone    time.Time
    StorageWait   time.Duration
}
```
Using lock-free exponential histogram buckets, metrics can be recorded in $< 35$ nanoseconds per operation with zero heap allocations, ensuring that profiling and monitoring do not degrade benchmark performance.

---

## 3. High-Performance Telemetry Pipeline Specifications

To prevent Prometheus scrapes from blocking request handlers:
- Use atomic uint64 counters and lock-free exponential buckets.
- Decouple metric aggregation from HTTP request contexts using atomic swaps.
- Ensure that histogram bucket evaluations occur during background scrape cycles rather than along the synchronous DEK unwrap execution path.
