# SDD ledger — plan: docs/superpowers/plans/2026-08-01-jetbridge-first-user-blocker-remediation.md

## Baseline

- Track requested: 2026-08-01.
- Worktree: `.worktrees/agentic-platform-rebase`.
- Branch: `codex/agentic-platform-rebase`.
- Track base: `524afc2460` (`docs(agent): record jetbridge first-user findings`).
- Required repository instructions read:
  - `CLAUDE.md`
  - `docs/superpowers/plans/2026-07-28-agentic-foundations-semantic-rebase-session-context.md`
- Prerequisite first-user commits present:
  - `a24e0771c2` — nested Fly archive compatibility and output-authority fallback.
  - `021b17d51e` — first-user reusable node packages.
  - `524afc2460` — first-user findings and scoped verification.
- Existing scoped verification at the base:
  - `go test ./agent/workflow ./fly/commands -count=1`: passed.
  - `go test ./agent/runner -count=1`: passed outside the filesystem/network
    sandbox required by its localhost HTTP tests.
  - `git diff --check`: passed.
- Protected unrelated user change:
  `.superpowers/sdd/2026-07-28-agentic-foundations-semantic-rebase/progress.md`.
  This track must not stage or edit it.
- Live baseline: target `home`, team `main`; `code-review@9` and
  `log-diagnosis@9` are imported, unreleased, and intentionally retain a
  positive `$5` slice. Their pre-rollout failed runs are not acceptance
  evidence. Either exact version may be rerun after rollout and released only
  if that fresh run pins the repaired runtime and produces an inspected valid
  output.

## Diagnosed blockers

| ID | Status at track creation | Evidence |
| --- | --- | --- |
| `JBUSER-001` | Proven repository defect | Both public and persisted-selection resource-capture paths build an immutable template name without the suffix required by `workflowrun.TemplateSaver`; two DB queries reconstruct the same legacy name. |
| `JBUSER-002` | Live failure proven; precise validator difference unproven | Current local `repository/v1` accepts the Fly-canonicalized clean repository, while live returned opaque 422 and the handler discards validator detail. |
| `JBUSER-003` | Proven repository defect | Output-builder health passes but its MCP adapter has no initialize lifecycle and no tool schemas. |
| `JBUSER-004` | Proven image/version skew | Runner emits the correct positive budget flag; runner image pins Claude Code 2.0.1, which rejects it. |
| `JBUSER-005` | Proven Fly omission | API carries `planned_build_id`; plain run-detail rendering drops it. |
| `JBUSER-006` | Proven wording defect | `fly targets` labels undecodable expiry as an invalid token without authenticating. |
| `JBUSER-007` | Proven credential disclosure; blocks the next rollout | The live `k8s-live-tests` log traced a projected Kubernetes service-account token assignment and its expanded bearer argument because `deploy/concourse-pipeline.yml` uses `sh -x`; `deploy/borg-pipeline.yml` duplicates the pattern. Token lifetime, audience, and bound object remain unknown and are not acceptance prerequisites after the trace path is closed. |
| `JBUSER-008` | Proven final-image identity defect; blocks acceptance | The final `v0.2.220` image still reports `0.2.220-rc`: the build task stamps only the RC server, and the release task replaces Fly assets without building or activating a final-stamped server. The correction must preserve the separately rebuilt frontend. |
| `JBUSER-009` | Proven nonblocking documentation/runtime mismatch | An empty chart `kubernetes.serviceAccount` omits the web argument and therefore selects the task namespace's default ServiceAccount, not the web ServiceAccount. Task 6 must record the live argument or absence and effective RBAC read-only without accessing a credential. |

## Track authoring review

- Round 1 found five blocking gaps in the draft: the persisted-selection
  capture constructor was omitted; the ATC command did not execute Ginkgo
  specs; live MCP success had no durable artifact; the pipeline did not prove
  the pushed image platform; and blanket test wording conflicted with the
  session rule against artificial duplicate RED tests.
- All five were corrected in the design and plan.
- Round 2 found no Critical, High, or acceptance-blocking issues. It also
  verified that the build-scoped `fly curl` metrics command is valid and that
  existing ingestion preserves the new `mcp.ready` event count.
