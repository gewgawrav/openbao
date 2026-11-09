# OpenBao Deep-Dive Engineering Performance Audit 🤖

## Executive Summary
This directory contains the exhaustive, evidence-based performance engineering audit of OpenBao tailored specifically to the **Cloakit Privacy Vault** target workload:
- **Workload Profile:** 95% reads, 5% writes.
- **Core Operations:** Frequent Subject Key retrieval, DEK wrapping (`encrypt`), and DEK unwrapping (`decrypt`).
- **Key Hierarchy:** Layer 1 Cloud KMS/HSM Root KEK $\to$ Layer 2 OpenBao Transit Subject Keys $\to$ Layer 3 Application PostgreSQL DEKs.

The audit spans 8 specialized investigations across 40 technical reports, strictly classified by evidence type (`SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`), referencing exact source files and line numbers, with zero fabricated measurements and strict preservation of security, multi-tenancy, and audit invariants.

---

## Audit Directory Index & Navigation

### 1. [Request Amplification & Execution Path Optimisation](request-amplification/)
- `REQUEST_EXECUTION_GRAPH.md`: Multi-layer trace across all 9 execution layers.
- `OPERATION_AMPLIFICATION_MATRIX.md`: Exact quantification of logical vs actual operations.
- `REDUNDANT_EXECUTION_ANALYSIS.md`: Breakdown of repeated cipher allocations, reflection, and base64 roundtrips.
- `CRITICAL_PATH_OPTIMISATION.md`: Elimination of unnecessary work along the hot read path.
- `IMPLEMENTATION_RECOMMENDATIONS.md`: Minimum secure work definition and implementation plan.

### 2. [PostgreSQL Schema, Index & Physical Data Layout Audit](postgres-data-layout/)
- `SCHEMA_AND_INDEX_AUDIT.md`: Analysis of `vault_kv_store`, composite primary keys, and parent path indexing.
- `QUERY_PLAN_ANALYSIS.md`: EXPLAIN plans, B-tree depth, and heap visibility costs.
- `PHYSICAL_DATA_LAYOUT.md`: BYTEA storage, TOAST compression thresholds (2 KB), page density, and HOT updates.
- `INDEX_OPTIMISATION_OPTIONS.md`: Covering indexes, partial indexes, and partitioning tradeoffs.
- `SCHEMA_REDESIGN_PROPOSALS.md`: Split metadata/payload models and prepared statement caching.

### 3. [Cache Coherence, Invalidation & Stampede Audit](cache-coherence/)
- `CURRENT_CACHE_IMPLEMENTATION.md`: Mechanics of `TransitLRU` (`TwoQueueCache`) and `TransitSyncMap`.
- `CACHE_CONSISTENCY_MODEL.md`: Invalidation dynamics and multi-instance split-brain risks.
- `INVALIDATION_AND_REVOCATION.md`: Critical failure modes (rotation, revocation, tenant deletion).
- `CACHE_STAMPEDE_ANALYSIS.md`: Singleflight duplicate request suppression and hot-key protection.
- `MULTI_INSTANCE_CACHE_DESIGN.md`: Real-time invalidation via PostgreSQL `LISTEN`/`NOTIFY`.
- `TARGET_CACHE_ARCHITECTURE.md`: Optimal L1/L2 caching blueprint.

### 4. [Queueing Theory, Backpressure & Admission Control](queueing/)
- `REQUEST_QUEUE_ANALYSIS.md`: Cascading queueing points (HTTP listener, PermitPool, database connection pool).
- `BACKPRESSURE_AUDIT.md`: Unbounded goroutine accumulation and GC pause blowout.
- `ADMISSION_CONTROL_DESIGN.md`: Adaptive concurrency limits (CoDel / Vegas) and priority queues.
- `CONCURRENCY_MODEL.md`: Mathematical $M/M/c/K$ and $M/G/c$ queueing models.
- `TAIL_LATENCY_AND_SATURATION.md`: Throughput collapse dynamics and load-shedding architecture.

### 5. [Multi-Instance Read Scaling & Consistency Audit](horizontal-scaling/)
- `MULTI_INSTANCE_ARCHITECTURE.md`: Active/Standby cluster mechanics and request forwarding limits.
- `POSTGRES_REPLICATION_ANALYSIS.md`: Streaming physical replication lag and stale-read race conditions.
- `READ_ROUTING_DESIGN.md`: Tenant-aware consistent hashing and cache affinity routing.
- `CONSISTENCY_AND_FAILOVER.md`: Read-after-write semantics via PostgreSQL LSN commit tokens.
- `HORIZONTAL_SCALING_BENCHMARK_PLAN.md`: Synthetic scaling test plan (1 to 10 instances).

### 6. [Cryptographic Lifecycle & Envelope Encryption Cost Audit](crypto-lifecycle/)
- `KEY_HIERARCHY_REVIEW.md`: 3-layer key hierarchy mapping and cryptographic separation guarantees.
- `ENVELOPE_ENCRYPTION_COST.md`: Detailed CPU cycle, memory, and wire size comparison across ciphers.
- `DEK_OPERATION_ANALYSIS.md`: Step-by-step cryptographic trace, CSPRNG nonces, and AAD context binding.
- `KEY_ROTATION_AND_REWRAPPING.md`: Lazy amortized rewrapping vs eager batch rewrapping, and cryptographic erasure.
- `CRYPTOGRAPHIC_OPTIMISATION_OPTIONS.md`: Pre-instantiated AEAD caching and binary wire transport.

### 7. [Observability & Performance Instrumentation Audit](observability/)
- `CURRENT_METRICS_INVENTORY.md`: Audit of existing OpenBao Prometheus metrics and runtime telemetry.
- `PERFORMANCE_VISIBILITY_GAPS.md`: Blind spots in PermitPool wait times and lock contention.
- `LOW_OVERHEAD_INSTRUMENTATION.md`: High-precision atomic phase timers and cardinality safety.
- `DISTRIBUTED_TRACING_DESIGN.md`: OpenTelemetry context propagation and security boundaries.
- `PERFORMANCE_DASHBOARD_SPECIFICATION.md`: Grafana dashboard specifications and diagnostic workflows.

### 8. [Capacity, Cost & Resource Efficiency Modelling](capacity-model/)
- `SUBJECT_KEY_CAPACITY_MODEL.md`: Rigorous sizing models for 10K, 100K, 1M, and 10M Subject Keys.
- `MEMORY_AND_STORAGE_FORECAST.md`: PostgreSQL heap size, TOAST storage, and WAL generation rates.
- `CPU_AND_CONNECTION_MODEL.md`: CPU allocation guidelines and database connection pool sizing.
- `SCALING_AND_COST_ANALYSIS.md`: Total Cost of Ownership (TCO) and Cloud KMS cost comparison (>95% savings).
- `CAPACITY_PLANNING_RECOMMENDATIONS.md`: Evolution triggers and bottleneck progression analysis.
