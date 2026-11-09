# Cache Stampede & Hot-Key Protection Analysis 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report analyzes what occurs when popular Cloakit Subject Keys experience cache eviction or cold starts under high concurrency.

---

## 1. Mechanics of a Cache Stampede (Thundering Herd)

### 1.1 The Vulnerability
- **Source Reference:** `sdk/helper/keysutil/lock_manager.go:308-340`
```go
// In GetPolicyWithLockType
p, err := lm.getPolicyFromStorage(ctx, storage, name)
```
1. Suppose Subject Key `sk-corporate-tenant` is accessed by 1,000 concurrent goroutines.
2. The key expires or is purged from `TransitLRU`.
3. 1,000 goroutines experience a cache miss simultaneously.
4. **Existing Guard:** OpenBao utilizes `LockManager` with hashed shard locks. Goroutines requesting the same key acquire `lm.locks[hash(name)].RLock()`.
5. **The Flaw:** When multiple readers miss simultaneously, they all acquire the shared `RLock()`, and **all 1,000 goroutines execute `getPolicyFromStorage` concurrently**.
6. **Result:** 1,000 concurrent database queries hit PostgreSQL for the exact same key row, triggering query queue saturation and connection exhaustion.

---

## 2. Stampede Prevention via Singleflight

To eliminate the thundering herd, OpenBao must adopt `golang.org/x/sync/singleflight`:
```go
type LockManager struct {
    // ...
    sfGroup singleflight.Group
}

func (lm *LockManager) getPolicyCoalesced(ctx context.Context, storage logical.Storage, name string) (*Policy, error) {
    v, err, _ := lm.sfGroup.Do(name, func() (interface{}, error) {
        return lm.getPolicyFromStorage(ctx, storage, name)
    })
    if err != nil {
        return nil, err
    }
    return v.(*Policy), nil
}
```
**Impact:** Collapses 1,000 concurrent cold-start storage queries into exactly **1 database round trip**.
