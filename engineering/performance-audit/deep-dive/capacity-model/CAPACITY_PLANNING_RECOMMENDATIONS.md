# Capacity Planning Recommendations & Evolution Triggers 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document outlines architectural evolution triggers and answers the Prompt 8 Final Question.

---

## 1. Evolution Triggers Across Cardinality Tiers

1. **Trigger Tier 1: 500,000 Subject Keys**
   - *Action:* Migrate from `TransitLRU` to `TransitSyncMap` (or sharded bounded map).
   - *Reason:* LRU mutex contention begins inflating P99 tail latency.
2. **Trigger Tier 2: 2,000,000 Subject Keys**
   - *Action:* Introduce PostgreSQL Table Partitioning (16 hash partitions) and tune `fillfactor = 85`.
   - *Reason:* Prevent B-tree depth from reaching 4 levels; preserve HOT update performance.
3. **Trigger Tier 3: 5,000,000 Subject Keys**
   - *Action:* Deploy Envoy / Consistent Hashing front-ends to partition tenant caches across OpenBao nodes.
   - *Reason:* Avoid having every OpenBao instance cache the full 10M key universe in RAM.

---

## 2. Final Question Answer

### What resources will become the first bottleneck as Cloakit grows from thousands to millions of Subject Keys, and how should the architecture evolve?

**The first bottleneck is NOT CPU or Storage Capacity; it is the PostgreSQL Connection Pool & Shared Buffer Cache during cold starts and cache invalidation surges.**

### Architectural Evolution Roadmap:
1. **At < 500K Keys:** Current architecture with in-memory caching is completely sufficient.
2. **At 1M - 5M Keys:** The primary bottleneck becomes database connection contention (`ProcArrayLock`) if `max_parallel` is set high across multiple instances. Reduce `max_parallel` to 32 and enable PostgreSQL `LISTEN`/`NOTIFY` cache invalidation.
3. **At 10M+ Keys:** The heap size (15+ GB) exceeds typical PostgreSQL `shared_buffers`, causing cache misses to trigger physical disk reads. Cloakit must evolve to **Hash-Partitioned PostgreSQL Storage** and **Tenant-Aware Consistent Routing** across OpenBao instances to guarantee that hot working sets fit entirely in RAM.
