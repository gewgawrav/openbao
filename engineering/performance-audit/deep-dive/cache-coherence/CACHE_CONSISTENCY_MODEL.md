# Cache Consistency Model & Lifecycle Synchronization 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document analyzes the consistency guarantees and failure modes of OpenBao's multi-layered caches when keys are updated, rotated, or revoked.

---

## 1. Key Invalidation Dynamics
- **Source Reference:** `sdk/helper/keysutil/lock_manager.go:110-115`
```go
func (lm *LockManager) InvalidatePolicy(name string) {
    if lm.cache != nil {
        lm.cache.Remove(name)
    }
}
```

### 1.1 Local Invalidation Path
When a key policy is rotated or updated on the active node:
1. `LockManager.GetPolicyExclusive` acquires the write lock.
2. `Policy.Rotate` or `Policy.Persist` writes the new version to storage.
3. The cached policy in `lm.cache` is either updated in-place or invalidated.
4. **Consistency within single instance:** Strong Read-After-Write (RAW) consistency is maintained locally through the exclusive shard lock.

---

## 2. Multi-Instance Invalidation Gap (The Split-Brain Risk)

### 2.1 The Distributed Problem
In an active/standby or multi-reader cluster backed by PostgreSQL:
1. Node A (Active) receives a request to revoke or rotate Subject Key `sk-42`.
2. Node A updates PostgreSQL and purges its own local cache (`lm.InvalidatePolicy("sk-42")`).
3. Node B (Standby or Read Replica) holds `sk-42` in its local memory cache (`TransitLRU` or `TransitSyncMap`).
4. **Failure State:** OpenBao provides **NO cross-instance cache invalidation bus** over PostgreSQL storage. Node B continues to unwrap DEKs using the old/revoked key until its cache entry naturally expires or the instance restarts.

```
+-------------------+                                    +-------------------+
|  OpenBao Node A   |                                    |  OpenBao Node B   |
|   (Rotates Key)   |                                    | (Serving Reads)   |
+---------+---------+                                    +---------+---------+
          | 1. Writes New Key                                      |
          v                                                        |
+-------------------+                                              |
|    PostgreSQL     |                                              |
|  (New Key Stored) |                                              |
+-------------------+                                              |
          |                                                        |
          X (No notification bus / no LISTEN-NOTIFY)                |
                                                                   v
                                                         [STALE CACHE HIT!]
                                                         Uses Revoked Key!
```
