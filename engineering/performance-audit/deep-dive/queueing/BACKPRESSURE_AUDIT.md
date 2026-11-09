# Backpressure Audit, Queue Growth & Memory Exhaustion 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report investigates whether OpenBao provides adequate backpressure mechanisms or allows requests, memory, and goroutines to accumulate unboundedly during traffic surges.

---

## 1. The Vulnerability: Unbounded HTTP Goroutine Spawning

### 1.1 `net/http` Server Architecture
- **Source Reference:** `internal/command/server.go`, Go standard library `net/http/server.go:3000-3100`
- OpenBao uses Go's standard `http.Server`. On every accepted TCP connection, Go creates a new goroutine:
  ```go
  go c.serve(connCtx)
  ```
- **Finding:** OpenBao does **NOT** enforce a global request concurrency limit at the HTTP transport layer.
- **Surge Dynamic:** If 10,000 client requests arrive per second and each request stalls for 200 ms waiting on database I/O, OpenBao spawns **2,000 concurrent goroutines**.
- **Memory Impact:** Each goroutine allocates a minimum 2 KB stack, rapidly scaling to 8-16 KB upon calling deep stack frames. 2,000 stalled goroutines consume 30-50 MB purely in thread stacks, plus intermediate buffers.

---

## 2. Lack of Explicit Storage Queue Timeouts

When requests queue in `PermitPool.Acquire()`:
- The context timeout passed from HTTP is respected IF the caller specifies a deadline.
- However, if the client sends requests with infinite or long (e.g. 60s) timeouts, requests queue indefinitely inside `PermitPool.sem`.
- As a consequence, OpenBao lacks a CoDel-style (Controlled Delay) or adaptive queue drop mechanism.
