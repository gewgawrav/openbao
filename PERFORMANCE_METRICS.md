# OpenBao Performance Metrics, SLOs & Observability Standard

This document defines the production performance indicators, service level objectives (SLOs), metric formulas, Prometheus instrumentation keys, and observability gaps for OpenBao.

---

## 1. Enterprise Service Level Objectives (SLOs)

| Service Area | Metric / Operation | Target SLO (p95) | Target SLO (p99) | Availability / Throughput Target |
|---|---|---|---|---|
| **Secret Retrieval** | KV v2 Secret Read | < 2 ms | < 5 ms | 99.999% availability (> 50,000 req/sec) |
| **Secret Mutation** | KV v2 Secret Write | < 10 ms | < 25 ms | 99.99% availability (> 5,000 req/sec) |
| **Authentication** | Service Token Creation | < 10 ms | < 30 ms | 99.99% availability (> 3,000 logins/sec) |
| **Authentication** | Batch Token Issuance | < 1 ms | < 2 ms | 99.999% availability (> 30,000 logins/sec) |
| **Transit Crypto** | Single-item Encrypt/Decrypt | < 0.5 ms | < 1.5 ms | 99.999% availability (> 50,000 ops/sec) |
| **Transit Crypto** | 1,000-item Batch Encrypt | < 3 ms | < 6 ms | 99.99% availability (> 10,000 batches/sec) |
| **Cluster HA** | Standby Read Latency | < 3 ms | < 6 ms | 99.999% availability (> 100,000 req/sec total) |
| **Leader Election** | Unplanned Failover Recovery | < 1.5 s | < 3.0 s | Seamless active promotion |

---

## 2. Core Prometheus Telemetry Reference

OpenBao exposes telemetry via the `/v1/sys/metrics?format=prometheus` endpoint. The following metrics are primary indicators of system performance and bottleneck formation:

### 2.1 Core API & Request Handling
- `openbao_core_handle_request_count`: Total API requests handled.
- `openbao_core_handle_request`: Histogram of total request duration.
- `openbao_core_handle_login_request`: Login request duration histogram.
- `openbao_route_read`, `openbao_route_write`: Subsystem routing duration.
- `openbao_token_lookup`: Token lookup latency distribution.

### 2.2 Cryptographic Barrier & Memory
- `openbao_barrier_get`: Latency of barrier decryption reads.
- `openbao_barrier_put`: Latency of barrier encryption writes.
- `openbao_barrier_delete`: Latency of barrier deletion operations.
- `openbao_runtime_heap_alloc_bytes`: Current live heap memory.
- `openbao_runtime_alloc_bytes`: Cumulative bytes allocated.
- `openbao_runtime_num_goroutines`: Live goroutine count.

### 2.3 Integrated Storage (Raft)
- `openbao_raft_storage_put`: Raft proposal and commit latency.
- `openbao_raft_storage_get`: Local FSM read latency.
- `openbao_raft_storage_entry_size`: Distribution of committed Raft entry payload sizes.
- `openbao_raft_apply`: Total Raft log application duration.
- `openbao_raft_commit_time`: Raft consensus round-trip duration.

### 2.4 Physical Storage Cache
- `openbao_cache_hit`: Counter of cache hits in `TwoQueueCache`.
- `openbao_cache_miss`: Counter of cache misses requiring physical reads.
- `openbao_cache_write`: Counter of cache writes.
- **Cache Hit Ratio Formula**:
  $$\text{Hit Ratio} = \frac{\Delta \text{openbao\_cache\_hit}}{\Delta \text{openbao\_cache\_hit} + \Delta \text{openbao\_cache\_miss}}$$

### 2.5 Audit Subsystem
- `openbao_audit_log_request`: Latency of request audit logging.
- `openbao_audit_log_response`: Latency of response audit logging.
- `openbao_audit_log_request_failure`: Counter of audit write failures.
- `openbao_audit_log_response_failure`: Counter of audit response write failures.

### 2.6 Lease & Expiration Manager
- `openbao_expire_num_leases`: Current number of active tracked leases.
- `openbao_expire_register`: Lease registration duration.
- `openbao_expire_revoke`: Lease revocation duration.
- `openbao_expire_revoke_irrevocable`: Irrevocable lease counter.

---

## 3. Observability Gaps & Required Additions

The audit identified several observability blind spots where additional telemetry is required:

1. **Transit Batch Processing Concurrency Metrics**:
   - *Missing*: Gauge tracking the number of parallel workers executing batch cryptography and duration per batch chunk.
   - *Recommendation*: Add `openbao_transit_batch_workers_active` and `openbao_transit_batch_item_duration`.

2. **Raft Group Commit Coalescing Telemetry**:
   - *Missing*: Metric measuring batch proposal efficiency (how many `Put` ops are coalesced into a single `raft.Apply`).
   - *Recommendation*: Add `openbao_raft_group_commit_batch_size` (histogram) and `openbao_raft_group_commit_wait_time`.

3. **Audit Backend Mutex Contention**:
   - *Missing*: Metric quantifying time API worker threads spend waiting for `b.fileLock` in `file/backend.go`.
   - *Recommendation*: Add `openbao_audit_file_lock_wait_time_microseconds`.

4. **Standby Invalidation Lag Duration**:
   - *Missing*: Real-time lag measurement between mutation commit on leader and cache invalidation application on standby.
   - *Recommendation*: Add `openbao_ha_standby_invalidation_lag_seconds`.

5. **PKCS#11 / HSM Session Pool Utilization**:
   - *Missing*: Metrics tracking available, leased, and waiting HSM sessions in external KMS plugins.
   - *Recommendation*: Standardize `openbao_kms_hsm_active_sessions` and `openbao_kms_hsm_queue_depth`.

---
