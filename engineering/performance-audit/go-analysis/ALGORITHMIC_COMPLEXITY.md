# Algorithmic Efficiency & Computational Complexity Analysis

## Overview

This report audits the asymptotic complexity and data structure efficiency of critical algorithms in OpenBao. While prefix routing in the core router utilizes radix trees achieving O(k) complexity (where k is path length), several authorization and entity management subsystems rely on linear scans, nested quadratic loops, and runtime sorting of rule sets.

---

## Findings

### Finding ALG-001: Dynamic Slice Sorting in Segment Wildcard ACL Evaluation
- **Finding ID**: ALG-001
- **Category**: Algorithmic Complexity / Sorting
- **File path**: `internal/vault/policy/acl.go`
- **Function**: `ACL.CheckAllowedFromNonExactPaths`
- **Line range**: Lines 830–845
- **Existing implementation**:
  When evaluating access for paths that match segment wildcards (`+` or `*`):
  ```go
  // SWCPATH loop checks all segmentWildcardPaths
  // Appends matching wcPathDescr to wcPathDescrs
  if bareMount || len(wcPathDescrs) == 0 {
      return nil
  }
  sort.Slice(wcPathDescrs, less)
  return wcPathDescrs[len(wcPathDescrs)-1].perms
  ```
- **Technical inefficiency**:
  For every single authorization check on non-exact paths:
  1. It loops over the entire map of `a.segmentWildcardPaths`.
  2. For every candidate, it performs `strings.Split` on both the path and the wildcard pattern.
  3. All matching descriptors are collected into a newly allocated slice `wcPathDescrs`.
  4. `sort.Slice` is invoked, which runs in O(M log M) time (where M is the number of matching wildcard rules) using reflection-based swaps.
  5. Only the highest-priority element (`len(wcPathDescrs)-1`) is returned.
- **Why it matters**:
  Sorting an entire slice just to find the single maximum element is an O(M log M) algorithm with heap allocation, when finding the maximum element can be done in O(M) time and O(1) space with a single pass tracker.
- **Potential improvement**:
  1. Replace `sort.Slice` with a linear O(M) scan tracking `bestMatch *wcPathDescr` using the existing `less(candidate, currentBest)` comparator.
  2. Index segment wildcards hierarchically by segment count to avoid scanning unrelated wildcard rules.
