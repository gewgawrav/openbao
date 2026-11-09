# Production Performance Dashboard & Bottleneck Diagnostic Guide 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document specifies production monitoring dashboards, alerting rules, and answers the Prompt 7 Final Question.

---

## 1. Production Dashboard Panels

1. **Cryptographic Throughput & Tail Latency:**
   - DEK Unwraps/sec vs Wraps/sec.
   - P50, P95, P99, P99.9 latency histograms.
2. **Cache Health:**
   - `TransitLRU` / `TransitSyncMap` Hit vs Miss Ratio.
   - L1 Cache item count and eviction rate.
3. **Queue & Resource Saturation:**
   - `PermitPool` queue depth and wait duration.
   - PostgreSQL active connection count vs `max_parallel`.
   - Go runtime goroutines and GC pause percentiles.

---

## 2. Final Question Answer

### What minimum instrumentation would allow engineers to identify the exact bottleneck of every major Cloakit key operation?

**To pinpoint the exact bottleneck without performance degradation, OpenBao must expose four discrete, low-cardinality latency phase histograms:**

1. **`vault_transit_queue_wait_seconds`:** Histogram measuring time spent blocked in `PermitPool.Acquire` or shard locks. (Distinguishes resource contention from processing time).
2. **`vault_transit_storage_read_seconds`:** Histogram measuring PostgreSQL query execution time during cache misses. (Pinpoints database disk/index latency).
3. **`vault_transit_crypto_seconds`:** Histogram measuring raw AEAD execution time. (Isolates CPU/assembly saturation).
4. **`vault_transit_cache_hit_ratio`:** Counter pair (`hits_total`, `misses_total`) labeled by mount and operation. (Identifies when tail latency is driven by cold-start database queries).
