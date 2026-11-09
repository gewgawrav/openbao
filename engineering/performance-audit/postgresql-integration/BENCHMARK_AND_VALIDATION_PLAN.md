# Benchmark and Validation Plan 🤖

## Executive Summary

This report specifies a controlled, reproducible benchmarking programme designed to validate OpenBao's PostgreSQL backend performance under Cloakit Privacy Vault's read-heavy workload. It defines test harnesses, traffic matrices (95:5, 99:1, 90:10), pool sizing experiments, bounded concurrency levels, and explicit metric collection methodologies.

Strict Constraint: In compliance with project constraints, benchmarks are designed for execution in controlled test environments using existing test harnesses without accessing live production infrastructure.

Evidence classifications: `SOURCE-CONFIRMED`, `STATIC-ANALYSIS-CONFIRMED`, `TEST-CONFIRMED`, `BENCHMARK-CONFIRMED`, `INFERRED`, `UNKNOWN`.

---

## 1. Controlled Benchmark Program Matrix

### 1.1 Workload Ratios
1. **Cloakit Target Workload (95:5)**: 95% reads (Subject Key lookup + DEK unwrap) / 5% writes (DEK wrap + key rotation).
2. **Read-Dominated Workload (99:1)**: 99% reads / 1% writes (simulating steady-state analytical query masking).
3. **Write-Stressed Workload (90:10)**: 90% reads / 10% writes (simulating high-frequency data ingestion and envelope creation).

### 1.2 Concurrency Tiers
Execute client concurrency across the following worker levels:
$$C \in \{1, 10, 50, 100, 250, 500, 1000\}$$

### 1.3 Pool Sizing Configuration Matrix
Test each combination against varied OpenBao pool configurations:
- `max_parallel`: 5, 10, 20, 50, 100, 128.
- `max_idle_connections`: 2 (Default uncoordinated), Equal to `max_parallel` (Coordinated).
- `transaction_max_parallel`: $0.5 	imes 	ext{max\_parallel}$.

---

## 2. Experimental Configurations to Compare

| Test Suite | Configuration Profile | Architectural Properties Tested |
|---|---|---|
| **Suite A** | Baseline (Current OpenBao Fork) | `max_parallel = 128`, `max_idle_connections = unset (2)`, direct PG |
| **Suite B** | Coordinated Idle Pooling | `max_parallel = 40`, `max_idle_connections = 40`, direct PG |
| **Suite C** | PgBouncer Transaction Mode | `max_parallel = 40`, `max_idle = 40`, PgBouncer (`default_pool_size = 32`) |
| **Suite D** | Optimized SQL (HOT UPSERT) | Rewritten `putQuery` (`ON CONFLICT DO UPDATE SET value = EXCLUDED.value`) |
| **Suite E** | Fence-Cache Optimization | Cached active node lease verification vs live database query |
| **Suite F** | Cache Hit vs Cache Miss | Transit cache warm (100% hit) vs cold pass-through (0% hit) |
| **Suite G** | Autovacuum Tuning | Default PG vacuum (20%) vs aggressive table vacuum (1%) |

---

## 3. Test Harness Specification

### 3.1 Local Reproducible Harness (Go Benchmark Specification)
The benchmark harness leverages Go's native testing framework and `internal/physical/postgresql/testing.go`:

```go
// benchmark_pg_test.go
package postgresql_test

import (
    "context"
    "fmt"
    "math/rand"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/openbao/openbao/sdk/v2/physical"
)

func BenchmarkCloakitWorkload(b *testing.B) {
    // Configurable parameters: pool sizes, ratios, concurrency
    ratios := []struct{ readPct, writePct int }{
        {95, 5}, {99, 1}, {90, 10},
    }
    concurrencies := []int{10, 50, 100, 250, 500}

    for _, r := range ratios {
        for _, c := range concurrencies {
            name := fmt.Sprintf("Ratio_%d_%d_Concurrency_%d", r.readPct, r.writePct, c)
            b.Run(name, func(b *testing.B) {
                runBenchmarkHarness(b, r.readPct, c)
            })
        }
    }
}
```

### 3.2 Metrics Collection Specifications

| Metric Category | Specific Telemetry Variable | Collection Mechanism | Target / Expected Behavior |
|---|---|---|---|
| **Throughput** | Requests Per Second (RPS) | Client Test Harness Timer | Linear scaling up to CPU saturation |
| **Latency** | P50, P95, P99 Latency | High-resolution HDR Histograms | Sub-millisecond P99 on warm reads |
| **Pool Waits** | `sql.DBStats.WaitDuration` | Go Runtime Periodic Poller | 0 ms in Coordinated mode; >50ms in Baseline |
| **Connections** | Active vs Idle Connections | `pg_stat_activity` | Bounded; no connection thrashing |
| **Database CPU**| % User, % System CPU | OS `top` / `sar` | Absence of context-switch thrashing |
| **WAL Rate** | Bytes Written / sec | `pg_stat_wal` / `pg_current_wal_lsn` | 40% reduction under HOT UPSERT rewrite |
| **Tuples** | Dead Tuple Churn | `pg_stat_user_tables` | Rapid reclamation by autovacuum |

---

## 4. Distinguishing Actual Measurements from Architectural Projections

In compliance with strict execution constraints:
- Source code structures, SQL statements, and connection pool mechanics are **`SOURCE-CONFIRMED`**.
- Baseline connection thrashing when `max_idle_connections` is unset is **`STATIC-ANALYSIS-CONFIRMED`** via Go's `database/sql` implementation.
- Quantitative latency distributions under multi-node infrastructure loads are **`INFERRED`** based on database queuing theory and provide reproducible specifications for post-audit validation runs.

---

## 5. Audit Verdict

This benchmark plan provides complete, reproducible specifications to measure the performance delta between uncoordinated defaults and optimized settings. Operators can execute this test suite against any isolated PostgreSQL container to obtain empirical proof of throughput and latency improvements.
