# Credential Management Behavioral Spec — Coverage Matrix & Implementation Plan

## Coverage Matrix

### Section 1: Secret Caching (7 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| CH-01 | Cache hit returns stored value | ✅ Full | cached_secrets_test.go:74-95 | Cached value persists even when source changes |
| CH-02 | Cache miss queries and stores | ✅ Full | cached_secrets_test.go:52-72 | Miss triggers underlying, result cached |
| CH-03 | Errors never cached | ✅ Full | cached_secrets_test.go:97-117 | Error not cached, subsequent call re-queries |
| CH-04 | Not-found separate TTL | ✅ Full | cached_secrets_test.go:147-170 | DurationNotFound applies to negative results |
| CH-05 | Expired entries re-fetched | ✅ Full | cached_secrets_test.go:119-145 | Post-TTL lookup re-queries |
| CH-06 | Lease-aware TTL capping | ✅ Full | cached_secrets_test.go:172-195 | Zero/negative duration handled |
| CH-07 | Tracing spans on lookup | ✅ Full | cached_secrets_test.go:213-268 | cache.hit + secret.found spans |

**Summary:** 7/7 Full (100%)

---

### Section 2: Secret Retry (3 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| RT-01 | Retry on retryable errors | ✅ Full | retryable_secrets_test.go:33-41 | Retries within attempt limit |
| RT-02 | Exhaust retries returns error | ✅ Full | retryable_secrets_test.go:43-51 | Fails after max attempts |
| RT-03 | SecretsWithParams interface | ✅ Full | retryable_secrets_test.go:29-31 | Type assertion verified |

**Summary:** 3/3 Full (100%)

---

### Section 3: Variable Lookup & Path Resolution (4 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| VL-01 | Multi-path fallthrough | ✅ Full | vault/vault_test.go:128-192 | Pipeline → team → shared precedence |
| VL-02 | Field traversal | ✅ Full | secret_var_lookup_test.go:32-38 | Dot-notation field extraction |
| VL-03 | Missing field error | ✅ Full | secret_var_lookup_test.go:41-44 | Error on missing field |
| VL-04 | Template-based paths | ✅ Full | vault/vault_test.go:194-249 | Custom templates used for path construction |

**Summary:** 4/4 Full (100%)

---

### Section 4: Variable Interpolation (5 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| VI-01 | Source interpolation | ✅ Full | source_test.go:27-36 | Variables replaced in Source map |
| VI-02 | List interpolation | ✅ Full | list_test.go:12-28 | Full-list and element-level |
| VI-03 | LoadVarPlan preserves name | ✅ Full | load_var_plan_test.go:29-38 | Name NOT interpolated, File/Format ARE |
| VI-04 | SetPipelinePlan preserves name | ✅ Full | set_pipeline_plan_test.go:31-40 | Name NOT interpolated, File/Vars ARE |
| VI-05 | String interpolation | ✅ Full | string_test.go:13-40 | Variable replacement, error returns raw, passthrough |

**Summary:** 5/5 Full (100%)

---

### Section 5: VarSourcePool Lifecycle (4 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| VP-01 | Pool dedup by config | ✅ Full | pool_test.go:135-181 | Same config = same instance, pool size unchanged |
| VP-02 | Pool tracks size | ✅ Full | pool_test.go:89-91, 130-132 | Size 1 and 2 verified |
| VP-03 | Pool close cleans up | ✅ Full | pool_test.go:192-204 | All sources cleaned on Close |
| VP-04 | TTL-based GC | ✅ Full | pool_test.go:218-233 | Expired managers evicted |

**Summary:** 4/4 Full (100%)

---

### Section 6: Vault Secret Lookup (8 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| VA-01 | Pipeline-scoped lookup | ✅ Full | vault/vault_test.go:128-141 | Pipeline path tried first |
| VA-02 | Team-scoped fallback | ✅ Full | vault/vault_test.go:143-156 | Falls back to team path |
| VA-03 | Shared fallback | ✅ Full | vault/vault_test.go:158-171 | Falls back to shared path |
| VA-04 | Pipeline precedence | ✅ Full | vault/vault_test.go:173-192 | Pipeline returned even when shared exists |
| VA-05 | Custom templates | ✅ Full | vault/vault_test.go:194-249 | Custom pipeline/team/shared templates |
| VA-06 | Root path access control | ✅ Full | vault/vault_test.go:281-316 | true allows root, false blocks |
| VA-07 | KV v2 support | ✅ Full | vault/vault_test.go:331-601 | /data/ prefix, response unwrapping |
| VA-08 | Login timeout error | ✅ Full | vault/vault_test.go:100-104 | Timeout error returned |

**Summary:** 8/8 Full (100%)

---

### Section 7: AWS SSM Secret Lookup (4 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| SM-01 | Parameter by name | ✅ Full | ssm/ssm_test.go:58-63 | GetParameter with path |
| SM-02 | Complex via path | ✅ Full | ssm/ssm_test.go:65-80 | GetParametersByPath returns map |
| SM-03 | Team and shared fallback | ✅ Full | ssm/ssm_test.go:93-119 | Team then shared |
| SM-04 | Numbers as strings | ✅ Full | ssm/ssm_test.go:82-91 | String type returned |

