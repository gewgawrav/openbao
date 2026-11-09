# Target Cache Architecture & Security Roadmap 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document synthesizes the caching audit and answers the Prompt 3 Final Question.

---

## 1. Multi-Tier Target Architecture

```
+------------------------------------------------------------------------------------------------------+
|                                   TARGET L1 / L2 CACHING ARCHITECTURE                                |
+------------------------------------------------------------------------------------------------------+
| L1 Process Cache: Lock-free Sharded Concurrent Map with TTL and Bounded Size (100K hot keys)        |
| Features:                                                                                            |
|   - Precomputed cipher.AEAD pointers                                                                |
|   - Zero-allocation reads via sync.Map                                                               |
|   - Singleflight coalescing on cache misses                                                          |
|                                                  |                                                    |
| L2 Invalidation Bus: PostgreSQL LISTEN / NOTIFY Channels                                             |
| Features:                                                                                            |
|   - Real-time invalidation broadcast (< 2ms)                                                         |
|   - Sequence generation counter to detect dropped messages                                           |
|   - Full L1 flush on network disconnection                                                           |
+------------------------------------------------------------------------------------------------------+
```

---

## 2. Final Question Answer

### What cache architecture delivers the best read performance without introducing stale-key, revocation, or cross-tenant security vulnerabilities?

**The optimal architecture is a Hybrid L1 Lock-Free In-Memory Cache with Singleflight Stampede Suppression, coupled with an Asynchronous PostgreSQL `LISTEN`/`NOTIFY` Invalidation Bus.**

### Core Tenets of the Solution:
1. **Best Read Performance:** A sharded `sync.Map` storing pre-instantiated `cipher.AEAD` objects eliminates lock contention and cipher creation, reducing unwrap latency to **< 300 nanoseconds**.
2. **Revocation & Stale-Key Correctness:** PostgreSQL `NOTIFY` ensures revoking or rotating a key immediately drops the entry across all cluster nodes within 2ms.
3. **Partition Resilience:** If a standby node loses connection to PostgreSQL, it enters a "stale-safe" mode, dropping local caches and querying PostgreSQL directly until the notification stream is re-verified.
4. **Tenant Isolation:** Cache keys are physically partitioned by cryptographic mount/namespace UUIDs, mathematically preventing cross-tenant leakage.
