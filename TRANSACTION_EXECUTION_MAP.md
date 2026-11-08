# TRANSACTION_EXECUTION_MAP.md
# OpenBao End-to-End Transaction Cost & Execution Map

This document establishes the authoritative, source-grounded execution map for all 16 major logical operations in OpenBao. For each operation, the complete call graph is traced across all 11 lifecycle phases:
1. HTTP Entry Point
2. Request Parsing & Validation
3. Authentication
4. Authorisation & Policy Evaluation
5. Namespace Resolution
6. Logical Backend Routing & Dispatch
7. Cryptographic Processing
8. Storage Reads
9. Storage Writes
10. Audit Event Generation
11. Response Construction & Serialization

---

## 1. Unified Request Ingress Architecture

All external HTTP requests enter through `internal/http/handler.go` and traverse a standardized pipeline before reaching operation-specific backend logic.

### 1.1 Ingress Pipeline & Middleware Chain
```
Client HTTP Request
   │
   ▼
[internal/http/handler.go: wrapMaxRequestSizeHandler] (DefaultMaxRequestSize = 32MB)
   │
   ▼
[internal/http/handler.go: wrapMetricsListenerHandler] (Prometheus / StatsD ingress tracking)
   │
   ▼
[internal/http/handler.go: genericWrapping] (Panic recovery, custom response headers)
   │
   ▼
[internal/http/handler.go: wrapClientCertificateHandler] (mTLS / forwarded client certs RFC 9440)
   │
   ▼
[internal/http/handler.go: rateLimitQuotaWrapping] (Rate limit quota enforcement)
   │
   ▼
[internal/http/handler.go: wrapCORSHandler] (CORS preflight & header injection)
   │
   ▼
[internal/http/handler.go: wrapHelpHandler] (Help query interception)
   │
   ▼
[internal/http/handler.go: wrapIndexForwardHandler] (Standby request forwarding or local handling)
   │
   ▼
[internal/http/handler.go: mux.ServeHTTP] (/v1/ routes -> handleLogicalInternal)
   │
   ▼
[internal/http/logical.go: buildLogicalRequest]
   ├── buildLogicalRequestNoAuth(): URL parsing, method-to-op mapping, UUID generation (`uuid.GenerateUUID()`), JSON unmarshaling (`parseBodyData()`)
   ├── requestAuth(): Extracts token from X-Vault-Token or Authorization: Bearer
   ├── requestWrapInfo(): Extracts X-Vault-Wrap-TTL header
   └── ParseMFAHeaders(): Parses X-Vault-MFA header
   │
   ▼
[internal/http/handler.go: request()]
   │ Calls Core.HandleRequest(ctx, req)
   ▼
[internal/vault/request_handling.go: HandleRequest()]
   │ Switched lock: c.stateLock.RLock() (Checks sealed state, active context)
   │ Namespace resolution: c.namespaceStore.ResolveNamespaceFromRequest()
   │ Inline authentication check: c.handleInlineAuth()
   │ Dispatch to c.handleCancelableRequest(ctx, req)
   ▼
[internal/vault/request_handling.go: handleCancelableRequest()]
   │ Router matching mount: c.router.MatchingMount(ctx, req.Path)
   │ Router resolve path: c.router.ResolvePath(ctx, req.MountPoint, req.Path, req.Data)
   │ Populate token entry: c.PopulateTokenEntry(ctx, req)
   │ Dispatch to c.handleRequest(ctx, req) or c.handleLoginRequest(ctx, req)
   ▼
[internal/vault/request_handling.go: handleRequest()]
   ├── Authentication & Token Verification: c.CheckToken(ctx, req, false)
   │    ├── c.fetchACLTokenEntryAndEntity(ctx, req) -> c.tokenStore.Lookup(ctx, req.ClientToken)
   │    ├── Salt calculation: c.tokenStore.SaltID(ctx, id) (SHA1 or HMAC-SHA256)
   │    ├── Barrier read: ts.idView(ns).Get(ctx, lookupID) -> AES-GCM Decrypt
   │    ├── Identity lookup: c.fetchEntityAndDerivedPolicies()
   │    └── Policy building: c.policyStore.ACL() -> construct ACL trees
   ├── Existence check: c.router.RouteExistenceCheck(ctx, req) (Forces Create vs Update)
   ├── Policy evaluation: c.performPolicyChecks() -> acl.AllowOperation(ctx, req)
   ├── Token usage update: c.tokenStore.UseToken(ctx, te) (Decrements NumUses if limited)
   ├── Audit Request Logging: c.auditBroker.LogRequest() (Synchronous write to all audit devices)
   ├── Dispatch Routing: c.doRoutingIfApproved() -> c.router.Route(ctx, req) -> Backend handler
   ├── Secret Lease Registration: ExpirationManager.RegisterSecret() (if dynamic lease generated)
   ├── Audit Response Logging: c.auditBroker.LogResponse() (Synchronous write to all audit devices)
   └── Response Construction: logical.LogicalResponseToHTTPResponse() -> JSON encoding
```