- The rollout addendum integrates `JBUSER-007` and `JBUSER-008` into Task 4,
  `JBUSER-009` into Task 5, and exact final-version plus runtime
  ServiceAccount evidence into Task 6. It does not authorize implementation,
  push, pipeline application, deployment, or credential inspection.
- Structural verification passed: Task 1 remained identical to `HEAD`; Tasks
  1–6 each retained exactly one Files block and one Interfaces block; Task 5
  contains no final-version rollout variable; shortcut/addendum residue is
  absent; and `git diff --check` passed.
- Independent blocking review round 1 of the rollout addendum passed with no
  Critical, High, or acceptance-blocking findings. Review budget used: 1 of 3.

## Tasks

- [x] Task 1 — repair exact resource-capture template identity
  - RED: direct and persisted-selection constructor assertions exposed the
    legacy unsuffixed name; real immutable-saver failure mapping exposed raw
    collision/platform errors; the API returned 500 for a template conflict;
    and the serial DB spec could not discover the valid suffixed template.
  - GREEN: one shared constructor derives
    `agent-resource-capture-<operation[:24]>-<target-config-hash[:12]>` while
    retaining raw canonical `FullHash`; real `TemplateSaver` accepts the
    capture spec; collision/platform errors map to stable capture categories;
    bounded API errors log causes and return 409/503 categories; both DB
    readers retain all ownership predicates and require the anchored lowercase
    12-hex suffix.
  - Verification: `go test ./agent/resourcecapture ./agent/workflowrun
    ./agent/api/snapshots -count=1` passed; `ginkgo --procs=1
    --silence-skips --focus='exact authorized resource-capture output'
    ./atc/db` passed (1/1); `git diff --check` passed.
  - Review: independent blocking review round 1 passed with no blocking
    findings. Review budget used: 1 of 3.
- [x] Task 2 — safe repository validation diagnostics and full upload contract
  - RED/GREEN: a closed `PublicValidationFailure` API permits exactly seven
    repository reasons; unknown and nil-cause construction remains private.
    Repository-category tests first exposed plain internal errors, then proved
    safe classification for missing/unsafe metadata, shallow history,
    unsupported object format, committed gitlinks, dirty work trees, and
    broken object graphs. Root and cancellation failures remain private.
  - RED/GREEN: the bounded error envelope initially had no `reason`; only an
    allow-listed public validation failure now returns its optional reason and
    fixed public message before the generic validation mapping. Generic 422
    responses retain their prior wire shape and test causes never reach JSON.
  - RED/GREEN (review round 1): a fake Git process exiting 97 was initially
    classified as public `repository_invalid`. Generic Git start and exit
    failures now stay private while explicitly semantic post-start Git checks
    retain their intended safe classification; the raw process detail remains
    available to internal logging.
  - Gate: a clean nested Git repository archived through the real Fly writer,
    canonicalizer, captured-tree root, and `repository/v1` admission succeeds.
  - Verification: `go test ./agent/snapshot -count=1`; `go test
    ./agent/snapshot/contracts -count=1`; `go test ./agent/api/snapshots
    -count=1`; `go test ./fly/commands -count=1`; and `git diff --check`
    passed.
  - Review: independent blocking round 1 found and the TDD process-failure
    regression fixed one boundary issue; independent round 2 passed with no
    findings. Review budget used: 2 of 3.
- [x] Task 3 — managed output-builder MCP lifecycle and runner preflight
  - Gate: initialize → initialized notification → tools/list → tools/call and
    zero provider starts against a protocol-broken managed builder; a successful
    managed preflight persists exactly one `mcp.ready` event.
  - RED/GREEN: output-builder now performs the exact MCP initialize,
    initialized notification, and tool-discovery lifecycle with bounded,
    authority-free schemas; runner preflight emits exactly one safe mcp.ready.
  - Review round 1 corrected helper-only Run proof, unbounded synthetic-204
    draining, and missing durable mcp.ready event-count evidence.
  - Review round 2 corrected provider-time typed event/schema assertions.
  - Review round 3 corrected the broken path to use a recording provider and
    typed results/events. Final round 3 PASS; no blocking findings.
  - Verification: outputbuilder/cmd/agent-output/runner, nested schema, and
    focused atc/exec durable-count Ginkgo all passed; gofmt and diff check
    passed. Minor nonblocking note: EventError payload is not separately
    unmarshaled.
