# Physical Data Layout, TOAST & Storage Density Audit 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report evaluates the physical on-disk layout, page density, TOAST behavior, fill factor, and dead tuple accumulation of OpenBao's data in PostgreSQL.

---

## 1. Heap Page Geometry & Tuple Density
PostgreSQL pages are 8192 bytes (8 KB). Each page includes a 24-byte page header, line pointers (4 bytes each), and heap tuples.

### 1.1 Row Sizing Breakdown for Subject Keys
In the Cloakit Privacy Vault, a Subject Key entry contains:
- `parent_path`: ~16-32 bytes (e.g. `/policy/tenant-12/`)
- `path`: ~32-64 bytes
- `key`: ~36-48 bytes (UUID or subject ID string)
- `value`: AES-256-GCM sealed policy JSON containing key versions, algorithms, timestamps (~600 to 1,500 bytes for 1-3 versions).

**Total Heap Tuple Size:** ~800 to 1,800 bytes per Subject Key.

### 1.2 Page Density
$$\text{Tuples Per Page} = \frac{8192 - 24}{1200 + 4} \approx 6 \text{ to } 7 \text{ keys per 8KB page.}$$
For 1,000,000 Subject Keys:
$$\text{Total Table Pages} = \frac{1,000,000}{6.8} \approx 147,000 \text{ pages} \approx 1.15 \text{ GB (raw heap)}.$$

---

## 2. TOAST Storage Dynamics (The Oversized Policy Risk)

PostgreSQL utilizes TOAST (The Oversized-Attribute Storage Technique) when row sizes exceed `TOAST_TUPLE_THRESHOLD` (typically 2048 bytes).
- **Threshold:** If a Subject Key undergoes multiple rotations (e.g. 10-20 rotated versions) or stores extensive access metadata, `value BYTEA` exceeds 2 KB.
- **TOAST Impact:**
  1. Compresses value using `pglz` or `lz4`.
  2. Moves chunks into a separate auxiliary TOAST table (`pg_toast_XXXXX`).
  3. Replaces `value` in the main heap with an 18-byte on-disk TOAST pointer.
- **Performance Penalty:** A `Get` query for an un-cached rotated key must execute an additional B-tree lookup and heap scan against the TOAST table to reconstruct chunk slices. This doubles the disk I/O cost per read.

---

## 3. Fill Factor, HOT Updates, and Dead Tuples

### 3.1 The 100% Fill Factor Default
OpenBao creates `vault_kv_store` without specifying a `fillfactor` (defaulting to 100%).
- Heap pages are packed completely full during initial inserts.
- When a key is rotated (`Put` update), PostgreSQL cannot place the new tuple version on the same page.
- **HOT (Heap-Only Tuples) Update Failure:** HOT optimization allows updates without modifying indexes IF the update is placed on the same page and no indexed columns are changed. Because pages are full, HOT updates **always fail**.

### 3.2 Dead Tuples and Index Bloat
- Every key update leaves a dead tuple on the original page.
- Both B-tree indexes (`pkey` and `idx`) must record a new leaf pointer to the new page.
- Vacuuming (`VACUUM`) is required to clean dead line pointers, leading to WAL amplification and index bloat over time.