---

## 2. Operation-by-Operation Deep Traces

### Operation 1: Secret Creation (KV v2 Data Write)
- **HTTP Entry Point**: `POST /v1/secret/data/:path` handled by `internal/http/logical.go: handleLogicalInternal()` via mux match `/v1/`.
- **Request Parsing**: `internal/http/logical.go: buildLogicalRequestNoAuth()` maps `POST` to `logical.UpdateOperation`. `internal/http/handler.go: parseBodyData()` reads up to 32MB JSON payload into `map[string]any` via `jsonutil.DecodeJSONFromReader()`. Generates UUID v4 (`uuid.GenerateUUID()`).
- **Authentication**: `internal/vault/request_handling.go: CheckToken()` calls `fetchACLTokenEntryAndEntity()`. Reads token from `X-Vault-Token`. `internal/vault/token_store.go: Lookup()` hashes token via `SaltID()` (`internal/vault/token_store.go:1111`) using HMAC-SHA256. Loads token metadata from barrier view `idView(ns).Get(ctx, lookupID)` (`internal/vault/token_store.go:1833`). Barrier decrypts token entry using `AESGCMBarrier.Get()` -> `cipher.AEAD.Open()`.
- **Authorisation**: `internal/vault/policy/policy_store.go: ACL()` fetches token policies from `tokenPoliciesLRU` cache or barrier view `policy/`. `internal/vault/policy/acl.go: NewACL()` builds radix prefix tree. `internal/vault/request_handling.go: RouteExistenceCheck()` calls `kv.dataExistenceCheck()` (`internal/builtin/logical/kv/path_data.go:68`) which reads metadata key `metadata/:path` from storage; if absent, transforms operation to `logical.CreateOperation`. `acl.AllowOperation()` verifies `create` capability on `secret/data/:path`.
- **Namespace Resolution**: `internal/vault/request_handling.go:582` calls `c.namespaceStore.ResolveNamespaceFromRequest()` matching `X-Vault-Namespace` header or path prefix.
- **Logical Backend Dispatch**: `internal/vault/core.go` router matches mount prefix `secret/` to `*versionedKVBackend`. Calls `versionedKVBackend.HandleRequest()` -> `pathDataWrite()` (`internal/builtin/logical/kv/path_data.go:261`).
- **Cryptographic Processing**:
  1. Token hash HMAC-SHA256 in auth.
  2. Storage encryption for version payload: `AESGCMBarrier.putInternal()` generates 12-byte random IV, encrypts marshaled protobuf `Version` using AES-256-GCM (`internal/vault/barrier/aes_gcm.go:1016`).
  3. Storage encryption for key metadata: Encrypts marshaled protobuf `KeyMetadata` using AES-256-GCM.
  4. Audit logging HMAC-SHA256: Hashes client token and sensitive request data before audit output.
- **Storage Reads**:
  1. `sys/token/id/<salted-token-id>` (Token lookup).
  2. `policy/<policy-name>` (if policy cache miss).
  3. `metadata/:path` (Existence check during auth/authz).
  4. `config` (KV engine configuration read).
  5. `metadata/:path` (Lock re-read inside `pathDataWrite()`).
- **Storage Writes**:
  1. `data/:path` (New version protobuf: `req.Storage.Put()` at `path_data.go:343`).
  2. `metadata/:path` (Updated metadata with version map: `writeKeyMetadata()` at `path_data.go:351`).
  3. (Optional) Old version deletion if `max_versions` exceeded.
  4. Raft Replication: Each physical Put translates to Raft Log entry proposed via `RaftBackend.applyLog()` (`internal/physical/raft/raft.go:1820`).
