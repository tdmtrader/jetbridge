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
  - Review budget: maximum three blocking rounds.
- [ ] Task 5 — safe build-log correlations, target wording, and node docs
  - Gate: Fly command/integration tests, stale-wording residue check, and chart
    documentation that an empty task ServiceAccount selects the namespace
    default rather than the web ServiceAccount.
  - Review budget: maximum three blocking rounds.
- [ ] Task 6 — same-commit deployment and fresh node-level dogfood acceptance
  - Gate: both repository ingress paths, positive `$5` run, one durable
    `mcp.ready` count per managed-builder run, sealed `diagnosis/v1` and
    `review/v1` outputs, exact failure-log hint, exact final non-RC server
    identities from `/api/v1/info`, read-only runtime ServiceAccount/RBAC
    disposition, and evidence-based release disposition for `JBUSER-007`
    through `JBUSER-009`.
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

## Completion gate

The track is complete only when Tasks 1–6 are checked, the final blocking
review finds no Critical, High, or acceptance-blocking issue, both exact node
versions have fresh-run evidence and an explicit release or unreleased
disposition, and
`JETBRIDGE_FIRST_USER_FINDINGS.md` contains the exact live evidence.
