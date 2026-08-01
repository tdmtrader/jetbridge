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

## Tasks

- [ ] Task 1 — repair exact resource-capture template identity
  - Gate: both public and persisted-selection constructors, resource-capture
    unit/API tests, and serial ATC DB capture Ginkgo specs.
  - Review budget: maximum three blocking rounds.
- [ ] Task 2 — safe repository validation diagnostics and full upload contract
  - Gate: real Fly archive → canonicalizer → `repository/v1` validator plus
    secret non-disclosure tests.
  - Review budget: maximum three blocking rounds.
- [ ] Task 3 — managed output-builder MCP lifecycle and runner preflight
  - Gate: initialize → initialized notification → tools/list → tools/call and
    zero provider starts against a protocol-broken managed builder; a successful
    managed preflight persists exactly one `mcp.ready` event.
  - Review budget: maximum three blocking rounds.
- [ ] Task 4 — immutable runner CLI/image capability and digest publication
  - Gate: checksum/version parity, Docker image smoke, and registry-reported
    digest evidence inspected as exactly `linux/amd64` after registry pull.
  - Review budget: maximum three blocking rounds.
- [ ] Task 5 — safe build-log correlations, target wording, and node docs
  - Gate: Fly command/integration tests and stale-wording residue check.
  - Review budget: maximum three blocking rounds.
- [ ] Task 6 — same-commit deployment and fresh node-level dogfood acceptance
  - Gate: both repository ingress paths, positive `$5` run, one durable
    `mcp.ready` count per managed-builder run, sealed `diagnosis/v1` and
    `review/v1` outputs, exact failure-log hint, and evidence-based release
    disposition.
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