- [ ] Task 4 — immutable runner CLI/image capability and digest publication
  - Gate: checksum/version parity, Docker image smoke, and registry-reported
    digest evidence inspected as exactly `linux/amd64` after registry pull;
    static no-xtrace coverage for projected-token consumers in both deployment
    pipelines; and a final server built from the frontend-rebuilt source,
    activated in the final image, and checked before push.
  - Repository implementation: complete. The runner uses the checksum-pinned
    native Claude `2.1.212`, runs its closed smoke after all binaries are
    installed, publishes a commit-tagged linux/amd64 image only after smoke,
    derives its immutable identity from the registry push response, and pulls
    and inspects that exact reference before printing deployment evidence.
  - RED: the image contract exposed npm-installed Claude `2.0.1`; the parsed
    pipeline policy exposed exactly three projected-token consumers with
    xtrace enabled; the immutable-runner test lacked commit-tag, smoke-before-
    push, digest, pull, platform, and evidence ordering; the release test lacked
    post-frontend RC/final builds and final-binary staging; and the recurring
    runbook lacked the pause-first compatibility sequence.
  - GREEN: `sh -n deploy/agent-runner/smoke.sh`; `env
    GOCACHE=/tmp/jetbridge-task4-go-build-cache go test ./deploy -run
    '^TestPipelineTasksDoNotTraceServiceAccountTokens$' -count=1`; the same
    command with `^TestConcourseReleaseImageUsesFinalStampedServer$` and
    `^TestAgentRunner`; and `git diff --check` all passed.
  - Broad verification: `go test ./deploy ./agent/runner -count=1` passed with
    `deploy` in 0.176s and `agent/runner` in 2.664s on the approved host. The
    sandbox attempt stopped only because `httptest` could not bind `[::1]:0`.
  - Local image probe: `docker info --format '{{.ServerVersion}}
    {{.OSType}}/{{.Architecture}}'` returned `Cannot connect to the Docker
    daemon at unix:///var/run/docker.sock. Is the docker daemon running?` No
    workaround, image build, push, pipeline application, or deployment was
    attempted.
  - Review: independent blocking round 1 PASS with no Critical, High, or
    acceptance-blocking findings. Review budget used: 1 of 3.
  - Live image gate (build `645354`): the exact selected commit
    `ae40bf0d2b0ac4e7268260c9388c7f80e4375e72` checksum-verified/downloaded
    Claude `2.1.212` and built every required Concourse binary, but the
    build-time smoke exited 1 before image push or digest publication. Root
    cause: the root smoke `RUN` preceded `ENV IS_SANDBOX=1`; `set -e` plus
    captured Claude help made that refusal silent.
  - Repair evidence: a focused Dockerfile-ordering assertion was RED against
    that order and GREEN after moving the unchanged `IS_SANDBOX=1` contract
    before smoke, while retaining all required binaries before both. Smoke now
    emits bounded status-only errors for failed Claude version/help calls and
    names missing required flags or Concourse binaries. `sh -n
    deploy/agent-runner/smoke.sh`, `env GOCACHE=/tmp/jetbridge-go-cache go
    test ./deploy -run '^TestAgentRunnerDockerfile$' -count=1`, full
    `go test ./deploy -count=1`, and the owned-file `git diff --check` passed.
  - Retry gate (build `645476`): exact commit
    `47aaaf7b3efa4dded22bbba685e53bd678dce509` reached Dockerfile smoke twice
    but failed before push/digest with bounded `ERROR: Claude help is missing
    required flag --max-turns`. The exact binary registers that hidden-help
    option and bundles its expected missing-argument parser diagnostic. The
    initial `--version` probe was rejected as a false green because an unknown
    flag also exits 0. The revised zero-work
    `claude --print --max-turns </dev/null` probe distinguishes registered
    missing-argument from unknown-option behavior without printing captured
    output. Its shell syntax, focused regression, full deploy suite, and diff
    check passed.
  - Review: independent final Task 4 blocking review round 3 PASS with no
    Critical, High, or acceptance-blocking findings. Review budget used: 3 of
    3.
  - PENDING: repeat the remote runner-image build and executable
    `agent-runner-image-smoke`, then capture the registry-reported digest,
    immutable pull, and exact `linux/amd64` inspection. The remote smoke is
    valuable acceptance evidence, not a replaceable static Dockerfile check.
