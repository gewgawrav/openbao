# Advanced Workload Engineering & Test Design 🤖

## 1. Executive Summary

This specification defines a comprehensive workload engineering test harness designed to stress-test OpenBao and Cloakit Vault across edge conditions, systemic failures, non-linear saturation points, and skewed access distributions.

Unlike previous audits that relied on synthetic microbenchmarks or closed-loop test runs, this test design incorporates **open-loop load injection**, **coordinated omission correction**, **Zipfian skew modeling**, and **cache stampede simulation**.

---

## 2. Open-Loop vs Closed-Loop & Coordinated Omission Mitigation

### The Coordinated Omission Defect:
- In closed-loop load generators (e.g. standard Go benchmark loops, Apache Bench, standard k6), a virtual user issues a request and waits for the response before sending the next request.
- If a server experiences a 2-second GC pause or database lock stall, the client stops generating requests during those 2 seconds. The test generator inadvertently hides the queue build-up and reports an artificially low tail latency.

### The Open-Loop Architectural Design:
- **Arrival Process**: Poisson arrival process where request issuance times $t_i$ are independent of server response times:
  $$t_{i+1} = t_i + \Delta t, \quad \Delta t \sim \text{Exponential}(\lambda)$$
- **Load Generation Engine**: Constant rate or scheduled target rate using a non-blocking generator (e.g. tuned `vegeta` or custom Go open-loop generator utilizing channel pipelines and worker pools independent of socket completion).
- **Latency Measurement**:
  - `Service Time`: $t_{response\_recv} - t_{request\_sent}$
  - `Wait Time (Queuing)`: $t_{request\_sent} - t_{scheduled\_send}$
  - `Total End-to-End Latency`: $\text{Service Time} + \text{Wait Time}$

---

## 3. Workload Test Suite Matrix

```
+-----------------------------------------------------------------------------------------------------------------------------+
|                                              WORKLOAD TEST SUITE MATRIX                                                     |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| Test ID             | Name                        | Concurrency & Traffic Pattern | Primary Failure Metric Evaluated        |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-01              | Saturation-Point Discovery  | Stepped ramp: 5k to 100k req/s| Knee-of-the-curve inflection point; P99 |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-02              | Zipfian Hot-Key Contention  | $s=1.2$ skew over 100k keys   | Atomic RLock cache-line bouncing        |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-03              | Cache Stampede / Cold Start | 10k parallel requests for     | Barrier decrypt storms, PostgreSQL pool |
|                     |                             | single un-cached Subject Key  | exhaustion & duplicate disk reads       |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-04              | Tenant Imbalance (Noisy-Nbr)| 1 tenant @ 80% quota, 99      | Fair scheduling, token bucket leak,     |
|                     |                             | tenants sharing remaining 20% | cross-tenant latency isolation          |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-05              | Micro-Burst Traffic Spike   | 10x baseline burst for 500ms  | CFS throttling, socket buffer drops,    |
|                     |                             | every 10 seconds              | TCP listen queue overflow               |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-06              | Long-Duration Soak Test     | 48-hour continuous 95/5 load  | Memory leaks, PostgreSQL bloat,         |
|                     |                             | at 70% capacity               | autovacuum stalls, goroutine leaks      |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-07              | Slow-Storage Dependency     | Injected 50ms disk I/O delay  | Connection pool starvation cascade,     |
|                     | (PostgreSQL storage stall)  | on PostgreSQL backend         | HA lock heartbeat dropped/failover      |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
| ADV-08              | Concurrent Rotate & Read    | 100 rotations/s concurrent    | Read-after-write consistency, cache     |
|                     |                             | with 50,000 decrypts/s        | invalidation gRPC storm, memory churn   |
+---------------------+-----------------------------+-------------------------------+-----------------------------------------+
```

---

## 4. Detailed Test Specifications

### Test ADV-02: Zipfian Skewed Load Profile
- **Objective**: Measure RWMutex cache-line invalidation and contention on hot Subject Keys.
- **Key Distribution**: 100,000 total Subject Keys in the system. Request distribution follows Zipfian power law:
  $$P(k) = \frac{1 / k^s}{\sum_{n=1}^{N} (1 / n^s)}, \quad s = 1.15$$
  Top 1% of keys receive $\approx 65\%$ of all DEK unwrapping requests.
- **Pass/Fail Criteria**:
  - P99 latency must not exceed $3 \times$ P50 latency.
  - No lock starvation errors or goroutine thread stack blowouts.

### Test ADV-03: Cache Stampede (Thundering Herd)
- **Objective**: Validate behavior when an evicted or freshly created Subject Key is concurrently accessed by thousands of worker threads.
- **Test Execution**:
  1. Purge Subject Key `tenant-alpha-subject-99` from local OpenBao cache.
  2. Fire 10,000 concurrent requests to `/transit/decrypt/tenant-alpha-subject-99` within a 10ms window.
- **Expected Behavior with Single-Flight / Mutex Locking**:
  - Exactly 1 PostgreSQL query issued to fetch `policy/tenant-alpha-subject-99`.
  - 9,999 requests wait on internal synchronization and resolve immediately upon cache population.
- **Failure Mode**:
  - If single-flight is missing or lock granularity is coarse, 10,000 queries hit PostgreSQL simultaneously, exhausting `max_parallel` connections and crashing the backend pool.

### Test ADV-07: Slow Dependency Injection & HA Resilience
- **Objective**: Verify that storage latency degradation does not cause spurious HA failovers.
- **Fault Injection**: Introduce 200ms latency to PostgreSQL queries via Linux `tc` (traffic control) or PostgreSQL latency simulator.
- **Validation Check**:
  - OpenBao HA lock renewal routine (`haRenewLockIdentityExec`, 15-second TTL) runs in an isolated thread/connection and does not starve behind slow data queries.
  - Active leader retains lock without unintended step-down.
