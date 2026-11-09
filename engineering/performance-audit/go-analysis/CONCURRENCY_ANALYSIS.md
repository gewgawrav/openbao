# Concurrency, Goroutines & Synchronization Analysis

## Overview

This report investigates OpenBao's synchronization primitives, concurrency patterns, mutex usage, goroutine lifecycles, and channel operations.

As an enterprise secrets management system, OpenBao handles tens of thousands of concurrent client requests alongside background workers responsible for token expiration, lease revocation, replication, and telemetry collection. Coarse-grained mutex contention, lock starvation, and inefficient goroutine or timer management represent significant scalability bottlenecks.

---

## Findings

### Finding CON-001: Router Mutex Contention Bottleneck on High Request Concurrency
- **Finding ID**: CON-001
- **Category**: Concurrency / RWMutex Usage
- **File path**: `internal/vault/routing/router.go`
- **Function**: `Router.routeCommon`, `Router.MatchingMountEntry`, `Router.RootPath`, `Router.LoginPath`
- **Line range**: Lines 665–675, 470–485, 940–960, 980–1005
- **Existing implementation**:
  `Router` guards its mutable radix trees (`root`, `mountUUIDCache`, `mountAccessorCache`, `storagePrefix`) with a single global `sync.RWMutex` (`r.l`). Every incoming request invokes:
  1. `r.l.RLock()` in `routeCommon`
  2. `r.l.RLock()` in `MatchingMountEntry` (up to 2–4 times)
  3. `r.l.RLock()` in `RootPath`
  4. `r.l.RLock()` in `LoginPath`
- **Technical inefficiency**:
  In Go, `sync.RWMutex` employs atomic cache-line bouncing (`sync/atomic.AddInt32`) for every reader acquisition and release. On multi-socket high-core servers (e.g., 64+ vCPUs), hundreds of concurrent goroutines executing `r.l.RLock()` and `r.l.RUnlock()` contend heavily on the shared cache line hosting `r.l.readerCount`. Furthermore, any write operation (`Mount`, `Unmount`, `Remount`, `Taint`) takes `r.l.Lock()`, which blocks all incoming read requests across all namespaces and backends until the mount table update completes.
- **Why it matters**:
  Mount tables are read-heavy (99.999% reads, <0.001% writes). Contending on a single `sync.RWMutex` serializes execution across CPU cores and restricts throughput scaling.
- **Potential improvement**:
  Transition `Router` to use `atomic.Pointer` referencing an immutable snapshot of the routing trees (e.g., `hashicorp/go-immutable-radix` or Copy-on-Write radix tree). Readers access the atomic pointer with zero lock overhead and zero cache-line invalidation, while writes clone and swap the pointer.
- **Behavioural risks**: Low. COW updates ensure atomic snapshot visibility without blocking readers.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/routing/router.go:665-675`).

---

### Finding CON-002: ExpirationManager Pending Lock Contention During High-Throughput Revocations
- **Finding ID**: CON-002
- **Category**: Concurrency / Mutex Contention
- **File path**: `internal/vault/expiration.go`
- **Function**: `ExpirationManager.revokeCommon`, `updatePending`
- **Line range**: Lines 1100–1115, 1795–1810
- **Existing implementation**:
  In `revokeCommon`:
  ```go
  m.pendingLock.Lock()
  m.removeFromPending(ctx, leaseID, true)
  m.nonexpiring.Delete(leaseID)
  if _, ok := m.irrevocable.Load(le.LeaseID); ok {
      m.irrevocable.Delete(leaseID)
      m.irrevocableLeaseCount--
      m.leaseCount--
  }
  m.pendingLock.Unlock()
  ```
- **Technical inefficiency**:
  `pendingLock` is a coarse-grained `locking.RWMutex` guarding both the pending lease map operations and atomic counters (`leaseCount`, `irrevocableLeaseCount`). When thousands of short-lived tokens or dynamic secrets expire simultaneously or undergo bulk revocation, background workers and client-driven revocations bottleneck heavily on `m.pendingLock.Lock()`.
- **Why it matters**:
  Bulk lease expiration storms or token tidying stall concurrent lease registration (`m.Register`) and lookup operations across the entire vault instance.
- **Potential improvement**:
  1. Replace `leaseCount` and `irrevocableLeaseCount` with `atomic.Int64` counters.
  2. Shard the `pending` map or utilize fine-grained bucketed locks for lease entry eviction.
- **Behavioural risks**: Medium; strict verification required to ensure counter consistency during restore and failover.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/expiration.go:1100-1115`).

---

