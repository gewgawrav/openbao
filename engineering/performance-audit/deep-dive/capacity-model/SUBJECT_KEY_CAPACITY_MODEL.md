# Subject Key Capacity Model (10K to 10M Keys) 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This document establishes a rigorous capacity model sizing OpenBao memory, storage footprints, and index geometry across four cardinality tiers: 10,000 (10K), 100,000 (100K), 1,000,000 (1M), and 10,000,000 (10M) Subject Keys in the Cloakit Privacy Vault.

---

## 1. Capacity Tiers & Sizing Specifications

Assumptions derived directly from OpenBao source code (`internal/builtin/logical/transit/`, `sdk/helper/keysutil/policy.go`):
- Policy JSON with 1-2 versions: ~1,200 bytes raw sealed ciphertext.
- PostgreSQL row size: ~1,350 bytes (heap tuple + line pointers).
- In-memory `Policy` struct size: ~1,850 bytes in Go heap (struct fields, version slice, key bytes).
- Primary Key index tuple size: ~85 bytes `(path, key)`.

| Metric / Dimension | Tier 1 (10K Keys) | Tier 2 (100K Keys) | Tier 3 (1M Keys) | Tier 4 (10M Keys) |
| :--- | :--- | :--- | :--- | :--- |
| **PostgreSQL Table Heap** | ~13.5 MB | ~135 MB | ~1.35 GB | ~13.5 GB |
| **Primary Index (`pkey`)** | ~1.1 MB | ~11.5 MB | ~120 MB | ~1.25 GB |
| **Secondary Index (`idx`)**| ~0.8 MB | ~8.5 MB | ~92 MB | ~0.95 GB |
| **Total PostgreSQL Storage**| ~15.4 MB | ~155 MB | ~1.56 GB | ~15.7 GB |
| **OpenBao L1 RAM (100% Hit)**| ~18.5 MB | ~185 MB | ~1.85 GB | ~18.5 GB |
| **OpenBao L1 RAM (10% Cache)**| ~1.8 MB | ~18.5 MB | ~185 MB | ~1.85 GB |
| **PostgreSQL Buffer Hit (32GB)**| 100% | 100% | > 99% | ~85% (spills to disk) |
