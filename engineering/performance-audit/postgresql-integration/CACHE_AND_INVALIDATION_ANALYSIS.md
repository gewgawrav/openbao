# Cache and Invalidation Analysis 🤖

## Executive Summary

This report performs a comprehensive audit of OpenBao's caching layers, specifically evaluating the physical storage LRU cache in `sdk/physical/cache.go`, the Transit Secret Engine policy cache in `sdk/helper/keysutil/lock_manager.go`, cache invalidation mechanisms, and cross-node cache coherence across OpenBao primary and standby instances.

Strict Security Mandate: Caching must never bypass authentication, authorization, token revocation, or cryptographic key version checks.

Evidence classifications: `SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`.

---

## 1. OpenBao Dual-Layer Caching Architecture

OpenBao employs two distinct caching layers relevant to Cloakit's workload:

```
[ HTTP / gRPC Client Request: Encrypt / Decrypt / Lookup ]
                         │
                         ▼
┌─────────────────────────────────────────────────────────┐
│ Layer 1: Transit Engine LockManager Policy Cache        │
│ (`sdk/helper/keysutil/lock_manager.go`)                 │
│ - Stores decoded, decrypted `*keysutil.Policy` objects  │
│ - Implementation: `TwoQueueCache` or `sync.Map`         │
└────────────────────────┬────────────────────────────────┘
                         │ Cache Miss
                         ▼
┌─────────────────────────────────────────────────────────┐
│ Layer 2: Core Physical LRU Cache                        │
│ (`sdk/physical/cache.go`)                               │
│ - Stores encrypted raw barrier `*physical.Entry` blobs   │
│ - Implementation: `lru.TwoQueueCache` (Default: 128k)   │
└────────────────────────┬────────────────────────────────┘
                         │ Cache Miss
                         ▼
┌─────────────────────────────────────────────────────────┐
│ PostgreSQL Physical Storage Backend                     │
│ (`internal/physical/postgresql/postgresql.go`)          │
└─────────────────────────────────────────────────────────┘
```

---

## 2. In-Depth Evaluation of Physical Cache (`sdk/physical/cache.go`)

### 2.1 Cache Characteristics
- **Algorithm**: `lru.TwoQueueCache[string, *Entry]` (`SOURCE-CONFIRMED`: line 98). TwoQueue (2Q) provides scan-resistance, preventing large directory listings from evicting hot keys.
- **Default Size**: `128 * 1024` entries (131,072 entries) (`DefaultCacheSize` in `sdk/physical/physical.go:13`).
- **Striped Locking**: Protected by 256 striped read-write locks (`locksutil.CreateLocks()`) to minimize mutex contention under parallel reads (`SOURCE-CONFIRMED`: line 100).
- **Exceptions**: Certain paths are explicitly excluded from caching via `cacheExceptionsPaths`:
  `sys/expire/`, `core/raft/tls`, `core/seal-config`, `core/recovery-config` (`SOURCE-CONFIRMED`: lines 28-33). Transit keys (`transit/keys/...`) are fully eligible for caching.

### 2.2 Cache Behavior on Read (`Get`)
In `sdk/physical/cache.go:197-227`:
```go
func (c *cache) Get(ctx context.Context, key string) (*Entry, error) {
    if !c.ShouldCache(key) {
        return c.backend.Get(ctx, key)
    }

    lock := locksutil.LockForKey(c.locks, key)
    lock.RLock()
    defer lock.RUnlock()

    if !cacheRefreshFromContext(ctx) {
        if raw, ok := c.lru.Get(key); ok {
            if raw == nil { return nil, nil }
            c.metricSink.IncrCounter([]string{"cache", "hit"}, 1)
            return raw, nil
        }
    }

    c.metricSink.IncrCounter([]string{"cache", "miss"}, 1)
    ent, err := c.backend.Get(ctx, key)
    if err != nil { return nil, err }

    // Cache the result, even if nil (Negative Caching!)
    c.lru.Add(key, ent)
    return ent, nil
}
```
- **Crucial Finding (`SOURCE-CONFIRMED`)**:
  1. **Zero Database Round Trips on Cache Hit**: If a key exists in `c.lru`, **no PostgreSQL read is performed**! The call returns immediately in memory.
  2. **Negative Caching Supported**: If the key does not exist (`ent == nil`), OpenBao caches the `nil` result! Repeated probes for non-existent Subject Keys hit the cache and do **not** query PostgreSQL.