- **Audit Generation**: `AuditBroker.LogRequest()` before backend execution; `AuditBroker.LogResponse()` after execution (`internal/vault/request_handling.go:1240`). Formats JSON audit entry, computes HMACs, writes synchronously to all audit backends (`internal/builtin/audit/file/backend.go:207`).
- **Response Construction**: `respondLogical()` converts `logical.Response` with version metadata into HTTP 200 JSON payload (`internal/http/logical.go:344`).

---

### Operation 2: Secret Retrieval (KV v2 Data Read)
- **HTTP Entry Point**: `GET /v1/secret/data/:path` -> `internal/http/logical.go: handleLogicalInternal()`.
- **Request Parsing**: `buildLogicalRequestNoAuth()` maps `GET` to `logical.ReadOperation`. Query parameters parsed (e.g., `?version=2`).
- **Authentication**: `CheckToken()` -> `tokenStore.Lookup()` -> `SaltID()` -> Barrier read `sys/token/id/<salted-id>` -> Barrier AES-GCM Decrypt -> JSON decode token entry.
- **Authorisation**: `policyStore.ACL()` builds/evaluates permissions. `acl.AllowOperation()` confirms `read` capability on `secret/data/:path`.
- **Namespace Resolution**: Resolved via `namespaceStore.ResolveNamespaceFromRequest()`.
- **Logical Backend Dispatch**: Router directs request to `versionedKVBackend.pathDataRead()` (`internal/builtin/logical/kv/path_data.go:88`).
- **Cryptographic Processing**:
  1. Token HMAC-SHA256 during auth.
  2. Barrier AES-GCM Decrypt for `metadata/:path`.
  3. Barrier AES-GCM Decrypt for `data/:path` version data.
  4. Audit HMAC-SHA256 calculations for tokens/request fields.
- **Storage Reads**:
  1. Token barrier read (`sys/token/id/<salted-id>`).
  2. KV key metadata read (`metadata/:path`).
  3. KV secret version read (`data/:path` or version storage path).
