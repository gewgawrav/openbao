# Admission Control & Adaptive Concurrency Limiting 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document designs an adaptive admission control and priority scheduling mechanism for OpenBao in Cloakit deployments.

---

## 1. Priority-Based Scheduling Architecture

Cloakit requests fall into distinct priority classes:
1. **P0 (Critical Hot Path):** DEK Unwrapping (`/transit/decrypt/`). Latency must be < 1 ms.
2. **P1 (Normal Operations):** DEK Wrapping (`/transit/encrypt/`), Key Lookups.
3. **P2 (Administrative Writes):** Key Rotation, Key Creation.

```
+------------------------------------------------------------------------------------------------------+
|                                   ADAPTIVE ADMISSION CONTROL (AIMD / CODEL)                          |
+------------------------------------------------------------------------------------------------------+
| [Incoming HTTP Requests]                                                                             |
|            |                                                                                         |
|            v                                                                                         |
| +-------------------------------------------------------------------+                                |
| | Token Bucket Admission Gate (Per-Tenant Max Concurrency Quota)    |                                |
| +-------------------------------------------------------------------+                                |
|            | (Admitted)                                         | (Over Quota)                       |
|            v                                                    v                                    |
| +---------------------------------------+             [HTTP 429 Too Many Requests]                   |
| | Priority Queues:                      |             [Retry-After Header]                           |
| |  - P0: DEK Unwrap (High Priority)     |                                                            |
| |  - P1: DEK Wrap (Medium Priority)     |                                                            |
| |  - P2: Rotation / Create (Low/Shed)   |                                                            |
| +---------------------------------------+                                                            |
+------------------------------------------------------------------------------------------------------+
```

### 1.1 Netflix Vegas / AIMD Adaptive Algorithm
Rather than static `max_parallel = 128`, OpenBao should adjust concurrency limits dynamically based on round-trip time:
$$\text{Limit}_{t+1} = \begin{cases} \text{Limit}_t + 1 & \text{if } RTT \le RTT_{target} \\ \text{Limit}_t \times 0.8 & \text{if } RTT > RTT_{target} \end{cases}$$
When PostgreSQL latency climbs, OpenBao immediately throttles concurrency, rejecting excess requests with HTTP 429 before goroutines accumulate.