- [x] Task 5 — safe build-log correlations, target wording, and node docs
  - Gate: Fly command/integration tests, stale-wording residue check, and chart
    documentation that an empty task ServiceAccount selects the namespace
    default rather than the web ServiceAccount.
  - RED: focused renderer tests failed because plain run detail omitted both
    `planned build: 418` and the exact `fly -t home watch -b 418` hint, unsafe
    aliases had no quoted command path, and an empty alias was accepted when a
    plain build hint was required. The serial focused targets integration ran
    seven specs and failed exactly the invalid-expiry case because both rows
    still reported `n/a: invalid token`.
  - GREEN: all six workflow/node run-detail callsites pass the selected
    `Fly.Target`. Plain detail emits the planned-build correlation and exact
    watch command only when a build exists; JSON remains the exact API
    `JsonPrint` serialization with no prose; an empty alias errors only for a
    required plain hint; and no stored terminal cause is exposed. Both
    copyable commands preserve safe aliases such as `home`/`test` unquoted and
    POSIX-single-quote unsafe aliases, including embedded quotes, without Go
    `%q` escaping.
  - Target boundary: undecodable local expiry now reports `expiry unavailable
    (run fly -t <target> status)` without an authenticated or other network
    request. Nil/empty tokens remain exactly `n/a`, and valid JWT UTC/RFC1123
    formatting is unchanged.
  - Documentation/chart: the operations guide now covers direct typed input
    creation, exact retained resource capture, exact import-version capture,
    unreleased run/show/watch, post-watch terminal-detail refresh, sealed
    output download/inspection, and release. It records positive-budget runner
    smoke dependence, zero as uncapped, portable model omission, the explicitly
    non-runnable sample image, and immutable/discoverable skills whose use is
    not guaranteed. The platform guide links that lifecycle, and chart docs
    correctly select the task namespace default ServiceAccount when empty,
    require explicit accounts only for intentional API access, and distinguish
    ordinary task token automount from the web pod's explicit projection.
  - Verification: `go test ./fly/commands -count=1` passed. After the sandbox
    attempt was denied permission to bind its localhost listener, the
    host-permitted serial `ginkgo -r --keep-going --procs=1 --focus='targets'
    ./fly/integration/` passed 7/7 focused specs (662 skipped). The exact stale
    wording `rg` returned no matches, and `git diff --check` passed.
  - Review: independent blocking review round 1 PASS with no Critical, High,
    or acceptance-blocking findings. Review budget used: 1 of 3.
