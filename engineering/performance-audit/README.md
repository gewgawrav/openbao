# OpenBao Performance Engineering Audit :robot:

## 1. Executive Summary

This audit represents a strictly offline, repository-only engineering investigation into the computational, architectural, and performance characteristics of OpenBao (`https://github.com/gewgawrav/openbao`).

The investigation was conducted under rigid boundaries:
- **No external connections:** No production/staging environments, databases, HSM/PKCS#11 hardware, or cloud endpoints were accessed.
- **No source code modifications:** Application code, schemas, and configurations remain untouched.
- **Repository-local evidence:** Findings are substantiated exclusively by Go source code analysis, static inspection, dependency definitions, and safe offline benchmarks.

### Scope & Structure of Deliverables
The audit deliverables are structured into the following files under `engineering/performance-audit/`:
1. `README.md` (this file): Executive summary, scope, methodological framework, taxonomy definitions, and architectural summary.
2. `findings.md`: Detailed engineering findings across core subsystems (Token Store, Transit Engine, Concurrency Sharding, Audit Logging, and Raft Storage Backend) with evidence classifications and impact dimensions.
3. `benchmarks.md`: Benchmark configurations, test harness executions, reproducible baseline figures, and memory profiling traces.

---

## 2. Evidence Classification Standard

Every performance finding and architectural observation in this report is rigorously tagged according to the following evidence taxonomy:

- **SOURCE-CONFIRMED:** Directly established from static examination of the source code logic and control flow.
- **STATIC-ANALYSIS-CONFIRMED:** Identified through offline static analysis tooling or AST inspection.
- **TEST-CONFIRMED:** Verified or reproduced through existing repository-local test suites.
- **BENCHMARK-CONFIRMED:** Supported by repository-local Go benchmark measurements executed in the local environment.
- **INFERRED RISK:** Plausible architectural risk based on algorithmic structure or concurrency patterns, requiring runtime validation under production-scale workloads.
- **UNKNOWN:** Cannot be determined from repository contents alone (e.g., hardware HSM latency, production database I/O, P99 latency SLA).

---

## 3. High-Level Architectural Analysis

OpenBao functions as a centralized cryptographic secret management server. Its core latency and throughput paths are dominated by:
1. **Barrier and Storage Operations:** The barrier encrypts and decrypts all persistent entries before writing to physical storage (e.g., Raft, Consul, File).
2. **Token Management & Accessor Indexing:** Token creation involves multiple synchronous storage writes (accessor index, primary ID, parent index) and cryptographic HMAC hashing.
3. **Transit Engine Cryptographic Dispatch:** Transit cryptographic operations handle batch inputs sequentially while holding read locks on policy structures.
4. **Lock Sharding & Key Hashing:** Key-based locking relies on hashing keys to select one of 256 mutex shards. Hashing overhead directly impacts write and read coordination latency.
5. **Audit Serialization & Filtering:** Every request/response passes through audit formatters where deep data cloning is performed before HMAC calculation.
