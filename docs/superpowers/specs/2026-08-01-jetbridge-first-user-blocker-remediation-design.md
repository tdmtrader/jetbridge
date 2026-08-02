# Jetbridge First-User Blocker Remediation Design

**Status:** Ready for implementation on 2026-08-01.

## Objective

Make the reusable-node lifecycle runnable by a first user from typed input
creation through a sealed typed output. The work repairs the blockers found by
the first-user trial without expanding into workflow composition or weakening
any snapshot, budget, or output-sealing boundary.

The acceptance path is deliberately node-level:

1. create or capture exact `repository/v1` and `log-bundle/v1` inputs;
2. re-import the exact node packages and record the immutable version returned
   by content deduplication;
3. start fresh runs of those exact versions with a positive USD slice;
4. use the managed output builder through a conformant MCP session;
5. inspect a terminal run and its underlying build without hidden knowledge;
6. release only a version that produced an inspected, valid typed output.

## Evidence and Diagnosed Blockers

| ID | First-user observation | Repository diagnosis | Confidence |
| --- | --- | --- | --- |
| `JBUSER-001` | Exact Git resource capture returned only `500 internal_error`. | Both template constructors in `agent/resourcecapture/capture.go`—the public `Capture` path and composition-only `CapturePersistedSelection` path—name the template `agent-resource-capture-<operation-prefix>`, while `agent/workflowrun/TemplateSaver.SaveOrReuse` rejects every immutable template whose name does not end in `-<target-config-hash[:12]>`. The failure occurs after exact version resolution and before a build starts. Two DB authorization queries also reconstruct the old name and must move with both constructors. | Proven from the live request and current call chain. |
| `JBUSER-002` | A locally valid clean repository reached the live server but returned a generic `422 validation_failed`. | Current local `repository/v1` validation accepts the source tree and the canonical Fly archive. The deployed-validator difference is not yet identified. `HandlerFactory.writeSnapshotError` intentionally discards every validator cause, so the live response cannot identify even a safe failure category. | Opaque live failure proven; deployment skew is a hypothesis until the repaired path is rerun. |
| `JBUSER-003` | The managed `output-builder` showed `status: failed` in every model session. | `agent/outputbuilder/adapters.go` implements `tools/list`, `tools/call`, and `ping`, but not MCP `initialize` or `notifications/initialized`; its tool declarations also omit descriptions and input schemas. `/healthz` therefore passes while a real MCP client cannot establish a usable session. | Proven from the server implementation and Claude behavior. |
| `JBUSER-004` | A node with `budget_slice_usd: 5` failed before its first model turn with unknown option `--max-budget-usd`. | `agent/runner/runner.go` correctly passes the positive cap, but `deploy/agent-runner/Dockerfile` pins Claude Code `2.0.1`, which does not implement the runner's required flag. Existing tests verify argument construction, not the packaged CLI. | Proven by the live log and image pin. |
| `JBUSER-005` | `nodes show-run` gave a terminal status but no diagnostic path. | `RunSummary` already contains `planned_build_id`; `printAgentWorkflowRunDetail` drops it in plain output. Raw stored error text is intentionally redacted, so the safe fix is to expose the build correlation and an exact `fly watch -b` command, not dependency errors. | Proven from API and Fly rendering code. |
| `JBUSER-006` | `fly targets` said `n/a: invalid token` immediately after successful authentication. | The command locally parses token expiry and calls any non-JWT/bearer format invalid; actual authenticated status can still succeed. | Proven from `fly/commands/targets.go`; usability issue, not an authorization failure. |
| `JBUSER-007` | The live `k8s-live-tests` build log exposed the projected Kubernetes service-account bearer token. | `deploy/concourse-pipeline.yml` runs the task with `sh -x`, assigns the contents of the standard projected token file to a shell variable, and expands that variable into `kubectl config set-credentials`; shell tracing prints both the assignment and expanded argument. `deploy/borg-pipeline.yml` repeats the same pattern in two tasks. Ordinary non-hermetic Jetbridge pods preserve Kubernetes automount defaults, but repository configuration does not establish this token's lifetime, audience, or bound object. | The disclosure path is proven and blocks the next rollout until its repository tests pass. Token claim details remain explicitly unknown and are not acceptance prerequisites. |
| `JBUSER-008` | The final `v0.2.220` image continued to report `0.2.220-rc`. | `build-image` stamps both server version globals with the RC value. The release task declares final server linker flags but never uses them to build a server; it rebuilds only Fly, then derives the final image from the RC image and copies only Fly assets. A safe correction must build the final server from the same frontend-rebuilt source tree as the RC server, not from the release task's raw checkout. | Proven from the pipeline and running final image; final-stamped release activation is an acceptance blocker. |
| `JBUSER-009` | Chart documentation said an empty task ServiceAccount setting uses the web ServiceAccount. | `deploy/chart/templates/web-deployment.yaml` emits `--kubernetes-service-account` only for a nonempty value, `atc/atccmd` documents an empty value as the namespace default, and Jetbridge passes that value directly into the task Pod spec. The values comment and chart README therefore contradict the runtime. | Proven nonblocking documentation/config mismatch. The live selected ServiceAccount and its effective privileges still require read-only disposition. |