- [ ] Task 6 — same-commit deployment and fresh node-level dogfood acceptance
  - Gate: both repository ingress paths, positive `$5` run, one durable
    `mcp.ready` count per managed-builder run, sealed `diagnosis/v1` and
    `review/v1` outputs, exact failure-log hint, exact final non-RC server
    identities from `/api/v1/info`, read-only runtime ServiceAccount/RBAC
    disposition, and evidence-based release disposition for `JBUSER-007`
    through `JBUSER-009`.
  - First rollout attempt: dispatcher paused at `2026-08-01T19:21:33Z` for
    exact pushed/ref commit `72b831de8a8a482f4dbcf4afea60928423663de9`.
    Set-self build `645222` and build-and-vet build `645223` succeeded. Unit-
    tests build `645231` consumed that exact ref and stopped the rollout before
    runtime image publication or deployment.
  - RED: `go test ./atc/exec -count=1` passed 689/693 specs and failed four
    flight-recorder fixture cases. Adding `mcp.ready` at index 1 had shifted the
    stream while downstream positional truncations and mutations still targeted
    the old locations; this also left cost-floor cases latently false-green.
    Production ingestion was not at fault.
  - GREEN: named semantic event indexes repaired every partial-stream and event
    mutation site while preserving `mcp.ready`; the full package passed 693/693
    (`ok github.com/concourse/concourse/atc/exec`), and `git diff --check`
    passed. Independent Task 6 blocking review round 1 PASS found no Critical,
    High, or acceptance-blocking findings.
  - Rollout update: corrected CI-fixture commit
    `ae40bf0d2b0ac4e7268260c9388c7f80e4375e72` was pushed. Set-self `645323`,
    build-and-vet `645324`, unit `645338`, k8s-runtime `645353`, tag-rc
    `645362`, build-image `645373`, self-upgrade `645388`, verify-upgrade
    `645391`, and k8s-live-tests `645394` succeeded. The web is
    `0.2.221-rc`; the runner remains old because build `645354` failed before
    image push. Dispatcher remains paused.
  - Safety disposition: after the `0.2.221-rc` live tests, `self-upgrade` and
    final `release` were manually paused before the next push. A status-only
    jobs query verified both `paused:true` and `next_build:null`, preventing
    smoke-fix RC deployment before matching runner activation and an
    incompatible `0.2.221` final publication. The runner-image job is manual
    and not a dependency of self-upgrade/release, so the web RC advanced despite
    the matching-runner failure. Manual compatibility-window control is required
    today; pipeline dependency wiring is a follow-up candidate, not part of
    this scoped delta. New smoke-fix commit/push/retry remains pending.
  - Promotion remains paused. Web RC build `645496` for the now-superseded
    commit succeeded but was not promoted.
  - Current disposition: no rollout or node acceptance is claimed. The
    repository pipeline may publish and verify an immutable runner digest, but
    external home-infra/ArgoCD owns activation. Same-commit node acceptance
    cannot proceed until that reviewed handoff completes.
  - Review budget: maximum three blocking rounds.

## Execution rules

- One Terra implementer owns each task's files; a distinct reviewer performs
  the blocking review after focused verification.
- PostgreSQL-backed suites run serially.
- A task is checked only after its implementation, focused verification,
  blocking review, diff hygiene, and scoped commit are all complete.
- Image capability is not accepted from Dockerfile text alone. A successful
  `agent-runner-image-smoke` against the built linux/amd64 image and a
  registry-pulled immutable digest inspected as `linux/amd64` are required.
- Live acceptance re-imports the exact packages after rollout; content
  deduplication to an existing version (likely `@9`) is expected. Only a fresh
  post-rollout run whose rendered target pins the repaired runtime can prove
  the repair; old runs cannot.
- A live failure records exact target time, web commit, runner digest, snapshot
  ID, node version, run ID, planned build ID, bounded log excerpt, spend, and
  release state. It does not authorize a hotfix outside the owning task.
- No task silently changes external deployment state, pushes Git, opens a pull
  request, or releases a node outside Task 6's explicit evidence gate.

## Task 6A addendum — verified runner GitOps writeback

- RED: the required focused deploy suite failed because the real-Git helper,
  verified metadata output, native Git resource, unprivileged update, rebase
  put, and Argo-ordered runbook did not exist.
- GREEN: seven real-Git behavioral subtests plus three pipeline/runbook
  contracts, shell syntax, full `go test ./deploy`, and `git diff --check`
  passed. The helper validates clean checked-out Git state, edits exactly one
  mapping, and leaves push authority to the native resource. The builder emits
  validated metadata after immutable verification; update parses it before a
  rebase-only five-minute put; self-upgrade requires both image jobs.
- Isolated home-infra comment-only commit (not pushed; digest unchanged):
  `f185d35266f4352b009eb5e8c9bcea552b2b7f68`.
- Blocking-only self-review round 1: no correctness, credential-boundary,
  data-loss, required-behavior, or GitOps-concurrency blocker.

## Completion gate

The track is complete only when Tasks 1–6 are checked, the final blocking
review finds no Critical, High, or acceptance-blocking issue, both exact node
versions have fresh-run evidence and an explicit release or unreleased
disposition, and
`JETBRIDGE_FIRST_USER_FINDINGS.md` contains the exact live evidence.