### 2.3 Transactional Cache Cloning (`cache.cloneWithStorage`)
In `sdk/physical/cache.go:250-298`:
- When a transaction begins (`BeginTx` or `BeginReadOnlyTx`), OpenBao instantiates a localized, private cache:
  ```go
  cacheCopy := newCache(b, c.size/TransactionCacheFactor, c.logger, c.metricSink).(*cache)
  ```
- **Performance Impact**: Transactional reads start with an **empty** local cache! Any read performed inside a transaction bypasses the parent cache and queries PostgreSQL.
- **Finding for Cloakit**: Subject Key lookups and DEK operations must use standard non-transactional reads (`Get`) to benefit from the global shared 128k LRU cache.

---

## 3. Cache Invalidation & Cross-Instance Consistency

### 3.1 Single-Node Write-Through Invalidation
- When `Put(ctx, entry)` or `Delete(ctx, key)` succeeds:
  - `Put` writes to PostgreSQL, then stores the clone into `c.lru` (`Write-Through`) (`SOURCE-CONFIRMED`: lines 180-192).
  - `Delete` deletes from PostgreSQL, then calls `c.lru.Remove(key)` (`SOURCE-CONFIRMED`: lines 242-244).
  - `cacheTransaction.Commit` invalidates modified keys in the parent cache (`lines 364-377`).

### 3.2 Cross-Node Invalidation (Active Primary to Standbys)
- PostgreSQL does not natively push row mutation notifications across connection boundaries reliably.
- In OpenBao, cross-node cache invalidation is handled via gRPC:
  In `internal/vault/core.go:1072-1085`:
  ```go
  if haEnabled && shouldUseGRPCInvalidation(phys) {
      c.logger.Info("enabling grpc-based invalidation on HA backend which doesn't natively support invalidation notifications")
      phys = physical.NewWriteNotifier(phys, grpcLogger, c.SendInvalidationNotice)
  }
  ```
- When the active node writes to storage, `WriteNotifier` sends an invalidation gRPC message to all connected standby nodes.
- Standby nodes immediately evict the modified key from their local `c.lru` cache (`SOURCE-CONFIRMED`).

---

## 4. Subject Key Metadata Index Placement: OpenBao vs Cloakit

A critical architectural question: *Should an additional Subject Key metadata index belong in OpenBao or Cloakit?*

1. **In OpenBao Core / Physical Backend**:
   - Would require modifying the generic key-value schema (`openbao_kv_store`) to parse and expose Transit-specific JSON attributes.
   - Violates the clean separation of concerns in OpenBao's storage barrier architecture.
2. **In Cloakit Application Layer**:
   - Cloakit maintains tenant IDs, subject metadata, and key ownership mappings in its own application database.
   - Cloakit references keys by deterministic path (`transit/keys/tenant-{id}/subject-{id}`).
   - OpenBao's Transit LRU and physical 2Q caches provide sub-millisecond retrieval of the key by this path.
- **Architectural Verdict**: The Subject Key index belongs **in Cloakit**. OpenBao should remain the high-speed cryptographic execution engine.

---

## 5. Audit Verdict

OpenBao's physical cache is highly sophisticated: scan-resistant (2Q), thread-safe (striped locks), supports negative caching, and incurs 0 database reads on cache hits. Cross-node coherence is maintained via gRPC write notifications. The primary consideration is ensuring Cloakit's DEK read path avoids unnecessary transaction wrappers so that it consistently hits the warm global cache.
