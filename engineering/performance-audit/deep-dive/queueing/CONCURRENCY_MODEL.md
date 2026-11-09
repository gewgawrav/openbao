# Mathematical Concurrency & Queueing Theory Model 🤖

- **Audit Classification:** `STATIC-ANALYSIS-CONFIRMED` / `INFERRED`

## Executive Summary
This document establishes an $M/M/c/K$ and $M/G/c$ queueing model for OpenBao under the Cloakit Privacy Vault workload.

---

## 1. Queueing Model Parameters

Let:
- $\lambda$: Request arrival rate (requests per second).
- $\mu$: Service rate per worker ($\mu = \frac{1}{T_{service}}$).
- $c$: Number of parallel workers ($c = 128$ for `max_parallel`, or $c = \text{CPU cores}$ for in-memory hits).
- $K$: Maximum queue buffer capacity before rejection.

### 1.1 In-Memory Cache Hit Path ($\mu_{warm}$)
For warm-path DEK unwraps served entirely from memory:
- $T_{service} \approx 25 \ \mu\text{s}$ ($0.000025 \text{ s}$).
- $\mu_{warm} = 40,000 \text{ req/sec per core}$.
- On a 16-core system, aggregate capacity $C_{max} \approx 640,000 \text{ req/sec}$.
- **Queue Wait:** Near zero. Bottlenecks only occur if CPU saturates 100%.

### 1.2 Storage Cache Miss Path ($\mu_{cold}$)
For cold-start Subject Key lookups requiring PostgreSQL read:
- $T_{service} \approx 2.5 \text{ ms}$ ($0.0025 \text{ s}$).
- $\mu_{cold} = 400 \text{ req/sec per DB connection}$.
- With `max_parallel = 128`:
  $$\text{Max Throughput } \Lambda_{cold} = 128 \times 400 = 51,200 \text{ req/sec}.$$
- If $\lambda$ exceeds $51,200$, queue length grows asymptotically towards infinity:
  $$L_q = \frac{P_0 (\lambda / \mu)^c \rho}{c! (1 - \rho)^2}$$
  where $\rho = \frac{\lambda}{c\mu}$. As $\rho \to 1$, $L_q \to \infty$.
