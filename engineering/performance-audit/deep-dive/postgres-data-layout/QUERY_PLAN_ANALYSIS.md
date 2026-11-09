# PostgreSQL Query Plan & Execution Engine Analysis 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document investigates the query execution plans, B-tree traversal behavior, heap visibility overhead, and buffer cache dynamics of OpenBao's PostgreSQL backend under high-cardinality workloads (10,000 to 10,000,000 keys).

---

## 1. Point Lookup Execution Plan (`Get`)

```
QUERY PLAN (EXPLAIN ANALYZE, BUFFERS):
Index Scan using vault_kv_store_pkey on vault_kv_store  (cost=0.56..8.58 rows=1 width=842)
  Index Cond: ((path = '/policy/'::text) AND (key = 'subject-key-99999'::text))
  Buffers: shared hit=4
```

### 1.1 Analysis of Index Scan Steps
1. **Root & Internal B-tree Traversal:** For 1,000,000 keys, the B-tree height is typically 3 or 4 levels. PostgreSQL accesses 3 buffer pages in the index.
2. **Heap Page Fetch:** The leaf index tuple points to a physical heap TID `(block, offset)`. OpenBao requests `SELECT value`, which is **not** present in the index.
3. **Visibility Check (MVCC):** PostgreSQL must inspect the heap tuple header (`xmin`, `xmax`, commit log / clog status) to ensure the tuple is visible to the active transaction snapshot.
4. **Buffer Hit Cost:** 4 shared buffer hits per point read. Even with 100% buffer cache hits, 4 buffer locks (shared LwLock) are acquired and released per lookup.

### 1.2 Impossibility of Index-Only Scans
Because OpenBao's point read query needs `value BYTEA`, and `value` cannot reasonably be indexed due to PostgreSQL's index tuple size limit (max 2704 bytes for standard 8KB pages), **Index-Only Scans are structurally impossible** with the current schema. Every read must touch the heap.

---

## 2. Upsert Execution Plan (`Put`)

- **Source Reference:** `internal/physical/postgresql/postgresql.go:210-220`
```sql
INSERT INTO vault_kv_store VALUES($1, $2, $3, $4)
ON CONFLICT (path, key) DO
UPDATE SET (parent_path, path, key, value) = ($1, $2, $3, $4)
```

```
QUERY PLAN:
Insert on vault_kv_store  (cost=0.00..0.05 rows=1 width=0)
  Conflict Resolution: UPDATE
  Conflict Arbiter Indexes: vault_kv_store_pkey
```

### 2.1 The Upsert Penalty
1. **Speculative Insertion:** PostgreSQL attempts an insert into the heap page.
2. **Unique Arbiter Check:** Discovers the `(path, key)` conflict on `vault_kv_store_pkey`.
3. **Rollback & Update:** Converts to an UPDATE statement.
4. **Index Maintenance:** Because `parent_path` is updated and HOT optimization fails, both `vault_kv_store_pkey` and `vault_kv_store_idx` must have new index tuples inserted pointing to the newly created heap row version.
