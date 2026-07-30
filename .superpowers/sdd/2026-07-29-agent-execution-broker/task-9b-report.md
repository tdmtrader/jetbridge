# Task 9b — managed broker companion

## Review-fix checkpoint

The round-one production-boundary findings are addressed:

- Native harnesses run through a separate `agent-broker sandbox-exec` process.
  The helper applies Landlock and then `exec`s the exact preflight-bound
  binary. Only the current run scratch is writable; selected immutable
  image-owned paths and required system runtime files are readable. The live
  `/workspace`, `/run/concourse/agent-broker`, `/proc`, the scratch parent,
  sibling runs, and credential files are not allowed. Landlock does not
  restrict provider networking.
- Startup performs the actual create/add/restrict Landlock sequence in a
  disposable child. Linux Landlock ABI 3 or newer is required (Linux 6.2+);
  `ENOSYS`, `EOPNOTSUPP`, `EPERM` (including RuntimeDefault seccomp denial),
  and older ABIs fail readiness with no fallback. Operators may declare
  additional immutable image-owned harness assets through
  `sandbox_read_paths`; workspace, scratch, broker authority, and `/proc`
  overlap is rejected.
- Each parent receives a random 256-bit MCP access capability. One copy is a
  main-container-only private file used to generate the parent's strict MCP
  configuration; a second copy is broker-only. The token is never a broker
  environment variable. `/mcp` requires its bearer token while `/healthz`
  remains unauthenticated. A Landlocked child can reach loopback but cannot
  read the capability or broker process state, so it cannot recursively call
  the broker.
- Workspace capture now creates its temporary index, object database, and
  verification checkout under configured broker scratch. The source Git object
  database is a read-only alternate. Regression coverage verifies capture
  without default temporary storage and without adding files to source
  `.git/objects`.
- The Kubernetes readiness probe is an exec probe invoking
  `/usr/local/bin/agent-broker healthcheck`, which tests the fixed loopback
  endpoint from inside the sidecar. No Pod-IP listener was added.

The companion remains numeric non-root, read-only-root, drop-ALL,
RuntimeDefault, bounded-scratch, and exact-secret only. This is medium
production hardening for a handful of managed clusters; fleet-wide kernel
discovery, dynamic policy administration, and compatibility fallback are
intentionally out of scope.

## Verification

Passed:

```text
go test ./agent/broker/workspace ./agent/broker/sandbox \
  ./agent/broker/adapter ./agent/broker/runtime ./cmd/agent-broker \
  ./atc/runtime ./atc/exec ./atc/atccmd -count=1

go test ./agent/runner \
  -run 'Test(AdmitBrokerMCP|WriteMCPConfig)' -count=1

go test ./atc/worker/jetbridge \
  -run TestManagedAgentBrokerGetsOnlyBrokerPrivateWorkspaceAndCredentialMounts \
  -count=1

GOOS=linux GOARCH=amd64 go test -c ./agent/broker/sandbox
GOOS=linux GOARCH=amd64 go test -c ./cmd/agent-broker
git diff --check
```

The broad runner and Jetbridge suites reached unrelated tests that create
`httptest` IPv6 listeners and were sandbox-blocked with
`listen tcp6 [::1]:0: bind: operation not permitted`. Their exact changed
tests pass.
