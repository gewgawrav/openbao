# Current Cache Implementation & In-Memory Storage Audit 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report analyzes OpenBao's existing in-memory caching mechanisms across the Transit engine (`sdk/helper/keysutil/`) and physical storage layer (`sdk/physical/cache.go`).

In Cloakit's Privacy Vault workload (95% reads, 5% writes), caching is the primary architectural barrier preventing PostgreSQL saturation. However, the current cache implementation suffers from structural lock contention, inconsistent cache sizing behaviors, and lack of cross-instance synchronization.

---

## 1. Transit Policy Caching Mechanisms

### 1.1 LockManager and Cache Selection
- **Source Reference:** `sdk/helper/keysutil/lock_manager.go:116-135`
```go
func (lm *LockManager) InitCache(cacheSize int) error {
    lm.cacheSize = cacheSize
    if cacheSize > 0 {
        cache, err := NewTransitLRU(cacheSize)
        lm.cache = cache
    } else if cacheSize == 0 {
        lm.cache = NewTransitSyncMap()
    }
}
```
- When `cacheSize > 0`: OpenBao initializes `TransitLRU`.
- When `cacheSize == 0`: OpenBao initializes `TransitSyncMap` (unbounded in-memory concurrent map).
- When `cacheSize < 0`: Caching is completely disabled.

### 1.2 `TransitLRU` (TwoQueueCache)
- **Source Reference:** `sdk/helper/keysutil/transit_lru.go:13-65`
- Implemented using HashiCorp's `golang-lru/v2/simplelru.TwoQueueCache`.
- **Lock Contention:** `TwoQueueCache.Get` acquires an exclusive lock (`c.lock.Lock()`) on every access to move accessed items between the recent and frequent queues.
- Under high concurrency, 64-128 goroutines requesting cryptographic unwrap serialize on this single mutex.

### 1.3 `TransitSyncMap`
- **Source Reference:** `sdk/helper/keysutil/transit_syncmap.go:14-45`
- Wraps Go's standard library `sync.Map`.
- Eliminates mutex lock contention on reads (`Load` is lock-free).
- **Limitation:** Does not enforce any size limit or TTL. Under 10M keys, memory grows unboundedly.

---

## 2. Physical Storage Backend Cache
- **Source Reference:** `sdk/physical/cache.go:30-110`
OpenBao wraps physical backends (including PostgreSQL) with an in-memory transactional cache (`TransactionalCacheBackend`).
- **Capacity:** Default size is `DefaultCacheSize = 128 * 1024` entries (128K entries).
- **Behavior:** Stores barrier-encrypted raw `physical.Entry` slices.
- Caches raw encrypted bytes; decryption must still occur across the barrier.