The nested-directory archive mismatch found during the trial is already fixed
by commit `a24e0771c2`: Fly now emits directory headers without a trailing
separator and a Fly-to-server canonicalization regression covers it. That
commit also preserves sealed-record prompt authority when the builder is
enabled. Both changes are prerequisites and are not reimplemented by this
track.

## Scope

### In scope

- immutable resource-capture template identity and every consumer of that
  identity;
- bounded, allow-listed validation diagnostics for `repository/v1`;
- an end-to-end Fly archive to real repository-validator compatibility test;
- the managed output-builder's MCP initialization, tool discovery, and runner
  preflight;
- a checksum-pinned Claude runtime whose CLI accepts every load-bearing runner
  flag;
- immutable runner-image digest publication and same-commit deployment
  evidence;
- one bounded automatic GitOps writeback of the verified runner digest to the
  normal home-infra manifest, followed by Argo activation evidence;
- safe Fly build-log hints and truthful target-expiry wording;
- node lifecycle and first-user documentation;
- a live, unreleased node-level dogfood gate using fresh post-rollout runs;
- no-xtrace deployment pipeline contracts and final-stamped server release
  activation; and
- explicit runtime Kubernetes ServiceAccount guidance and read-only live
  disposition.

### Out of scope

- workflow composition or workflow-level product testing;
- new snapshot types or contract revisions;
- automatic enforcement that a model reads every bundled skill;
- changing code-review or diagnosis reasoning rubrics beyond what is necessary
  to obtain a valid typed output;
- omitting a positive `--max-budget-usd` cap when the packaged CLI is stale;
- returning arbitrary validator, Git, database, filesystem, registry, or
  Kubernetes errors to an API client;
- releasing any version without a fresh post-rollout run, downloaded valid
  output, and explicit release disposition;
- changing external home-infra outside the narrowly authorized automatic
  runner-digest writeback described below, or bypassing ArgoCD with a direct
  Kubernetes mutation.

## Architecture

The repair is a sequence of independently reviewable vertical slices. Ingress
first makes both documented repository paths testable. Output authoring then
makes the managed MCP service protocol-ready and refuses to spend model budget
when its server-owned sidecar is only superficially healthy. Runtime packaging
makes a positive budget cap part of the image contract. Finally, operator
diagnostics, documentation, and a live dogfood pass prove the assembled
node-level experience.

```text
Fly archive ─┐
             ├─> canonical archive ─> typed validator ─> snapshot ID
Git resource ┘          │                    │
       │                │                    └─> allow-listed public reason
       └─> immutable capture template ─> build ─> captured snapshot ID

snapshot IDs + exact node version
             │
             ├─> runner image capability gate (`claude` flags)
             ├─> output-builder MCP protocol preflight
             └─> model ─> candidate record ─> independent final sealer
                                      │
                                      └─> run summary + exact build-log hint

commit-tagged runner image
             │
             ├─> image smoke → registry digest → immutable amd64 pull
             └─> one-file GitOps writeback → Argo exact-digest activation
                                                │
                                                └─> matching web rollout → dogfood
```

## Design Decisions

### 1. Resource-capture identity keeps both operation and config identity

The capture template name becomes:

```text
agent-resource-capture-<operation-key[:24]>-<target-config-hash[:12]>
```

The existing prefix remains because `atc/db/pipeline_run_factory.go` admits
server-owned capture templates by that prefix. The 24-hex operation fragment
preserves the current idempotency namespace; the 12-hex target-config suffix
satisfies the generic immutable-template registry. The full operation key
continues to live in server-authored production metadata and remains the
authorization identity.

