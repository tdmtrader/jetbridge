# Agent execution broker

The agent execution broker adds two synchronous MCP tools to eligible
schema-v3 agent nodes:

- `request_review` performs a static review of an exact captured Git
  workspace.
- `consult_agent` answers a bounded question from only the caller-supplied
  prompt and declared snapshot attachments.

The caller selects only a provider-neutral `economy`, `balanced`, or
`frontier` tier and `medium` or `high` effort. ATC resolves that selector
through the node's frozen catalog subset. Provider, model, native harness,
credential slot, image, limits, and instruction digest remain operator-only
and deterministic for the workflow or reusable-node revision.

Calls are synchronous and independently concurrent. There is no broker
parallelism limit beyond pod resources and provider limits. Each child starts
with a fresh harness context: it receives its image-owned instruction, the
caller prompt, and only explicitly named attachments. It does not inherit the
parent transcript, user configuration, MCP configuration, or live workspace.

## Review semantics

`request_review` does not require a commit. The broker captures the repository
base plus staged, unstaged, deleted, renamed, executable, symlink, binary, and
nonignored untracked state using a private Git index and object database. It
verifies the result tree before starting the reviewer. Ignored files are
excluded.

Review is deliberately static. Review children are instructed not to run
tests, builds, formatters, or linters, the fixed contract marks tests
unavailable, and the runtime image omits build toolchains. Native read-only
enforcement varies by harness; Codex and Cursor may still invoke basic image
utilities for inspection. Every inspection and sealed `review/v1` result
records `tests_run: false`, and any child-generated test claim is
non-authoritative. Supply an authoritative `validation/v1` attachment when
test evidence is relevant; the reviewer may cite it but cannot regenerate it.

The result is exactly one validated `review/v1` body. `consult_agent` similarly
returns exactly one validated `consultation/v1` body. Prose, duplicate JSON
keys, unknown fields, oversized output, invalid anchors, or a missing terminal
event fail the child rather than becoming a successful typed record.

## Deployment model

ATC owns catalog resolution, admission, short-lived capabilities, the durable
child ledger, workspace/result sealing, and inspection. Jetbridge injects one
managed companion only for an agent node whose frozen definition contains
broker profiles. There is no broker Deployment or Service.

The companion mounts the parent workspace read-only only long enough to
capture it. A native harness runs over a materialized capture behind an
unprivileged Landlock ABI 3+ filesystem boundary. It can write only its
per-call scratch and read only the configured immutable runtime paths. The
live workspace, broker authority, provider Secret files, `/proc`, the scratch
parent, and sibling calls are not granted.

The parent reaches `/mcp` over loopback with a random per-parent bearer file.
The child harness receives neither bearer copy. Broker readiness is an
in-container exec probe against the loopback-only listener.

Managed nodes require:

- Linux 6.2 or newer with Landlock enabled (ABI 3 or newer);
- a `RuntimeDefault` seccomp profile that admits Landlock syscalls;
- the chart's non-root, read-only-root, drop-all-capabilities security context;
- durable agent snapshots and authenticated artifact-daemon storage; and
- an immutable `linux/amd64` broker image digest.

Startup fails closed when the process sandbox, exact harness versions, image
assets, catalog, runtime file, or credentials are unavailable. There is no
unsandboxed compatibility fallback.

> **Promotion blocker:** the current child seccomp/process-group boundary is
> not yet accepted for deployment. A same-UID child may still be able to join
> the broker's process group and signal it through `kill(0)`, and async-I/O
> ownership can provide another signal path. The packaged image is therefore
> build/test material, not promotion-ready, until that boundary receives human
> review, a code correction, and a live Linux regression on the managed
> cluster kernel/runtime. Filesystem, credential, and network boundaries do
> not turn this availability/integrity gap into an acceptable fallback.

## Build and publish the image

Run:

```sh
make build-agent-broker-image \
  AGENT_BROKER_IMAGE=registry.example/concourse/agent-broker:reviewed-build
```

The manual `build-agent-broker-image` pipeline job builds and pushes a
commit-addressed image and prints the registry-reported digest. Put that exact
`repository@sha256:...` value in `agentBroker.image` and every profile's
`worker_image`.

The initial `linux/amd64` image contains:

| Harness | Version | Delivery |
| --- | --- | --- |
| Codex CLI | `0.146.0` | Official x86_64 musl GitHub release archive |
| Claude Code | `2.1.212` | Official Anthropic Linux x64 binary |
| Cursor CLI | `2026.07.23-e383d2b` | Complete x64 agent package |

All downloads use versioned URLs and literal SHA-256 checks. The Cursor
checksum is project-computed because the vendor does not publish one for this
archive; mirror the verified archive into an operator-controlled immutable
registry before relying on it for long-lived or disconnected clusters. The
image has no update channel, package installer, or writable harness location.

Changing a harness version requires updating its image checksum, the explicit
adapter compatibility table, CLI fixture/preflight tests, and the affected
operator profiles as one reviewed change.

The pinned Codex `rust-v0.146.0` command definition was checked directly: its
`exec` parser declares `--strict-config`, `--ignore-user-config`, and
`--ignore-rules` as global flags. The adapter's exact-argv test pins all three,
so an upgrade that removes or renames one fails review before image
publication.

## Helm configuration

The feature is disabled by default. Create a random capability key separately
from provider credentials:

```sh
openssl rand 32 | kubectl -n concourse create secret generic agent-child-capability \
  --from-file=capability.key=/dev/stdin

kubectl -n concourse create secret generic agent-provider-credentials \
  --from-literal=openai-api-key='...' \
  --from-literal=anthropic-api-key='...' \
  --from-literal=cursor-api-key='...'
```