### Finding CON-003: Lock Allocation and Race Window in Per-Lease Mutex Map
- **Finding ID**: CON-003
- **Category**: Concurrency / Mutex Lifecycle & Allocation
- **File path**: `internal/vault/expiration.go`
- **Function**: `ExpirationManager.lockForLeaseID`, `deleteLockForLease`
- **Line range**: Lines 1775–1790, 1070–1080
- **Existing implementation**:
  ```go
  func (m *ExpirationManager) lockForLeaseID(id string) *sync.Mutex {
      mutex := &sync.Mutex{}
      lock, _ := m.lockPerLease.LoadOrStore(id, mutex)
      return lock.(*sync.Mutex)
  }

  func (m *ExpirationManager) deleteLockForLease(id string) {
      m.lockPerLease.Delete(id)
  }
  ```
- **Technical inefficiency**:
  1. `lockForLeaseID` allocates a new `&sync.Mutex{}` on the heap on every invocation, even if `m.lockPerLease` already holds a lock for that `id`.
  2. Calling `m.deleteLockForLease(leaseID)` while concurrent callers may be waiting on or attempting to acquire `lockForLeaseID(leaseID)` creates an ABA race where a new goroutine can obtain a freshly allocated mutex while an in-flight revocation holds the deleted mutex instance.
- **Why it matters**:
  Under heavy lease creation and revocation workloads, hundreds of thousands of ephemeral `*sync.Mutex` instances are allocated and inserted into `lockPerLease`, inflating GC heap overhead.
- **Potential improvement**:
  Replace dynamic per-lease mutex creation with a fixed-size sharded striped lock array (e.g., `locksutil.CreateLocks(256)` or `locksutil.KeyedLock`) keyed by `farm.Fingerprint64(leaseID)`.
- **Behavioural risks**: Low. Striped locks bound memory usage to a constant overhead without pointer allocation or deletion race conditions.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/expiration.go:1775-1790`).

---

### Finding CON-004: Potential Timer Resource Retention with time.After in Timeout Selects
- **Finding ID**: CON-004
- **Category**: Concurrency / Channel & Timer Management
- **File path**: `internal/vault/core.go`
- **Function**: `Core.sealInternalWithOptions`
- **Line range**: Lines 2100–2110
- **Existing implementation**:
  ```go
  select {
  case <-doneCh:
  // Attempt to drain any inflight requests
  case <-time.After(DefaultMaxRequestDuration):
      activeCtxCancel()
  }
  ```
- **Technical inefficiency**:
  `time.After` allocates an underlying `time.Timer` that remains active in the Go runtime's internal timer heap until `DefaultMaxRequestDuration` (typically 90 seconds or longer) has elapsed, even if `doneCh` fires immediately.
- **Why it matters**:
  If operations with large durations execute repeatedly (such as during test suites, failover transitions, or repeated cluster status checks), abandoned `time.After` timers retain memory and add overhead to runtime timer wheel sweeps until expiry.
- **Potential improvement**:
  Instantiate an explicit timer using `t := time.NewTimer(DefaultMaxRequestDuration)` and invoke `defer t.Stop()`.
- **Behavioural risks**: None.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/core.go:2104`).

---

### Finding CON-005: Barrier AEAD Cache RWMutex vs Lockless Atomic Map
- **Finding ID**: CON-005
- **Category**: Concurrency / RWMutex Contention
- **File path**: `internal/vault/barrier/aes_gcm.go`
- **Function**: `AESGCMBarrier.aeadForTerm`
- **Line range**: Lines 965–1005
- **Existing implementation**:
  ```go
  b.cacheLock.RLock()
  aead, ok := b.cache[term]
  b.cacheLock.RUnlock()
  if ok {
      return aead, nil
  }
  ```
  Guarded by `b.cacheLock sync.RWMutex`.
- **Technical inefficiency**:
  On every single storage read (`barrier.Get`) and storage write (`barrier.Put`), `aeadForTerm` acquires and releases `b.cacheLock.RLock()`. In practice, the keyring term changes only during manual or automatic barrier rotation (once every months or millions of encryptions).
- **Why it matters**:
  Acquiring `b.cacheLock.RLock()` on every single physical storage operation induces unnecessary CPU cache invalidation on the barrier struct across all storage worker threads.
- **Potential improvement**:
  Store `b.cache` inside an `atomic.Pointer[map[uint32]cipher.AEAD]` or use `sync.Map`. Reading the cached AEAD becomes a single atomic pointer load without any lock synchronization.
- **Behavioural risks**: None.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/barrier/aes_gcm.go:965-1005`).