- **Behavioural risks**: Low.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/policy/acl.go:830-845`).

---

### Finding ALG-002: O(N * M) Quadratic Alias Comparison Loop in Identity Merge
- **Finding ID**: ALG-002
- **Category**: Algorithmic Complexity / Nested Loops
- **File path**: `internal/vault/identity/store_entities.go`
- **Function**: `IdentityStore.mergeEntity`
- **Line range**: Lines 920–975
- **Existing implementation**:
  In `mergeEntity`:
  ```go
  for _, fromEntityID := range sanitizedFromEntityIDs {
      ...
      if !forceMergeAliases && len(conflictingAliasIDsToKeep) == 0 {
          for _, toAlias := range toEntity.Aliases {
              for _, fromAlias := range fromEntity.Aliases {
                  id, mountAccessorInAnotherFromEntity := fromEntityAccessors[fromAlias.MountAccessor]
                  ...
                  fromEntityAccessors[fromAlias.MountAccessor] = fromEntityID
                  if toAlias.MountAccessor == fromAlias.MountAccessor {
                      // Alias clash handling
                      mountValidationRespToAlias := i.router.ValidateMountByAccessor(toAlias.MountAccessor)
                      mountValidationRespFromAlias := i.router.ValidateMountByAccessor(fromAlias.MountAccessor)
  ```
- **Technical inefficiency**:
  For every entity in `sanitizedFromEntityIDs`, the algorithm performs a nested O(N_to * M_from) loop comparing every alias of `toEntity` with every alias of `fromEntity`. Inside the innermost loop, when a match is found, it calls `i.router.ValidateMountByAccessor(toAlias.MountAccessor)`, which executes a radix search under `Router.l.RLock()`.
- **Why it matters**:
  In enterprise directories (e.g., LDAP/OIDC sync) where entities accumulate dozens of aliases across plugins and auth backends, merging accounts with multiple from-entities incurs O(E * N * M) complexity with repeated router lock acquisitions.
- **Potential improvement**:
  Index `toEntity.Aliases` by `MountAccessor` into a hash map `toAccessors map[string]*identity.Alias` before entering the loop. This converts the inner comparison loop into an O(1) map lookup per `fromAlias`, reducing overall complexity from O(N * M) to O(N + M).
- **Behavioural risks**: Low.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/identity/store_entities.go:920-975`).

---

### Finding ALG-003: Linear Traversal and Template Re-evaluation in Filtered List Responses
- **Finding ID**: ALG-003
- **Category**: Algorithmic Complexity / Linear Scans
- **File path**: `internal/vault/request_handling_list_filtering.go`
- **Function**: `Core.filterListResponse`
- **Line range**: Lines 80–125
- **Existing implementation**:
  When a list response is subject to policy filtering (`auth.ResponseKeysFilterPath`):
  ```go
  for _, key := range keys {
      checkPath, err := template.UseTemplateForFiltering(tmpl, req.Path, key)
      ...
      checkReq := &logical.Request{
          Operation: op,
          Path: checkPath,
          Headers: req.Headers,
          Data: map[string]any{},
      }
      rootPath := c.router.RootPath(ctx, checkPath)
      authResults := c.performPolicyChecks(ctx, acl, te, checkReq, entity, ...)
      if authResults.Allowed {
          filteredKeys = append(filteredKeys, key)
      }
  }
  ```
- **Technical inefficiency**:
  For every key in a list response (which can contain 10,000 to 100,000 keys in active vaults):
  1. `template.UseTemplateForFiltering` is executed.
  2. `&logical.Request{}` and `map[string]any{}` are allocated.
  3. `c.router.RootPath(ctx, checkPath)` performs a radix lookup under `Router.l.RLock()`.
  4. Full policy checking (`c.performPolicyChecks`) is executed.
- **Why it matters**:
  On a list response with 50,000 keys, this executes 50,000 radix lookups, 50,000 policy checks, and 100,000+ heap allocations, causing long latency spikes and potential client timeouts.
- **Potential improvement**:
  1. Pre-check if the wildcard or prefix rule grants broad read access to the entire namespace subtree; if so, bypass per-key policy checks.
  2. Reuse a single mutable `checkReq` struct across loop iterations.
- **Behavioural risks**: Medium; permission filtering semantics must remain strictly identical for edge-case denies.
- **Security implications**: Extreme care needed to ensure explicit deny rules at subpaths are strictly evaluated.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/request_handling_list_filtering.go:80-125`).

---

### Finding ALG-004: Inefficient Parameter Deep Copying and Merge in ACL Path Construction
- **Finding ID**: ALG-004
- **Category**: Algorithmic Complexity / Deep Copying
- **File path**: `internal/vault/policy/acl.go`
- **Function**: `NewACL`
- **Line range**: Lines 190–215
- **Existing implementation**:
  When merging multiple policies that specify `AllowedParameters`:
  ```go
  if len(pc.Permissions.AllowedParameters) > 0 {
      if existingPerms.AllowedParameters == nil {
          clonedAllowed, err := copystructure.Copy(pc.Permissions.AllowedParameters)
          if err != nil {
              return nil, err
          }
          existingPerms.AllowedParameters = clonedAllowed.(map[string][]any)
      } else {
          for key, value := range pc.Permissions.AllowedParameters {
              ...
              existingPerms.AllowedParameters[key] = append(value, existingPerms.AllowedParameters[key]...)
          }
      }
  }
  ```
- **Technical inefficiency**:
  `copystructure.Copy` uses full reflection walking to clone the `map[string][]any`. During subsequent merges, `append(value, existingPerms.AllowedParameters[key]...)` modifies and reallocates parameter slices.
- **Why it matters**:
  Constructing ACLs for policies with fine-grained parameter constraints incurs heavy reflection costs and O(P * K) allocation overhead during token authorization.
- **Potential improvement**:
  Implement a specialized typed cloner for `map[string][]any` instead of generic `copystructure.Copy`.
- **Behavioural risks**: None.
- **Security implications**: None.
- **Confidence level**: High
- **Validation methodology**: SOURCE-CONFIRMED (`internal/vault/policy/acl.go:190-215`).
