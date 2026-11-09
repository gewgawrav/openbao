# Multi-Instance Cache Synchronization Design 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document designs a distributed cache synchronization protocol across multiple OpenBao instances backed by PostgreSQL.

---

## 1. Distributed Invalidation Options

```
+------------------------------------------------------------------------------------------------------+
| Mechanism             | Latency | PG Overhead | Complexity | Network Partitions                      |
+------------------------------------------------------------------------------------------------------+
| 1. Short Polling      | 1-5 sec | High        | Very Low   | Safe but stale                          |
| 2. PG LISTEN/NOTIFY   | 1-5 ms  | Negligible  | Low        | Requires reconnect handler              |
| 3. Raft Log Broadcast | <1 ms   | None (PG)   | Medium     | Only works with integrated storage      |
+------------------------------------------------------------------------------------------------------+
```

### 1.1 PostgreSQL `LISTEN` / `NOTIFY` Architecture
PostgreSQL features built-in pub/sub via `LISTEN` and `NOTIFY`:
1. On active node key update/revoke:
   ```sql
   NOTIFY openbao_policy_inval, '{"namespace": "ns1", "name": "sk-42", "version": 2}';
   ```
2. Standby/reader instances maintain a persistent connection running:
   ```sql
   LISTEN openbao_policy_inval;
   ```
3. When a notification arrives, the standby node immediately invokes:
   ```go
   b.lm.InvalidatePolicy(name)
   ```
4. **Latency:** Under 2 milliseconds across instances.
5. **Safety Guard:** If connection is interrupted, the standby flushes its L1 cache completely on reconnect.
