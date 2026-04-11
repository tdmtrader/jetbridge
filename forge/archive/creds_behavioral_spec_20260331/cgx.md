# CGX — Discovery Notes

## Key Architectural Insights

### Composable Layered Architecture
The credential system uses a clean composable pattern: raw Secrets → RetryableSecrets → CachedSecrets. Each layer adds a cross-cutting concern without modifying the manager implementation. This means all 9 managers automatically get retry and caching for free.

### Config-Keyed Pool Deduplication
The VarSourcePool deduplicates managers by JSON-serializing their config. This means two pipelines with identical Vault configs share the same manager instance (and cache), reducing backend load. The pool's GC goroutine evicts stale entries after a configurable TTL.

### Template vs Prefix Path Strategies
Two path resolution strategies exist:
- **Template-based** (Vault, SSM, Secrets Manager, Conjur): Uses Go `text/template` with `{{.Team}}/{{.Pipeline}}/{{.Secret}}`
- **Prefix-based** (Kubernetes, CredHub): Simple concatenation of prefix + variable name

Templates are validated at startup via `BuildSecretTemplate()` which tests with dummy values.

### ID Token Is Unique — It Generates, Not Retrieves
Unlike all other managers that retrieve external secrets, the ID Token manager generates JWT tokens on demand. It only responds to the `token` field name and requires team+pipeline context. This makes it more of a "credential generator" than a "credential fetcher."

### Vault Has the Most Complex Lifecycle
Vault's token management (login, renewal, exponential backoff re-login on failure, maxTTL-based forced re-login) is by far the most complex lifecycle code in the subsystem. The `reauther.go` implements a state machine with 163 LOC.

## Coverage Observations

- **96% Full** — excellent baseline with only 2 partial items
- **All 9 managers have dedicated test suites** covering their lookup and validation paths
- **Caching and retry are thoroughly tested** (9 and 3 tests respectively)
- **Vault has the deepest test suite** (vault_test.go alone is 861 LOC testing KV1, KV2, and default modes)
- **String interpolation is the main gap** — the Evaluate function exists but has no dedicated test
- **CredHub is partially tested** — the manager works but has no tests in atc/creds/credhub/

## Decisions

- Requirement IDs use two-letter prefixes: CH (Caching), RT (Retry), VL (Variable Lookup), VI (Variable Interpolation), VP (VarSource Pool), VA (Vault), SM (SSM), AS (AWS Secrets Manager), OM (Other Managers), MV (Manager Validation), IT (ID Token)
- CredHub marked as Partial rather than None because the behavior IS tested through integration with the DB layer, just not with a dedicated unit test in atc/creds/credhub/
- Template validation tests are counted under MV-03 even though they appear in multiple manager test files