**Summary:** 4/4 Full (100%)

---

### Section 8: AWS Secrets Manager Lookup (4 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| AS-01 | Secret string retrieval | ✅ Full | secretsmanager/secretsmanager_test.go:52-83 | JSON parsed, raw fallback |
| AS-02 | Binary secret retrieval | ✅ Full | secretsmanager/secretsmanager_test.go:59-70 | Binary JSON parsed |
| AS-03 | Deleted = not found | ✅ Full | secretsmanager/secretsmanager_test.go:131-139 | InvalidRequestException handled |
| AS-04 | Team and shared fallback | ✅ Full | secretsmanager/secretsmanager_test.go:85-109 | Team then shared |

**Summary:** 4/4 Full (100%)

---

### Section 9: Other Manager Lookups (5 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| OM-01 | Kubernetes namespace/secret | ✅ Full | kubernetes/kubernetes_test.go:78-146 | value key + all keys as map |
| OM-02 | Conjur with/without templates | ✅ Full | conjur/conjur_test.go:60-111 | Template and direct path |
| OM-03 | Noop always not found | ✅ Full | noop/noop_test.go:25-30 | Never locates variable |
| OM-04 | CredHub FindByPartialName | ⚠️ Partial | — | No test file in atc/creds/credhub/ (CredHub tests in atc/db/) |
| OM-05 | ID Token generates JWT | ✅ Full | idtoken/idtoken_test.go:63-87 | Correct claims in generated token |

**Summary:** 4/5 Full, 1 Partial (80%)

---

### Section 10: Manager Configuration & Validation (6 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| MV-01 | IsConfigured detection | ✅ Full | vault/manager_test.go:30-37, ssm/manager_test.go:20-27, etc. | All managers tested |
| MV-02 | Incomplete credentials rejected | ✅ Full | ssm/manager_test.go:57-69, secretsmanager/manager_test.go:55-67, conjur/manager_test.go:61-80 | Partial creds rejected |
| MV-03 | Template validation | ✅ Full | ssm/manager_test.go:71-109, conjur/manager_test.go:82-120 | Empty/invalid templates rejected |
| MV-04 | ID Token config validation | ✅ Full | idtoken/manager_test.go:30-101 | Malformed audience, scope, algorithm, expires_in |
| MV-05 | Vault TLS configuration | ✅ Full | vault/manager_test.go:174-200 | TLS certs + all config attributes |
| MV-06 | ID Token key rotation | ✅ Full | idtoken/lifecycle_test.go:70-148 | Create, rotate, remove outdated |

**Summary:** 6/6 Full (100%)

---

### Section 11: ID Token Generation (5 requirements)

| ID | Requirement | Coverage | Test Location | Notes |
|----|------------|----------|---------------|-------|
| IT-01 | Valid token generation | ✅ Full | idtoken/token_generator_test.go:73-84 | Valid JWT produced |
| IT-02 | Subject scope variations | ✅ Full | idtoken/token_generator_test.go:86-129 | team, instance, job scopes |
| IT-03 | URL-safe encoding | ✅ Full | idtoken/token_generator_test.go:131-152 | Slashes and special chars encoded |
| IT-04 | Audience claim | ✅ Full | idtoken/token_generator_test.go:154-167, 243-245 | Present when configured, absent by default |
| IT-05 | Algorithm selection | ✅ Full | idtoken/token_generator_test.go:169-182 | RS256 and ES256 |

**Summary:** 5/5 Full (100%)

---

## Overall Summary

| Section | Requirements | Full | Partial | None | Coverage |
|---------|-------------|------|---------|------|----------|
| 1. Secret Caching | 7 | 7 | 0 | 0 | 100% |
| 2. Secret Retry | 3 | 3 | 0 | 0 | 100% |
| 3. Variable Lookup | 4 | 4 | 0 | 0 | 100% |
| 4. Variable Interpolation | 5 | 5 | 0 | 0 | 100% |
| 5. VarSourcePool | 4 | 4 | 0 | 0 | 100% |
| 6. Vault Lookup | 8 | 8 | 0 | 0 | 100% |
| 7. AWS SSM Lookup | 4 | 4 | 0 | 0 | 100% |
| 8. AWS Secrets Manager | 4 | 4 | 0 | 0 | 100% |
| 9. Other Managers | 5 | 4 | 1 | 0 | 80% |
| 10. Config & Validation | 6 | 6 | 0 | 0 | 100% |
| 11. ID Token Generation | 5 | 5 | 0 | 0 | 100% |
| **TOTAL** | **55** | **54** | **1** | **0** | **98%** |

## Gap-Filling Summary

### P1 Gaps — Fixed

- [x] **VI-05**: Added `atc/creds/string_test.go` — variable replacement, error passthrough, plain string passthrough

### P2 Gaps — Accepted

- **OM-04**: CredHub lookup logic tested via integration in atc/db layer; no dedicated unit test needed

### New Tests Added (3 specs in 1 file)

| File | New Specs | What They Test |
|------|-----------|---------------|
| `atc/creds/string_test.go` | 3 | String.Evaluate with variable, missing variable error, plain passthrough |

### Verification

- `ginkgo --focus=String ./atc/creds/` — 3 specs PASSED
