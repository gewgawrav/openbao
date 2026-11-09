# bbolt Physical Layout, Memory Mapping & Freelist Internals 🤖

## Executive Summary
This report analyzes the physical on-disk page layout of bbolt in OpenBao, memory mapping configurations, freelist mechanics, page splits, and allocation dynamics. We investigate why database files grow monotonically and evaluate garbage collection pressures.

---

## 1. Physical Page Structure & Header Layout

bbolt files consist of contiguous pages of uniform size (typically 4,096 bytes). Each page starts with an internal header:

```
+-------------------------------------------------------+
| Page ID (uint64, 8B)                                  |
| Flags (uint16, 2B): Branch(0x01), Leaf(0x02),         |
|                     Meta(0x04), Freelist(0x10)        |
| Count (uint16, 2B): Number of elements/leaf items     |
| Overflow (uint32, 4B): Number of following pages      |
+-------------------------------------------------------+
| Elements Array / Free Page IDs / Key-Value Data       |
+-------------------------------------------------------+
```

### 1.1 Meta Pages (Pages 0 and 1)
- bbolt allocates two alternating meta pages at file offset 0 and 4096.
- A write transaction writes to the alternate meta page, flips the transaction ID (`txid`), and executes an `fdatasync()`. This guarantees atomicity and crash consistency.

### 1.2 Leaf Pages and Overflow Pages
- Small keys and values (<2KB) are stored inside the leaf page alongside element offsets.
- **Large Values (>2KB - 4KB)**: If a single key-value entry exceeds available leaf page capacity, bbolt allocates contiguous **overflow pages** (`page.overflow = (size + 4095)/4096`).
- Large secrets or keys require sequential page allocations, increasing disk fragmentation.

---

## 2. Memory Mapping (mmap) & OS Memory Allocation

### 2.1 100GB Virtual Memory Allocation
- **Source**: `internal/physical/raft/vars_64bit.go:8` (`initialMmapSize = 100 * 1024 * 1024 * 1024`).
- **Classification**: `SOURCE-CONFIRMED`.
- On startup, bbolt mmaps a **100 GB sparse virtual address range** (`PROT_READ`, `MAP_SHARED`).
- **Operating System Impact**:
  - Virtual memory (`VIRT` in top) shows >100GB immediately upon startup.
  - Resident Set Size (`RES`) reflects only pages actually loaded into physical memory.
  - In containerized environments (Kubernetes/cgroups), memory limits tracking virtual memory or overcommit (`vm.max_map_count`) must be configured appropriately.

### 2.2 Linux `MAP_POPULATE` Dynamics
- **Source**: `internal/physical/raft/bolt_linux.go:19-40`.
- **Classification**: `SOURCE-CONFIRMED`.
- If `v.Total > uint64(dbSize)`: OpenBao requests `unix.MAP_POPULATE`.
- This causes the Linux kernel to pre-fault all pages of `vault.db` and `raft.db` into physical RAM during `bolt.Open()`, preventing page fault latency spikes on first read.
- Can be disabled via `BAO_RAFT_DISABLE_MAP_POPULATE=1`.

---

## 3. Freelist Management & Database File Growth

### 3.1 Freelist Map vs Freelist Array
- **Source**: `internal/physical/raft/raft.go:2182-2189`.
- **Classification**: `SOURCE-CONFIRMED`.
- OpenBao defaults to `bolt.FreelistMapType` (`FreelistType: bolt.FreelistMapType`).
- `FreelistMapType` uses Go hash sets to track free pages, reducing free page allocation complexity from $O(N)$ (in `FreelistArrayType`) to $O(1)$ operations.
- Operators can revert to array type via `BAO_RAFT_FREELIST_TYPE=array`.

### 3.2 `NoFreelistSync: true`
- **Source**: `internal/physical/raft/raft.go:2183`.
- **Classification**: `SOURCE-CONFIRMED`.
- By default, OpenBao configures `NoFreelistSync = true`.
- **Performance Benefit**: Avoids writing and syncing the entire freelist page structure to disk on every write transaction, significantly reducing write IOPS and metadata amplification.
- **Recovery Cost**: On crash or unclean shutdown, bbolt must reconstruct the freelist by performing a full B-tree scan of the entire database, increasing recovery time proportionally to database size.

### 3.3 Page Splits, Free Page Reuse & File Growth
- bbolt utilizes an **append-only copy-on-write** allocation model.
- When an existing key is updated:
  1. The existing page is marked as free in the active transaction.
  2. A new page is allocated (either from the freelist or by growing the file).
  3. The file size expands via `os.Truncate()`.
- **Monotonic File Growth**: bbolt **never shrinks its database file**. Once allocated, file size on disk never decreases, even if 90% of keys are deleted! Free pages are retained internally for future writes.
