# Advanced Workload Test Design: Closed vs Open Loop, Skew & Saturation 🤖

**Author:** OpenBao Performance & Systems Engineering Team
**Workload Focus:** Cloakit Privacy Vault (95% Reads, 5% Writes; Subject Key Lookup & DEK Wrapping/Unwrapping)
**Target Environment:** OpenBao Layer 2 Key Management Engine on PostgreSQL Storage Backend
**Date:** March 2025
**Evidence Standard:** SOURCE-CONFIRMED, STATIC-ANALYSIS-CONFIRMED, TEST-CONFIRMED, BENCHMARK-CONFIRMED, INFERRED, UNKNOWN

---

## 1. Executive Summary

Benchmarking cryptographic vaults under the assumption of uniform, closed-loop, synthetic traffic yields deceptively optimistic results that fail in production. Production workloads at Cloakit scale exhibit:
- **Arrival rate independence** (open-loop request bursts from hundreds of microservices)
- **Severe key skew** (Zipfian / Pareto distribution over Subject Keys)
- **Tenant-level imbalance** (noisy neighbors monopolizing connection pools)
- **State transitions** (cold cache startup, cache stampedes during key rotations)

This document provides rigorous, production-grade test designs to expose saturation limits, coordinated omission, and tail latency under 14 distinct advanced test scenarios.

---

## 2. Test Suite 1: Arrival Rate Models & Coordinated Omission

### 2.1. Open-Loop vs Closed-Loop Load Generation
- **The Problem:** Standard HTTP benchmarking tools (`ab`, `wrk`, `hey`) use closed-loop execution: each virtual user sends a request, waits for the response, and then sends the next. If OpenBao experiences a 100ms lock pause, the virtual users stall, the total request rate drops, and tail latency spikes are artificially suppressed.
- **Test Design (Open-Loop Arrival):**
  - Use open-loop generators (e.g. `k6` with `constant-arrival-rate` or `wrk2` / `Vegeta`) where requests are scheduled according to a Poisson process at fixed target rates lambda in {5k, 10k, 20k, 40k, 60k ops/sec}.
  - Measure true service time including queue delay: T_latency = T_response_received - T_scheduled_arrival.
- **Acceptance Threshold:**
  At 30,000 ops/sec, P99 open-loop latency must remain <= 2.5 ms, with 0 dropped requests over 30 minutes.

---

## 3. Test Suite 2: Traffic Skew & Multi-Tenancy

### 3.1. Hot-Key Skew (Zipfian Distribution)
- **Objective:** Evaluate `KeyEntry` lock contention and cache behavior when a tiny percentage of Subject Keys receive the vast majority of traffic.
- **Test Design:**
  - Seed dataset with N = 100,000 Subject Keys.
  - Draw target key identifiers using a Zipfian distribution with skew parameter s = 0.99 (80% of requests target ~200 keys).
  - Concurrently execute 95% reads (DEK wrap/unwrap) and 5% writes (key rotations and policy updates) on those same hot keys.
- **Failure Target to Detect:** Writer lock escalation in `KeyEntry` causing reader starvation and multi-millisecond tail latency spikes.

### 3.2. Tenant-Level Workload Imbalance (Noisy Neighbor)
- **Objective:** Verify tenant isolation and prevent one compromised or heavy tenant from degrading others.
- **Test Design:**
  - Configure 10 tenants (`tenant_01` to `tenant_10`).
  - Tenants 1-9 issue steady background load (1,000 ops/sec each).
  - Tenant 10 initiates an un-throttled batch wrapping burst of 40,000 ops/sec or deep list scans.
- **Acceptance Threshold:**
  Tenants 1-9 must maintain P99 latency <= 3.0 ms despite Tenant 10 saturating its own quota.

---

## 4. Test Suite 3: Cache Dynamics & Transients

### 4.1. Cache Stampede (Thundering Herd) on Rotation
- **Objective:** Measure latency spikes when a hot Subject Key is rotated or evicted.
- **Test Design:**
  - Generate 10,000 concurrent DEK wrap requests against `hot_subject_key_01`.
  - At t = 30s, issue a key rotation: `POST /transit/keys/hot_subject_key_01/rotate`.
  - The rotation invalidates the `TransitLRU` cache entry and acquires an exclusive lock.
  - Observe how many concurrent reader requests hit PostgreSQL simultaneously.
- **Remediation Under Test:** Mutex single-flight / coalescing so that only one goroutine re-reads PostgreSQL while others await the single-flight result.

### 4.2. Cold vs Warm Cache Soak Test
- **Test Design:**
  - **Phase A (Cold Start):** Launch OpenBao with empty caches. Ramp immediately to 10,000 ops/sec. Measure time-to-steady-state and peak PostgreSQL I/O.
  - **Phase B (Warm State):** Continue for 4 hours to verify memory footprint stability, GC pause frequency, and absence of goroutine leaks.
  - **Phase C (Cardinality Shock):** Inject 500,000 unique keys to force total eviction of the 128,000-entry `TwoQueueCache`.

---

## 5. Test Suite 4: Dependency Degradation & Recovery

### 5.1. Slow Storage Injection (PostgreSQL I/O Latency)
- **Test Design:**
  - Using Linux `tc` or `iptables`, inject 15ms artificial latency on port 5432.
  - Observe OpenBao's `PermitPool` exhaustion:
    - Does OpenBao reject incoming HTTP requests gracefully with HTTP 429 / 503?
    - Or does it queue thousands of goroutines until the OS runs out of file descriptors and crashes?

### 5.2. Connection Pool Exhaustion & Recovery
- **Test Design:**
  - Flood PostgreSQL with out-of-band locking queries (`pg_sleep(60)` inside an exclusive row lock on `vault_kv_store`).
  - Observe how OpenBao's connection pool drains and recovers after the block is cleared.