Both `Capturer.Capture` and `Capturer.CapturePersistedSelection` compute
`workflow.TargetConfigHash(config)` after canonicalization and before template
construction. They use one shared template-spec constructor so the public and
persisted-selection paths cannot drift. `TemplateSpec.FullHash` continues to
mean the raw canonical-config SHA-256 expected by `ImmutableTemplateStore`;
that adapter independently computes the domain-separated target-config hash
passed to `TemplateSaver`. Conflating these two hashes would silently change a
current boundary and is forbidden. A persisted-selection success/idempotency
regression is required in addition to the existing drift-rejection test.

`FindResourceCaptureOutput` and `ListPendingResourceCaptureOutputs` match the
new exact grammar: the fixed prefix, the operation fragment, one hyphen, and a
12-character lowercase hexadecimal suffix. They still re-establish template
ownership, instance identity, succeeded build, output port, exact full
operation metadata, type, and content state. A suffix wildcard alone is not an
authorization decision; all existing server-derived predicates remain.

Known immutable-template collisions are mapped to a stable capture conflict,
platform/store failures are mapped to capture unavailability, and every
unexpected resource-capture cause is logged before its bounded response is
written. No raw cause is placed on the wire.

### 2. Validation diagnostics are closed and allow-listed

The snapshot domain gains a typed `PublicValidationFailure`, constructed only
from a `ValidationFailureReason` enum whose public message is fixed in the
snapshot package. The type wraps the original error for structured logs while
exposing only `Reason()` and `PublicMessage()` to the handler. It is impossible
for a validator to supply arbitrary public prose.

The initial allow-list is intentionally repository-specific:

| Reason | Stable public message |
| --- | --- |
| `repository_metadata_missing` | repository metadata is incomplete |
| `repository_metadata_unsafe` | repository metadata contains an unsupported or unsafe setting |
| `repository_history_incomplete` | repository history is shallow or incomplete |
| `repository_object_format_unsupported` | repository object format is unsupported |
| `repository_gitlinks_unsupported` | repositories containing submodule gitlinks are unsupported |
| `repository_dirty` | repository work tree and index must be clean |
| `repository_invalid` | repository object graph is invalid or incomplete |

The API retains `error: validation_failed` and adds optional `reason` to the
existing bounded error envelope. Unknown validator failures keep the present
generic message and omit `reason`. Tests must prove that Git stderr, config
values, filenames, temp paths, and dependency errors never appear in either
case.

A vertical regression starts with the real Fly directory archiver, passes its
tar stream through the real snapshot canonicalizer, opens the resulting tree,
and invokes the real registry's `repository/v1` validator. This is the missing
compatibility boundary between existing unit suites.

### 3. Output-builder readiness means a usable MCP session

The managed server implements the repository's existing MCP baseline from
`atc/api/mcpserver`: protocol version `2024-11-05`, `initialize`, exact
`notifications/initialized` returning HTTP 204, `ping`, `tools/list`, and
`tools/call`. It advertises only `describe_output`, `validate_output`, and
`write_output`, with stable descriptions and JSON Schemas matching
`mcpOutput` and `WriteRequest`. Unknown notifications and methods do not gain a
blanket success path.

After ordinary `/healthz` polling, the runner performs a protocol preflight
only for the server-owned `output-builder` admitted by
`CONCOURSE_OUTPUT_BUILDER_MCP`. It initializes, sends the initialized
notification, lists tools, and requires the exact three-name set with object
input schemas before launching Claude. A protocol-incompatible builder is a
platform error and consumes no model turn. User-authored MCP sidecars are not
subject to this platform-specific contract.

Successful managed preflight emits one bounded, non-secret `mcp.ready` event
to `flight/events.ndjson` with the server name, negotiated protocol version,
and exact tool names. Ingestion makes `event_counts["mcp.ready"] == 1` a durable
run-metrics proof available through the existing build metrics endpoint. The
live gate uses that artifact; it does not infer protocol success from
`/healthz`, model prose, or the absence of an error log.

The exact sealed-record authority preamble added in `a24e0771c2` remains in the
prompt regardless of builder health. The builder remains an authoring aid; the
ordinary post-step sealer is still the only snapshot authority.

### 4. A positive budget slice is an immutable image capability

The runner keeps its current behavior: every `BudgetSliceUSD > 0` produces
`--max-budget-usd`, and zero remains the explicit uncapped runner value. The
image moves from npm-installed Claude Code `2.0.1` to the same native,
checksum-pinned Claude release already packaged by the broker image:

```text
version: 2.1.212
sha256: 044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e
```

