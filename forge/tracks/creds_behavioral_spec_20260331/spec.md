# Credential Management Behavioral Specification

**Track:** `creds_behavioral_spec_20260331`
**Type:** docs
**Status:** active

## Overview

This specification defines the observable behavioral contract for Concourse's credential management subsystem (`atc/creds/`). The creds system is responsible for resolving `((variables))` in pipeline configurations from external secret managers (Vault, AWS SSM, AWS Secrets Manager, CredHub, Kubernetes, Conjur) and internal generators (ID tokens). Correct behavior is security-critical — misresolution, caching errors, or path traversal bugs could expose or misroute secrets.

### Scope

- Secret caching (hit/miss, TTL, error non-caching, tracing)
- Secret retry logic (retryable errors, attempt exhaustion)
- Variable lookup and path resolution (templates, prefixes, team/pipeline scoping)
- Variable interpolation (Source, Params, List, String, LoadVarPlan, SetPipelinePlan)
- VarSourcePool lifecycle (creation, dedup, GC, close)
- Per-manager secret lookup (Vault KV1/KV2, SSM, Secrets Manager, CredHub, Kubernetes, Conjur, Noop)
- Per-manager configuration and validation
- ID Token generation and lifecycle (JWT claims, signing key rotation, algorithms)

### Out of Scope

- External secret manager server behavior
- Network transport / TLS configuration details
- Database storage of credentials
- Pipeline variable resolution in atc/exec/ — covered by exec step specs

---

## Section 1: Secret Caching (7 requirements)

### CH-01: Cache hit returns previously stored value

When a secret is fetched and found in the cache, the system MUST return the cached value without querying the underlying secret manager.

### CH-02: Cache miss queries underlying and stores result

When a secret is not in the cache, the system MUST query the underlying secret manager and cache the result for the configured duration.

### CH-03: Errors are never cached

When the underlying secret manager returns an error, the system MUST NOT cache the error. Subsequent lookups MUST retry the underlying manager.

### CH-04: Not-found responses cached with separate TTL

When a secret is not found (found=false, no error), the system MUST cache the negative response for `DurationNotFound` (default 10s), not the regular cache duration.

### CH-05: Expired entries re-fetched

When a cached entry's TTL has elapsed, the system MUST re-fetch from the underlying manager on the next lookup.

### CH-06: Lease-aware TTL capping

When the underlying manager returns an expiration time shorter than the configured cache duration, the system MUST use the shorter expiration as the cache TTL.

### CH-07: Tracing spans emitted on lookup

When tracing is enabled, the caching layer MUST emit a `creds.lookup` span with `cache.hit`, `secret.found`, and `secret.path` attributes.

---

## Section 2: Secret Retry (3 requirements)

### RT-01: Retry on retryable errors

When a secret lookup returns a retryable error, the system MUST retry up to the configured number of attempts with a delay between retries.

### RT-02: Exhaust retries returns final error

When all retry attempts are exhausted, the system MUST return an error including the retry count.

### RT-03: SecretsWithParams interface compliance

The retryable wrapper MUST implement both the `Secrets` and `SecretsWithParams` interfaces, wrapping both `Get` and `GetWithParams`.

---

## Section 3: Variable Lookup & Path Resolution (4 requirements)

### VL-01: Multi-path fallthrough resolution

When resolving a variable, the system MUST try each lookup path in order (pipeline scope, team scope, shared scope) and return the first match.

### VL-02: Field traversal for nested secrets

When a variable reference includes dot-notation fields (e.g., `((secret.username))`), the system MUST traverse the returned secret structure to extract the specific field.

### VL-03: Missing field returns error

When a variable reference includes fields that don't exist in the returned secret, the system MUST return an error.

### VL-04: Template-based path construction

When secret lookup templates are configured (e.g., `/concourse/{{.Team}}/{{.Pipeline}}/{{.Secret}}`), the system MUST construct paths by interpolating team, pipeline, and secret name.

---

## Section 4: Variable Interpolation (5 requirements)

### VI-01: Source interpolation

When evaluating an `atc.Source` map containing `((variables))`, the system MUST replace variables with resolved values and return the interpolated source.

### VI-02: List interpolation

When evaluating a list containing `((variables))`, the system MUST interpolate both full-list and element-level variables.

### VI-03: LoadVarPlan interpolation preserves name

When evaluating a LoadVarPlan, the system MUST interpolate File and Format fields but MUST NOT interpolate the Name field.

### VI-04: SetPipelinePlan interpolation preserves name

When evaluating a SetPipelinePlan, the system MUST interpolate File, VarFiles, and Vars fields but MUST NOT interpolate the Name field.

### VI-05: String interpolation

When evaluating a `creds.String` containing `((variables))`, the system MUST return the resolved string value.

---

## Section 5: VarSourcePool Lifecycle (4 requirements)

### VP-01: Pool deduplication by config

When `FindOrCreate` is called multiple times with the same manager config, the pool MUST return the same manager instance (deduplicate by JSON-serialized config key).

### VP-02: Pool tracks size correctly

The pool MUST accurately report its size — the number of distinct manager configs currently pooled.

### VP-03: Pool close cleans up all sources

When `Close()` is called, the pool MUST clean up all pooled manager instances.

### VP-04: TTL-based garbage collection

