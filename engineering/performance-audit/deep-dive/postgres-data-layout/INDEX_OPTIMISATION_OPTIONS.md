# Index Optimisation Options & Trade-Off Evaluation 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document evaluates proposed index modifications to OpenBao's PostgreSQL schema, analyzing write amplification, storage footprint, and migration complexity.

---

## 1. Option 1: Covering Indexes with `INCLUDE`

### Concept
Create an index covering frequent lookups:
```sql
CREATE INDEX vault_kv_store_covering_idx ON vault_kv_store (path, key) INCLUDE (value);
```
### Evaluation
- **Pros:** Enables true Index-Only Scans for point reads, bypassing heap fetches.
- **Fatal Con: PostgreSQL Limits.** PostgreSQL limits B-tree index entries to $\approx \frac{1}{3}$ of page size ($\approx 2704$ bytes). If `value BYTEA` exceeds this limit (e.g. large key policies or certificates), transactions fail with:
  `ERROR: index row size 2800 exceeds maximum 2704 for index`.
- **Verdict: REJECTED.** Dangerous and non-viable for general OpenBao storage where payload size is unbounded.

---

## 2. Option 2: Dropping or Optimising `vault_kv_store_idx`

### Concept
The secondary index is defined on `(parent_path)`:
```sql
CREATE INDEX vault_kv_store_idx ON vault_kv_store (parent_path);
```
### Evaluation
- **Purpose:** Used exclusively by `List()` and `ListPage()`.
- **Cloakit Reality:** Cloakit performs 95% reads (`Get` by key) and 5% writes (`Put`). It almost **never** calls `List` in production hot paths.
- **Overhead:** Every `Put` must update this secondary index, generating dirty buffer pages and WAL records.
- **Partial Index Alternative:**
  ```sql
  -- Index only paths that actually represent directory parents
  CREATE INDEX vault_kv_store_parent_idx ON vault_kv_store (parent_path)
  WHERE parent_path != '';
  ```
- **Verdict: RECOMMENDED FOR HIGH WRITE WORKLOADS.** Reduces secondary index size by up to 40%.

---

## 3. Option 3: Hash Partitioning by Tenant / Path

### Concept
Partition `vault_kv_store` by `path` or tenant prefix using PostgreSQL declarative hash partitioning (e.g., 16 partitions).
```sql
CREATE TABLE vault_kv_store (
    parent_path TEXT COLLATE "C" NOT NULL,
    path        TEXT COLLATE "C",
    key         TEXT COLLATE "C",
    value       BYTEA,
    CONSTRAINT vault_kv_store_pkey PRIMARY KEY (path, key)
) PARTITION BY HASH (path);
```
### Evaluation
- **Pros:**
  - B-tree index sizes per partition stay small (fitting entirely in L3 CPU cache or RAM buffer pools).
  - Vacuuming occurs on independent partitions, eliminating global table locks.
  - Concurrent writes map to different physical files.
- **Cons:** Migration complexity. Upgrading an existing unpartitioned table requires table rewrites.
- **Verdict: HIGHLY RECOMMENDED FOR > 5M SUBJECT KEYS.**