An image smoke gate checks the packaged binary's version and its support for
the runner's load-bearing flags, including `--max-budget-usd`, `--mcp-config`,
`--strict-mcp-config`, `--max-turns`, `--append-system-prompt`,
`--output-format`, `--verbose`, and `--dangerously-skip-permissions`. The
Dockerfile contract test also prevents the runner and broker Claude pins from
drifting independently.

The manual pipeline builds a commit-tagged image with an explicit `docker
build --platform linux/amd64`, runs the smoke gate before push, parses the
registry-reported digest, and validates the exact lowercase `sha256:<64 hex>`
grammar. It then pulls that immutable digest from the registry with the amd64
platform selected and requires Docker's inspected OS/architecture to be
exactly `linux/amd64` before printing
`CONCOURSE_AGENT_STEP_IMAGE=<repository>@<digest>`. Mutable tags may remain for
human convenience but are never deployment evidence.

The same pipeline task also closes two release-safety defects before another
rollout. Any task that reads a projected Kubernetes service-account token runs
without shell xtrace; a static contract covers both the Concourse pipeline and
the duplicate Borg scripts. The exposed token's lifetime, audience, and bound
object cannot be established from repository configuration and remain
explicitly unknown. Fixing the proven trace path is required before rollout;
discovering those token claims is not an acceptance prerequisite and no test
may print or decode a real credential.

The web release builds both RC and final server binaries from the same source
tree only after the separately rebuilt frontend has replaced `web/public`.
The RC artifact carries its RC server as the entrypoint and the final-stamped
server at a release-only staging path. `Dockerfile.release` activates that
staged server, retains the final Fly assets, and checks the exact final image's
`concourse --version` before push. The release task never recompiles the server
from its raw checkout, which would bypass the frontend rebuild and reopen the
stale embedded-UI failure. Static coverage requires final linker values for
both `concourse.Version` and `concourse.JetBridgeVersion`; live acceptance
confirms both fields through `/api/v1/info`.

### 5. Diagnostics expose correlations, not secrets

Plain Fly run detail prints `planned build: <id>` whenever the API supplies
one, followed by an exact `fly -t <selected-target> watch -b <id>` hint. JSON
output remains unchanged. The renderer receives the target alias explicitly;
it does not guess it from a URL or print a fake placeholder.

`fly targets` changes only its local status wording. A token whose expiry
cannot be decoded is reported as `expiry unavailable (run fly -t <target>
status)`, because the command has not established that the credential is
invalid. `fly status` remains the authenticated truth check.

The platform and reusable-node guides link the complete lifecycle, explain
typed input creation and exact resource capture, make the placeholder sample
image explicit, describe model/budget portability, show the build-log path,
and state that bundled skills are frozen and discoverable but not guaranteed
to be invoked. Contract-critical output mechanics therefore remain in the
initial prompt and managed builder.

### 6. Live acceptance uses fresh runs, not artificial version bumps

The immutable node content hash does not include the operator-selected common
runner image. Re-importing a byte-identical package after rollout therefore
correctly returns its existing version, likely `@9`; changing an irrelevant
asset merely to allocate a new integer would corrupt the meaning of node
versioning. At run admission, `WorkflowTargetRenderer` injects the currently
configured immutable runtime image and that digest changes the rendered target
hash/template identity. The dogfood proof is consequently a fresh run whose
rendered target names the new runner digest. The exact returned node version
stays unreleased until a valid output from that post-rollout run is downloaded
and inspected.

The live pass uses the smallest cases that prove the repaired boundaries:

- a nested, clean repository uploaded directly as `repository/v1`;
- the same exact retained Git version captured through the resource adapter;
- `log-diagnosis` with `budget_slice_usd: 5`, a conformant output-builder
  session, and a sealed `diagnosis/v1` output;
- `code-review` with two exact repository snapshots and a sealed `review/v1`
  output;
- one deliberately failed run shown through the safe build-log hint.

Any failure preserves the tested node version as unreleased and records the exact
build, image digest, API response, and bounded logs in the findings and track
ledger. It does not authorize a live hotfix or an uncapped retry.

### 7. Verified runner publication writes one bounded GitOps change

The user explicitly authorizes this track to write the normal external
home-infra repository and requests no routine confirmation questions for that
operation. That authority is deliberately narrower than general home-infra
administration: the `build-agent-runner-image` pipeline job may invoke its
unprivileged `deploy/write-agent-runner-home-infra.sh` stage only after the
exact commit-tagged image has built, passed its Dockerfile smoke, passed its exact-image
linux/amd64 smoke, returned a registry digest matching
`sha256:<64 lowercase hex>`, been pulled back by immutable reference, and
inspected as exactly `linux/amd64`. The mutable convenience tags are pushed
before the writeback; none is valid deployment evidence.

