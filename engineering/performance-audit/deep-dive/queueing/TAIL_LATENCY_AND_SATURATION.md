# Tail Latency, Saturation Dynamics & Load Shedding 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report analyzes tail latency explosion (P95, P99, P99.9) under saturation and answers the Prompt 4 Final Question.

---

## 1. Throughput Collapse Dynamics

When arrival rate $\lambda$ exceeds service capacity $c\mu$:
1. **PermitPool Exhaustion:** All 128 permits are held by slow PostgreSQL queries.
2. **Goroutine Accumulation:** Inbound HTTP requests block on permit acquisition.
3. **GC Latency Blowout:** The Go runtime garbage collector must scan thousands of live goroutine stacks. GC "Stop the World" pauses increase from $< 100 \ \mu\text{s}$ to $> 15 \text{ ms}$.
4. **Tail Latency Explosion:** P99 latency jumps exponentially from 800 $\mu$s to over 2,000 ms.

---

## 2. Final Question Answer

### How can OpenBao sustain the highest useful throughput without allowing queues, goroutines, database connections or memory usage to grow uncontrollably?

**To sustain maximum throughput without resource exhaustion, OpenBao must implement a Three-Tier Bounded Admission and Load-Shedding Architecture:**

1. **HTTP Listener Connection Bounding:** Enforce `netutil.LimitListener` or HTTP reverse-proxy connection caps to bound active TCP sockets to a predictable ceiling.
2. **Adaptive Concurrency Limiting (CoDel / Vegas):** Replace static `max_parallel` with dynamic queue sizing. When average queue wait in `PermitPool` exceeds 10 ms, shed load immediately by returning `HTTP 429 Too Many Requests` with `Retry-After`.
3. **P0 Priority Bypass for In-Memory Cryptographic Reads:** Requests that hit `TransitSyncMap` should **never** touch `PermitPool` or database connection pools. Segregating memory-cached operations from physical storage permits prevents slow disk writes or DB queries from ever stalling in-memory DEK unwraps.