Then configure static profiles. This abbreviated example shows three
provider-neutral choices mapped to three different harnesses:

```yaml
agentBroker:
  enabled: true
  image: registry.example/concourse/agent-broker@sha256:<reviewed-digest>
  authorityEndpoint: https://concourse.example.com
  capabilitySecret:
    name: agent-child-capability
    key: capability.key
  credentials:
    - slot: openai-shared
      secretName: agent-provider-credentials
      key: openai-api-key
    - slot: anthropic-shared
      secretName: agent-provider-credentials
      key: anthropic-api-key
    - slot: cursor-shared
      secretName: agent-provider-credentials
      key: cursor-api-key
  profiles:
    - id: balanced-review-high
      revision: 1
      selector: {tier: balanced, effort: high}
      tools: [request_review]
      purpose: Static code review
      worker_image: registry.example/concourse/agent-broker@sha256:<reviewed-digest>
      adapter: {name: codex, version: 0.146.0}
      provider: {name: openai, model: <exact-model>}
      native_effort: high
      instructions_digest: sha256:9982a935820d5177131cf16e285ab137b0774a0d4701181aaf180358a3a6f669
      credential_slot: openai-shared
      limits: {timeout: 60000000000, max_input_bytes: 1048576, max_output_bytes: 1048576}
      controls:
        read_only_workspace: true
        no_broker_recursion: true
        tests_unavailable: true
        native_output_schema: true
        ignores_user_config: true
    - id: frontier-consult-high
      revision: 1
      selector: {tier: frontier, effort: high}
      tools: [consult_agent]
      purpose: Difficult architecture consultation
      worker_image: registry.example/concourse/agent-broker@sha256:<reviewed-digest>
      adapter: {name: claude-code, version: 2.1.212}
      provider: {name: anthropic, model: <exact-model>}
      native_effort: high
      instructions_digest: sha256:e79f4aa92d601d27222d53fb07fbba5e306856006c260c0a8019220153e23dbe
      credential_slot: anthropic-shared
      limits: {timeout: 60000000000, max_input_bytes: 1048576, max_output_bytes: 1048576}
      controls:
        read_only_workspace: true
        no_broker_recursion: true
        tests_unavailable: true
        native_output_schema: true
        ignores_user_config: false
    - id: economy-consult-medium
      revision: 1
      selector: {tier: economy, effort: medium}
      tools: [consult_agent]
      purpose: Fast second opinion
      worker_image: registry.example/concourse/agent-broker@sha256:<reviewed-digest>
      adapter: {name: cursor-cli, version: 2026.07.23-e383d2b}
      provider: {name: cursor, model: <exact-model>}
      native_effort: medium
      instructions_digest: sha256:e79f4aa92d601d27222d53fb07fbba5e306856006c260c0a8019220153e23dbe
      credential_slot: cursor-shared
      limits: {timeout: 60000000000, max_input_bytes: 1048576, max_output_bytes: 1048576}
      controls:
        read_only_workspace: true
        no_broker_recursion: true
        tests_unavailable: true
        native_output_schema: false
        ignores_user_config: false
  networkPolicy:
    # Supply complete CNI-specific rules for ATC, DNS when required,
    # provider endpoints, and destinations needed by the parent agent.
    egress: []
```

Profile `limits.timeout` is a Go `time.Duration` encoded in nanoseconds in the
strict JSON catalog. Keep profile selectors unique per tool. The catalogue is
cluster/node guidance, not a fleet scheduler: existing compiled workflow and
node revisions retain their exact imported profiles when the deployment
catalog changes.

Kubernetes NetworkPolicy selects pods, not containers. The broker policy
therefore applies to the whole managed agent pod, including the parent agent,
and loopback is unaffected. NetworkPolicy allow rules are additive across
every policy selecting a pod; `agentBroker.networkPolicy.egress: []` does not
revoke traffic allowed elsewhere. Audit all matching policies and configure
the union deliberately.

## Inspection and troubleshooting

Authenticated team users can inspect one execution at:

```text
GET /api/v1/teams/<team>/agent-child-executions/<execution-id>
```

The response exposes neutral selector, exact profile identity/digest,
lifecycle state and sequence, sealed workspace/result references, duration,
safe error summary, and the static-review/tests-not-run markers. It does not
expose prompt text, input digests, provider error bodies, credentials, or
private capabilities.

Common failures:

- startup sandbox errors: verify Linux/Landlock and the node seccomp policy;
- harness incompatibility: image binary and frozen profile versions differ;
- catalog required: Fly validated the neutral selector, but ATC has no enabled
  static catalog;
- capture rejected: repository base changed, the Git tree mutated during
  capture, or patch/entry bounds were exceeded;
- authority rejected: the capability expired or an old capture/lifecycle
  token was replayed after the execution advanced;
- provider unavailable: check only the selected Secret key, provider egress,
  and profile timeout; provider bodies are intentionally redacted;
- broker lost: inspect pod events and the durable terminal ledger before
  retrying with the same idempotency key.

For this initial medium-hardening deployment, validate a broker image and
chart render in CI. The credential-free adapter/engine/MCP smoke can be run
explicitly with:

```sh
CONCOURSE_AGENT_BROKER_SMOKE=1 make test-agent-broker-smoke
```

Then run the real PostgreSQL ledger smoke and a Kind/K3s pod smoke on the
actual managed kernel/CNI. Live-provider smoke calls remain an
operator-controlled gate. None of those gates supersedes the process-boundary
promotion blocker above.