The `home-infra` Git resource, not the privileged image builder, owns the
official HTTPS repository (`https://github.com/tdmtrader/home-infra.git`),
branch `main`, `disable_ci_skip: true`, and its username/password secret
interpolation; no debug resource field is permitted. The builder writes its
verified immutable image, exact 40-lowercase-hex source commit, and
`RUNNER_VERSION=<major.minor.patch>` only to a data-only output metadata file
after all image gates and mutable-tag pushes complete. A separate unprivileged
pipeline task consumes that metadata and the checked-out `home-infra` resource,
then calls `deploy/write-agent-runner-home-infra.sh`. The helper accepts only
`registry.home/agent-runner@sha256:<64 lowercase hex>`, counts exactly one
inline YAML mapping whose `name` is `CONCOURSE_AGENT_STEP_IMAGE`, and replaces
only that mapping's quoted `value` in `apps/concourse.yaml`. It fails before
committing on malformed image/source/version input, a mutable reference, a
missing or duplicate target, or any unexpected diff. Its commit records the
version, full source SHA, and immutable image. An equal value is a successful
no-op with no commit. `apps/concourse.yaml` is fixed; tests may safely override
only the checked-out repository path for a local bare-remote fixture. It must
accept a detached resource checkout and linked worktree by checking
`git rev-parse --is-inside-work-tree`, not a current branch or `.git`
directory.

The helper never clones, fetches, authenticates, pushes, accepts a token, or
constructs an authenticated URL. It runs with xtrace disabled. The pipeline's
unprivileged `put: home-infra` uses the resource's normal secret-backed HTTPS
credentials with `rebase: true`, `timeout: 5m`, and no force option. A rebase
conflict or a non-fast-forward refusal fails closed, leaves the remote without
the helper's new value, and requires an operator/job retrigger; neither task
force-pushes. The privileged builder is forbidden from a home-infra checkout
or Git operation. It retains its existing `github-token` only for GHCR login,
so the design does not claim that the same named secret is absent from that
task.

This writeback is a GitOps request, not proof of activation. Keep promotion
paused until ArgoCD reports the `concourse` Application synced and healthy and
the running web deployment's effective `CONCOURSE_AGENT_STEP_IMAGE` is the
same immutable GHCR digest. Only then may the matching same-commit web rollout
continue and the fresh node dogfood gate begin. Normalize the stale
`apps/concourse.yaml` runner-image comment separately in a clean, isolated
external home-infra worktree; do not combine it with the automated one-field
digest commit.

## Dependency Order

1. The archive/fallback prerequisite commit must be present.
2. Resource-capture naming and its DB consumers land atomically.
3. Public validation diagnostics and the full repository upload regression
   land before interpreting the live 422.
4. MCP conformance lands before runner preflight can require it.
5. Runner image capability and digest publication land before deployment.
6. Service-account no-xtrace and final-stamped web release contracts land
   before deployment.
7. The bounded GitOps writeback lands after immutable publication and before
   any promotion; it must remain independently testable and reviewable.
8. ArgoCD must activate that exact digest before the matching web rollout.
9. Operator hints and ServiceAccount documentation land before the live
   first-user rerun.
10. Same-commit web/runner deployment precedes package re-import and every
    acceptance run.

## Verification Strategy

Each implementation task starts from the smallest existing behavioral failure
that proves the defect, adds a test only when load-bearing behavior is not
already covered, runs focused Go tests, ends with `gofmt` and `git diff
--check`, and receives one blocking review before the next task. When a new
test is necessary, observe it fail for the actual defect before implementation;
never add a duplicate or artificial failure merely to manufacture another red
signal. PostgreSQL-backed packages run serially. No task exceeds three review
rounds; a third-round blocker is escalated in the ledger rather than silently
widening scope.

Repository implementation gates:

