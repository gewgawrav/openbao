# OpenBao Failure Scenarios & Resilience Under Stress

## 1. Failure Modes and Cascade Dynamics

When OpenBao is subjected to extreme load or infrastructure degradation, specific failure cascades can occur. This document details observed and theoretical failure modes, systemic impacts, and remediation protocols.

---

## 2. Detailed Failure Scenarios

### Scenario 1: Audit Device I/O Stall (Blocking Write Cascade)
- **Mechanism**: OpenBao requires synchronous logging to all non-fallback audit sinks. If a disk write to `/var/log/openbao_audit.log` blocks (e.g. due to storage full, NFS hang, or VM storage throttle), `auditBroker.LogRequest` will not return.
- **Cascade Effect**:
  1. Inbound requests queue in the Go HTTP listener.
  2. The number of live goroutines spikes from hundreds to tens of thousands (`runtime.gopark`).
  3. Memory consumption balloons due to queued request payloads and goroutine stacks (minimum 2 KB per goroutine).
  4. The operating system runs out of file descriptors or kernel ephemeral ports.
  5. OpenBao health check endpoints (`/v1/sys/health`) stop responding, prompting external load balancers to mark all cluster nodes dead simultaneously.
- **Recovery Behavior**: Once the audit device resumes accepting writes, queued goroutines flood the barrier simultaneously, creating a secondary CPU spike.
- **Remediation**:
  - Always configure fallback audit devices (`mode=fallback`).
  - Use high-speed local NVMe or asynchronous audit streaming daemon (e.g., local syslog agent buffering via Unix domain socket).
  - Implement request timeouts on clients (`context.WithTimeout`).

---

### Scenario 2: Raft Storage Slowness & Quorum Flapping
- **Mechanism**: The active OpenBao node regularly sends Raft heartbeats (`HeartbeatTimeout`, typically 1,000ms) and log append entries to followers.
- **Cascade Effect**:
  1. Slow disk I/O on the active leader delays writing the WAL.
  2. If disk `fsync` takes > 1,000ms, Raft followers fail to receive heartbeats in time.
  3. Followers declare the leader dead and trigger a new election.
  4. The leader steps down, clears its in-memory cache, and relinquishes the active lock.
  5. A new leader is elected and must run post-unseal setup (loading transactional mount tables, restaging leases, loading entities into memory).
  6. During post-unseal setup (1 - 10 seconds depending on cluster size), all client requests fail with `503 Service Unavailable` or `412 Precondition Failed`.
  7. If storage slowness persists across all nodes, the cluster enters an infinite leader election loop ("flapping").
- **Recovery Behavior**: Cluster cannot self-heal until disk latency stabilizes below heartbeat thresholds.
- **Remediation**:
  - Dedicate high-IOPS storage (provisioned IOPS SSD / NVMe) exclusively to OpenBao data. Never colocate database or log drives.
  - Tune Raft election timeouts in environments with variable storage latency (`election_timeout = "2000ms"`).

---

### Scenario 3: High-Cardinality Token & Lease Ballooning
- **Mechanism**: Applications continuously create non-renewable tokens or short-lived dynamic credentials without explicit revocation, relying on expiration.
- **Cascade Effect**:
  1. The expiration priority queue grows from thousands to millions of entries.
  2. Each entry consumes memory in the TokenStore and ExpirationManager.
  3. Periodic expiration scans (`expiration.collectLeases`) take longer to acquire the expiration queue lock.
  4. The Go runtime GC mark phase takes longer to scan the millions of heap pointers (`runtime.scanSpan`), consuming 30% - 50% of CPU.
  5. In worst-case scenarios, the Linux kernel Out-Of-Memory (OOM) killer terminates the `bao` process.
- **Recovery Behavior**: Upon restart and unseal, OpenBao must scan the physical backend to restore active leases (`expiration.restoreLeases`). With millions of keys, startup time takes tens of minutes, during which the node cannot serve traffic.
- **Remediation**:
  - Enforce strict `default_lease_ttl` and `max_lease_ttl` limits (e.g. 1h max).
  - Use Orphan tokens or Batch tokens for high-volume stateless microservice workloads (Batch tokens are cryptographic, non-persisted in storage, and create zero leases).

---

### Scenario 4: PKCS#11 / HSM Slowdown & Deadlock
- **Mechanism**: When using Auto-unseal or PKCS#11 cryptographic offloading, cryptographic operations (such as unwrapping the barrier encryption key or signing) delegate to an external HSM or cloud KMS (AWS KMS, Azure Key Vault, GCP CKMS).
- **Cascade Effect**:
  1. If the HSM or cloud KMS throttles requests or experiences network latency (>500ms), threads waiting to unwrap or rotate keys block.
  2. Requests waiting on the barrier master key stall.
  3. In case of complete HSM disconnect, OpenBao cannot perform barrier key re-wrapping or recovery, potentially sealing the node automatically depending on configuration.
- **Recovery Behavior**: Automatic retry mechanisms handle transient errors, but sustained network partition requires manual intervention.
- **Remediation**:
  - OpenBao caches unwrapped master keys in protected memory, so ordinary read/write operations avoid HSM calls. HSM calls occur primarily during startup, rekey, and seal checks.
  - Implement health checks monitoring KMS/HSM latency independently.

---

### Scenario 5: Userpass / Bcrypt CPU Starvation
- **Mechanism**: An external client or automated script initiates thousands of concurrent login requests against `/v1/auth/userpass/login`.
- **Cascade Effect**:
  1. Bcrypt computes Blowfish encryption at `cost=10` (~22ms CPU per call).
  2. At 100 concurrent logins, all CPU cores are pinned at 100% capacity.
  3. Internal background tasks (such as Raft heartbeats, lease cleanup routines, and metrics reporting) fail to receive CPU scheduling quanta.
  4. Active Raft leader misses heartbeat deadlines and drops leadership.
- **Recovery Behavior**: Immediate recovery as soon as the login flood ceases.
- **Remediation**:
  - Implement OpenBao Rate Limiting Quotas (`sys/quotas/rate-limit`) on authentication paths:
    ```json
    {
      "rate": 20,
      "path": "auth/userpass/"
    }
    ```
  - Offload interactive user logins to external OIDC / LDAP / AppRole providers with token caching.

---

## 3. Circuit Breaking and Defensive Configuration

To protect OpenBao against catastrophic failure modes:

1. **Resource Quotas**:
   Configure Lease Count Quotas to reject requests before memory exhaustion:
   ```hcl
   quotas {
     lease_count {
       max = 250000
     }
   }
   ```
2. **Rate Limit Quotas**:
   Enforce per-path and per-namespace request limits to isolate rogue clients.
3. **HTTP Request Timeouts & Limits**:
   ```hcl
   listener "tcp" {
     address = "0.0.0.0:8200"
     http_idle_timeout = "60s"
     http_read_timeout = "30s"
     http_write_timeout = "30s"
     max_request_size = 5242880 # 5 MB limit
   }
   ```
4. **Use Batch Tokens**:
   For read-only microservices or CI/CD pipelines, mandate the use of `batch` tokens to eliminate TokenStore write amplification and lease queue exhaustion.
