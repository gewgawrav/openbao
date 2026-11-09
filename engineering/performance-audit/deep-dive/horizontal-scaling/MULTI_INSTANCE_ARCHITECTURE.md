# Multi-Instance Cluster Architecture & Read Scaling Audit 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document analyzes the architectural mechanics of scaling OpenBao horizontally across multiple instances when backed by PostgreSQL for Cloakit's 95% read workload.

OpenBao supports High Availability (HA) through distributed locking. In a standard PostgreSQL configuration, one instance is elected Active while secondary instances remain Standby. We evaluate request forwarding, direct standby read handling, and performance limits.

---

## 1. High Availability (HA) & Leader Election in PostgreSQL
- **Source Reference:** `internal/physical/postgresql/postgresql.go:50-80`, `internal/physical/postgresql/postgresql.go:530-580`

### 1.1 Lock Acquisition Mechanics
- OpenBao creates an HA locks table (`openbao_ha_locks` or `vault_ha_locks`):
```sql
CREATE TABLE IF NOT EXISTS openbao_ha_locks (
    ha_key      TEXT COLLATE "C" NOT NULL,
    ha_identity TEXT COLLATE "C" NOT NULL,
    ha_value    TEXT COLLATE "C",
    valid_until TIMESTAMP WITH TIME ZONE NOT NULL,
    CONSTRAINT openbao_ha_locks_pkey PRIMARY KEY (ha_key)
);
```
- Active leader identity is maintained via periodic renewal (`PostgreSQLLockTTLSeconds = 15s`, `PostgreSQLLockRenewInterval = 5s`).
- Only **one** OpenBao instance holds the active lock at any given time.

---

## 2. Standby Request Handling & Forwarding

### 2.1 The Request Forwarding Model
- **Source Reference:** `internal/vault/core.go:HandleRequest`, `internal/vault/request_forwarding.go`
- By default, standby instances proxy all incoming client requests across an internal mTLS RPC connection to the active leader node.
- **Impact on Read Scaling:** In default HA mode, **adding OpenBao standby instances does NOT increase read throughput**. All requests are forwarded to the active node, which remains the single CPU and memory bottleneck.

### 2.2 Performance Standby / Direct Standby Reads
- To achieve horizontal read scaling, secondary nodes must be configured to decrypt DEKs and serve Subject Key lookups locally without forwarding to the leader.
- Standby nodes query PostgreSQL directly to hydrate their local `TransitLRU` or `TransitSyncMap`.