```bash
go test ./agent/resourcecapture ./agent/workflowrun -count=1
ginkgo --procs=1 --focus='exact authorized resource-capture output' ./atc/db
go test ./agent/snapshot/contracts ./agent/snapshot ./agent/api/snapshots ./fly/commands -count=1
go test ./agent/schema ./agent/outputbuilder ./cmd/agent-output ./agent/runner -count=1
go test ./deploy -run '^TestPipelineTasksDoNotTraceServiceAccountTokens$' -count=1
go test ./deploy -run '^TestConcourseReleaseImageUsesFinalStampedServer$' -count=1
go test ./deploy -run '^(TestWriteAgentRunnerHomeInfra|TestAgentRunnerPipelineWritesVerifiedHomeInfraDigest|TestAgentRunnerWritebackRunbookOrdersArgoActivation)$' -count=1
go test ./deploy ./fly/commands -count=1
git diff --check
```

Image verification is explicit and may run on Borg when the local Docker
daemon is unavailable:

```bash
make build-agent-runner-image
CONCOURSE_AGENT_RUNNER_SMOKE=1 make test-agent-runner-smoke
```

Completion requires the live node-level acceptance evidence described above,
not merely green unit tests or a healthy `/healthz` endpoint.

## Rollout and Recovery

Pause new agent dispatch and the promotion jobs during the compatibility
window. Build the web and runner artifacts from the same commit, capture the
immutable runner digest, and let the bounded helper commit the one-file change
for the `home-infra` resource's rebase-only `main` write. Do not unpause promotion merely because that Git push
succeeds: first require ArgoCD to report `concourse` synced and healthy and
require the running web deployment's effective
`CONCOURSE_AGENT_STEP_IMAGE` to equal the recorded immutable GHCR digest.
Then perform the matching web rollout and verify the configured runtime digest
before re-enabling dispatch. Capture the exact final, non-RC tag on that
commit and require the running `/api/v1/info` response's `version` and
`jetbridge_version` fields to equal it exactly. A retained `-rc` value fails
the rollout even when the image tag itself is final.

Inspect the running web arguments read-only and record the exact
`--kubernetes-namespace` and `--kubernetes-service-account` values. An absent
ServiceAccount argument means the task namespace's default ServiceAccount and
is not itself a failure. Review that selected account's effective RBAC against
the intended task policy and block only unintended broad privileges. Never
read a projected token or Secret to collect this evidence. Token lifetime,
audience, and bound-object claims remain unknown and are not prerequisites for
acceptance after the proven xtrace path is closed.

Re-import affected node packages only after those checks; content deduplication
to an existing exact version is the expected result.

If the rollout fails, restore the prior web and exact runner digests together.
Do not keep a new web against an old runner, do not retag a mutable image to
simulate rollback, and do not release a node that has not sealed a valid output
on the restored pair. A writeback push race is not a rollback case: retain the
paused state, require an operator/job retrigger after the bounded resource put,
and never force-push or edit the contested remote checkout. The stale explanatory comment is a
separate external-worktree maintenance change and must not be folded into an
automatic rollback or digest commit.

## Completion Criteria

- Both direct upload and exact resource capture produce usable
  `repository/v1` snapshot IDs on the target.
- Known repository validation failures return a stable allow-listed reason;
  unknown causes remain generic and no secret-bearing error is exposed.
- The output-builder completes initialize, initialized notification,
  tool-list, and tool-call exchanges; successful preflight persists exactly one
  `mcp.ready` metric event, and the runner rejects a broken managed protocol
  before starting the model.
- A positive `$5` node slice executes on the packaged runtime without an
  unknown-option failure.
- The deployed runner is named by a registry-reported immutable digest from
  the same commit as the web.
- The pipeline has committed exactly one `apps/concourse.yaml` field change
  carrying that verified GHCR digest (or recorded an idempotent no-op), without
  exposing a Git token; ArgoCD has activated that exact value before promotion.
- Plain `nodes show-run` output provides an exact build-log command whenever a
  planned build exists.
- Fresh post-rollout log-diagnosis and code-review runs each pin the deployed
  runner digest and produce one inspected, valid typed output before release.
- `JETBRIDGE_FIRST_USER_FINDINGS.md` and the track ledger contain exact
  verification and live disposition.
- Both deployment pipelines pass the no-xtrace token contract before another
  rollout. Token lifetime, audience, and bound object remain unknown and are
  not acceptance prerequisites after the proven disclosure path is closed.
- The final image activates the final server built from the frontend-rebuilt
  source tree; its pre-push version check and live `/api/v1/info` evidence show
  the exact final version for both server identities, with no `-rc` suffix.
- Chart documentation states that an empty task ServiceAccount selects the
  namespace default. Live evidence records the configured argument or its
  absence and effective RBAC without reading a credential; only privileges
  broader than the intended task policy block acceptance.
