# Distributed Tracing Design & Context Propagation 🤖

- **Audit Classification:** `SOURCE-CONFIRMED` / `STATIC-ANALYSIS-CONFIRMED`

## Executive Summary
This report details OpenTelemetry (OTel) distributed tracing integration for Cloakit and OpenBao.

---

## 1. Distributed Trace Waterfall

```
+------------------------------------------------------------------------------------------------------+
|                                   DISTRIBUTED TRACE WATERFALL SPANS                                  |
+------------------------------------------------------------------------------------------------------+
| [Client Service] HTTP POST /transit/decrypt (traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736...)    |
|   |                                                                                                  |
|   +-- [OpenBao: Core.HandleRequest]                                                                  |
|         |                                                                                            |
|         +-- [OpenBao: Auth.ValidateToken] (0.12 ms)                                                  |
|         +-- [OpenBao: Transit.LockAcquisition] (0.02 ms)                                             |
|         +-- [OpenBao: Transit.CacheLookup] (0.01 ms)                                                 |
|         +-- [OpenBao: Crypto.AES_GCM_Open] (0.13 ms)                                                 |
|         +-- [OpenBao: Audit.EmitRecord] (0.04 ms - async)                                            |
+------------------------------------------------------------------------------------------------------+
```

### 1.1 W3C Trace Context Standard
OpenBao propagates `traceparent` and `tracestate` headers per W3C specifications, allowing end-to-end tracing from application database queries through Cloakit proxies to OpenBao.
