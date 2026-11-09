# PostgreSQL Schema, Primary Key & Index Architecture Audit 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report analyzes OpenBao's current PostgreSQL storage schema, indexing strategies, and database query mechanics defined in `internal/physical/postgresql/postgresql.go:300-360`.

OpenBao persists all barrier data in a generic key-value table (`vault_kv_store` by default). For the Cloakit Privacy Vault workload (managing millions of Subject Keys beneath Layer 1 KMS/HSM and above Layer 3 PostgreSQL DEKs), this schema introduces severe indexing and storage trade-offs that limit read throughput and inflate index bloat.

---

## 1. Concrete Schema Definition & SQL DDL
- **Source Reference:** `internal/physical/postgresql/postgresql.go:310-335`

The database schema is initialized directly by OpenBao at startup:
```sql
CREATE TABLE IF NOT EXISTS vault_kv_store (
    parent_path TEXT COLLATE "C" NOT NULL,
    path        TEXT COLLATE "C",
    key         TEXT COLLATE "C",
    value       BYTEA,
    CONSTRAINT vault_kv_store_pkey PRIMARY KEY (path, key)
);

CREATE INDEX IF NOT EXISTS vault_kv_store_idx ON vault_kv_store (parent_path);
```

### 1.1 Structural Properties
1. **Primary Key Definition:** A composite B-tree index on `(path, key)` with `"C"` collation.
   - `path`: Text path directory with leading and trailing slashes (e.g. `/policy/` or `/sys/token/id/`).
   - `key`: The individual entity identifier (e.g. `subject-key-12345`).
2. **Secondary Index:** A standalone B-tree index on `(parent_path)` with `"C"` collation to facilitate hierarchical prefix listings.
3. **Payload Column:** `value BYTEA`, which stores AES-256-GCM sealed ciphertext blobs from OpenBao's barrier.

---

## 2. Key Path Decomposition Mechanics
- **Source Reference:** `internal/physical/postgresql/postgresql.go:390-415` (`splitKey`)

When OpenBao writes an entry with logical key `policy/tenant-a/user-123`:
```go
pieces := strings.Split(fullPath, "/")
// depth determines parentPath, path, key
```
- `parentPath`: `/policy/`
- `path`: `/policy/tenant-a/`
- `key`: `user-123`

### 2.1 Collation Impact
Using `COLLATE "C"` bypasses operating system locale rules (such as `en_US.UTF-8` ICU collations), ensuring standard byte-order comparison (`strcmp`). This significantly speeds up B-tree key comparison and index traversal.

---

## 3. Query Analysis & Index Selectivity

### 3.1 Point Lookup (`Get`)
- **Query:** `SELECT value FROM vault_kv_store WHERE path = $1 AND key = $2`
- **Plan:** `Index Scan using vault_kv_store_pkey on vault_kv_store`
- **Cost:** Requires traversing the B-tree depth ($O(\log N)$) followed by a heap fetch to read `value BYTEA`.
- **Selectivity:** High. `(path, key)` uniquely identifies a single row.

### 3.2 List Hierarchy (`List` / `ListPage`)
- **Query:**
  ```sql
  SELECT key FROM vault_kv_store WHERE path = $1
  UNION ALL
  SELECT DISTINCT substring(substr(path, length($1)+1) from '^.*?/')
  FROM vault_kv_store WHERE parent_path LIKE $1 || '%'
  ORDER BY key
  ```
- **Plan:** Bitmap Index Scan on `vault_kv_store_idx` combined with regex parsing.
- **Bottleneck:** While acceptable for small directories, listing large key namespaces triggers expensive string manipulations (`substring`, `substr`) on the PostgreSQL engine.
