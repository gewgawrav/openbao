# Current Prometheus Metrics & Telemetry Inventory 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document audits OpenBao's existing Prometheus telemetry metrics implemented across `internal/vault/metrics.go`, logical backends (`internal/builtin/logical/transit/`), and physical storage drivers (`internal/physical/postgresql/`).

Understanding which metrics are currently emitted establishes the operational baseline and highlights telemetry gaps that impede production bottleneck identification in Cloakit Privacy Vault deployments.

---

## 1. Metrics Inventory Relevant to Cloakit Workload

### 1.1 HTTP and Core Request Metrics
- **Source Reference:** `internal/vault/core.go:HandleRequest`
- `vault.core.handle_request`: Timer measuring overall request duration across all handlers.
- `vault.route.read`: Timer for read route dispatch.
- `vault.token.lookup`: Timer measuring token validation time in `TokenStore`.

### 1.2 Storage Backend Metrics
- **Source Reference:** `internal/physical/postgresql/postgresql.go:420, 440, 460`
- `vault.postgres.get`: Timer for PostgreSQL point lookups (`Get`). Measures query time for fetching entries.
- `vault.postgres.put`: Timer for PostgreSQL row inserts/updates (`Put`).
- `vault.postgres.delete`: Timer for row deletions (`Delete`).
- `vault.postgres.list`: Timer for directory listings.

### 1.3 Go Runtime Metrics
- **Source Reference:** Go runtime runtime/metrics integration.
- `vault.runtime.alloc_bytes`: Current bytes allocated on the Go heap.
- `vault.runtime.num_goroutines`: Total active goroutines in the process.
- `vault.runtime.gc_pause_ns`: Histogram of garbage collector STW pause durations.
- `vault.runtime.total_gc_runs`: Cumulative GC invocation counter.