- **Storage Writes**: 0 (pure read operation; no lease registered for KV v2).
- **Audit Generation**: `AuditBroker.LogRequest()` + `AuditBroker.LogResponse()` (Response data HMAC'd unless configured otherwise).
- **Response Construction**: Constructs `logical.Response{Data: map[string]any{"data": vData, "metadata": meta}}`, JSON-marshaled to client.

---

### Operation 3: Secret Update (KV v2 Data Patch / Update)
- **HTTP Entry Point**: `POST /v1/secret/data/:path` or `PATCH /v1/secret/data/:path`.
- **Request Parsing**: `POST` mapped to `logical.UpdateOperation`; `PATCH` mapped to `logical.PatchOperation`. Checks `Content-Type: application/merge-patch+json` for PATCH (`internal/http/logical.go:128`).
- **Authentication**: Standard token lookup and barrier decryption.
- **Authorisation**: `RouteExistenceCheck()` verifies secret exists -> sets `UpdateOperation`. ACL checks `update` or `patch` capabilities.
- **Namespace Resolution**: Standard namespace context resolution.
- **Logical Backend Dispatch**: Dispatched to `pathDataWrite()` or `pathDataPatch()`.
- **Cryptographic Processing**:
  1. Auth token HMAC-SHA256 & barrier AES-GCM decrypt.
  2. Barrier AES-GCM encryption of new version data.
  3. Barrier AES-GCM encryption of updated metadata entry.
  4. Audit log HMAC-SHA256 calculations.
- **Storage Reads**:
  1. Token storage entry.
  2. `metadata/:path` (Existence check).
  3. `config` (KV engine configuration).
  4. `metadata/:path` (Metadata lookup under lock).
  5. (For PATCH): Current version data read (`data/:path`).
- **Storage Writes**:
  1. Storage write of new version `Version{Data: marshaledData, CreatedTime: ...}`.
  2. Storage write of `KeyMetadata{CurrentVersion: N+1, ...}`.
  3. Raft replication proposals for each write.
- **Audit Generation**: Synchronous request and response logging via `AuditBroker`.
- **Response Construction**: Serializes version metadata response to HTTP 200.

---

### Operation 4: Secret Deletion (KV v2 Soft Delete)
- **HTTP Entry Point**: `DELETE /v1/secret/data/:path` or `POST /v1/secret/delete/:path`.
- **Request Parsing**: `DELETE` mapped to `logical.DeleteOperation`.
- **Authentication**: Token lookup, salting, barrier decrypt.
- **Authorisation**: ACL check verifies `delete` capability on `secret/data/:path`.
- **Namespace Resolution**: Resolves target namespace.
- **Logical Backend Dispatch**: Dispatched to `pathDataDelete()` (`internal/builtin/logical/kv/path_data.go:614`) or `pathDeleteWrite()` (`internal/builtin/logical/kv/path_delete.go:154`).
- **Cryptographic Processing**:
  1. Token HMAC-SHA256 & barrier decrypt.
  2. Barrier AES-GCM encryption of updated `KeyMetadata` (marking version `DeletionTime = Now()`). Note: Soft delete updates metadata rather than purging payload.
  3. Audit HMAC hashing.
- **Storage Reads**:
  1. Token entry.
  2. `metadata/:path`.
- **Storage Writes**:
  1. `metadata/:path` (persists tombstone / deletion time).
  2. Raft log proposal for metadata update.
- **Audit Generation**: Standard request & response audit logging.
- **Response Construction**: Returns HTTP 204 No Content (empty response body).

---

### Operation 5: Transit Encryption
- **HTTP Entry Point**: `POST /v1/transit/encrypt/:name` -> `handleLogicalInternal()`.
- **Request Parsing**: Decodes JSON payload: `plaintext` (base64 string), optional `context`, `key_version`, `associated_data`.
- **Authentication**: Token verification via `tokenStore.Lookup()`.
- **Authorisation**: Evaluates `update` permission on `transit/encrypt/:name`.
- **Namespace Resolution**: Resolved via `namespaceStore`.
- **Logical Backend Dispatch**: Router routes to `transit.pathEncryptWrite()` (`internal/builtin/logical/transit/path_encrypt.go:300`).
- **Cryptographic Processing**:
  1. Token HMAC & barrier decrypt.
  2. Base64 decode of plaintext & context.
  3. Policy retrieval: `b.GetPolicy()` checks in-memory LM cache (`backend.go:145`). If cached, 0 storage reads!
  4. Symmetric encryption: `p.EncryptWithFactory()` -> AES-256-GCM (or ChaCha20-Poly1305) encrypts plaintext with derived/raw key version. Formats ciphertext: `vault:v1:<base64-ciphertext>`.
  5. Audit HMAC generation.
- **Storage Reads**:
  1. Token entry read (Barrier).
  2. (If key policy not cached): `archive/` or `policy/:name` storage read.
- **Storage Writes**: 0 (Transit encryption is purely in-memory computation unless upsert is triggered).
- **Audit Generation**: Request and response audit logs. Plaintext is omitted/sanitized; ciphertext logged.
- **Response Construction**: Returns `{"data": {"ciphertext": "vault:v1:...", "key_version": 1}}`.

---

### Operation 6: Transit Decryption
- **HTTP Entry Point**: `POST /v1/transit/decrypt/:name` -> `handleLogicalInternal()`.
- **Request Parsing**: Decodes JSON: `ciphertext` string, optional `context`, `associated_data`.
- **Authentication**: Standard token verification.
- **Authorisation**: Checks `update` capability on `transit/decrypt/:name`.
- **Namespace Resolution**: Standard namespace resolution.
- **Logical Backend Dispatch**: Routed to `transit.pathDecryptWrite()` (`internal/builtin/logical/transit/path_decrypt.go:100`).
- **Cryptographic Processing**:
  1. Token HMAC & barrier decrypt.
  2. Key policy lookup from cache.
  3. Ciphertext parsing: Extracts version ID from prefix `vault:v<version>:`.
  4. AEAD Decryption: `p.DecryptWithFactory()` -> AES-GCM Open using the key version retrieved from policy. Returns base64 encoded plaintext.
  5. Audit logging HMAC hashing.
- **Storage Reads**:
  1. Token storage entry.
  2. (If policy uncached): Transit key policy barrier read.
- **Storage Writes**: 0.
- **Audit Generation**: LogRequest and LogResponse. Decrypted plaintext is never written to audit log.
- **Response Construction**: Returns `{"data": {"plaintext": "<base64-encoded-plaintext>"}}`.

---

### Operation 7: Transit Batch Encryption
- **HTTP Entry Point**: `POST /v1/transit/encrypt/:name` with `batch_input: [...]`.
- **Request Parsing**: `decodeEncryptBatchRequestItems()` parses array of $N$ items without mapstructure overhead (`internal/builtin/logical/transit/path_encrypt.go:183`).
- **Authentication**: Single token verification for entire batch.
- **Authorisation**: Single ACL authorization check on path `transit/encrypt/:name`.
- **Namespace Resolution**: Standard namespace context.
- **Logical Backend Dispatch**: Handled by `transit.pathEncryptWrite()`.
- **Cryptographic Processing**:
  1. Single token validation crypto.
  2. Single policy acquisition from cache under RLock.
  3. Sequential loop: $N$ AES-GCM Encrypt operations (`p.EncryptWithFactory()`).
  4. Audit logging: Formats batch output.
- **Storage Reads**: 1 (Token lookup only, assuming key is cached).
- **Storage Writes**: 0.
- **Audit Generation**: Request & response logged with batch results.
- **Response Construction**: Returns `{"data": {"batch_results": [{"ciphertext": "vault:v1:..."}, ...]}}`.

---

### Operation 8: Transit Batch Decryption
- **HTTP Entry Point**: `POST /v1/transit/decrypt/:name` with `batch_input: [...]`.
- **Request Parsing**: `decodeDecryptBatchRequestItems()` parses array of $N$ ciphertext objects.
- **Authentication**: Single token verification.
- **Authorisation**: Single ACL authorization check.
- **Namespace Resolution**: Standard.
- **Logical Backend Dispatch**: Handled by `transit.pathDecryptWrite()`.
- **Cryptographic Processing**:
  1. Token verification.
  2. Cached policy lookup.
  3. Sequential loop: $N$ AES-GCM Decrypt operations.
  4. Audit logging.
- **Storage Reads**: 1 (Token lookup).
- **Storage Writes**: 0.
- **Audit Generation**: Full batch request & response logged.
- **Response Construction**: Returns `{"data": {"batch_results": [{"plaintext": "..."}, ...]}}`.

---

### Operation 9: Token Creation (Service Token)
- **HTTP Entry Point**: `POST /v1/auth/token/create` -> `handleLogicalInternal()`.
- **Request Parsing**: Decodes JSON parameters: `policies`, `ttl`, `meta`, `num_uses`, `display_name`, `orphan`.
- **Authentication**: Caller token lookup and validation via `CheckToken()`.
- **Authorisation**: ACL verifies `update` capability on `auth/token/create`.
- **Namespace Resolution**: Standard.
- **Logical Backend Dispatch**: Dispatched to `TokenStore.handleCreate()` (`internal/vault/token_store.go`). Calls `ts.create()` (`internal/vault/token_store.go:1255`).
- **Cryptographic Processing**:
  1. CSPRNG generation: 32 random bytes for Token ID via `base62.Random()`.
  2. CSPRNG generation: 32 random bytes for Accessor ID.
  3. CSPRNG generation: 32 random bytes for Cubbyhole ID.
  4. HMAC-SHA256 computation: `SaltID()` for Accessor indexing.
  5. HMAC-SHA256 computation: `SaltID()` for Token ID indexing.
  6. Server-Side Consistent (SSC) Token HMAC signature generation: `CalculateSignedTokenHMAC()` (`internal/vault/token_store.go:1522`).
  7. Barrier AES-GCM Encrypt for Accessor storage entry (`accessorView.Put()`).
  8. Barrier AES-GCM Encrypt for Token storage entry (`idView.Put()`).
  9. (If parent exists): Barrier AES-GCM Encrypt for Parent index entry (`parentView.Put()`).
- **Storage Reads**:
  1. Caller token read.
  2. Parent token lookup (if parent exists, `ts.Lookup(ctx, entry.Parent)` at `token_store.go:1568`).
- **Storage Writes**:
  1. `sys/token/accessor/<salted-accessor>` (Accessor mapping).
  2. `sys/token/parent/<salted-parent-id>/<salted-child-id>` (Parent index mapping).
  3. `sys/token/id/<salted-token-id>` (Primary token payload).
  4. Expiration Manager Lease Entry (if lease created).
  5. 3 to 4 Raft log proposals committed to physical disk.
- **Audit Generation**: Audit request & response logged (Client token accessor logged, token ID HMAC'd).
- **Response Construction**: Returns `{"auth": {"client_token": "hvs....", "accessor": "...", "policies": [...]}}`.

---

### Operation 10: Token Validation (CheckToken / Lookup)
- **HTTP Entry Point**: Internal critical path for every authenticated request, or `POST /v1/auth/token/lookup-self` / `GET /v1/auth/token/lookup-self`.
- **Request Parsing**: Token extracted from header `X-Vault-Token`.
- **Authentication**:
  1. `internal/vault/request_handling.go: fetchACLTokenEntryAndEntity()` invokes `c.tokenStore.Lookup(ctx, req.ClientToken)`.
  2. Detects token type: If starts with `hvb.` or `b.`, branches to `lookupBatchTokenInternal()`. If service token `hvs.` or `s.`, decodes SSC HMAC signature.
  3. Computes salted hash: `SaltID(ctx, id)` (`token_store.go:1111`) with SHA-256 HMAC against cached namespace salt.
  4. Barrier lookup: `ts.idView(ns).Get(ctx, lookupID)` -> Reads physical storage entry.
  5. Decrypts barrier entry: AES-256-GCM Open.
  6. Deserializes JSON payload into `logical.TokenEntry`.
  7. Decrements usage if `NumUses > 0` via `UseToken()` (triggers barrier write).
- **Authorisation**: Checks if token is expired, revoked, or tainted.
- **Namespace Resolution**: Resolves token's home namespace from token suffix or metadata.
- **Cryptographic Processing**:
  1. SSC signature verification (HMAC-SHA256).
  2. Salt hashing (HMAC-SHA256).
  3. Storage decryption (AES-256-GCM).
- **Storage Reads**: 1 barrier read (`sys/token/id/<salted-id>`).
- **Storage Writes**: 0 (or 1 write if use-count decremented).
- **Audit Generation**: Token accessor and identity metadata attached to request log.
- **Response Construction**: Token context attached to request context for downstream processing.

---

### Operation 11: Policy Evaluation
- **HTTP Entry Point**: Executed inline within `handleRequest()` on every API request.
- **Request Parsing**: Operates on `req.Path`, `req.Operation`, and `req.Data`.
- **Authentication**: Requires resolved `TokenEntry` and `Entity`.
- **Authorisation Pipeline**:
  1. `fetchEntityAndDerivedPolicies()` collects policy names from token, entity, and groups.
  2. `policyStore.ACL()` retrieves `Policy` structures: checks 2-Queue LRU cache `tokenPoliciesLRU`. If cache miss, reads barrier view `sys/policy/<name>` and parses HCL/JSON.
  3. Resolves templated identity policies: If templated (`pol.Templated == true`), parses policy with dynamic substitution (`ParseACLPolicyWithTemplating()`).
  4. Radix tree construction: `NewACL()` builds exact match and prefix radix trees (`internal/vault/policy/acl.go:76`).
  5. Evaluation: `acl.AllowOperation(ctx, req)` performs:
     - Exact path match lookup (`a.exactRules.Get(path)`).
     - Prefix tree match (`a.prefixRules.LongestPrefix(path)`).
     - Segment wildcard evaluation (`a.segmentWildcardPaths`).
     - Capability bitmask comparison (`CapabilitiesBitmap & requiredBit`).
     - Allowed/Denied parameter enforcement (`matchParameters()`).
- **Namespace Resolution**: Namespace path prepended to request path for cross-namespace checks.
- **Cryptographic Processing**: None (unless reading uncached policy from barrier).
- **Storage Reads**: 0 on cache hits; 1 barrier read per uncached policy on cache miss.
- **Storage Writes**: 0.
- **Audit Generation**: Granting policy names captured for inclusion in audit log.
- **Response Construction**: Passes or returns 403 Forbidden (`logical.ErrPermissionDenied`).

---

### Operation 12: PKI Certificate Issuance
- **HTTP Entry Point**: `POST /v1/pki/issue/:role` -> `handleLogicalInternal()`.
- **Request Parsing**: Decodes JSON: `common_name`, `alt_names`, `ip_sans`, `ttl`, `format` (`pem`, `der`, `pem_bundle`), `private_key_format`.
- **Authentication**: Standard caller token verification.
- **Authorisation**: Evaluates `update` permission on `pki/issue/:role`.
- **Namespace Resolution**: Standard.
- **Logical Backend Dispatch**: Dispatched to `internal/builtin/logical/pki/path_issue_sign.go: pathIssueSignCert()`.
- **Cryptographic Processing**:
  1. Token verification HMAC/AES-GCM.
  2. CA Signing Bundle lookup: `fetchCaSigningBundle()` reads CA cert & private key from storage/cache.
  3. Key Generation: Generates ephemeral client private key (RSA 2048/4096 or ECDSA P-256/P-384/Ed25519) via `crypto/rand`.
  4. CSR / Template Construction: Builds `x509.Certificate` template with SANs, KeyUsage, ExtKeyUsage, Policies, CRL endpoints.
  5. Certificate Signing: `x509.CreateCertificate()` signs client cert with CA private key (CPU intensive!).
  6. Serialization: Encodes cert and private key to PEM blocks.
- **Storage Reads**:
  1. Caller token entry.
  2. Role configuration (`roles/:role`).
  3. CA Certificate & Key bundle (`config/ca_bundle` or issuer storage).
- **Storage Writes**:
  1. (If `role.NoStore == false`): Stored certificate entry: `certs/<serial>` (`path_issue_sign.go:785`).
  2. (If dynamic lease enabled): Expiration manager lease entry.
  3. Raft log proposals for stored certificate and lease.
- **Audit Generation**: Full audit request and response logging (Certificate serial logged).
- **Response Construction**: Returns `{"data": {"certificate": "-----BEGIN CERTIFICATE...", "issuing_ca": "...", "private_key": "-----BEGIN RSA PRIVATE KEY...", "serial_number": "..."}}`.

---

### Operation 13: Lease Renewal
- **HTTP Entry Point**: `PUT /v1/sys/leases/renew` -> `handleLogicalInternal()`.
- **Request Parsing**: Decodes `lease_id` and optional `increment` (duration in seconds).
- **Authentication**: Caller token validated via `CheckToken()`.
- **Authorisation**: ACL checks `update` on `sys/leases/renew` and backend-specific path.
- **Namespace Resolution**: Extracts embedded namespace ID from `lease_id` string (`SplitIDFromString()`).
- **Logical Backend Dispatch**: Router directs request to `ExpirationManager.Renew()` (`internal/vault/expiration.go:1261`).
- **Cryptographic Processing**:
  1. Token auth HMAC & barrier decrypt.
  2. Barrier AES-GCM Decrypt of lease entry.
  3. Barrier AES-GCM Encrypt of updated lease entry.
  4. Audit HMAC hashing.
- **Storage Reads**:
  1. Caller token entry.
  2. Lease entry read: `sys/expire/id/<lease-id>` (`m.loadEntry()`).
  3. Token lookup of lease's client token (to verify token hasn't expired).
- **Storage Writes**:
  1. Updated lease entry: `sys/expire/id/<lease-id>` with new `ExpireTime` and `LastRenewalTime` (`m.persistEntry()`).
  2. Raft log proposal for persisted lease update.
- **Audit Generation**: Synchronous request and response audit logs.
- **Response Construction**: Returns updated `lease_duration`, `renewable: true`, `lease_id`.

---

### Operation 14: Lease Revocation
- **HTTP Entry Point**: `PUT /v1/sys/leases/revoke` -> `handleLogicalInternal()`.
- **Request Parsing**: Decodes `lease_id`.
- **Authentication**: Caller token validated.
- **Authorisation**: ACL checks `update` capability on `sys/leases/revoke`.
- **Namespace Resolution**: Resolves namespace from `lease_id`.
- **Logical Backend Dispatch**: Dispatched to `ExpirationManager.Revoke()` -> `revokeCommon()` (`internal/vault/expiration.go:1034`).
- **Cryptographic Processing**:
  1. Token auth HMAC & barrier decrypt.
  2. Barrier decrypt of lease entry.
  3. Backend revocation callback (e.g., database user drop, AWS IAM credential deletion).
  4. Audit HMAC hashing.
- **Storage Reads**:
  1. Caller token entry.
  2. Lease storage entry: `sys/expire/id/<lease-id>`.
  3. (If batch token): Parent token lookup.
- **Storage Writes**:
  1. Storage Delete: `sys/expire/id/<lease-id>` (`m.deleteEntry()`).
  2. Storage Delete: Secondary token index `sys/expire/token/<token-id>/<lease-id>`.
  3. Raft log proposals for each physical deletion.
- **Audit Generation**: Logs lease revocation event.
- **Response Construction**: Returns HTTP 204 or HTTP 200 empty data.

---

### Operation 15: Key Rotation (Barrier Master Key Rotation)
- **HTTP Entry Point**: `POST /v1/sys/rotate` -> `handleLogicalInternal()`.
- **Request Parsing**: No body required; root or sudo privileges required.
- **Authentication**: Caller token lookup (must possess root or `sys/rotate` sudo capability).
- **Authorisation**: Verified against root path policies.
- **Namespace Resolution**: Target namespace resolved.
- **Logical Backend Dispatch**: Dispatched to `SealManager.RotateBarrierKey()` (`internal/vault/rotate.go:106`) -> `AESGCMBarrier.Rotate()` (`internal/vault/barrier/aes_gcm.go:571`).
- **Cryptographic Processing**:
  1. Generates 32-byte cryptographic random key via `AESGCMBarrier.GenerateKey()`.
  2. Instantiates new AES-256-GCM AEAD cipher for `term + 1`.
  3. Re-encrypts entire Keyring structure with Root Seal Key.
  4. Updates barrier active term.
  5. (If HA enabled): Calls `CreateUpgrade()` to write upgrade key for standby nodes.
- **Storage Reads**:
  1. Caller token entry.
  2. Current barrier keyring storage entry (`core/keyring`).
- **Storage Writes**:
  1. Updated keyring entry (`core/keyring`) encrypted with Root Seal Key.
  2. Upgrade term record (`core/upgrade`) if HA enabled.
  3. Raft log proposal to replicate new keyring across cluster.
- **Audit Generation**: Logs key rotation audit event.
- **Response Construction**: Returns HTTP 200 with new rotation term and timestamp.

---

### Operation 16: Audit Event Generation
- **HTTP Entry Point**: Synchronous callback invoked twice per request within `handleRequest()`: `AuditBroker.LogRequest()` (pre-flight) and `AuditBroker.LogResponse()` (post-flight).
- **Request Parsing**: Wraps `*logical.Request`, `*logical.Response`, `*logical.Auth`, and context headers into `*logical.LogInput`.
- **Authentication / Identity**: Ingests client token, accessor, entity ID, display name, and namespace.
- **Authorisation**: Checks `audit_non_hmac_request_keys` and `audit_non_hmac_response_keys` from mount cache.
- **Namespace Resolution**: Captures request namespace ID and path.
- **Logical Backend Dispatch**: Handled by `AuditBroker` iterating through all registered audit backends (file, syslog, socket).
- **Cryptographic Processing**:
  1. For every sensitive data key, token, and header: computes HMAC-SHA256 salted hash using backend-specific salt: `be.backend.GetHash()`.
  2. If salt is not cached: reads salt from storage `sys/audit/<name>/salt`.
- **Storage Reads**: 0 (assuming audit backend salts are cached in memory).
- **Storage Writes**: 0 to Vault barrier storage; 1 blocking I/O write per registered audit backend to disk file/pipe/socket (`internal/builtin/audit/file/backend.go:225`).
- **Audit Broker Semantics**: OpenBao requires **at least one** audit device to successfully log each event before allowing the API request to succeed or fail. If all audit devices fail or block, the entire API request is aborted (`ErrInternalError` or 500 status).
- **Response Construction**: In-memory serialization of `AuditRequestEntry` / `AuditResponseEntry` to JSON buffer.