When a pooled manager has not been used within its TTL, the pool's garbage collection loop MUST evict and close it.

---

## Section 6: Vault Secret Lookup (8 requirements)

### VA-01: Pipeline-scoped secret lookup

When looking up a secret, the Vault manager MUST first try the pipeline-scoped path (e.g., `/concourse/TEAM/PIPELINE/SECRET`).

### VA-02: Team-scoped secret fallback

When a secret is not found at pipeline scope, the Vault manager MUST fall back to the team-scoped path (e.g., `/concourse/TEAM/SECRET`).

### VA-03: Shared secret fallback

When a secret is not found at team scope and shared path is configured, the Vault manager MUST fall back to the shared path.

### VA-04: Pipeline scope takes precedence

When a secret exists at both pipeline and shared scope, the Vault manager MUST return the pipeline-scoped value.

### VA-05: Custom lookup templates

When custom secret lookup templates are configured, the Vault manager MUST use them for path construction instead of defaults.

### VA-06: Root path access control

When `allowRootPath=false`, the Vault manager MUST NOT look up secrets at the root path. When `allowRootPath=true`, it MUST include the root path in lookups.

### VA-07: KV v2 support

When using Vault KV v2 engine, the manager MUST add `/data/` to paths and unwrap the nested `data` key from responses.

### VA-08: Login timeout error

When Vault login times out, the manager MUST return a timeout error.

---

## Section 7: AWS SSM Secret Lookup (4 requirements)

### SM-01: Parameter retrieval by name

When looking up a secret, the SSM manager MUST call GetParameter with the resolved path and return the decrypted value.

### SM-02: Complex parameter via path

When looking up a secret with sub-keys, the SSM manager MUST use GetParametersByPath to retrieve all parameters under the path and return them as a map.

### SM-03: Team and shared fallback

When a secret is not found at pipeline scope, the SSM manager MUST fall back to team scope, then shared scope.

### SM-04: Numbers returned as strings

When a parameter value is numeric, the SSM manager MUST return it as a string (not a number type).

---

## Section 8: AWS Secrets Manager Lookup (4 requirements)

### AS-01: Secret string retrieval

When looking up a secret that contains a SecretString, the Secrets Manager MUST parse it as JSON. If not valid JSON, return the raw string.

### AS-02: Binary secret retrieval

When looking up a secret with SecretBinary, the Secrets Manager MUST parse the binary data as JSON.

### AS-03: Deleted secrets treated as not found

When a secret is marked for deletion (InvalidRequestException), the Secrets Manager MUST treat it as not found.

### AS-04: Team and shared fallback

When a secret is not found at pipeline scope, the Secrets Manager MUST fall back to team scope, then shared scope.

---

## Section 9: Other Manager Lookups (5 requirements)

### OM-01: Kubernetes namespace/secret lookup

When looking up a secret, the Kubernetes manager MUST parse the path as `namespace/secret-name`, fetch the Secret, and return the `value` key if present, or all keys as a map.

### OM-02: Conjur secret retrieval with templates

When templates are configured, the Conjur manager MUST construct paths via templates. When no templates are configured, it MUST use the full variable path directly.

### OM-03: Noop always returns not found

The Noop manager MUST always return `found=false` for any secret lookup.

### OM-04: CredHub FindByPartialName + GetLatestVersion

When looking up a secret, the CredHub manager MUST first use FindByPartialName, then fetch the latest version.

### OM-05: ID Token generates JWT with correct claims

When the `token` field is requested, the ID Token manager MUST generate a JWT containing claims: issuer, subject (scoped per config), team, pipeline, job, instance_vars, expiry, and issued_at.

---

## Section 10: Manager Configuration & Validation (6 requirements)

### MV-01: IsConfigured detects missing config

Each manager's `IsConfigured()` MUST return false when no configuration is provided and true when the minimum required fields are set.

### MV-02: Validate rejects incomplete credentials

Each manager's `Validate()` MUST reject configurations with partial credentials (e.g., AWS access key without secret key).

### MV-03: Template validation

When secret lookup templates are configured, `Validate()` MUST reject empty templates and templates with invalid parameters.

### MV-04: ID Token config validation

The ID Token manager MUST reject: malformed audience, invalid subject_scope, invalid algorithm, unknown settings, and expires_in > 24h.

### MV-05: Vault TLS configuration

The Vault manager MUST configure TLS client certificates when provided and verify all config attributes are mapped correctly.

### MV-06: ID Token signing key rotation

The ID Token lifecycle MUST create signing keys when none exist, generate new keys when existing keys expire, and remove outdated keys after a grace period.

---

## Section 11: ID Token Generation (5 requirements)

### IT-01: Valid token generation

The token generator MUST produce a valid, parseable JWT signed with the configured algorithm.

### IT-02: Subject scope variations

The token generator MUST support subject scopes: `team` (team only), `instance` (team + pipeline + instance vars), `job` (team + pipeline + instance + job).

### IT-03: URL-safe subject encoding

The token generator MUST URL-encode special characters (slashes, colons) in the subject string.

### IT-04: Audience claim when configured

When an audience is specified, the token generator MUST include the `aud` claim. When not specified, the claim MUST be empty.

### IT-05: Algorithm selection

The token generator MUST support RS256 and ES256 signing algorithms and use the configured one.
