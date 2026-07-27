# Harvest Step Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. The harvest step's independent gate re-run and judge scoring remain live in spirit; its ticket-transition and per-ticket credential model below are historical — see the current outcomes/snapshot design.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the deterministic terminal `harvest:` step that independently re-runs build/test/lint gates through the repo's dev-mcp (as code, via the Go client), scores the branch with a platform-funded rubric judge, pushes `agent/ticket-<n>` with git credentials that exist only in the harvest pod, and walks the ticket to its outcome through ticket-core's transition function with evidence attached.

**Architecture:** A new `atc.HarvestStep` plan-union step (copying the freshly-landed `agent:` step recipe) whose `exec.HarvestStep` builds a jetbridge pod containing a `harvest-runner` main container (deterministic Go binary, no LLM control flow) plus the repo's dev-mcp sidecar; the runner executes gates/judge/push and writes the flight recorder, and the exec step ingests results server-side — metrics row, `agent_reviews` evidence row with new ticket/run linkage columns, cost-ledger record, and the single-writer ticket transition. Git credentials reach the pod through a new main-container-only `SecretMounts` seam in `runtime.ContainerSpec`/jetbridge; the judge's Anthropic token comes from the long-lived `agent-platform-credential` secret via `SecretEnv` — agent pods never see either.

**Tech Stack:** Go (atc plan/exec/engine, `agent/harvest` package, `cmd/harvest-runner`), PostgreSQL migration 1773106080, Ginkgo/Gomega + counterfeiter fakes (`devmcpfakes`, `ticketsfakes`, `metricsfakes`), jetbridge fake-clientset specs, plain-Go `//go:build live` theborg tests, Elm (build-page step rendering), claude CLI (judge only).

---

## Context

**Charter (workstreams.json `harvest-step`, wave 3, size L).** Scope in: (1) harvest step type reusing agent-step's plan/engine plumbing, with the terminal-step config schema published early in the wave for dispatch's renderer; (2) gate-policy language v1 with an explicit flake/retry stance, interpreted here, gates invoked through the dev-mcp Go client; (3) rubric judge (schema-constrained, ci-agent scoring style) with the rubric→six-verdict mapping agreed with delivery-outcomes/scorecards before either UI lands, funded per the platform-credential policy; (4) push `agent/ticket-<n>` with per-repo git credentials mounted only into the harvest pod + a live theborg security test + patch manifest + review-evidence links; (5) extend `agent_reviews`/`agent_feedback` with ticket/run linkage columns; (6) ticket updates through the transition function on every outcome; (7) a fixture-workspace test suite covering gates pass / fail / flaky. Scope OUT (do not build): diff/PR rendering on the ticket page, merge detection, cross-provider review calls, budget enforcement call-sites.

**Landed prior waves (assume these exist exactly as 00-shared-contracts.md defines; do not re-implement):**
- **dev-mcp:** `agent/devmcp` contract types (`Component`, `ToolResult`, `Failure`, `Status` with `StatusOK/StatusFailed/StatusError`, `EnvEndpoint = "DEV_MCP_URL"`), the `Client` interface + `NewClient(endpoint, opts...)` streamable-HTTP client with `WithProgress`, `RPCError`, and generated `devmcpfakes.FakeClient` (§3.1).
- **agent-step:** `atc.AgentStep`/`atc.AgentPlan` and the full step recipe; `atc/exec/sidecars.go` helpers `loadSidecarConfigs`, `resolveSidecarImages`, `sidecarProcessIO`; `exec.AgentStep` with `attachOrRun` resumability; `agent/schema` nested module with `Event`, `EventWriter`/`EventReader`, `Results`, `ThreeWayStatus`, `RunStatusOK/RunStatusFailed/RunStatusError`, and payload structs `StepStartData`, `StepEndData`, `GateStartData`, `GateResultData`, `JudgeScoreDimension`, `JudgeScoreData`, `PushDoneData`, `CostRecordData`; `agent/api/metrics` `Store` (`Upsert/GetByBuild/ListByTicket`) + `db.NewAgentRunMetricsFactory`; engine options `WithAgentStepImage/WithAgentMetricsStore/WithAgentBudgetChecker`; the `--agent-step-image` flag; `deploy/agent-runner/Dockerfile`; `db.ContainerTypeAgent`; Elm `BuildStepAgent`.
- **ticket-core:** `agent/api/tickets` (`Ticket`, `State` constants, `Store` with `Transition(id, from, to, meta)`, `TransitionMeta{PipelineRunID *int, Branch string, ErrorDetail string}` — frozen by ticket-core §2.1; there is **no** actor/`By` field, so harvest carries "harvest" attribution via flight-recorder events, not the transition), `db.NewAgentTicketsFactory(dbConn)`, `ticketsfakes` counterfeiter fakes.
- **credentials-and-budgets:** `agent/budget` (`Checker`, `LedgerEntry`, `Remaining`, `SourceHarvestJudge = "harvest_judge"`), `agent_cost_ledger` with `source` CHECK including `harvest_judge`, the long-lived `agent-platform-credential` K8s secret (key `anthropic-token`) kept in sync by the platform-credential syncer (§1.13, §8.2).
- **workflow-store:** the §6 YAML grammar with the `gate_policy` and `judge` slots this step interprets (declared-but-inert until now).

**Contract surfaces this plan PRODUCES** (00-shared-contracts.md sections): §1.10 "`agent_reviews` / `agent_feedback` linkage", the harvest half of §2.8 "Agent + Harvest step config (plan union)" (frozen for dispatch's renderer via the Task 1 addendum §2.8.1), §6.3 "Gate-policy language" Go types + interpretation (plus the `retries` flake-stance addendum), §6.4 "Judge rubric → six-verdict mapping" (sign-off addendum §6.4.1), and the §8.3 "Harvest-only git credentials" pod posture (implemented here via the new `SecretMounts` seam).

**Contract surfaces this plan CONSUMES:** §3.1 "dev-mcp" (Go client + error taxonomy + progress semantics), §1.7/§2.1 "Ticket tables"/"Ticket" (transition function), §1.8/§2.4/§5 "agent_run_metrics"/"RunMetrics"/"Flight-recorder event schema", §1.4/§2.7 "agent_cost_ledger"/"Budget library", §1.13/§8.2 "Platform credential policy"/secrets, §8.1 env contract, §8.5 sidecar image packaging (the dev-mcp image mounted as the harvest pod's sidecar).

**Key design decisions (recorded in the Task 1 addendum):**
- The harvest pod's main container reuses the **agent-runner image** (`--agent-step-image`): it already ships git, ca-certs, and the pinned claude CLI the judge needs; this plan adds the `harvest-runner` binary to that image. No new flag.
- Harvest-runner **exit taxonomy**: 0 = gates passed (push/judge per config), 1 = gates failed (verification rejected the work), 2 = platform error. `results.json` status `pass/fail/error` mirrors it; ticket transitions: `ok → needs_review` (+branch), `failed → needs_review` (nothing pushed), `error → errored`.
- **Push-by-sha:** harvest pushes the exact `HEAD` sha captured *before* the judge runs (`git push --force-with-lease=refs/heads/<branch> origin <sha>:refs/heads/<branch>` — *amended 2026-07-09, F32*: the lease makes attempt 2+'s fresh-clone re-push of the stable branch succeed instead of erroring non-fast-forward; harvest is the branch's only credentialed writer per §8.3), so a misbehaving judge process can never alter what is pushed.
- **Clean tree or fail** *(2026-07-09, F33)*: `git status --porcelain` runs right after the head sha is captured; any output ⇒ `workspace-dirty` evidence finding + exit 1 (`needs_review`) with no gates, no judge, no push, and no auto-discard — gates verify the tree while push delivers HEAD, so a dirty tree would let green evidence attach to unverified commits.
- **Sidecar readiness before gates** *(2026-07-09, F34)*: harvest-runner waits on dev-mcp's `GET /healthz` (2s/60s, shared `agent/devmcp.WaitHealthy` helper also used by agent-runner) before the first gate call; a never-healthy sidecar is exit 2 (platform error), not a gate failure.
- **Judge is advisory:** a judge *error* (CLI crash, malformed verdict) never blocks the push and never fails gates — it is recorded as `judge_error` in evidence/events. Gate failures are the only hard gate (spec §5: independent re-verification is the trust mechanism; §8: judge is triage signal).

---

### Task 1: Wave-start contract addendum — harvest terminal-step schema, flake stance, judge mapping sign-off

The charter requires the terminal-step schema "published early in the wave" and the rubric mapping "agreed … before either UI lands". §2.8's `HarvestStep` sketch references an undefined `JudgeConfig` and lacks the fields needed to actually execute (target branch for the gate diff, the dev-mcp sidecar source, pipeline-run linkage). Freeze all of it now, in writing, where dispatch/delivery-outcomes/scorecards will read it.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (insert `### 2.8.1` after the §2.8 render-time-resolution paragraph at :777; insert the `retries` field + stance into §6.3 at :1286 region; insert `### 6.4.1` after §6.4 at :1309; append to the §11 Amendment log at :1463)

**Steps:**

> *Amended 2026-07-09 (final review F32/F33/F30):* the fenced §2.8.1 text below now (a) pins the push as `--force-with-lease` (F32), (b) pins the worktree-cleanliness check (F33), and (c) adds the additive `env` schema field + the `AGENT_PIPELINE_RUN_ID` exec fallback (F30, co-signed pipeline-runs + dispatch per 00 §7/§11). Note that 00-shared-contracts.md ALREADY contains a standalone `### 2.8.1 Harvest push addendum (2026-07-09, F32)` block (landed by the final-review contracts pass at :779): when inserting the subsection below, place it immediately ABOVE that block and demote the block by deleting its `### 2.8.1 Harvest push addendum …` heading line — its body text stays as the closing "Push pin" paragraph of the single, merged §2.8.1.

- [ ] Insert the following subsection immediately after §2.8's closing "**Render-time-resolution rule (binding)**" paragraph (line 777 region):

````markdown
### 2.8.1 Harvest terminal-step schema — owner: **harvest-step** (addendum, 2026-07-08; frozen for dispatch's renderer)

The executable form of §2.8's `HarvestStep` sketch. Additive deltas from the sketch: `target_branch` (gate-diff base + `affected_components` input), `dev_mcp` (the repo's dev-mcp sidecar — §2.8 gave the step no way to reach a dev-mcp server), `pipeline_run_id` (evidence linkage; renderer emits it, hand-written pipelines may omit), `env` (renderer-emitted §8.1 identity rows — added 2026-07-09, F30: a Go renderer cannot place `((run_id))` in the int `pipeline_run_id` field, so identity travels as env exactly as it does on agent steps), and package-qualified policy/judge types (§6.3 places them in `agent/harvest`).

```go
// atc/steps.go — parse key "harvest", registered before "run" in StepPrecedence
type HarvestStep struct {
	Name          string               `json:"harvest"`
	Workspace     string               `json:"workspace"`                 // input artifact containing committed work
	Repo          string               `json:"repo"`                      // canonical slug (joins agent_reviews.repo)
	TargetBranch  string               `json:"target_branch,omitempty"`   // default "main"
	TicketID      int                  `json:"ticket_id,omitempty"`       // 0 = no ticket (pure-CI use)
	PipelineRunID int                  `json:"pipeline_run_id,omitempty"` // 0 = unknown (hand-dispatched)
	Branch        string               `json:"branch,omitempty"`          // e.g. agent/ticket-42
	Push          bool                 `json:"push,omitempty"`            // requires branch
	Env           map[string]string    `json:"env,omitempty"`             // renderer-emitted §8.1 identity/provenance rows (2026-07-09, F30)
	DevMCP        *SidecarSource       `json:"dev_mcp,omitempty"`         // repo's dev-mcp image; required when gates declared
	GatePolicy    harvest.GatePolicy   `json:"gate_policy"`               // §6.3
	Judge         *harvest.JudgeConfig `json:"judge,omitempty"`           // §6.4; nil = no judge
	Timeout       string               `json:"timeout,omitempty"`
}
```

`atc.HarvestPlan` mirrors these fields 1:1 (plus `Name`). `JudgeConfig` (previously referenced but undefined):

```go
// agent/harvest/policy.go
type JudgeConfig struct {
	Rubric        []RubricDimension `json:"rubric"`
	PassThreshold float64           `json:"pass_threshold"`       // 0–10 weighted total (§6 judge block)
	Model         string            `json:"model,omitempty"`
	BudgetUSD     float64           `json:"budget_usd,omitempty"` // §6 budget.judge_usd; 0 = uncapped
}
type RubricDimension struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Guidance string  `json:"guidance"`
}
```

**Execution contract [DECIDED HERE]:**
- Main container image = the `--agent-step-image` image (it gains a `harvest-runner` binary; git + claude CLI already present). Process: path `harvest-runner`, well-known process ID `harvest` (resumable via attachOrRun).
- Env (extends §8.1): `HARVEST_CONFIG` = JSON of `agent/harvest.Config` (step_name/workspace/repo/target_branch/ticket_id/pipeline_run_id/branch/push/gate_policy/judge); `AGENT_FLIGHT_DIR`; `AGENT_TICKET_ID` (when > 0); `DEV_MCP_URL=http://127.0.0.1:7780/mcp` (when `dev_mcp` declared). Judge-configured pods additionally get `CLAUDE_CODE_OAUTH_TOKEN` via `secretKeyRef` from `agent-platform-credential`/`anthropic-token` (§8.2) — never the per-run user token. The token key is `SecretEnv`-only (no literal counterpart): jetbridge `applySecretRefs` APPENDS the secretKeyRef EnvVar (§8.2 Consumption, 2026-07-09, F20 — landed by agent-step Task 11B). *(2026-07-09, F30)* The exec var-interpolates `HarvestStep.Env` rows and copies them into the pod env; **run-id fallback (binding, §7/§8.1):** when `pipeline_run_id` is 0 (the renderer leaves it 0 — see the schema note), the exec resolves the effective run id from the `AGENT_PIPELINE_RUN_ID` env row and uses it everywhere the run id flows (HARVEST_CONFIG, pod env, metrics/evidence linkage).
- Git credentials (§8.3): secret `agent-harvest-git-<slug>` (`harvest.GitCredSecretName(repo)`: lowercase, non-alphanumerics → `-`) volume-mounted read-only at `/var/run/agent/git/` on the **main container only**, only when `push: true`. New seam: `runtime.ContainerSpec.SecretMounts []runtime.SecretMount{SecretName, MountPath}`; jetbridge mounts these exclusively on the main container — sidecars never receive them.
- Exit taxonomy: 0 = gates ok, 1 = gates failed, 2 = platform error. `results.json` status `pass/fail/error` respectively; `metadata` carries `{"gates": [GateOutcome…], "judge": {…}|null, "judge_error": "…", "pushed_branch": "…", "head_sha": "…", "base_sha": "…"}`.
- Push-by-sha *(amended 2026-07-09, F32 — see the Push pin paragraph at the end of this subsection, co-signed ticket-core)*: `git push --force-with-lease=refs/heads/<branch> origin <head-sha-recorded-before-judge>:refs/heads/<branch>` — judge-process workspace mutation cannot alter the pushed state, and attempt 2+ of the rework loop (a fresh clone re-pushing the stable `agent/ticket-<n>` branch) is not rejected non-fast-forward. Safe: §8.3 makes harvest the branch's only credentialed writer; the lease is taken against the remote-tracking ref fetched at clone time, so it only fails on a concurrent harvest (which correctly errors). Per-attempt branch names are FORBIDDEN (they break §1.7 branch identity and §1.11 merge detection). Judge errors are advisory (recorded, never block push).
- Worktree cleanliness *(2026-07-09, F33)*: immediately after resolving the head sha, the runner checks `git status --porcelain`; ANY output ⇒ the finding is recorded in the evidence payload (proven issue `workspace-dirty`) and the run returns status **fail** (exit 1 → `needs_review`) — no gates run, nothing is judged, nothing is pushed. Rationale: gates verify the working tree while push delivers committed HEAD, so a dirty tree lets green evidence attach to a branch that would fail those gates. A dirty tree is the AGENT's failure, not a platform fault, and is NEVER auto-discarded (`git clean -fdx` would delete gitignored build caches).
- Flight-dir outputs: `events.ndjson` (`step.start/gate.start/gate.result/judge.score/cost.record/push.done/step.end`, §5), `results.json`, `manifest.json` (patch manifest: `{"repo","branch","base_sha","head_sha","commits":[{"sha","author","subject"}],"files":[{"path","added","deleted"}]}`; `push.done.manifest_artifact = "manifest.json"`), `review.json` (evidence payload, §6.4.1).
- Server-side (exec, synchronous before the step returns): `agent_run_metrics` upsert; `agent_reviews` upsert with `ticket_id`/`pipeline_run_id` (§1.10); `agent_cost_ledger` record with `source='harvest_judge'` (fire-and-forget); ticket transition (`running→needs_review` on ok/failed with `branch` set on ok, `running→errored` with `error_detail` on error) — skipped when `ticket_id = 0`.
````

- [ ] In §6.3, after the `Gate` struct's `Timeout` field line (`Timeout string \`json:"timeout,omitempty"\` // per-gate; default 30m`), add the field and stance paragraph:

````markdown
```go
	Retries int    `json:"retries,omitempty"` // 0–2; failed-only re-runs (flake stance below)
```

**Flake/retry stance [DECIDED HERE — harvest-step, agreed with workflow-store as YAML-grammar owner]:** retries are opt-in per gate (`retries: 0..2`, default 0). Only `failed` results are retried — `error` (tooling broke) is never retried, and a gate that exhausts retries keeps `failed`. A gate that passes on a retry is recorded `ok` **with `flaky: true` and `attempt: N`** on its `gate.result` event and in the evidence payload — flakiness is surfaced, never hidden. The §6 workflow YAML `gate_policy.gates[]` entries accept the same optional `retries` key.
````

- [ ] Insert after the §6.4 paragraph (before "## 7."):

````markdown
### 6.4.1 Judge execution + finding conventions — owner: **harvest-step** (addendum, 2026-07-08; sign-off: delivery-outcomes, scorecards, process-intel-experiments)

- The judge is a **single schema-constrained claude CLI call** made by platform code (`agent/harvest/judge.go`, ci-agent `llm.Client` style), funded by the platform credential (§1.13), capped by `JudgeConfig.BudgetUSD` (post-hoc: overage is logged + recorded in the ledger — a single call cannot be pre-metered), working dir = the workspace, prompt = rubric dimensions + guidance + truncated `git diff <base>..<head>`.
- Verdict JSON (the judge's required output): `{"dimensions":[{"name","score" (0–10),"rationale","issues":[{"title","description","file","line"}]}]}` — one entry per rubric dimension, validated by name. Weighted total = `Σ(score·weight)/Σ(weight)`; `pass = total ≥ pass_threshold`. Emitted as the `judge.score` event (§5) with `rubric_hash` = sha256 of the rubric's canonical JSON.
- **Cited issues → findings:** each issue becomes an entry in the evidence payload's `observations` array (existing findings shape, §agent/api/reviews) with `id: "judge-<dimension>-<n>"` and `category: "judge"`. Failing gates become `proven_issues` entries with `id: "gate-<gate>[-<component>]"`, `category: "gate"` (they are objectively proven). Feedback on judge findings is submitted with `finding_type: "judge"` (delivery-outcomes wires the UI), flowing into the existing six-verdict calibration loop unchanged.
- Evidence payload (`review.json`, upserted into `agent_reviews.review`): the existing `ReviewPayload` JSON shape (`schema_version: "harvest/1"`, `metadata{repo, commit, branch, agent_model, duration_seconds}`, `score{value, max, pass}`, `proven_issues`, `observations`, `summary`) **plus** `gates` (GateOutcome array) and `judge` (`{rubric_hash, dimensions, total, max_total, pass}`) keys — consumers ignore unknown keys. `score.value` = judge total when the judge ran, else 10/0 for gates ok/failed; `score.pass` = gates ok AND (no judge OR judge pass OR judge errored).
- `agent_reviews` upsert semantics with linkage (§1.10): `ticket_id`/`pipeline_run_id` update via `COALESCE(EXCLUDED.x, agent_reviews.x)` so a later NULL-linkage CI publish on the same `(build_id, repo, commit_sha)` key never erases linkage.
- Shared-schema delta: `agent/schema.GateResultData` gains additive `attempt`/`flaky` keys (§5 rule: producers may add keys).
````

- [ ] Append to the §11 Amendment log:

```markdown
- 2026-07-08 (harvest-step planning): added §2.8.1 (executable HarvestStep schema: target_branch/dev_mcp/pipeline_run_id additions, agent/harvest.JudgeConfig definition, HARVEST_CONFIG env, SecretMounts main-container-only git-cred seam, exit taxonomy, push-by-sha, flight-dir manifest/review conventions, server-side ingestion duties); §6.3 gains per-gate `retries` + the failed-only/flaky-surfaced stance (agreed with workflow-store); added §6.4.1 (judge execution, verdict JSON, judge/gate finding conventions with finding_type "judge", evidence payload shape, COALESCE linkage upsert, GateResultData attempt/flaky keys). Affects: dispatch, delivery-outcomes, scorecards, process-intel-experiments, workflow-store.
- 2026-07-09 (harvest final-review fixes F30/F33/F34; affects: dispatch, pipeline-runs, agent-step): §2.8.1 gains the additive `HarvestStep.env` field (renderer-emitted §8.1 identity rows; the harvest exec falls back to the `AGENT_PIPELINE_RUN_ID` env row when `pipeline_run_id` is 0 — the F30 co-signed contract's plan-09 leg) and the worktree-cleanliness pin (F33: `git status --porcelain` right after head-sha; dirty ⇒ evidence finding + status fail/exit 1, nothing pushed, no auto-discard); the F32 push pin (already landed at :779) is folded into the merged §2.8.1. Sidecar readiness (F34): harvest-runner reuses agent-runner's 2s/60s `GET /healthz` wait via the shared `agent/devmcp.WaitHealthy` helper before any gate call; never-healthy ⇒ exit 2 (platform error).
```

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agentic): harvest-step contract addendum - terminal-step schema, flake stance, judge mapping"`

---

### Task 2: Migration 1773106080 — `agent_reviews`/`agent_feedback` ticket linkage

**Files:**
- Create: `atc/db/migration/migrations/1773106080_add_ticket_linkage_to_agent_reviews.up.sql`
- Create: `atc/db/migration/migrations/1773106080_add_ticket_linkage_to_agent_reviews.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration` constant)
- Test: `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/`

**Steps:**

- [ ] Write `1773106080_add_ticket_linkage_to_agent_reviews.up.sql` — SQL from shared-contracts §1.10 EXACTLY:

```sql
ALTER TABLE agent_reviews  ADD COLUMN ticket_id INTEGER;        -- NULL = plain CI review (today's rows)
ALTER TABLE agent_reviews  ADD COLUMN pipeline_run_id INTEGER;
ALTER TABLE agent_feedback ADD COLUMN ticket_id INTEGER;

CREATE INDEX agent_reviews_ticket  ON agent_reviews  (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_feedback_ticket ON agent_feedback (ticket_id) WHERE ticket_id IS NOT NULL;
```

- [ ] Write `1773106080_add_ticket_linkage_to_agent_reviews.down.sql`:

```sql
DROP INDEX agent_feedback_ticket;
DROP INDEX agent_reviews_ticket;

ALTER TABLE agent_feedback DROP COLUMN ticket_id;
ALTER TABLE agent_reviews  DROP COLUMN pipeline_run_id;
ALTER TABLE agent_reviews  DROP COLUMN ticket_id;
```

- [ ] Update `atc/db/migration/legacy_upgrade_test.go:37`: set `jetbridgeHeadMigration` to `1773106080` **only if the current value is lower** (wave-mate platform-mcp-hitl owns 1773106070 and may have bumped it first; never lower it — delivery-outcomes' 1773106090 lands next wave).
- [ ] Run `pg_isready && ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` — expect green (the suite migrates an empty and a fixture DB to HEAD; SQL syntax errors or a stale head constant fail here).
- [ ] Commit: `git add atc/db/migration && git commit -m "feat(db): agent_reviews/agent_feedback ticket linkage columns (migration 1773106080)"`

---

### Task 3: `agent/api/reviews` — linkage fields + `ListByTicket`

Extend, don't duplicate: `StoredReview` gains nullable `TicketID`/`PipelineRunID`; the `Store` interface gains `ListByTicket` (delivery-outcomes reads evidence per ticket). The existing HTTP publish path (`ParseSubmission`/`ToStoredReview`) is untouched — its rows keep NULL linkage.

**Files:**
- Modify: `agent/api/reviews/types.go:69` (`StoredReview` struct), `:122` (`Store` interface)
- Modify: `agent/api/reviews/memory_store.go` (add `ListByTicket`)
- Test: `agent/api/reviews/types_test.go`

**Steps:**

- [ ] Add a failing spec to `agent/api/reviews/types_test.go` (the file already exists with a Ginkgo suite; append to the outer `Describe`):

```go
var _ = Describe("ticket linkage", func() {
	It("marshals TicketID and PipelineRunID when set, omits when nil", func() {
		tid, prid := 42, 7
		rec := reviews.StoredReview{BuildID: 1, Repo: "o/r", CommitSha: "abc",
			TicketID: &tid, PipelineRunID: &prid}
		data, err := json.Marshal(rec)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"ticket_id":42`))
		Expect(string(data)).To(ContainSubstring(`"pipeline_run_id":7`))

		bare, err := json.Marshal(reviews.StoredReview{BuildID: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(bare)).NotTo(ContainSubstring("ticket_id"))
		Expect(string(bare)).NotTo(ContainSubstring("pipeline_run_id"))
	})

	It("lists reviews by ticket from the memory store, oldest-first", func() {
		store := reviews.NewMemoryStore()
		tid := 42
		Expect(store.Upsert(&reviews.StoredReview{BuildID: 2, Repo: "o/r", CommitSha: "b", TicketID: &tid})).To(Succeed())
		Expect(store.Upsert(&reviews.StoredReview{BuildID: 1, Repo: "o/r", CommitSha: "a", TicketID: &tid})).To(Succeed())
		Expect(store.Upsert(&reviews.StoredReview{BuildID: 3, Repo: "o/r", CommitSha: "c"})).To(Succeed())

		got, err := store.ListByTicket(42)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].BuildID).To(Equal(1))
		Expect(got[1].BuildID).To(Equal(2))
	})
})
```

- [ ] Run `ginkgo ./agent/api/reviews/` — expect compile failure (`TicketID` undefined, `ListByTicket` missing).
- [ ] In `agent/api/reviews/types.go`, add to `StoredReview` after `Review json.RawMessage` (:86):

```go
	// TicketID / PipelineRunID link harvest-published evidence to a ticket
	// and a pipeline run (shared-contracts §1.10). nil = plain CI review.
	TicketID      *int `json:"ticket_id,omitempty"`
	PipelineRunID *int `json:"pipeline_run_id,omitempty"`
```

  and add to the `Store` interface after `ListByTeam` (:133):

```go
	// ListByTicket returns records linked to the ticket ordered oldest-first
	// (created ascending).
	ListByTicket(ticketID int) ([]StoredReview, error)
```

- [ ] In `agent/api/reviews/memory_store.go`, implement `ListByTicket` following the file's existing iteration/sort style (records sorted by BuildID ascending as the in-memory proxy for created-ascending):

```go
// ListByTicket returns records whose TicketID matches, oldest-first.
func (s *MemoryStore) ListByTicket(ticketID int) ([]StoredReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []StoredReview
	for _, rec := range s.records {
		if rec.TicketID != nil && *rec.TicketID == ticketID {
			out = append(out, *rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuildID < out[j].BuildID })
	return out, nil
}
```

  (Adapt the receiver/field names to the actual `MemoryStore` struct in that file — it stores records keyed by the upsert key; keep its locking discipline.)
- [ ] Run `ginkgo ./agent/api/reviews/` — expect pass.
- [ ] Run `go build ./...` — expect ONE compile break: `atc/db/agent_reviews_factory.go` no longer satisfies `reviews.Store` (missing `ListByTicket`). That is Task 4's job; if anything else breaks, fix the same way. Add a temporary stub is NOT allowed — proceed straight to Task 4 before committing if the build is red, then commit both together, or commit types+memory-store now with: `git add agent/api/reviews && git commit -m "feat(reviews): ticket/run linkage fields and ListByTicket on the Store contract"` (acceptable because `agent/` compiles; the atc break is fixed in the very next task and `go build ./atc/...` gates Task 4's commit).

---

### Task 4: `atc/db` factories — reviews linkage columns + feedback ticket backfill

**Files:**
- Modify: `atc/db/agent_reviews_factory.go:26` (Upsert), `:61` (reviewColumns), `:69`/`:82` region (queries), `:108` (scan) — add `ListByTicket`
- Modify: `atc/db/agent_feedback_factory.go:26` (Save: populate `ticket_id` via subselect)
- Test: `atc/db/agent_reviews_factory_test.go`, `atc/db/agent_feedback_factory_test.go`

**Steps:**

- [ ] Add failing specs to `atc/db/agent_reviews_factory_test.go` (append to its existing `Describe`, using the suite's `dbConn` setup conventions):

```go
	Describe("ticket linkage", func() {
		It("persists ticket_id/pipeline_run_id and lists by ticket oldest-first", func() {
			tid, prid := 42, 7
			err := factory.Upsert(&reviews.StoredReview{
				BuildID: 201, Repo: "o/r", CommitSha: "aaa", TeamName: "main",
				Review: json.RawMessage(`{}`), TicketID: &tid, PipelineRunID: &prid,
			})
			Expect(err).ToNot(HaveOccurred())
			err = factory.Upsert(&reviews.StoredReview{
				BuildID: 202, Repo: "o/r", CommitSha: "bbb", TeamName: "main",
				Review: json.RawMessage(`{}`), TicketID: &tid,
			})
			Expect(err).ToNot(HaveOccurred())

			got, err := factory.ListByTicket(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(2))
			Expect(got[0].BuildID).To(Equal(201))
			Expect(*got[0].TicketID).To(Equal(42))
			Expect(*got[0].PipelineRunID).To(Equal(7))
			Expect(got[1].PipelineRunID).To(BeNil())
		})

		It("preserves linkage when a NULL-linkage upsert hits the same key", func() {
			tid := 42
			Expect(factory.Upsert(&reviews.StoredReview{
				BuildID: 203, Repo: "o/r", CommitSha: "ccc", TeamName: "main",
				Review: json.RawMessage(`{}`), TicketID: &tid,
			})).To(Succeed())
			// same (build_id, repo, commit_sha) key, no linkage — the CI path
			Expect(factory.Upsert(&reviews.StoredReview{
				BuildID: 203, Repo: "o/r", CommitSha: "ccc", TeamName: "main",
				Review: json.RawMessage(`{}`),
			})).To(Succeed())

			got, err := factory.ListByTicket(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(ContainElement(SatisfyAll(
				WithTransform(func(r reviews.StoredReview) int { return r.BuildID }, Equal(203)),
			)))
		})
	})
```

- [ ] Run `ginkgo --focus="ticket linkage" ./atc/db/` — expect compile failure (`ListByTicket` undefined on the factory).
- [ ] In `atc/db/agent_reviews_factory.go`:
  - Extend `Upsert` (:27): add `"ticket_id", "pipeline_run_id"` to `Columns(...)` and `rec.TicketID, rec.PipelineRunID` to `Values(...)`; add to the `ON CONFLICT` suffix (before `updated_at = now()`):

```sql
				ticket_id = COALESCE(EXCLUDED.ticket_id, agent_reviews.ticket_id),
				pipeline_run_id = COALESCE(EXCLUDED.pipeline_run_id, agent_reviews.pipeline_run_id),
```

  - Extend `reviewColumns` (:61): append `, r.ticket_id, r.pipeline_run_id` after the feedback-count subselect.
  - Extend `scanReviewRows` (:113): append `&rec.TicketID, &rec.PipelineRunID` to `dest` (before the conditional payload append) — `*int` scans NULL as nil directly.
  - Add after `ListByTeam` (:106):

```go
func (f *agentReviewsFactory) ListByTicket(ticketID int) ([]reviews.StoredReview, error) {
	rows, err := f.conn.Query(
		`SELECT `+reviewColumns+`
		 FROM agent_reviews r WHERE r.ticket_id = $1 ORDER BY r.created_at ASC, r.id ASC`,
		ticketID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReviewRows(rows, false)
}
```

- [ ] Run `ginkgo --focus="ticket linkage" ./atc/db/` — expect pass. Run `go build ./atc/...` — the Task 3 interface break is now healed.
- [ ] Add a failing spec to `atc/db/agent_feedback_factory_test.go`:

```go
	It("backfills ticket_id from the linked review on Save", func() {
		tid := 42
		Expect(reviewsFactory.Upsert(&reviews.StoredReview{
			BuildID: 301, Repo: "o/r", CommitSha: "ddd", TeamName: "main",
			Review: json.RawMessage(`{}`), TicketID: &tid,
		})).To(Succeed())

		rec := &feedback.StoredFeedback{
			FindingID: "judge-correctness-1", FindingType: "judge",
			Verdict: "accurate", Reviewer: "human",
		}
		rec.ReviewRef.Repo = "o/r"
		rec.ReviewRef.Commit = "ddd"
		Expect(factory.Save(rec)).To(Succeed())

		var got sql.NullInt64
		err := dbConn.QueryRow(
			`SELECT ticket_id FROM agent_feedback WHERE repo = 'o/r' AND commit_sha = 'ddd' AND finding_id = 'judge-correctness-1'`,
		).Scan(&got)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Valid).To(BeTrue())
		Expect(got.Int64).To(Equal(int64(42)))
	})
```

  (Adapt the `StoredFeedback` literal to the exact exported field names in `agent/api/feedback` — `ReviewRef.Repo`/`ReviewRef.Commit` per `agent_feedback_factory.go:36`; construct a `reviewsFactory := db.NewAgentReviewsFactory(dbConn)` in the suite `BeforeEach` if not present.)
- [ ] Run `ginkgo --focus="backfills ticket_id" ./atc/db/` — expect failure (column inserted as NULL).
- [ ] In `atc/db/agent_feedback_factory.go` `Save` (:29): add `"ticket_id"` to `Columns(...)` and to `Values(...)` add:

```go
			sq.Expr(`(SELECT ticket_id FROM agent_reviews
			          WHERE repo = ? AND commit_sha = ?
			          ORDER BY id DESC LIMIT 1)`, rec.ReviewRef.Repo, rec.ReviewRef.Commit),
```

  and to the `ON CONFLICT` suffix add `ticket_id = COALESCE(EXCLUDED.ticket_id, agent_feedback.ticket_id),` before `updated_at = now()`.
- [ ] Run `ginkgo ./atc/db/` — full suite green (~90s; if `database "testdb_template" already exists`, another test process is running — wait for it).
- [ ] Commit: `git add agent/api/reviews atc/db && git commit -m "feat(db): agent_reviews ticket/run linkage + agent_feedback ticket backfill (contracts s1.10)"`

---

### Task 5: `agent/harvest` policy types — GatePolicy, JudgeConfig, Config, secret naming

Stdlib-only types file so `atc` (and fly, transitively) can import it without weight. Also the additive `attempt`/`flaky` keys on `agent/schema.GateResultData`.

**Files:**
- Create: `agent/harvest/policy.go`
- Create: `agent/harvest/harvest_suite_test.go`
- Modify: `agent/schema/event.go` (`GateResultData` — additive fields; agent/schema is the nested module landed by agent-step)
- Test: `agent/harvest/policy_test.go`, `agent/schema/event_test.go`

**Steps:**

- [ ] Write `agent/harvest/harvest_suite_test.go`:

```go
package harvest_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHarvest(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Harvest Suite")
}
```

- [ ] Write the failing test `agent/harvest/policy_test.go`:

```go
package harvest_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/harvest"
)

var _ = Describe("GatePolicy", func() {
	It("round-trips the §6.3 JSON shape including retries", func() {
		raw := `{"gates":[{"gate":"test","scope":"affected_then_full","timeout":"45m","retries":1}],"on_gate_failure":"needs_review"}`
		var p harvest.GatePolicy
		Expect(json.Unmarshal([]byte(raw), &p)).To(Succeed())
		Expect(p.Gates[0].Gate).To(Equal("test"))
		Expect(p.Gates[0].Scope).To(Equal("affected_then_full"))
		Expect(p.Gates[0].Retries).To(Equal(1))
		out, err := json.Marshal(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(MatchJSON(raw))
	})

	It("validates gate names, scopes, retries, timeouts, and failure policy", func() {
		valid := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "build", Scope: "affected"}}}
		Expect(valid.Validate()).To(Succeed())

		Expect(harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "deploy", Scope: "full"}}}.Validate()).
			To(MatchError(ContainSubstring(`unknown gate "deploy"`)))
		Expect(harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "sometimes"}}}.Validate()).
			To(MatchError(ContainSubstring(`unknown scope "sometimes"`)))
		Expect(harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Retries: 3}}}.Validate()).
			To(MatchError(ContainSubstring("retries must be 0-2")))
		Expect(harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Timeout: "bogus"}}}.Validate()).
			To(MatchError(ContainSubstring("invalid timeout")))
		Expect(harvest.GatePolicy{
			Gates:         []harvest.Gate{{Gate: "test", Scope: "full"}},
			OnGateFailure: "explode",
		}.Validate()).To(MatchError(ContainSubstring(`on_gate_failure must be "needs_review"`)))
	})

	It("resolves per-gate timeouts with the 30m default", func() {
		d, err := harvest.Gate{Gate: "test", Scope: "full"}.TimeoutDuration()
		Expect(err).NotTo(HaveOccurred())
		Expect(d.String()).To(Equal("30m0s"))
		d, err = harvest.Gate{Gate: "test", Scope: "full", Timeout: "45m"}.TimeoutDuration()
		Expect(err).NotTo(HaveOccurred())
		Expect(d.String()).To(Equal("45m0s"))
	})
})

var _ = Describe("JudgeConfig", func() {
	It("validates rubric, weights, and threshold", func() {
		valid := harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 3, Guidance: "g"}},
			PassThreshold: 6.5,
		}
		Expect(valid.Validate()).To(Succeed())

		Expect(harvest.JudgeConfig{PassThreshold: 6.5}.Validate()).
			To(MatchError(ContainSubstring("rubric must not be empty")))
		Expect(harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "x", Weight: 0}},
			PassThreshold: 6.5,
		}.Validate()).To(MatchError(ContainSubstring("weight must be positive")))
		Expect(harvest.JudgeConfig{
			Rubric:        []harvest.RubricDimension{{Name: "x", Weight: 1}},
			PassThreshold: 11,
		}.Validate()).To(MatchError(ContainSubstring("pass_threshold must be between 0 and 10")))
	})

	It("hashes the rubric deterministically", func() {
		a := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{{Name: "x", Weight: 1, Guidance: "g"}}}
		b := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{{Name: "x", Weight: 1, Guidance: "g"}}}
		c := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{{Name: "y", Weight: 1, Guidance: "g"}}}
		Expect(a.RubricHash()).To(Equal(b.RubricHash()))
		Expect(a.RubricHash()).NotTo(Equal(c.RubricHash()))
		Expect(a.RubricHash()).To(HaveLen(64))
	})
})

var _ = Describe("GitCredSecretName", func() {
	It("sanitizes the repo slug per contracts §8.3", func() {
		Expect(harvest.GitCredSecretName("tdmtrader/concourse")).
			To(Equal("agent-harvest-git-tdmtrader-concourse"))
		Expect(harvest.GitCredSecretName("Org/Some_Repo.Name")).
			To(Equal("agent-harvest-git-org-some-repo-name"))
	})
})
```

- [ ] Run `ginkgo ./agent/harvest/` — expect compile failure (package does not exist).
- [ ] Write `agent/harvest/policy.go`:

```go
// Package harvest implements the deterministic terminal harvest step:
// gate re-verification via dev-mcp, the rubric judge, branch push, and
// evidence assembly (shared-contracts §2.8.1, §6.3, §6.4.1).
package harvest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Gate names, scopes, and policy constants (shared-contracts §6.3).
const (
	GateBuild = "build"
	GateTest  = "test"
	GateLint  = "lint"

	ScopeAffected         = "affected"
	ScopeFull             = "full"
	ScopeAffectedThenFull = "affected_then_full"

	OnGateFailureNeedsReview = "needs_review"

	DefaultGateTimeout = 30 * time.Minute
	MaxGateRetries     = 2
)

// GatePolicy is the §6.3 gate-policy language, consumed verbatim from the
// workflow definition's gate_policy slot by the renderer and interpreted here.
type GatePolicy struct {
	Gates         []Gate `json:"gates"`
	OnGateFailure string `json:"on_gate_failure,omitempty"` // ""/"needs_review"
}

// Gate is one ordered verification gate.
type Gate struct {
	Gate    string `json:"gate"`              // build | test | lint
	Scope   string `json:"scope"`             // affected | full | affected_then_full
	Focus   string `json:"focus,omitempty"`   // run_tests focus filter
	Timeout string `json:"timeout,omitempty"` // per-gate; default 30m
	Retries int    `json:"retries,omitempty"` // 0-2 failed-only re-runs (§6.3 flake stance)
}

var validGates = map[string]bool{GateBuild: true, GateTest: true, GateLint: true}
var validScopes = map[string]bool{ScopeAffected: true, ScopeFull: true, ScopeAffectedThenFull: true}

// Validate eagerly checks the whole policy (phaseconfig-style).
func (p GatePolicy) Validate() error {
	for i, g := range p.Gates {
		if err := g.Validate(); err != nil {
			return fmt.Errorf("gates[%d]: %w", i, err)
		}
	}
	if p.OnGateFailure != "" && p.OnGateFailure != OnGateFailureNeedsReview {
		return fmt.Errorf(`on_gate_failure must be "needs_review" (got %q)`, p.OnGateFailure)
	}
	return nil
}

// Validate checks a single gate entry.
func (g Gate) Validate() error {
	if !validGates[g.Gate] {
		return fmt.Errorf("unknown gate %q (must be build, test, or lint)", g.Gate)
	}
	if !validScopes[g.Scope] {
		return fmt.Errorf("unknown scope %q (must be affected, full, or affected_then_full)", g.Scope)
	}
	if g.Retries < 0 || g.Retries > MaxGateRetries {
		return fmt.Errorf("retries must be 0-2 (got %d)", g.Retries)
	}
	if _, err := g.TimeoutDuration(); err != nil {
		return err
	}
	return nil
}

// TimeoutDuration resolves the per-gate timeout, defaulting to 30m (§6.3).
func (g Gate) TimeoutDuration() (time.Duration, error) {
	if g.Timeout == "" {
		return DefaultGateTimeout, nil
	}
	d, err := time.ParseDuration(g.Timeout)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", g.Timeout, err)
	}
	return d, nil
}

// RubricDimension is one scored dimension of the judge rubric (§6.4).
type RubricDimension struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Guidance string  `json:"guidance"`
}

// JudgeConfig configures the schema-constrained rubric judge (§2.8.1).
type JudgeConfig struct {
	Rubric        []RubricDimension `json:"rubric"`
	PassThreshold float64           `json:"pass_threshold"`       // 0-10 weighted total
	Model         string            `json:"model,omitempty"`
	BudgetUSD     float64           `json:"budget_usd,omitempty"` // 0 = uncapped
}

// Validate eagerly checks the judge configuration.
func (j JudgeConfig) Validate() error {
	if len(j.Rubric) == 0 {
		return fmt.Errorf("judge rubric must not be empty")
	}
	for i, d := range j.Rubric {
		if d.Name == "" {
			return fmt.Errorf("rubric[%d]: name is required", i)
		}
		if d.Weight <= 0 {
			return fmt.Errorf("rubric[%d] (%s): weight must be positive", i, d.Name)
		}
	}
	if j.PassThreshold < 0 || j.PassThreshold > 10 {
		return fmt.Errorf("pass_threshold must be between 0 and 10 (got %v)", j.PassThreshold)
	}
	return nil
}

// RubricHash is the sha256 hex of the rubric's canonical JSON — the
// judge.score event's rubric_hash (§5, §6.4.1).
func (j JudgeConfig) RubricHash() string {
	canonical, _ := json.Marshal(j.Rubric) // struct order is deterministic
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// Config is the HARVEST_CONFIG env payload the exec step hands the
// harvest-runner (shared-contracts §2.8.1).
type Config struct {
	StepName      string       `json:"step_name"`
	Workspace     string       `json:"workspace"`
	Repo          string       `json:"repo"`
	TargetBranch  string       `json:"target_branch,omitempty"` // default main
	TicketID      int          `json:"ticket_id,omitempty"`
	PipelineRunID int          `json:"pipeline_run_id,omitempty"`
	Branch        string       `json:"branch,omitempty"`
	Push          bool         `json:"push,omitempty"`
	GatePolicy    GatePolicy   `json:"gate_policy"`
	Judge         *JudgeConfig `json:"judge,omitempty"`
}

// EnvConfig is the env var carrying the JSON-encoded Config.
const EnvConfig = "HARVEST_CONFIG"

// GitCredMountPath is where the per-repo git credential secret is
// volume-mounted in the harvest pod (shared-contracts §8.3).
const GitCredMountPath = "/var/run/agent/git"

// PlatformCredentialSecret is the long-lived platform Anthropic credential
// secret name (shared-contracts §8.2); key below.
const (
	PlatformCredentialSecret    = "agent-platform-credential"
	PlatformCredentialSecretKey = "anthropic-token"
)

// GitCredSecretName maps a repo slug to its harvest-only git credential
// secret: agent-harvest-git-<slug-sanitized> (shared-contracts §8.3).
func GitCredSecretName(repo string) string {
	slug := strings.ToLower(repo)
	slug = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, slug)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return "agent-harvest-git-" + strings.Trim(slug, "-")
}
```

- [ ] Run `ginkgo ./agent/harvest/` — expect pass.
- [ ] In `agent/schema/event.go`, add the two additive fields to `GateResultData` (after `Summary`, before `LogArtifact` — additive per §5/§6.4.1):

```go
	Attempt int  `json:"attempt,omitempty"` // 1-based attempt that produced this result
	Flaky   bool `json:"flaky,omitempty"`   // true when ok only after a failed-only retry
```

- [ ] Add a spec to `agent/schema/event_test.go` next to the existing `GateResultData` marshaling coverage:

```go
	It("marshals GateResultData attempt/flaky and omits them at zero", func() {
		data, err := json.Marshal(schema.GateResultData{
			Gate: "test", Component: "atc", Scope: "affected",
			Status: "ok", DurationSeconds: 4.2, Summary: "passed on retry",
			Attempt: 2, Flaky: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"attempt":2`))
		Expect(string(data)).To(ContainSubstring(`"flaky":true`))

		bare, err := json.Marshal(schema.GateResultData{Gate: "test", Scope: "full", Status: "ok"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(bare)).NotTo(ContainSubstring("attempt"))
		Expect(string(bare)).NotTo(ContainSubstring("flaky"))
	})
```

- [ ] Run `cd agent/schema && go test ./... && cd ../..` — expect pass (nested module).
- [ ] Commit: `git add agent/harvest agent/schema && git commit -m "feat(harvest): gate-policy and judge types with flake stance + GateResultData attempt/flaky (contracts s6.3, s2.8.1)"`

---

### Task 6: `atc.HarvestStep` config, `atc.HarvestPlan`, and all visitor implementations

One coherent compile unit, mirroring the landed agent-step recipe: adding `VisitHarvest` to `StepVisitor` forces `StepRecursor`, `StepValidator`, and `planVisitor` updates in the same change. Config shape is addendum §2.8.1 verbatim.

**Files:**
- Modify: `atc/steps.go` (`StepVisitor` interface at :190 region — after agent-step's `VisitAgent`, add `VisitHarvest(*HarvestStep) error`; `StepPrecedence` at :224 — insert the `harvest` detector immediately before the `agent` detector agent-step added before `run` at :253 region; add the `HarvestStep` struct after the landed `AgentStep`)
- Modify: `atc/step_recursor.go` (add `OnHarvest` hook + `VisitHarvest`, after agent-step's `OnAgent`/`VisitAgent`)
- Modify: `atc/step_validator.go` (add `VisitHarvest` after agent-step's `VisitAgent`, which follows `VisitRun` at :233)
- Modify: `atc/plan.go` (`Plan` struct at :3 — add `Harvest *HarvestPlan` after agent-step's `Agent` field; add `HarvestPlan` struct after `AgentPlan`, which follows `RunPlan` at :379)
- Modify: `atc/builds/planner.go` (add `VisitHarvest` after agent-step's `VisitAgent`, which follows `VisitRun` at :83)
- Test: `atc/steps_test.go` (parse cases), `atc/configvalidate/validate_test.go` (validator cases), `atc/builds/planner_test.go` (plan-mapping case)

**Steps:**

- [ ] Add parse test cases to the `factoryTests` table in `atc/steps_test.go` (after the agent-step cases):

```go
	{
		Title: "harvest step",
		ConfigYAML: `
			harvest: verify-and-push
			workspace: workspace
			repo: tdmtrader/concourse
			target_branch: main
			ticket_id: 42
			pipeline_run_id: 7
			branch: agent/ticket-42
			push: true
			env:
			  AGENT_PIPELINE_RUN_ID: ((run_id))
			dev_mcp:
			  name: dev
			  image: ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0
			gate_policy:
			  gates:
			  - gate: build
			    scope: affected
			  - gate: test
			    scope: affected_then_full
			    timeout: 45m
			    retries: 1
			  on_gate_failure: needs_review
			judge:
			  rubric:
			  - name: correctness
			    weight: 3
			    guidance: does it satisfy the spec
			  pass_threshold: 6.5
			timeout: 2h
		`,
		StepConfig: &atc.HarvestStep{
			Name:          "verify-and-push",
			Workspace:     "workspace",
			Repo:          "tdmtrader/concourse",
			TargetBranch:  "main",
			TicketID:      42,
			PipelineRunID: 7,
			Branch:        "agent/ticket-42",
			Push:          true,
			Env:           map[string]string{"AGENT_PIPELINE_RUN_ID": "((run_id))"}, // F30: renderers emit the id as env
			DevMCP: &atc.SidecarSource{
				Config: &atc.SidecarConfig{Name: "dev", Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0"},
			},
			GatePolicy: harvest.GatePolicy{
				Gates: []harvest.Gate{
					{Gate: "build", Scope: "affected"},
					{Gate: "test", Scope: "affected_then_full", Timeout: "45m", Retries: 1},
				},
				OnGateFailure: "needs_review",
			},
			Judge: &harvest.JudgeConfig{
				Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 3, Guidance: "does it satisfy the spec"}},
				PassThreshold: 6.5,
			},
			Timeout: "2h",
		},
	},
	{
		Title: "harvest step, gates only",
		ConfigYAML: `
			harvest: verify
			workspace: workspace
			repo: tdmtrader/concourse
			dev_mcp:
			  name: dev
			  image: ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0
			gate_policy:
			  gates:
			  - gate: test
			    scope: full
		`,
		StepConfig: &atc.HarvestStep{
			Name:      "verify",
			Workspace: "workspace",
			Repo:      "tdmtrader/concourse",
			DevMCP: &atc.SidecarSource{
				Config: &atc.SidecarConfig{Name: "dev", Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0"},
			},
			GatePolicy: harvest.GatePolicy{
				Gates: []harvest.Gate{{Gate: "test", Scope: "full"}},
			},
		},
	},
```

  (Import `"github.com/concourse/concourse/agent/harvest"` in `atc/steps_test.go`.)
- [ ] Run `go test ./atc/ -count=1` — expect compile failure (`atc.HarvestStep` undefined).
- [ ] In `atc/steps.go`:
  - Add to `StepVisitor` (after `VisitAgent`): `VisitHarvest(*HarvestStep) error`
  - Insert into `StepPrecedence` immediately before the `agent` detector:

```go
	{
		Key: "harvest",
		New: func() StepConfig { return &HarvestStep{} },
	},
```

  - Add after the `AgentStep` struct + its `Visit` method (import `"github.com/concourse/concourse/agent/harvest"` at the top of `atc/steps.go`):

```go
// HarvestStep is the deterministic terminal platform step: it re-runs
// verification gates through the repo's dev-mcp, runs the rubric judge,
// pushes agent/ticket-<n> with harvest-only credentials, and updates the
// ticket with evidence (shared-contracts §2.8.1). Like the agent step it
// is fully resolved at render time and never reads workflow tables.
type HarvestStep struct {
	Name          string               `json:"harvest"`
	Workspace     string               `json:"workspace"`
	Repo          string               `json:"repo"`
	TargetBranch  string               `json:"target_branch,omitempty"`
	TicketID      int                  `json:"ticket_id,omitempty"`
	PipelineRunID int                  `json:"pipeline_run_id,omitempty"`
	Branch        string               `json:"branch,omitempty"`
	Push          bool                 `json:"push,omitempty"`
	// Env carries renderer-emitted §8.1 identity/provenance rows (e.g.
	// AGENT_PIPELINE_RUN_ID: ((run_id))), mirroring AgentStep.Env — a Go
	// renderer cannot place ((run_id)) in the int PipelineRunID field
	// (§2.8.1 additive delta, 2026-07-09, F30).
	Env           map[string]string    `json:"env,omitempty"`
	DevMCP        *SidecarSource       `json:"dev_mcp,omitempty"`
	GatePolicy    harvest.GatePolicy   `json:"gate_policy"`
	Judge         *harvest.JudgeConfig `json:"judge,omitempty"`
	Timeout       string               `json:"timeout,omitempty"`
}

func (step *HarvestStep) Visit(v StepVisitor) error {
	return v.VisitHarvest(step)
}
```

- [ ] In `atc/step_recursor.go` add (after `OnAgent` and `VisitAgent`):

```go
	// OnHarvest will be invoked for any *HarvestStep present in the StepConfig.
	OnHarvest func(*HarvestStep) error
```

```go
// VisitHarvest calls the OnHarvest hook if configured.
func (recursor StepRecursor) VisitHarvest(step *HarvestStep) error {
	if recursor.OnHarvest != nil {
		return recursor.OnHarvest(step)
	}

	return nil
}
```

- [ ] In `atc/step_validator.go` add after `VisitAgent`:

```go
func (validator *StepValidator) VisitHarvest(step *HarvestStep) error {
	validator.pushContextf(".harvest(%s)", step.Name)
	defer validator.popContext()

	warning, err := ValidateIdentifier(step.Name, validator.context...)
	if err != nil {
		validator.recordError(err.Error())
	}
	if warning != nil {
		validator.recordWarning(*warning)
	}

	if step.Workspace == "" {
		validator.recordError("must specify `workspace:` (the input artifact containing committed work)")
	}

	if step.Repo == "" {
		validator.recordError("must specify `repo:`")
	}

	if step.Push && step.Branch == "" {
		validator.recordError("`push: true` requires `branch:`")
	}

	if err := step.GatePolicy.Validate(); err != nil {
		validator.recordErrorf("gate_policy: %s", err.Error())
	}

	if len(step.GatePolicy.Gates) > 0 && step.DevMCP == nil {
		validator.recordError("gates are declared but no `dev_mcp:` sidecar is configured")
	}

	if step.Judge != nil {
		if err := step.Judge.Validate(); err != nil {
			validator.recordErrorf("judge: %s", err.Error())
		}
	}

	if step.DevMCP != nil && step.DevMCP.Config != nil {
		validator.pushContext(".dev_mcp")
		if err := step.DevMCP.Config.Validate(); err != nil {
			validator.recordError(err.Error())
		}
		if IsReservedContainerName(step.DevMCP.Config.Name) {
			validator.recordErrorf("reserved container name %q", step.DevMCP.Config.Name)
		}
		validator.popContext()
	}

	return nil
}
```

- [ ] In `atc/plan.go`: add `Harvest *HarvestPlan \`json:"harvest,omitempty"\`` to `Plan` after the `Agent` field, and add after `AgentPlan` (import `"github.com/concourse/concourse/agent/harvest"`):

```go
type HarvestPlan struct {
	Name          string               `json:"name"`
	Workspace     string               `json:"workspace"`
	Repo          string               `json:"repo"`
	TargetBranch  string               `json:"target_branch,omitempty"`
	TicketID      int                  `json:"ticket_id,omitempty"`
	PipelineRunID int                  `json:"pipeline_run_id,omitempty"`
	Branch        string               `json:"branch,omitempty"`
	Push          bool                 `json:"push,omitempty"`
	Env           map[string]string    `json:"env,omitempty"` // §8.1 identity rows (2026-07-09, F30)
	DevMCP        *SidecarSource       `json:"dev_mcp,omitempty"`
	GatePolicy    harvest.GatePolicy   `json:"gate_policy"`
	Judge         *harvest.JudgeConfig `json:"judge,omitempty"`
	Timeout       string               `json:"timeout,omitempty"`
}
```

- [ ] In `atc/builds/planner.go` add after `VisitAgent`:

```go
func (visitor *planVisitor) VisitHarvest(step *atc.HarvestStep) error {
	visitor.plan = visitor.planFactory.NewPlan(atc.HarvestPlan{
		Name:          step.Name,
		Workspace:     step.Workspace,
		Repo:          step.Repo,
		TargetBranch:  step.TargetBranch,
		TicketID:      step.TicketID,
		PipelineRunID: step.PipelineRunID,
		Branch:        step.Branch,
		Push:          step.Push,
		Env:           step.Env,
		DevMCP:        step.DevMCP,
		GatePolicy:    step.GatePolicy,
		Judge:         step.Judge,
		Timeout:       step.Timeout,
	})

	return nil
}
```

- [ ] Run `go build ./atc/...` — expect pass (`grep -rln "VisitAgent" atc/ --include="*.go" | grep -v _test` lists exactly the four visitor files to touch).
- [ ] Run `go test ./atc/ -count=1` — parse cases pass.
- [ ] Add validator cases to `atc/configvalidate/validate_test.go` (mirror the agent-step contexts landed there; the file validates a full `atc.Config` and asserts on `errorMessages`):

```go
	Context("when a harvest step has no workspace", func() {
		BeforeEach(func() {
			job.PlanSequence = append(job.PlanSequence, atc.Step{
				Config: &atc.HarvestStep{Name: "h", Repo: "o/r"},
			})
			config.Jobs = append(config.Jobs, job)
		})

		It("returns an error", func() {
			Expect(errorMessages).To(HaveLen(1))
			Expect(errorMessages[0]).To(ContainSubstring("must specify `workspace:`"))
		})
	})

	Context("when a harvest step declares gates without dev_mcp", func() {
		BeforeEach(func() {
			job.PlanSequence = append(job.PlanSequence, atc.Step{
				Config: &atc.HarvestStep{
					Name: "h", Workspace: "workspace", Repo: "o/r",
					GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full"}}},
				},
			})
			config.Jobs = append(config.Jobs, job)
		})

		It("returns an error", func() {
			Expect(errorMessages).To(HaveLen(1))
			Expect(errorMessages[0]).To(ContainSubstring("no `dev_mcp:` sidecar"))
		})
	})

	Context("when a harvest step pushes without a branch", func() {
		BeforeEach(func() {
			job.PlanSequence = append(job.PlanSequence, atc.Step{
				Config: &atc.HarvestStep{Name: "h", Workspace: "workspace", Repo: "o/r", Push: true},
			})
			config.Jobs = append(config.Jobs, job)
		})

		It("returns an error", func() {
			Expect(errorMessages).To(HaveLen(1))
			Expect(errorMessages[0]).To(ContainSubstring("requires `branch:`"))
		})
	})
```

- [ ] Run `ginkgo ./atc/configvalidate/` — expect pass.
- [ ] Add a planner case to the `atc/builds/planner_test.go` table (after the agent case):

```go
	{
		Title: "harvest step",
		Config: &atc.HarvestStep{
			Name:      "verify-and-push",
			Workspace: "workspace",
			Repo:      "tdmtrader/concourse",
			TicketID:  42,
			Branch:    "agent/ticket-42",
			Push:      true,
			GatePolicy: harvest.GatePolicy{
				Gates: []harvest.Gate{{Gate: "test", Scope: "full"}},
			},
		},
		PlanJSON: `{
			"id": "(unique)",
			"harvest": {
				"name": "verify-and-push",
				"workspace": "workspace",
				"repo": "tdmtrader/concourse",
				"ticket_id": 42,
				"branch": "agent/ticket-42",
				"push": true,
				"gate_policy": {
					"gates": [{"gate": "test", "scope": "full"}]
				}
			}
		}`,
	},
```

- [ ] Run `go test ./atc/builds/ -count=1` — expect pass.
- [ ] Run `ginkgo ./atc/ ./atc/builds/ ./atc/configvalidate/` — expect pass.
- [ ] Commit: `git add atc/steps.go atc/steps_test.go atc/step_recursor.go atc/step_validator.go atc/plan.go atc/builds atc/configvalidate && git commit -m "feat(atc): harvest step config, plan union, validator, and planner (contracts s2.8.1)"`

---

### Task 7: `agent/harvest` workspace git helpers — head/base/diff/manifest/push

Real-git fixture tests (the foundation of the fixture-workspace suite). Push is by-sha so later phases can never alter the pushed state.

> *Amended 2026-07-09 (final review F32/F33):* the push is pinned as `--force-with-lease=refs/heads/<branch>` per the §2.8.1 push addendum (attempt 2+ of the rework loop is a fresh clone re-pushing the stable `agent/ticket-<n>` branch — a plain push is deterministically rejected non-fast-forward). The required divergent-remote-head fixture spec is below. This task also adds the `Porcelain` cleanliness helper Task 10's F33 check consumes. Both behaviors verified against real git 2026-07-09: with no remote-tracking ref the lease permits branch creation; a fresh clone's lease matches the fetched tip, so the re-push force-updates; only a concurrent writer breaks the lease.

**Files:**
- Create: `agent/harvest/workspace.go`
- Test: `agent/harvest/workspace_test.go`

**Steps:**

- [ ] Write the failing test `agent/harvest/workspace_test.go`:

```go
package harvest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/harvest"
)

// git runs a git command in dir, failing the spec on error.
func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=concourse-agent[bot]", "GIT_AUTHOR_EMAIL=agent@concourse.local",
		"GIT_COMMITTER_NAME=concourse-agent[bot]", "GIT_COMMITTER_EMAIL=agent@concourse.local",
	)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// fixtureWorkspace builds: a bare "origin" with main at one commit, cloned
// to a workspace, plus two agent commits on top. Returns (workspace, bare).
func fixtureWorkspace(tmp string) (string, string) {
	bare := filepath.Join(tmp, "origin.git")
	Expect(os.MkdirAll(bare, 0o755)).To(Succeed())
	git(bare, "init", "--bare", "--initial-branch=main")

	seed := filepath.Join(tmp, "seed")
	git(tmp, "clone", bare, seed)
	Expect(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644)).To(Succeed())
	git(seed, "add", ".")
	git(seed, "commit", "-m", "base commit")
	git(seed, "push", "origin", "HEAD:main")

	ws := filepath.Join(tmp, "workspace")
	git(tmp, "clone", bare, ws)
	Expect(os.WriteFile(filepath.Join(ws, "feature.go"), []byte("package f\n"), 0o644)).To(Succeed())
	git(ws, "add", ".")
	git(ws, "commit", "-m", "add feature")
	Expect(os.WriteFile(filepath.Join(ws, "feature.go"), []byte("package f // v2\n"), 0o644)).To(Succeed())
	git(ws, "add", ".")
	git(ws, "commit", "-m", "refine feature")
	return ws, bare
}

var _ = Describe("workspace git helpers", func() {
	var ws, bare string

	BeforeEach(func() {
		ws, bare = fixtureWorkspace(GinkgoT().TempDir())
	})

	It("resolves head, base, changed paths, and diff", func() {
		head, err := harvest.HeadSHA(ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(head).To(HaveLen(40))

		base, err := harvest.BaseSHA(ws, "main")
		Expect(err).NotTo(HaveOccurred())
		Expect(base).NotTo(Equal(head))

		paths, err := harvest.ChangedPaths(ws, base)
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).To(Equal([]string{"feature.go"}))

		diff, err := harvest.Diff(ws, base, 1<<20)
		Expect(err).NotTo(HaveOccurred())
		Expect(diff).To(ContainSubstring("feature.go"))
	})

	It("truncates oversized diffs", func() {
		base, _ := harvest.BaseSHA(ws, "main")
		diff, err := harvest.Diff(ws, base, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(diff)).To(BeNumerically("<=", 10+len(harvest.DiffTruncatedMarker)))
		Expect(diff).To(HaveSuffix(harvest.DiffTruncatedMarker))
	})

	It("builds the patch manifest", func() {
		head, _ := harvest.HeadSHA(ws)
		base, _ := harvest.BaseSHA(ws, "main")
		m, err := harvest.BuildManifest(ws, base, head, "tdmtrader/concourse", "agent/ticket-42")
		Expect(err).NotTo(HaveOccurred())
		Expect(m.Repo).To(Equal("tdmtrader/concourse"))
		Expect(m.Branch).To(Equal("agent/ticket-42"))
		Expect(m.BaseSHA).To(Equal(base))
		Expect(m.HeadSHA).To(Equal(head))
		Expect(m.Commits).To(HaveLen(2))
		Expect(m.Commits[0].Subject).To(Equal("add feature")) // oldest first
		Expect(m.Commits[0].Author).To(Equal("concourse-agent[bot]"))
		Expect(m.Files).To(ConsistOf(harvest.ManifestFile{Path: "feature.go", Added: 1, Deleted: 0}))
	})

	It("pushes the recorded sha to the branch (by-sha, not by-ref)", func() {
		head, _ := harvest.HeadSHA(ws)
		// mutate the worktree AND advance HEAD after recording the sha —
		// push must still deliver exactly `head`.
		Expect(os.WriteFile(filepath.Join(ws, "tampered.txt"), []byte("x"), 0o644)).To(Succeed())
		git(ws, "add", ".")
		git(ws, "commit", "-m", "post-judge tamper")

		Expect(harvest.Push(nil, ws, head, "agent/ticket-42", "", "")).To(Succeed())

		remoteHead := git(bare, "rev-parse", "refs/heads/agent/ticket-42")
		Expect(remoteHead).To(Equal(head))
	})

	It("re-pushes a rework attempt over a divergent remote head (force-with-lease, F32/§2.8.1)", func() {
		head, _ := harvest.HeadSHA(ws)
		Expect(harvest.Push(nil, ws, head, "agent/ticket-42", "", "")).To(Succeed())

		// attempt 2 of the rework loop: a FRESH clone with different
		// commits — a plain push would be rejected non-fast-forward. The
		// clone fetches the branch tip, so the lease matches the remote.
		ws2 := filepath.Join(filepath.Dir(ws), "workspace-attempt-2")
		git(filepath.Dir(ws), "clone", bare, ws2)
		Expect(os.WriteFile(filepath.Join(ws2, "rework.go"), []byte("package r\n"), 0o644)).To(Succeed())
		git(ws2, "add", ".")
		git(ws2, "commit", "-m", "rework after review")
		head2, err := harvest.HeadSHA(ws2)
		Expect(err).NotTo(HaveOccurred())
		Expect(head2).NotTo(Equal(head))

		Expect(harvest.Push(nil, ws2, head2, "agent/ticket-42", "", "")).To(Succeed())
		Expect(git(bare, "rev-parse", "refs/heads/agent/ticket-42")).To(Equal(head2))
	})

	It("reports worktree cleanliness via Porcelain (F33)", func() {
		out, err := harvest.Porcelain(ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(BeEmpty(), "committed fixture must be clean")

		Expect(os.WriteFile(filepath.Join(ws, "uncommitted.txt"), []byte("wip"), 0o644)).To(Succeed())
		out, err = harvest.Porcelain(ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("uncommitted.txt"))
	})

	It("refuses credentials on non-https remotes", func() {
		head, _ := harvest.HeadSHA(ws)
		err := harvest.Push(nil, ws, head, "agent/ticket-42", "bot", "tok")
		Expect(err).To(MatchError(ContainSubstring("https")))
	})
})
```

- [ ] Run `ginkgo --focus="workspace git helpers" ./agent/harvest/` — expect compile failure.
- [ ] Write `agent/harvest/workspace.go`:

```go
package harvest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DiffTruncatedMarker terminates a diff cut at maxBytes.
const DiffTruncatedMarker = "\n[diff truncated]"

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadSHA returns the workspace's HEAD commit.
func HeadSHA(dir string) (string, error) {
	return runGit(nil, dir, "rev-parse", "HEAD")
}

// Porcelain returns `git status --porcelain` output — empty means the
// worktree is clean. Harvest fails fast on a dirty tree (§2.8.1
// cleanliness pin, F33): gates verify the tree while push delivers HEAD,
// so a dirty tree would let green evidence attach to unverified commits.
func Porcelain(dir string) (string, error) {
	return runGit(nil, dir, "status", "--porcelain")
}

// BaseSHA returns the merge-base of HEAD and the target branch, preferring
// origin/<target> and falling back to a local <target> ref (§6.3: the gate
// diff base for affected_components).
func BaseSHA(dir, targetBranch string) (string, error) {
	if targetBranch == "" {
		targetBranch = "main"
	}
	if sha, err := runGit(nil, dir, "merge-base", "HEAD", "origin/"+targetBranch); err == nil {
		return sha, nil
	}
	return runGit(nil, dir, "merge-base", "HEAD", targetBranch)
}

// ChangedPaths lists paths changed between base and HEAD (base is already a
// merge-base, so two-dot equals the §6.3 three-dot semantics).
func ChangedPaths(dir, baseSHA string) ([]string, error) {
	out, err := runGit(nil, dir, "diff", "--name-only", baseSHA+"..HEAD")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Diff returns the base..HEAD patch text, truncated to maxBytes.
func Diff(dir, baseSHA string, maxBytes int) (string, error) {
	out, err := runGit(nil, dir, "diff", baseSHA+"..HEAD")
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(out) > maxBytes {
		return out[:maxBytes] + DiffTruncatedMarker, nil
	}
	return out, nil
}

// Manifest is the patch manifest written to the flight dir (§2.8.1).
type Manifest struct {
	Repo    string           `json:"repo"`
	Branch  string           `json:"branch"`
	BaseSHA string           `json:"base_sha"`
	HeadSHA string           `json:"head_sha"`
	Commits []ManifestCommit `json:"commits"`
	Files   []ManifestFile   `json:"files"`
}

type ManifestCommit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Subject string `json:"subject"`
}

type ManifestFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// BuildManifest assembles the patch manifest for base..head.
func BuildManifest(dir, baseSHA, headSHA, repo, branch string) (*Manifest, error) {
	m := &Manifest{Repo: repo, Branch: branch, BaseSHA: baseSHA, HeadSHA: headSHA}

	logOut, err := runGit(nil, dir, "log", "--reverse", "--format=%H%x1f%an%x1f%s", baseSHA+".."+headSHA)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(logOut, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		m.Commits = append(m.Commits, ManifestCommit{SHA: parts[0], Author: parts[1], Subject: parts[2]})
	}

	statOut, err := runGit(nil, dir, "diff", "--numstat", baseSHA+".."+headSHA)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(statOut, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])   // "-" (binary) parses to 0
		deleted, _ := strconv.Atoi(parts[1])
		m.Files = append(m.Files, ManifestFile{Path: parts[2], Added: added, Deleted: deleted})
	}

	return m, nil
}

// Push delivers exactly `sha` to refs/heads/<branch> on origin (push-by-sha,
// §2.8.1). The push is --force-with-lease against the branch (§2.8.1 push
// addendum, F32): attempt 2+ of the rework loop runs from a fresh clone, so
// a plain push to the existing agent/ticket-<n> head is deterministically
// non-fast-forward; the lease (taken against the remote-tracking ref fetched
// at clone time) keeps a concurrent writer an error. Harvest is the branch's
// only credentialed writer (§8.3). With credentials, the origin remote must
// be http(s); the token is injected via a temp git credential-store file,
// never argv (§8.3).
func Push(ctx context.Context, dir, sha, branch, username, token string) error {
	args := []string{
		"push",
		"--force-with-lease=refs/heads/" + branch,
		"origin",
		sha + ":refs/heads/" + branch,
	}

	if username == "" && token == "" {
		_, err := runGit(ctx, dir, args...)
		return err
	}

	originURL, err := runGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return err
	}
	u, err := url.Parse(originURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("git credentials require an https origin remote (got %q)", originURL)
	}

	credFile, err := os.CreateTemp("", "harvest-git-cred-")
	if err != nil {
		return err
	}
	defer os.Remove(credFile.Name())
	cred := fmt.Sprintf("%s://%s:%s@%s\n", u.Scheme, url.QueryEscape(username), url.QueryEscape(token), u.Host)
	if _, err := credFile.WriteString(cred); err != nil {
		credFile.Close()
		return err
	}
	credFile.Close()

	full := append([]string{
		"-c", "credential.helper=",
		"-c", "credential.helper=store --file=" + filepath.ToSlash(credFile.Name()),
	}, args...)
	_, err = runGit(ctx, dir, full...)
	return err
}
```

- [ ] Run `ginkgo --focus="workspace git helpers" ./agent/harvest/` — expect pass.
- [ ] Commit: `git add agent/harvest && git commit -m "feat(harvest): workspace git helpers - head/base/diff/manifest and push-by-sha"`

---

### Task 8: `agent/harvest` gates engine — dev-mcp invocation, scopes, retries, events

Gates as code through `devmcp.Client` (the only way harvest runs anything). Semantics per §6.3 + the Task 1 flake stance: gates in order; `affected` resolves components once via `AffectedComponents(changedPaths)`; empty affected-set falls back to `full` for that gate; `affected_then_full` runs affected first then the full suite; failed-only retries up to `Retries` with `flaky: true` on a retry pass; the first gate whose final status is `failed` or `error` stops the sequence.

**Files:**
- Create: `agent/harvest/gates.go`
- Test: `agent/harvest/gates_test.go`

**Steps:**

- [ ] Write the failing test `agent/harvest/gates_test.go`:

```go
package harvest_test

import (
	"bytes"
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
	"github.com/concourse/concourse/agent/devmcp/devmcpfakes"
	"github.com/concourse/concourse/agent/harvest"
	schema "github.com/concourse/concourse/agent/schema"
)

func eventTypes(buf *bytes.Buffer) []string {
	var types []string
	r := schema.NewEventReader(bytes.NewReader(buf.Bytes()))
	for {
		e, err := r.Read()
		if err != nil {
			break
		}
		types = append(types, string(e.Type))
	}
	return types
}

var _ = Describe("RunGates", func() {
	var (
		client *devmcpfakes.FakeClient
		buf    *bytes.Buffer
		events *schema.EventWriter
	)

	ok := &devmcp.ToolResult{Status: devmcp.StatusOK, Summary: "passed", DurationSeconds: 1.5}
	failed := &devmcp.ToolResult{Status: devmcp.StatusFailed, Summary: "2 specs failed", DurationSeconds: 2, OutputTail: "FAIL"}

	BeforeEach(func() {
		client = new(devmcpfakes.FakeClient)
		buf = &bytes.Buffer{}
		events = schema.NewEventWriter(buf)
	})

	It("runs affected components per gate and passes overall", func() {
		client.AffectedComponentsReturns([]string{"atc", "fly"}, nil)
		client.BuildReturns(ok, nil)
		client.RunTestsReturns(ok, nil)

		policy := harvest.GatePolicy{Gates: []harvest.Gate{
			{Gate: "build", Scope: "affected"},
			{Gate: "test", Scope: "affected"},
		}}
		outcomes, overall := harvest.RunGates(context.Background(), client, policy, []string{"atc/api/handler.go"}, events)

		Expect(overall).To(Equal("ok"))
		Expect(client.AffectedComponentsCallCount()).To(Equal(1)) // resolved once, reused
		Expect(client.BuildCallCount()).To(Equal(2))              // atc, fly
		Expect(client.RunTestsCallCount()).To(Equal(2))
		Expect(outcomes).To(HaveLen(4))
		Expect(eventTypes(buf)).To(ContainElement("gate.result"))
	})

	It("falls back to full when the affected set is empty", func() {
		client.AffectedComponentsReturns([]string{}, nil)
		client.LintReturns(ok, nil)

		policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "lint", Scope: "affected"}}}
		outcomes, overall := harvest.RunGates(context.Background(), client, policy, []string{"README.md"}, events)

		Expect(overall).To(Equal("ok"))
		_, component := client.LintArgsForCall(0)
		Expect(component).To(Equal("")) // whole repo
		Expect(outcomes[0].Scope).To(Equal("full"))
	})

	It("affected_then_full runs affected first, then the full suite", func() {
		client.AffectedComponentsReturns([]string{"atc"}, nil)
		client.RunTestsReturns(ok, nil)

		policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "affected_then_full"}}}
		outcomes, overall := harvest.RunGates(context.Background(), client, policy, []string{"atc/x.go"}, events)

		Expect(overall).To(Equal("ok"))
		Expect(client.RunTestsCallCount()).To(Equal(2))
		_, c0, _ := client.RunTestsArgsForCall(0)
		_, c1, _ := client.RunTestsArgsForCall(1)
		Expect(c0).To(Equal("atc"))
		Expect(c1).To(Equal(""))
		Expect(outcomes).To(HaveLen(2))
	})

	It("stops at the first failed gate and reports failed", func() {
		client.AffectedComponentsReturns([]string{"atc"}, nil)
		client.BuildReturns(failed, nil)
		client.RunTestsReturns(ok, nil)

		policy := harvest.GatePolicy{Gates: []harvest.Gate{
			{Gate: "build", Scope: "affected"},
			{Gate: "test", Scope: "full"},
		}}
		outcomes, overall := harvest.RunGates(context.Background(), client, policy, []string{"atc/x.go"}, events)

		Expect(overall).To(Equal("failed"))
		Expect(client.RunTestsCallCount()).To(BeZero()) // never reached
		Expect(outcomes).To(HaveLen(1))
		Expect(outcomes[0].Status).To(Equal("failed"))
		Expect(outcomes[0].OutputTail).To(Equal("FAIL"))
	})

	It("retries failed-only gates and marks a retry pass flaky", func() {
		client.RunTestsReturnsOnCall(0, failed, nil)
		client.RunTestsReturnsOnCall(1, ok, nil)

		policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Retries: 1}}}
		outcomes, overall := harvest.RunGates(context.Background(), client, policy, nil, events)

		Expect(overall).To(Equal("ok"))
		Expect(outcomes[0].Status).To(Equal("ok"))
		Expect(outcomes[0].Flaky).To(BeTrue())
		Expect(outcomes[0].Attempts).To(Equal(2))
	})

	It("never retries error results and reports error", func() {
		client.RunTestsReturns(nil, errors.New("connection refused"))

		policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Retries: 2}}}
		outcomes, overall := harvest.RunGates(context.Background(), client, policy, nil, events)

		Expect(overall).To(Equal("error"))
		Expect(client.RunTestsCallCount()).To(Equal(1))
		Expect(outcomes[0].Status).To(Equal("error"))
		Expect(outcomes[0].Summary).To(ContainSubstring("connection refused"))
	})

	It("treats an affected_components failure as a platform error", func() {
		client.AffectedComponentsReturns(nil, errors.New("boom"))
		policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "build", Scope: "affected"}}}
		_, overall := harvest.RunGates(context.Background(), client, policy, []string{"x.go"}, events)
		Expect(overall).To(Equal("error"))
	})
})
```

- [ ] Run `ginkgo --focus="RunGates" ./agent/harvest/` — expect compile failure.
- [ ] Write `agent/harvest/gates.go`:

```go
package harvest

import (
	"context"
	"encoding/json"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
	schema "github.com/concourse/concourse/agent/schema"
)

// GateOutcome is the evidence record for one gate execution (one per
// component for affected scopes; §6.4.1 evidence payload `gates` key).
type GateOutcome struct {
	Gate            string  `json:"gate"`
	Component       string  `json:"component"` // "" = whole repo
	Scope           string  `json:"scope"`     // affected | full (the scope this run actually used)
	Status          string  `json:"status"`    // ok | failed | error
	Attempts        int     `json:"attempts"`
	Flaky           bool    `json:"flaky,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
	Summary         string  `json:"summary"`
	OutputTail      string  `json:"output_tail,omitempty"`
}

// RunGates executes the policy's gates in order through the dev-mcp client
// (§6.3 semantics + the failed-only retry stance). It returns the recorded
// outcomes and the overall status: "ok" when every gate passed, "failed"
// when a gate's final status is failed, "error" when tooling broke.
// Platform faults are folded into the "error" status, never a Go error.
func RunGates(ctx context.Context, client devmcp.Client, policy GatePolicy, changedPaths []string, events *schema.EventWriter) ([]GateOutcome, string) {
	var outcomes []GateOutcome

	// Resolve affected components once, only if some gate needs them.
	var affected []string
	needsAffected := false
	for _, g := range policy.Gates {
		if g.Scope == ScopeAffected || g.Scope == ScopeAffectedThenFull {
			needsAffected = true
		}
	}
	if needsAffected {
		var err error
		affected, err = client.AffectedComponents(ctx, changedPaths)
		if err != nil {
			outcomes = append(outcomes, GateOutcome{
				Gate: "affected_components", Scope: ScopeAffected, Status: string(devmcp.StatusError),
				Attempts: 1, Summary: "affected_components failed: " + err.Error(),
			})
			return outcomes, string(devmcp.StatusError)
		}
	}

	for _, gate := range policy.Gates {
		var runs []gateRun
		switch gate.Scope {
		case ScopeFull:
			runs = []gateRun{{component: "", scope: ScopeFull}}
		case ScopeAffected:
			runs = affectedRuns(affected)
		case ScopeAffectedThenFull:
			if len(affected) == 0 {
				// the empty affected-set already falls back to a full run —
				// never run the full suite twice
				runs = []gateRun{{component: "", scope: ScopeFull}}
			} else {
				runs = append(affectedRuns(affected), gateRun{component: "", scope: ScopeFull})
			}
		}

		gateFinal := string(devmcp.StatusOK)
		for _, r := range runs {
			outcome := runGateOnce(ctx, client, gate, r, events)
			outcomes = append(outcomes, outcome)
			if outcome.Status != string(devmcp.StatusOK) {
				gateFinal = outcome.Status
				break
			}
		}

		if gateFinal != string(devmcp.StatusOK) {
			return outcomes, gateFinal
		}
	}

	return outcomes, string(devmcp.StatusOK)
}

type gateRun struct {
	component string
	scope     string
}

// affectedRuns maps the resolved component set to runs, falling back to a
// single full-repo run when nothing mapped (§6.3).
func affectedRuns(affected []string) []gateRun {
	if len(affected) == 0 {
		return []gateRun{{component: "", scope: ScopeFull}}
	}
	runs := make([]gateRun, len(affected))
	for i, c := range affected {
		runs[i] = gateRun{component: c, scope: ScopeAffected}
	}
	return runs
}

// runGateOnce executes one (gate, component) pair with the per-gate timeout
// and the failed-only retry stance, emitting gate.start/gate.result events.
func runGateOnce(ctx context.Context, client devmcp.Client, gate Gate, r gateRun, events *schema.EventWriter) GateOutcome {
	timeout, err := gate.TimeoutDuration()
	if err != nil {
		timeout = DefaultGateTimeout
	}

	outcome := GateOutcome{Gate: gate.Gate, Component: r.component, Scope: r.scope}

	for attempt := 1; attempt <= gate.Retries+1; attempt++ {
		outcome.Attempts = attempt
		emit(events, schema.EventGateStart, schema.GateStartData{
			Gate: gate.Gate, Component: r.component, Scope: r.scope,
		})

		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		res, callErr := invokeGate(attemptCtx, client, gate, r.component)
		cancel()

		switch {
		case callErr != nil:
			// tooling broke (transport, RPCError, timeout): never retried
			outcome.Status = string(devmcp.StatusError)
			outcome.Summary = gate.Gate + " tooling error: " + callErr.Error()
		default:
			outcome.Status = string(res.Status)
			outcome.Summary = res.Summary
			outcome.DurationSeconds = res.DurationSeconds
			outcome.OutputTail = res.OutputTail
		}

		flaky := outcome.Status == string(devmcp.StatusOK) && attempt > 1
		outcome.Flaky = flaky
		emit(events, schema.EventGateResult, schema.GateResultData{
			Gate: gate.Gate, Component: r.component, Scope: r.scope,
			Status: outcome.Status, DurationSeconds: outcome.DurationSeconds,
			Summary: outcome.Summary, Attempt: attempt, Flaky: flaky,
		})

		if outcome.Status != string(devmcp.StatusFailed) {
			break // ok stops retrying; error is never retried
		}
	}

	return outcome
}

func invokeGate(ctx context.Context, client devmcp.Client, gate Gate, component string) (*devmcp.ToolResult, error) {
	switch gate.Gate {
	case GateBuild:
		return client.Build(ctx, component)
	case GateTest:
		return client.RunTests(ctx, component, gate.Focus)
	default: // GateLint — Validate() guarantees the closed set
		return client.Lint(ctx, component)
	}
}

// emit writes a flight-recorder event, ignoring writer errors (the flight
// recorder must never break the harvest control flow).
func emit(events *schema.EventWriter, t schema.EventType, payload any) {
	if events == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = events.Write(schema.Event{Timestamp: time.Now().UTC().Format(time.RFC3339), Type: t, Data: data})
}
```

  (If the landed `schema.Event` shape differs — e.g. `Data` stayed `map[string]interface{}` instead of the `json.RawMessage` agent-step's Task 4 tests assume, or `Timestamp` is set by `EventWriter.Write` itself — align `emit` and the ingestion `json.Unmarshal(event.Data, …)` calls with `agent/schema/event.go`/`event_writer.go` as actually landed by agent-step; the wire format `{"ts","event","data"}` is the binding contract, and agent-step's own exec ingestion already unmarshals `event.Data` into typed payload structs — follow whatever mechanism it landed with.)
- [ ] Run `ginkgo --focus="RunGates" ./agent/harvest/` — expect pass.
- [ ] Commit: `git add agent/harvest && git commit -m "feat(harvest): gates engine - dev-mcp invocation, scope semantics, failed-only retries with flaky evidence"`

---

### Task 9: `agent/harvest` judge — schema-constrained rubric scoring

One claude CLI call by platform code (ci-agent `llm.ClaudeClient` pattern, envelope parity with `ci-agent/llm/result.go` + `total_cost_usd` fallback). ci-agent is a separate Go module the main module cannot import — the small envelope struct is deliberately reimplemented here, as `agent/runner` did.

**Files:**
- Create: `agent/harvest/judge.go`
- Test: `agent/harvest/judge_test.go`

**Steps:**

- [ ] Write the failing test `agent/harvest/judge_test.go`:

```go
package harvest_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/harvest"
)

// stubClaude writes an executable that emits the given CLI envelope.
func stubClaude(dir, envelope string) string {
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho '" + envelope + "'\n"
	Expect(os.WriteFile(path, []byte(script), 0o755)).To(Succeed())
	return path
}

var _ = Describe("RunJudge", func() {
	cfg := harvest.JudgeConfig{
		Rubric: []harvest.RubricDimension{
			{Name: "correctness", Weight: 3, Guidance: "does it work"},
			{Name: "tests", Weight: 1, Guidance: "are behaviors covered"},
		},
		PassThreshold: 6.5,
		Model:         "claude-sonnet-4-5",
	}

	It("scores the rubric, computes the weighted total, and maps issues", func() {
		dir := GinkgoT().TempDir()
		// verdict: correctness 8 (weight 3), tests 4 (weight 1) → (24+4)/4 = 7.0
		envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"solid\",\"issues\":[]},{\"name\":\"tests\",\"score\":4,\"rationale\":\"thin\",\"issues\":[{\"title\":\"missing edge case\",\"description\":\"no nil test\",\"file\":\"x.go\",\"line\":10}]}]}","model":"claude-sonnet-4-5","total_cost_usd":0.31,"num_turns":1,"is_error":false,"usage":{"input_tokens":900,"output_tokens":120}}`

		res, err := harvest.RunJudge(context.Background(), cfg, harvest.JudgeOpts{
			ClaudePath: stubClaude(dir, envelope),
			WorkDir:    dir,
			Diff:       "diff --git a/x.go b/x.go",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Total).To(BeNumerically("~", 7.0, 1e-9))
		Expect(res.MaxTotal).To(Equal(10.0))
		Expect(res.Pass).To(BeTrue())
		Expect(res.RubricHash).To(Equal(cfg.RubricHash()))
		Expect(res.Dimensions).To(HaveLen(2))
		Expect(res.Dimensions[0].Name).To(Equal("correctness"))
		Expect(res.Dimensions[0].Score).To(Equal(8.0))
		Expect(res.Dimensions[0].Max).To(Equal(10.0))
		Expect(res.Issues).To(HaveLen(1))
		Expect(res.Issues[0].Dimension).To(Equal("tests"))
		Expect(res.Issues[0].Title).To(Equal("missing edge case"))
		Expect(res.CostUSD).To(BeNumerically("~", 0.31, 1e-9))
		Expect(res.Model).To(Equal("claude-sonnet-4-5"))
	})

	It("errors when a rubric dimension is missing from the verdict", func() {
		dir := GinkgoT().TempDir()
		envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"r\",\"issues\":[]}]}","model":"m","cost_usd":0.1,"num_turns":1,"is_error":false,"usage":{}}`
		_, err := harvest.RunJudge(context.Background(), cfg, harvest.JudgeOpts{
			ClaudePath: stubClaude(dir, envelope), WorkDir: dir,
		})
		Expect(err).To(MatchError(ContainSubstring(`missing dimension "tests"`)))
	})

	It("errors when the CLI reports is_error", func() {
		dir := GinkgoT().TempDir()
		envelope := `{"type":"result","subtype":"error_during_execution","result":"\"\"","is_error":true,"usage":{}}`
		_, err := harvest.RunJudge(context.Background(), cfg, harvest.JudgeOpts{
			ClaudePath: stubClaude(dir, envelope), WorkDir: dir,
		})
		Expect(err).To(MatchError(ContainSubstring("judge CLI reported error")))
	})
})
```

- [ ] Run `ginkgo --focus="RunJudge" ./agent/harvest/` — expect compile failure.
- [ ] Write `agent/harvest/judge.go`:

```go
package harvest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

// DefaultJudgeTimeout bounds the single judge CLI call.
const DefaultJudgeTimeout = 10 * time.Minute

// JudgeIssue is one cited issue from a rubric dimension — it becomes a
// finding with id "judge-<dimension>-<n>" and category "judge" (§6.4.1).
type JudgeIssue struct {
	Dimension   string `json:"dimension"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
}

// JudgeResult is the scored verdict.
type JudgeResult struct {
	RubricHash   string                       `json:"rubric_hash"`
	Dimensions   []schema.JudgeScoreDimension `json:"dimensions"`
	Total        float64                      `json:"total"`     // 0-10 weighted
	MaxTotal     float64                      `json:"max_total"` // 10
	Pass         bool                         `json:"pass"`
	Issues       []JudgeIssue                 `json:"issues,omitempty"`
	Model        string                       `json:"model"`
	CostUSD      float64                      `json:"cost_usd"`
	Turns        int                          `json:"turns"`
	InputTokens  int64                        `json:"input_tokens"`
	OutputTokens int64                        `json:"output_tokens"`
}

// JudgeOpts configures a judge invocation.
type JudgeOpts struct {
	ClaudePath string        // default "claude"
	WorkDir    string        // the workspace checkout
	Diff       string        // truncated base..head patch embedded in the prompt
	Timeout    time.Duration // default DefaultJudgeTimeout
}

// judgeEnvelope is the claude CLI --output-format json envelope (parity
// with ci-agent/llm/result.go; total_cost_usd fallback for newer CLIs).
type judgeEnvelope struct {
	Type         string          `json:"type"`
	Result       json.RawMessage `json:"result"`
	Model        string          `json:"model"`
	CostUSD      float64         `json:"cost_usd"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	NumTurns     int             `json:"num_turns"`
	IsError      bool            `json:"is_error"`
	Usage        struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (e judgeEnvelope) cost() float64 {
	if e.TotalCostUSD > 0 {
		return e.TotalCostUSD
	}
	return e.CostUSD
}

type judgeVerdict struct {
	Dimensions []struct {
		Name      string  `json:"name"`
		Score     float64 `json:"score"`
		Rationale string  `json:"rationale"`
		Issues    []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			File        string `json:"file"`
			Line        int    `json:"line"`
		} `json:"issues"`
	} `json:"dimensions"`
}

var judgeJSONBlockRe = regexp.MustCompile("(?s)```json\\s*\\n(.+?)\\n```")

// extractJSON mirrors ci-agent/llm.ExtractJSON: unwrap ```json fences.
func extractJSON(data []byte) json.RawMessage {
	if m := judgeJSONBlockRe.FindSubmatch(data); m != nil {
		return json.RawMessage(m[1])
	}
	return json.RawMessage(data)
}

// RunJudge makes one schema-constrained claude CLI call scoring the rubric
// against the workspace + diff (§6.4.1). Funded by the platform credential:
// CLAUDE_CODE_OAUTH_TOKEN must already be in the process env (§8.2).
func RunJudge(ctx context.Context, cfg JudgeConfig, opts JudgeOpts) (*JudgeResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cli := opts.ClaudePath
	if cli == "" {
		cli = "claude"
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultJudgeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := buildJudgePrompt(cfg, opts.Diff)
	args := []string{"-p", prompt, "--output-format", "json", "--dangerously-skip-permissions"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}

	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Dir = opts.WorkDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("judge timed out: %w", ctx.Err())
		}
		return nil, fmt.Errorf("judge CLI failed (%v): %s", err, strings.TrimSpace(stderr.String()))
	}

	var env judgeEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil || env.Type == "" {
		return nil, fmt.Errorf("judge CLI output is not a result envelope: %.200s", stdout.String())
	}
	if env.IsError {
		return nil, fmt.Errorf("judge CLI reported error: %.500s", string(env.Result))
	}

	raw := env.Result
	if len(raw) > 0 && raw[0] == '"' { // result is a JSON string, unquote then unfence
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = extractJSON([]byte(s))
		}
	}

	var verdict judgeVerdict
	if err := json.Unmarshal(raw, &verdict); err != nil {
		return nil, fmt.Errorf("judge verdict is not valid JSON: %w", err)
	}

	byName := map[string]int{}
	for i, d := range verdict.Dimensions {
		byName[d.Name] = i
	}

	res := &JudgeResult{
		RubricHash:   cfg.RubricHash(),
		MaxTotal:     10,
		Model:        env.Model,
		CostUSD:      env.cost(),
		Turns:        env.NumTurns,
		InputTokens:  env.Usage.InputTokens,
		OutputTokens: env.Usage.OutputTokens,
	}

	var weighted, weightSum float64
	for _, dim := range cfg.Rubric {
		i, found := byName[dim.Name]
		if !found {
			return nil, fmt.Errorf("judge verdict missing dimension %q", dim.Name)
		}
		d := verdict.Dimensions[i]
		score := d.Score
		if score < 0 {
			score = 0
		}
		if score > 10 {
			score = 10
		}
		weighted += score * dim.Weight
		weightSum += dim.Weight
		res.Dimensions = append(res.Dimensions, schema.JudgeScoreDimension{
			Name: dim.Name, Score: score, Max: 10, Rationale: d.Rationale,
		})
		for _, iss := range d.Issues {
			res.Issues = append(res.Issues, JudgeIssue{
				Dimension: dim.Name, Title: iss.Title, Description: iss.Description,
				File: iss.File, Line: iss.Line,
			})
		}
	}
	if weightSum > 0 {
		res.Total = weighted / weightSum
	}
	res.Pass = res.Total >= cfg.PassThreshold

	return res, nil
}

func buildJudgePrompt(cfg JudgeConfig, diff string) string {
	var b strings.Builder
	b.WriteString("You are a strict code-review judge. Score the committed change in this workspace against each rubric dimension from 0 (unacceptable) to 10 (exemplary). You may read files in the working directory to verify claims.\n\nRubric:\n")
	for _, d := range cfg.Rubric {
		fmt.Fprintf(&b, "- %s (weight %g): %s\n", d.Name, d.Weight, d.Guidance)
	}
	b.WriteString("\nRespond with ONLY a JSON object, no prose, exactly this shape:\n")
	b.WriteString(`{"dimensions":[{"name":"<rubric name>","score":<0-10>,"rationale":"<one paragraph>","issues":[{"title":"","description":"","file":"","line":0}]}]}`)
	b.WriteString("\nInclude one dimensions entry per rubric dimension, in order. Cite concrete issues only when you can point at a file.\n\nThe change under review (diff against the target branch):\n\n")
	b.WriteString(diff)
	return b.String()
}
```

- [ ] Run `ginkgo --focus="RunJudge" ./agent/harvest/` — expect pass.
- [ ] Commit: `git add agent/harvest && git commit -m "feat(harvest): schema-constrained rubric judge with weighted scoring and cited-issue findings (contracts s6.4.1)"`

---

### Task 10: `agent/harvest` runner orchestration + `cmd/harvest-runner` + image

The deterministic pod entrypoint: cleanliness → gates → judge → push → evidence, all under the flight recorder, with the §2.8.1 exit taxonomy. Also adds the binary to the agent-runner image.

> *Amended 2026-07-09 (final review F33/F34):* (a) `run()` now checks `git status --porcelain` (Task 7's `Porcelain`) right after resolving the head sha — a dirty tree records a `workspace-dirty` evidence finding and returns **StatusFail** (exit 1 → `needs_review`): nothing gated, nothing judged, nothing pushed, never auto-discarded (§2.8.1 cleanliness pin). (b) Before the first gate call, harvest-runner waits for the dev-mcp sidecar's `GET /healthz` — the same 2s-interval/60s-timeout wait agent-runner does (07 Task 15 step 2) — extracted here into the shared helper `agent/devmcp.WaitHealthy` (additive file in dev-mcp's package; agent-runner swaps its inline wait for this helper, a pure refactor its existing specs already cover). Timeout ⇒ `error` event + StatusError "dev-mcp sidecar never became healthy" ⇒ exit 2 (platform error, not agent failure — matches the frozen exit taxonomy). Sidecars have no readiness probe and RestartPolicyNever; jetbridge execs on pod-Running only, so without this wait a startup race becomes an errored ticket.

**Files:**
- Create: `agent/harvest/harvest.go`
- Create: `agent/devmcp/health.go` (shared sidecar-readiness helper, F34 — consumed here and by agent-runner)
- Create: `cmd/harvest-runner/main.go`
- Modify: `deploy/agent-runner/Dockerfile` (created by agent-step Task 16 — add the harvest-runner build + COPY)
- Test: `agent/harvest/harvest_test.go`, `agent/devmcp/health_test.go`

**Steps:**

- [ ] Write the failing test `agent/harvest/harvest_test.go` (uses Task 7's `fixtureWorkspace`/`git`/Task 9's `stubClaude` helpers, all in package `harvest_test`):

```go
package harvest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
	"github.com/concourse/concourse/agent/devmcp/devmcpfakes"
	"github.com/concourse/concourse/agent/harvest"
	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("harvest.Run", func() {
	var (
		tmp, ws, bare, flight string
		client                *devmcpfakes.FakeClient
		cfg                   harvest.Config
		deps                  harvest.RunDeps
	)

	okRes := &devmcp.ToolResult{Status: devmcp.StatusOK, Summary: "passed", DurationSeconds: 1}

	judgeEnvelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"r\",\"issues\":[{\"title\":\"nit\",\"description\":\"d\",\"file\":\"feature.go\",\"line\":1}]}]}","model":"m1","total_cost_usd":0.2,"num_turns":1,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5}}`

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		ws, bare = fixtureWorkspace(tmp)
		flight = filepath.Join(tmp, "flight")
		Expect(os.MkdirAll(flight, 0o755)).To(Succeed())

		client = new(devmcpfakes.FakeClient)
		client.AffectedComponentsReturns([]string{"app"}, nil)
		client.RunTestsReturns(okRes, nil)

		cfg = harvest.Config{
			StepName: "verify-and-push", Workspace: "workspace",
			Repo: "tdmtrader/concourse", TargetBranch: "main",
			TicketID: 42, Branch: "agent/ticket-42", Push: true,
			GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "affected"}}},
			Judge: &harvest.JudgeConfig{
				Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}},
				PassThreshold: 6.5,
			},
		}
		deps = harvest.RunDeps{
			Client:     client,
			ClaudePath: stubClaude(tmp, judgeEnvelope),
			GitCredDir: filepath.Join(tmp, "no-creds"), // absent dir = file:// push without creds
		}
	})

	readResults := func() schema.Results {
		raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		var r schema.Results
		Expect(json.Unmarshal(raw, &r)).To(Succeed())
		return r
	}

	It("gates pass: judges, pushes the pre-judge head, writes evidence, exits 0", func() {
		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(0))

		head := git(ws, "rev-parse", "HEAD")
		Expect(git(bare, "rev-parse", "refs/heads/agent/ticket-42")).To(Equal(head))

		res := readResults()
		Expect(res.Status).To(Equal(schema.StatusPass))
		Expect(res.Metadata["pushed_branch"]).To(Equal("agent/ticket-42"))
		Expect(res.Metadata["head_sha"]).To(Equal(head))

		var manifest harvest.Manifest
		raw, err := os.ReadFile(filepath.Join(flight, "manifest.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(raw, &manifest)).To(Succeed())
		Expect(manifest.HeadSHA).To(Equal(head))
		Expect(manifest.Commits).To(HaveLen(2))

		evidence, err := os.ReadFile(filepath.Join(flight, "review.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(evidence)).To(ContainSubstring(`"schema_version":"harvest/1"`))
		Expect(string(evidence)).To(ContainSubstring(`"judge-correctness-1"`))
		Expect(string(evidence)).To(ContainSubstring(`"category":"judge"`))

		var types []string
		f, _ := os.Open(filepath.Join(flight, "events.ndjson"))
		r := schema.NewEventReader(f)
		for {
			e, err := r.Read()
			if err != nil {
				break
			}
			types = append(types, string(e.Type))
		}
		f.Close()
		Expect(types[0]).To(Equal(string(schema.EventStepStart)))
		Expect(types).To(ContainElements("gate.start", "gate.result", "judge.score", "cost.record", "push.done"))
		Expect(types[len(types)-1]).To(Equal(string(schema.EventStepEnd)))
	})

	It("gates fail: pushes nothing, writes failing evidence, exits 1", func() {
		client.RunTestsReturns(&devmcp.ToolResult{Status: devmcp.StatusFailed, Summary: "2 failed", OutputTail: "FAIL"}, nil)

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(1))

		out := new(bytes.Buffer)
		lsCmd := git(bare, "for-each-ref", "refs/heads/agent")
		_ = out
		Expect(lsCmd).To(BeEmpty()) // nothing pushed

		res := readResults()
		Expect(res.Status).To(Equal(schema.StatusFail))

		evidence, err := os.ReadFile(filepath.Join(flight, "review.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(evidence)).To(ContainSubstring(`"gate-test"`))
		Expect(string(evidence)).To(ContainSubstring(`"category":"gate"`))
	})

	It("platform error: exits 2 with status error", func() {
		client.AffectedComponentsReturns(nil, context.DeadlineExceeded)

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(2))
		Expect(readResults().Status).To(Equal(schema.StatusError))
	})

	It("dirty workspace: fails fast, runs no gates, pushes nothing (F33/§2.8.1)", func() {
		Expect(os.WriteFile(filepath.Join(ws, "uncommitted.txt"), []byte("wip"), 0o644)).To(Succeed())

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(1)) // agent's failure -> needs_review, NOT a platform error

		By("gates never ran and nothing was pushed")
		Expect(client.RunTestsCallCount()).To(BeZero())
		Expect(git(bare, "for-each-ref", "refs/heads/agent")).To(BeEmpty())

		By("no auto-discard: the uncommitted file is untouched")
		_, statErr := os.Stat(filepath.Join(ws, "uncommitted.txt"))
		Expect(statErr).NotTo(HaveOccurred())

		res := readResults()
		Expect(res.Status).To(Equal(schema.StatusFail))
		Expect(res.Metadata["dirty_workspace"]).To(ContainSubstring("uncommitted.txt"))

		evidence, err := os.ReadFile(filepath.Join(flight, "review.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(evidence)).To(ContainSubstring(`"workspace-dirty"`))
		Expect(string(evidence)).To(ContainSubstring(`"category":"gate"`))
	})

	It("gates with a never-healthy dev-mcp sidecar: exits 2 without a gate call (F34)", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		GinkgoT().Setenv(devmcp.EnvEndpoint, srv.URL+"/mcp")

		deps.Client = nil // production path: client construction is health-gated
		deps.HealthInterval = 10 * time.Millisecond
		deps.HealthTimeout = 100 * time.Millisecond

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(2))

		res := readResults()
		Expect(res.Status).To(Equal(schema.StatusError))
		Expect(res.Summary).To(ContainSubstring("never became healthy"))
		Expect(git(bare, "for-each-ref", "refs/heads/agent")).To(BeEmpty())
	})

	It("judge error is advisory: still pushes and exits 0", func() {
		deps.ClaudePath = filepath.Join(tmp, "missing-claude")

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(0))

		res := readResults()
		Expect(res.Status).To(Equal(schema.StatusPass))
		Expect(res.Metadata["judge_error"]).NotTo(BeEmpty())

		head := git(ws, "rev-parse", "HEAD")
		Expect(git(bare, "rev-parse", "refs/heads/agent/ticket-42")).To(Equal(head))
	})
})
```

- [ ] Run `ginkgo --focus="harvest.Run" ./agent/harvest/` — expect compile failure.
- [ ] Write the failing helper test `agent/devmcp/health_test.go` (F34 — the extracted sidecar-readiness wait, shared with agent-runner):

```go
package devmcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
)

func TestWaitHealthyDelayedHealthy(t *testing.T) {
	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("polled %q, want /healthz", r.URL.Path)
		}
		if atomic.AddInt32(&polls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // still starting
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := devmcp.WaitHealthy(context.Background(), srv.URL+"/mcp", 10*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("WaitHealthy on a delayed-healthy sidecar: %v", err)
	}
	if atomic.LoadInt32(&polls) < 3 {
		t.Fatalf("expected >=3 polls, got %d", polls)
	}
}

func TestWaitHealthyNeverHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := devmcp.WaitHealthy(context.Background(), srv.URL+"/mcp", 10*time.Millisecond, 80*time.Millisecond)
	if err == nil {
		t.Fatal("WaitHealthy must time out on a never-healthy sidecar")
	}
}
```

- [ ] Run `go test ./agent/devmcp/ -run TestWaitHealthy` — expect FAIL (`undefined: devmcp.WaitHealthy`).
- [ ] Write `agent/devmcp/health.go`:

```go
package devmcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Default sidecar-readiness wait (§8.5: every sidecar exposes GET /healthz;
// jetbridge execs on pod-Running only, sidecars have no readiness probe and
// RestartPolicyNever, so runners must wait before the first call).
const (
	DefaultHealthInterval = 2 * time.Second
	DefaultHealthTimeout  = 60 * time.Second
)

// HealthURL derives a sidecar's health endpoint from its MCP endpoint by
// replacing the trailing /mcp with /healthz.
func HealthURL(mcpURL string) string {
	return strings.TrimSuffix(mcpURL, "/mcp") + "/healthz"
}

// WaitHealthy polls GET HealthURL(mcpURL) every interval until it returns
// 200 or timeout elapses. Shared by agent-runner (every declared MCP
// sidecar, 07 Task 15 step 2) and harvest-runner (the dev-mcp sidecar
// before the first gate call, F34). Zero interval/timeout use the defaults.
func WaitHealthy(ctx context.Context, mcpURL string, interval, timeout time.Duration) error {
	if interval <= 0 {
		interval = DefaultHealthInterval
	}
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: interval}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, HealthURL(mcpURL), nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sidecar at %s never became healthy within %s", mcpURL, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
```

- [ ] Run `go test ./agent/devmcp/ -run TestWaitHealthy` — expect PASS.
- [ ] Cross-plan note (agent-step, 07 Task 15 step 2): agent-runner's inline per-sidecar `/healthz` wait becomes a call to `devmcp.WaitHealthy(ctx, url, 0, 0)` — a pure refactor; its existing runner specs keep passing unchanged.
- [ ] Write `agent/harvest/harvest.go`:

```go
package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
	schema "github.com/concourse/concourse/agent/schema"
)

// Exit codes (shared-contracts §2.8.1).
const (
	ExitGatesPassed   = 0
	ExitGatesFailed   = 1
	ExitPlatformError = 2
)

// RunDeps are the runner's injectable dependencies.
type RunDeps struct {
	Client     devmcp.Client // nil → health-gated devmcp.NewClient(os.Getenv(devmcp.EnvEndpoint)) when gates exist
	ClaudePath string        // "" → "claude"
	GitCredDir string        // "" → GitCredMountPath; username/token files (§8.3)
	Stdout     io.Writer     // "" → os.Stdout (build log)

	// Sidecar-readiness wait (F34): zero values use devmcp's 2s/60s
	// defaults; only exercised on the nil-Client production path.
	HealthInterval time.Duration
	HealthTimeout  time.Duration
}

// FromEnv assembles (Config, flightDir, workDir) from the §2.8.1 env contract.
func FromEnv() (Config, string, string, error) {
	var cfg Config
	raw := os.Getenv(EnvConfig)
	if raw == "" {
		return cfg, "", "", fmt.Errorf("%s is not set", EnvConfig)
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, "", "", fmt.Errorf("parsing %s: %w", EnvConfig, err)
	}
	flightDir := os.Getenv("AGENT_FLIGHT_DIR")
	if flightDir == "" {
		return cfg, "", "", fmt.Errorf("AGENT_FLIGHT_DIR is not set")
	}
	workDir, err := os.Getwd()
	if err != nil {
		return cfg, "", "", err
	}
	return cfg, flightDir, workDir, nil
}

// Run executes the harvest sequence: cleanliness check → gates → judge
// (advisory) → push-by-sha → evidence, all recorded to the flight dir.
// workDir is the step working directory; the workspace checkout is
// workDir/<cfg.Workspace>.
func Run(ctx context.Context, cfg Config, flightDir, workDir string, deps RunDeps) (int, error) {
	start := time.Now()
	stdout := deps.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	eventsFile, err := os.Create(filepath.Join(flightDir, "events.ndjson"))
	if err != nil {
		return ExitPlatformError, err
	}
	defer eventsFile.Close()
	events := schema.NewEventWriter(eventsFile)

	emit(events, schema.EventStepStart, schema.StepStartData{StepName: cfg.StepName})

	status, meta, evidence := run(ctx, cfg, flightDir, workDir, deps, events, stdout)

	summary := evidence.Summary
	results := schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    1,
		Summary:       summary,
		Artifacts:     []schema.Artifact{},
		Metadata:      meta,
	}
	writeJSON(filepath.Join(flightDir, "results.json"), results)
	writeJSON(filepath.Join(flightDir, "review.json"), evidence)

	threeWay, _ := schema.ThreeWayStatus(status)
	emit(events, schema.EventStepEnd, schema.StepEndData{
		StepName: cfg.StepName, Status: threeWay, Summary: summary,
		WallTimeSeconds: int(time.Since(start).Seconds()),
		CostUSD:         evidenceJudgeCost(evidence), Turns: evidenceJudgeTurns(evidence),
	})

	switch status {
	case schema.StatusPass:
		return ExitGatesPassed, nil
	case schema.StatusFail:
		return ExitGatesFailed, nil
	default:
		return ExitPlatformError, nil
	}
}

// run does the actual sequence and assembles evidence; it never returns an
// error — faults become status error with a summary.
func run(ctx context.Context, cfg Config, flightDir, workDir string, deps RunDeps, events *schema.EventWriter, stdout io.Writer) (schema.Status, map[string]any, *Evidence) {
	meta := map[string]any{}
	evidence := NewEvidence(cfg.Repo)

	ws := filepath.Join(workDir, cfg.Workspace)

	headSHA, err := HeadSHA(ws)
	if err != nil {
		evidence.Summary = "workspace is not a git checkout: " + err.Error()
		return schema.StatusError, meta, evidence
	}

	// --- cleanliness (§2.8.1 pin, F33): gates verify the tree, push
	// delivers HEAD — a dirty tree fails fast BEFORE anything is verified.
	// The agent's failure (exit 1 -> needs_review), never auto-discarded.
	dirty, err := Porcelain(ws)
	if err != nil {
		evidence.Summary = "cannot check workspace cleanliness: " + err.Error()
		return schema.StatusError, meta, evidence
	}
	if dirty != "" {
		meta["head_sha"] = headSHA
		meta["dirty_workspace"] = dirty
		evidence.Metadata.Commit = headSHA
		evidence.ProvenIssues = append(evidence.ProvenIssues, EvidenceFinding{
			ID: "workspace-dirty", Severity: "high",
			Title:       "workspace has uncommitted changes",
			Description: "gates verify the working tree but push delivers committed HEAD; nothing was verified, judged, or pushed. The agent must commit its work.\n\n" + dirty,
			Category:    "gate",
		})
		evidence.Score(0, false)
		evidence.Summary = "workspace has uncommitted changes — nothing verified, nothing pushed"
		return schema.StatusFail, meta, evidence
	}

	baseSHA, err := BaseSHA(ws, cfg.TargetBranch)
	if err != nil {
		evidence.Summary = "cannot resolve target-branch base: " + err.Error()
		return schema.StatusError, meta, evidence
	}
	meta["head_sha"] = headSHA
	meta["base_sha"] = baseSHA
	evidence.Metadata.Commit = headSHA

	changed, err := ChangedPaths(ws, baseSHA)
	if err != nil {
		evidence.Summary = "cannot compute changed paths: " + err.Error()
		return schema.StatusError, meta, evidence
	}

	// --- gates ---
	overall := string(devmcp.StatusOK)
	if len(cfg.GatePolicy.Gates) > 0 {
		client := deps.Client
		if client == nil {
			// Readiness wait before the FIRST gate call (F34): sidecars
			// have no readiness probe and RestartPolicyNever — a startup
			// race must be a bounded wait, not an errored ticket. Same
			// 2s/60s wait agent-runner does (shared devmcp.WaitHealthy).
			endpoint := os.Getenv(devmcp.EnvEndpoint)
			if werr := devmcp.WaitHealthy(ctx, endpoint, deps.HealthInterval, deps.HealthTimeout); werr != nil {
				evidence.Summary = "dev-mcp sidecar never became healthy: " + werr.Error()
				emit(events, schema.EventError, map[string]string{"message": evidence.Summary})
				evidence.Score(0, false)
				return schema.StatusError, meta, evidence
			}
			client = devmcp.NewClient(endpoint, devmcp.WithProgress(func(tool, msg string) {
				fmt.Fprintf(stdout, "dev-mcp %s: %s\n", tool, msg)
			}))
		}
		var outcomes []GateOutcome
		outcomes, overall = RunGates(ctx, client, cfg.GatePolicy, changed, events)
		evidence.Gates = outcomes
		meta["gates"] = outcomes
		evidence.AddGateFindings(outcomes)
	}

	if overall == string(devmcp.StatusError) {
		evidence.Summary = "gate tooling error — nothing verified, nothing pushed"
		evidence.Score(0, false)
		return schema.StatusError, meta, evidence
	}

	gatesOK := overall == string(devmcp.StatusOK)

	// --- judge (advisory; runs only when gates passed, §6.4.1) ---
	judgePass := true
	if gatesOK && cfg.Judge != nil {
		diff, _ := Diff(ws, baseSHA, 200<<10)
		res, jerr := RunJudge(ctx, *cfg.Judge, JudgeOpts{
			ClaudePath: deps.ClaudePath, WorkDir: ws, Diff: diff,
		})
		if jerr != nil {
			meta["judge_error"] = jerr.Error()
			emit(events, schema.EventError, map[string]string{"message": "judge failed: " + jerr.Error()})
		} else {
			judgePass = res.Pass
			meta["judge"] = res
			evidence.SetJudge(res)
			emit(events, schema.EventJudgeScore, schema.JudgeScoreData{
				RubricHash: res.RubricHash, Dimensions: res.Dimensions,
				Total: res.Total, MaxTotal: res.MaxTotal, Model: res.Model, CostUSD: res.CostUSD,
			})
			emit(events, schema.EventCostRecord, schema.CostRecordData{
				Source: "harvest_judge", Provider: "anthropic", Model: res.Model,
				InputTokens: res.InputTokens, OutputTokens: res.OutputTokens,
				Turns: res.Turns, CostUSD: res.CostUSD,
			})
		}
	}

	// --- push-by-sha (gates are the only hard gate) ---
	if gatesOK && cfg.Push && cfg.Branch != "" {
		manifest, merr := BuildManifest(ws, baseSHA, headSHA, cfg.Repo, cfg.Branch)
		if merr != nil {
			evidence.Summary = "manifest build failed: " + merr.Error()
			return schema.StatusError, meta, evidence
		}
		writeJSON(filepath.Join(flightDir, "manifest.json"), manifest)

		username, token := readGitCreds(deps.GitCredDir)
		if perr := Push(ctx, ws, headSHA, cfg.Branch, username, token); perr != nil {
			evidence.Summary = "push failed: " + perr.Error()
			return schema.StatusError, meta, evidence
		}
		meta["pushed_branch"] = cfg.Branch
		evidence.Metadata.Branch = cfg.Branch
		emit(events, schema.EventPushDone, schema.PushDoneData{
			Branch: cfg.Branch, Sha: headSHA, ManifestArtifact: "manifest.json",
		})
	}

	if gatesOK {
		evidence.Score(evidenceTotalOr(evidence, 10), judgePass || evidence.Judge == nil || meta["judge_error"] != nil)
		evidence.Summary = summarize(cfg, evidence, meta)
		return schema.StatusPass, meta, evidence
	}

	evidence.Score(evidenceTotalOr(evidence, 0), false)
	evidence.Summary = summarize(cfg, evidence, meta)
	return schema.StatusFail, meta, evidence
}

func readGitCreds(dir string) (string, string) {
	if dir == "" {
		dir = GitCredMountPath
	}
	u, err1 := os.ReadFile(filepath.Join(dir, "username"))
	t, err2 := os.ReadFile(filepath.Join(dir, "token"))
	if err1 != nil || err2 != nil {
		return "", "" // no mounted creds — push falls back to the remote as-is
	}
	return strings.TrimSpace(string(u)), strings.TrimSpace(string(t))
}

func writeJSON(path string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func summarize(cfg Config, e *Evidence, meta map[string]any) string {
	var parts []string
	if len(e.Gates) > 0 {
		failing := 0
		for _, g := range e.Gates {
			if g.Status != "ok" {
				failing++
			}
		}
		if failing == 0 {
			parts = append(parts, fmt.Sprintf("%d gate run(s) passed", len(e.Gates)))
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d gate run(s) failed", failing, len(e.Gates)))
		}
	}
	if e.Judge != nil {
		parts = append(parts, fmt.Sprintf("judge %.1f/10", e.Judge.Total))
	}
	if b, ok := meta["pushed_branch"].(string); ok {
		parts = append(parts, "pushed "+b)
	}
	if len(parts) == 0 {
		return "harvest completed"
	}
	return strings.Join(parts, "; ")
}

func evidenceTotalOr(e *Evidence, fallback float64) float64 {
	if e.Judge != nil {
		return e.Judge.Total
	}
	return fallback
}

func evidenceJudgeCost(e *Evidence) float64 {
	if e.Judge == nil {
		return 0
	}
	return e.judgeCost
}

func evidenceJudgeTurns(e *Evidence) int {
	if e.Judge == nil {
		return 0
	}
	return e.judgeTurns
}
```

- [ ] Add `agent/harvest/evidence.go` in the same step (the `review.json` payload, §6.4.1 — existing `ReviewPayload` JSON shape plus `gates`/`judge` keys):

```go
package harvest

import "fmt"

// EvidenceFinding matches the existing agent-review findings shape
// (agent/api/reviews/handler.go Finding) so judge/gate findings are
// feedback-eligible in the existing UI (§6.4.1).
type EvidenceFinding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Category    string `json:"category"`
}

// EvidenceMetadata mirrors ReviewPayload.Metadata keys.
type EvidenceMetadata struct {
	Repo            string `json:"repo"`
	Commit          string `json:"commit"`
	Branch          string `json:"branch"`
	AgentModel      string `json:"agent_model"`
	DurationSeconds int    `json:"duration_seconds"`
}

// EvidenceScore mirrors ReviewPayload.Score keys.
type EvidenceScore struct {
	Value float64 `json:"value"`
	Max   float64 `json:"max"`
	Pass  bool    `json:"pass"`
}

// EvidenceJudge is the judge section of the evidence payload.
type EvidenceJudge struct {
	RubricHash string  `json:"rubric_hash"`
	Total      float64 `json:"total"`
	MaxTotal   float64 `json:"max_total"`
	Pass       bool    `json:"pass"`
}

// Evidence is the review.json payload (§6.4.1): parseable as the existing
// ReviewPayload (metadata/score/proven_issues/observations/summary) with
// additive gates/judge keys.
type Evidence struct {
	SchemaVersion string            `json:"schema_version"` // "harvest/1"
	Metadata      EvidenceMetadata  `json:"metadata"`
	ScoreField    EvidenceScore     `json:"score"`
	ProvenIssues  []EvidenceFinding `json:"proven_issues"`
	Observations  []EvidenceFinding `json:"observations"`
	Summary       string            `json:"summary"`
	Gates         []GateOutcome     `json:"gates,omitempty"`
	Judge         *EvidenceJudge    `json:"judge,omitempty"`

	judgeCost  float64
	judgeTurns int
}

// NewEvidence returns an empty payload for the repo.
func NewEvidence(repo string) *Evidence {
	return &Evidence{
		SchemaVersion: "harvest/1",
		Metadata:      EvidenceMetadata{Repo: repo},
		ScoreField:    EvidenceScore{Max: 10},
		ProvenIssues:  []EvidenceFinding{},
		Observations:  []EvidenceFinding{},
	}
}

// AddGateFindings converts non-ok gate outcomes to proven issues
// (id gate-<gate>[-<component>], category "gate" — objectively proven).
func (e *Evidence) AddGateFindings(outcomes []GateOutcome) {
	for _, g := range outcomes {
		if g.Status == "ok" {
			continue
		}
		id := "gate-" + g.Gate
		if g.Component != "" {
			id += "-" + g.Component
		}
		e.ProvenIssues = append(e.ProvenIssues, EvidenceFinding{
			ID: id, Severity: "high",
			Title:       fmt.Sprintf("%s gate %s", g.Gate, g.Status),
			Description: g.Summary + "\n\n" + g.OutputTail,
			Category:    "gate",
		})
	}
}

// SetJudge records the judge verdict: dimensions land in the judge section,
// cited issues become observations (id judge-<dimension>-<n>, category
// "judge"; feedback uses finding_type "judge", §6.4.1).
func (e *Evidence) SetJudge(res *JudgeResult) {
	e.Judge = &EvidenceJudge{
		RubricHash: res.RubricHash, Total: res.Total, MaxTotal: res.MaxTotal, Pass: res.Pass,
	}
	e.Metadata.AgentModel = res.Model
	e.judgeCost = res.CostUSD
	e.judgeTurns = res.Turns
	perDim := map[string]int{}
	for _, iss := range res.Issues {
		perDim[iss.Dimension]++
		e.Observations = append(e.Observations, EvidenceFinding{
			ID:          fmt.Sprintf("judge-%s-%d", iss.Dimension, perDim[iss.Dimension]),
			Title:       iss.Title,
			Description: iss.Description,
			File:        iss.File,
			Line:        iss.Line,
			Category:    "judge",
		})
	}
}

// Score fixes the payload's score section (§6.4.1 semantics decided by Run).
func (e *Evidence) Score(value float64, pass bool) {
	e.ScoreField = EvidenceScore{Value: value, Max: 10, Pass: pass}
}
```

  Note: `Evidence.ScoreField` marshals under the JSON key `"score"` while the Go method `Score` sets it — Go allows the field/method name split because the field is `ScoreField`.
- [ ] Run `ginkgo ./agent/harvest/` — iterate until the six `harvest.Run` specs pass (the judge-error advisory case, the pass/fail exit codes, push-by-sha, the dirty-workspace fail-fast, the never-healthy sidecar exit 2, and evidence keys are the load-bearing assertions).
- [ ] Write `cmd/harvest-runner/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/concourse/concourse/agent/harvest"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, flightDir, workDir, err := harvest.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harvest-runner: %v\n", err)
		os.Exit(harvest.ExitPlatformError)
	}

	exit, err := harvest.Run(ctx, cfg, flightDir, workDir, harvest.RunDeps{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harvest-runner: %v\n", err)
		if exit == 0 {
			exit = harvest.ExitPlatformError
		}
	}
	os.Exit(exit)
}
```

- [ ] Run `go build ./cmd/harvest-runner ./agent/harvest/...` — expect pass.
- [ ] In `deploy/agent-runner/Dockerfile` (agent-step Task 16), extend the build stage's `go build` to also produce the harvest binary and COPY it:

```dockerfile
RUN CGO_ENABLED=0 go build -o /out/agent-runner ./cmd/agent-runner \
 && CGO_ENABLED=0 go build -o /out/harvest-runner ./cmd/harvest-runner
```

```dockerfile
COPY --from=build /out/harvest-runner /usr/local/bin/harvest-runner
```

  (Replace the existing single-binary `RUN`/add the second `COPY` next to the agent-runner one; the runtime stage already has git + the claude CLI — everything harvest needs. *Amended 2026-07-09, F25/§8.5 scoping:* the shared runner image runs as **root** with `ENV IS_SANDBOX=1` per 07 Task 16 as amended — jetbridge hostPath step volumes are kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a non-root harvest-runner would EACCES writing the flight recorder. No separate image change lands in this plan; harvest-runner inherits it.)
- [ ] Commit: `git add agent/harvest agent/devmcp cmd/harvest-runner deploy/agent-runner && git commit -m "feat(harvest): runner orchestration, evidence payload, cleanliness+readiness guards, harvest-runner binary in the agent-runner image"`

---

### Task 11: `runtime.ContainerSpec.SecretMounts` + jetbridge main-container-only mounting

The §8.3 seam: secret volumes mounted read-only on the MAIN container only. Today `buildSidecarContainers` (atc/worker/jetbridge/container.go:510) copies the main container's mount list to every sidecar — so the git-cred mount must be appended AFTER the sidecar containers are built.

> *Scope narrowed 2026-07-09 (final review F20 / runtime-seams package):* this task is the **SecretMounts seam ONLY**. The `applySecretRefs` APPEND change (secretKeyRef-only EnvVars for `SecretEnv` keys with no literal counterpart — how the judge's `CLAUDE_CODE_OAUTH_TOKEN` reaches the harvest pod) is **removed from this task's scope**: it lands in wave 2 as part of agent-step **07 Task 11B "jetbridge runtime seams"**, with its own fake-clientset spec. 07 Task 11B is a stated **prerequisite** of this task. Two consequences here: (a) `buildSidecarContainers` now takes FIVE args — `buildSidecarContainers(sidecars, mainMounts, defaultDir, sidecarEnv, sidecarSecretEnv)` — so the `buildPod` insertion point below anchors on the five-arg call; (b) `applySecretRefs(env, secretEnv)` already RETURNS the (possibly grown) slice — do not re-implement either behavior here, and do not add any literal-placeholder workaround (forbidden by §8.2).

**Files:**
- Modify: `atc/runtime/types.go:143` region (after `SecretEnv`)
- Modify: `atc/worker/jetbridge/container.go:380` region (`buildPod` — append secret volumes + main-container mounts after `buildSidecarContainers` is called at :443)
- Test: `atc/worker/jetbridge/container_test.go`

**Steps:**

- [ ] Add a failing spec to `atc/worker/jetbridge/container_test.go`, copying the one-sidecar context scaffolding at :2790 (fake clientset, `setupFakeDBContainer`, `worker.FindOrCreateContainer`):

```go
	Context("when secret mounts are configured", func() {
		BeforeEach(func() {
			setupFakeDBContainer(fakeDBWorker, "secret-mount-handle")

			var err error
			container, _, err = worker.FindOrCreateContainer(
				ctx,
				db.NewFixedHandleContainerOwner("secret-mount-handle"),
				db.ContainerMetadata{Type: db.ContainerTypeTask},
				runtime.ContainerSpec{
					TeamID:    1,
					Dir:       "/tmp/build/workdir",
					ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					SecretMounts: []runtime.SecretMount{
						{SecretName: "agent-harvest-git-tdmtrader-concourse", MountPath: "/var/run/agent/git"},
					},
					Sidecars: []atc.SidecarConfig{
						{Name: "dev", Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0"},
					},
				},
				delegate,
			)
			Expect(err).ToNot(HaveOccurred())
		})

		It("mounts the secret read-only on the main container ONLY", func() {
			_, err := container.Run(ctx, runtime.ProcessSpec{
				Path: "/bin/sh", Args: []string{"-c", "true"},
			}, runtime.ProcessIO{})
			Expect(err).ToNot(HaveOccurred())

			pods, err := fakeClientset.CoreV1().Pods("test-namespace").List(ctx, metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred())
			pod := pods.Items[0]

			By("declaring a pod volume backed by the secret")
			var secretVolume *corev1.Volume
			for i := range pod.Spec.Volumes {
				v := &pod.Spec.Volumes[i]
				if v.Secret != nil && v.Secret.SecretName == "agent-harvest-git-tdmtrader-concourse" {
					secretVolume = v
				}
			}
			Expect(secretVolume).ToNot(BeNil())

			By("mounting it read-only at the requested path on main")
			main := pod.Spec.Containers[0]
			Expect(main.Name).To(Equal("main"))
			var mount *corev1.VolumeMount
			for i := range main.VolumeMounts {
				if main.VolumeMounts[i].Name == secretVolume.Name {
					mount = &main.VolumeMounts[i]
				}
			}
			Expect(mount).ToNot(BeNil())
			Expect(mount.MountPath).To(Equal("/var/run/agent/git"))
			Expect(mount.ReadOnly).To(BeTrue())

			By("NOT mounting it on any sidecar")
			for _, c := range pod.Spec.Containers[1:] {
				for _, m := range c.VolumeMounts {
					Expect(m.Name).ToNot(Equal(secretVolume.Name),
						"sidecar %s must not see the secret mount", c.Name)
				}
			}
		})
	})
```

- [ ] Run `ginkgo --focus="secret mounts" ./atc/worker/jetbridge/` — expect compile failure (`runtime.SecretMount` undefined).
- [ ] In `atc/runtime/types.go`, after `SecretEnv` (:143):

```go
	// SecretMounts lists K8s Secrets to volume-mount read-only into the
	// MAIN container only — sidecars never receive these mounts. Used for
	// the harvest step's git credentials (shared-contracts §8.3).
	SecretMounts []SecretMount
```

  and next to the other spec types:

```go
// SecretMount is a read-only, main-container-only K8s Secret volume mount.
type SecretMount struct {
	// SecretName is the K8s Secret to mount.
	SecretName string
	// MountPath is the absolute container path (each secret key = one file).
	MountPath string
}
```

- [ ] In `atc/worker/jetbridge/container.go` `buildPod`, AFTER the sidecar append — since 07 Task 11B the five-arg call `containers = append(containers, buildSidecarContainers(c.containerSpec.Sidecars, volumeMounts, dir, c.containerSpec.SidecarEnv, c.containerSpec.SidecarSecretEnv)...)` (:443 region) — insert:

```go
	// Secret volume mounts go on the MAIN container only (contracts §8.3):
	// appended after sidecar construction so sidecars never inherit them.
	for i, sm := range c.containerSpec.SecretMounts {
		volName := fmt.Sprintf("secret-mount-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: sm.SecretName},
			},
		})
		containers[0].VolumeMounts = append(containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: sm.MountPath,
			ReadOnly:  true,
		})
	}
```

- [ ] Run `ginkgo --focus="secret mounts" ./atc/worker/jetbridge/` — expect pass.
- [ ] Run `ginkgo ./atc/worker/jetbridge/` — full package green (fake-clientset suites only; live/behavioral tests are build-tag/label gated).
- [ ] Commit: `git add atc/runtime atc/worker/jetbridge && git commit -m "feat(runtime): main-container-only SecretMounts for harvest git credentials (contracts s8.3)"`

---

### Task 12: `exec.HarvestStep` — container spec, env, secrets, process

Modeled on the landed `exec.AgentStep` (which itself mirrors `TaskStep.run`): same delegate (`TaskDelegateFactory`), same sidecar helpers, same `attachOrRun` resumability. Ingestion/evidence/transition come in Task 13.

> *Amended 2026-07-09 (final review F20/F21/F30):* (a) step 6's judge token works precisely because 07 Task 11B's `applySecretRefs` **APPENDS** a secretKeyRef EnvVar for the `SecretEnv`-only `CLAUDE_CODE_OAUTH_TOKEN` key (`agent-platform-credential`/`anthropic-token`) — no literal placeholder is set, and adding one is forbidden (§8.2). (b) step 7 normalizes the dev sidecar's `WorkingDir` to the workspace artifact's mount path per the §8.5 CWD convention (Piece 4b). (c) steps 3–4 resolve the effective pipeline-run id: `step.plan.PipelineRunID` when > 0, else the `AGENT_PIPELINE_RUN_ID` row of the plan's var-interpolated `Env` (F30 — a Go renderer cannot put `((run_id))` in the int field); the resolved id feeds `HARVEST_CONFIG`, the pod env, and Task 13's linkage.

**Files:**
- Create: `atc/exec/harvest_step.go`
- Test: `atc/exec/harvest_step_test.go`

**Steps:**

- [ ] Write the first specs in `atc/exec/harvest_step_test.go`. Copy the fixture scaffolding from `atc/exec/agent_step_test.go` (fakePool/chosenWorker/chosenContainer/fakeStreamer/fake delegate factory/`exec.NewRunState`, all landed by agent-step Task 12; register a `workspace` artifact in the repository the way the missing-inputs agent spec does for its inputs):

```go
var _ = Describe("HarvestStep", func() {
	// fixture: plan := atc.HarvestPlan{
	//   Name: "verify-and-push", Workspace: "workspace",
	//   Repo: "tdmtrader/concourse", TargetBranch: "main",
	//   TicketID: 42, PipelineRunID: 7, Branch: "agent/ticket-42", Push: true,
	//   DevMCP: &atc.SidecarSource{Config: &atc.SidecarConfig{Name: "dev", Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0"}},
	//   GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full"}}},
	//   Judge: &harvest.JudgeConfig{Rubric: []harvest.RubricDimension{{Name: "correctness", Weight: 1, Guidance: "g"}}, PassThreshold: 6.5},
	// }
	// step := exec.NewHarvestStep(planID, plan, atc.ContainerLimits{}, atc.ContainerLimits{},
	//   stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
	//   0, "registry.home/agent-runner:v1")

	It("errors clearly when no step image is configured", func() {
		step := exec.NewHarvestStep(planID, atc.HarvestPlan{Name: "h", Workspace: "workspace", Repo: "o/r"},
			atc.ContainerLimits{}, atc.ContainerLimits{}, stepMetadata, containerMetadata,
			fakePool, fakeStreamer, fakeDelegateFactory, 0, "")
		_, err := step.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("--agent-step-image")))
	})

	It("builds the container spec per the s2.8.1 contract", func() {
		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)

		Expect(spec.ImageSpec.ImageURL).To(Equal("registry.home/agent-runner:v1"))

		By("passing the serialized harvest config")
		var cfgEnv string
		for _, e := range spec.Env {
			if strings.HasPrefix(e, "HARVEST_CONFIG=") {
				cfgEnv = strings.TrimPrefix(e, "HARVEST_CONFIG=")
			}
		}
		Expect(cfgEnv).ToNot(BeEmpty())
		var cfg harvest.Config
		Expect(json.Unmarshal([]byte(cfgEnv), &cfg)).To(Succeed())
		Expect(cfg.StepName).To(Equal("verify-and-push"))
		Expect(cfg.Repo).To(Equal("tdmtrader/concourse"))
		Expect(cfg.TicketID).To(Equal(42))
		Expect(cfg.PipelineRunID).To(Equal(7))
		Expect(cfg.Branch).To(Equal("agent/ticket-42"))
		Expect(cfg.Push).To(BeTrue())
		Expect(cfg.GatePolicy.Gates).To(HaveLen(1))
		Expect(cfg.Judge).ToNot(BeNil())

		Expect(spec.Env).To(ContainElements(
			"AGENT_TICKET_ID=42",
			"DEV_MCP_URL=http://127.0.0.1:7780/mcp",
		))
		Expect(spec.Env).To(ContainElement(HavePrefix("AGENT_FLIGHT_DIR=")))

		By("mounting the workspace input and only the flight output")
		Expect(spec.Inputs).To(HaveLen(1))
		Expect(spec.Outputs).To(HaveKey("flight"))
		Expect(spec.Outputs).To(HaveLen(1))

		By("declaring the dev-mcp sidecar")
		Expect(spec.Sidecars).To(HaveLen(1))
		Expect(spec.Sidecars[0].Name).To(Equal("dev"))

		By("pointing the sidecar's CWD at the workspace mount (§8.5 CWD convention, F21)")
		Expect(spec.Sidecars[0].WorkingDir).To(Equal(spec.Inputs[0].DestinationPath))

		By("mounting the git-cred secret main-container-only, judge token via SecretEnv")
		Expect(spec.SecretMounts).To(ConsistOf(runtime.SecretMount{
			SecretName: "agent-harvest-git-tdmtrader-concourse",
			MountPath:  "/var/run/agent/git",
		}))
		Expect(spec.SecretEnv).To(HaveKeyWithValue("CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{
			Name: "agent-platform-credential", Key: "anthropic-token",
		}))
	})

	It("omits secrets when not pushing and not judging", func() {
		// plan without Push/Branch/Judge
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.SecretMounts).To(BeEmpty())
		Expect(spec.SecretEnv).ToNot(HaveKey("CLAUDE_CODE_OAUTH_TOKEN"))
	})

	It("falls back to the AGENT_PIPELINE_RUN_ID env row when the plan field is 0 (F30/§7)", func() {
		// fixture plan but with PipelineRunID: 0 and
		// Env: map[string]string{"AGENT_PIPELINE_RUN_ID": "7"} — the shape a
		// Go renderer emits (it cannot put ((run_id)) in the int field).
		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)

		var cfg harvest.Config
		for _, e := range spec.Env {
			if strings.HasPrefix(e, "HARVEST_CONFIG=") {
				Expect(json.Unmarshal([]byte(strings.TrimPrefix(e, "HARVEST_CONFIG=")), &cfg)).To(Succeed())
			}
		}
		Expect(cfg.PipelineRunID).To(Equal(7))
		Expect(spec.Env).To(ContainElement("AGENT_PIPELINE_RUN_ID=7"))
	})

	It("runs harvest-runner as the well-known harvest process", func() {
		step.Run(ctx, state)
		// assert on chosenContainer's recorded process spec (runtimetest):
		// Path "harvest-runner", ID "harvest"
	})

	It("fails on a missing workspace input", func() {
		// empty artifact repository
		_, err := step.Run(ctx, state)
		Expect(err).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
	})

	It("succeeds iff the process exits 0", func() {
		// runtimetest.ProcessStub exit 1 → ok=false, err=nil
		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeFalse())
	})
})
```

- [ ] Run `ginkgo --focus="HarvestStep" ./atc/exec/` — expect compile failure.
- [ ] Write `atc/exec/harvest_step.go`:

```go
package exec

// imports: context, encoding/json, errors, fmt, strconv, time,
// code.cloudfoundry.org/lager/v3 + lagerctx, atc, atc/db, atc/exec/build,
// atc/imageresolver, atc/metric, atc/runtime, tracing, vars,
// github.com/concourse/concourse/agent/api/metrics,
// github.com/concourse/concourse/agent/api/reviews,
// github.com/concourse/concourse/agent/api/tickets,
// github.com/concourse/concourse/agent/budget,
// github.com/concourse/concourse/agent/harvest

const harvestProcessID = "harvest"

type HarvestStepOption func(*HarvestStep)

func WithHarvestImageResolver(r imageresolver.Resolver) HarvestStepOption {
	return func(s *HarvestStep) { s.imageResolver = r }
}

func WithHarvestMetricsStore(m metrics.Store) HarvestStepOption {
	return func(s *HarvestStep) { s.metricsStore = m }
}

func WithHarvestReviewsStore(r reviews.Store) HarvestStepOption {
	return func(s *HarvestStep) { s.reviewsStore = r }
}

func WithHarvestTicketStore(t tickets.Store) HarvestStepOption {
	return func(s *HarvestStep) { s.ticketStore = t }
}

func WithHarvestBudgetChecker(c budget.Checker) HarvestStepOption {
	return func(s *HarvestStep) { s.budgetChecker = c }
}

// HarvestStep runs harvest-runner in a jetbridge pod with the repo's
// dev-mcp sidecar, then ingests the flight recorder, publishes evidence,
// and transitions the ticket (shared-contracts §2.8.1).
type HarvestStep struct {
	planID            atc.PlanID
	plan              atc.HarvestPlan
	defaultLimits     atc.ContainerLimits
	defaultRequests   atc.ContainerLimits
	metadata          StepMetadata
	containerMetadata db.ContainerMetadata
	workerPool        Pool
	streamer          Streamer
	delegateFactory   TaskDelegateFactory
	defaultTimeout    time.Duration
	harvestImage      string
	imageResolver     imageresolver.Resolver
	metricsStore      metrics.Store
	reviewsStore      reviews.Store
	ticketStore       tickets.Store
	budgetChecker     budget.Checker

	// pipelineRunID is the EFFECTIVE run id, resolved once in run():
	// plan.PipelineRunID when > 0, else the AGENT_PIPELINE_RUN_ID row of
	// the interpolated plan Env (F30/§7 — renderers leave the int field 0).
	// Consumed by ingestAndRecord (Task 13) for metrics/evidence linkage.
	pipelineRunID int
}

func NewHarvestStep(
	planID atc.PlanID,
	plan atc.HarvestPlan,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	streamer Streamer,
	delegateFactory TaskDelegateFactory,
	defaultTimeout time.Duration,
	harvestImage string,
	opts ...HarvestStepOption,
) Step {
	s := &HarvestStep{
		planID: planID, plan: plan,
		defaultLimits: defaultLimits, defaultRequests: defaultRequests,
		metadata: metadata, containerMetadata: containerMetadata,
		workerPool: workerPool, streamer: streamer,
		delegateFactory: delegateFactory, defaultTimeout: defaultTimeout,
		harvestImage: harvestImage,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (step *HarvestStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "harvest", tracing.Attrs{"name": step.plan.Name})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "harvest", step.plan.Name, time.Since(start))

	return ok, err
}
```

  `run` implements, in order (each block adapted from the landed `agent_step.go`, which cites `task_step.go`):
  1. Logger session `"harvest-step"`; `delegate.Initializing(logger)`.
  2. Guard: `if step.harvestImage == "" { return false, errors.New("harvest step requires the web node to be started with --agent-step-image (the image also carries harvest-runner)") }`.
  3. **Env interpolation + effective run id (F30):** resolve `step.plan.Env` exactly like the agent step (07 Task 12 step 3: keys sorted with `sort.Strings`, each value through `creds.NewString(state, raw).Evaluate()`; interpolation failure → return the error) into `resolvedEnv map[string]string`. Then `step.pipelineRunID = step.plan.PipelineRunID`; when it is 0, `if id, ok := envInt(resolvedEnv, "AGENT_PIPELINE_RUN_ID"); ok { step.pipelineRunID = id }` (`envInt` as landed by agent-step Task 12 — Go renderers cannot put `((run_id))` in the int field, so the id arrives as an env row; §7/§8.1 fallback contract). **Config env:** build `harvest.Config{StepName: step.plan.Name, Workspace: step.plan.Workspace, Repo: step.plan.Repo, TargetBranch: step.plan.TargetBranch, TicketID: step.plan.TicketID, PipelineRunID: step.pipelineRunID, Branch: step.plan.Branch, Push: step.plan.Push, GatePolicy: step.plan.GatePolicy, Judge: step.plan.Judge}`; `cfgJSON, err := json.Marshal(cfg)` (error → return it).
  4. **Env assembly:** `env := step.metadata.TaskEnv()` + each `resolvedEnv` row `k+"="+v` (sorted; skip the `AGENT_TICKET_ID`/`AGENT_PIPELINE_RUN_ID` keys — re-emitted canonically next) + `harvest.EnvConfig+"="+string(cfgJSON)` + `"AGENT_STEP_NAME="+step.plan.Name` + `"AGENT_FLIGHT_DIR="+artifactPath(step.containerMetadata.WorkingDirectory, "flight", "")` + when `step.plan.TicketID > 0` `"AGENT_TICKET_ID="+strconv.Itoa(step.plan.TicketID)` + when `step.pipelineRunID > 0` `"AGENT_PIPELINE_RUN_ID="+strconv.Itoa(step.pipelineRunID)`.
  5. **ContainerSpec:** `TeamID`/`TeamName`/`JobID` from metadata, `StepName: step.plan.Name`, `ImageSpec: runtime.ImageSpec{ImageURL: step.harvestImage}`, `Type: step.containerMetadata.Type`, `Dir: step.containerMetadata.WorkingDirectory`, `Env: env`. Inputs: exactly one — `state.ArtifactRepository().ArtifactFor(build.ArtifactName(step.plan.Workspace))`; missing → `MissingInputsError{[]string{step.plan.Workspace}}`; `DestinationPath: artifactPath(workdir, step.plan.Workspace, "")`. Outputs: `runtime.OutputPaths{"flight": ensureTrailingSlash(artifactPath(workdir, "flight", ""))}` — nothing else leaves the harvest pod. Limits/Requests: `step.defaultLimits`/`step.defaultRequests`.
  6. **Secrets:** when `step.plan.Push && step.plan.Branch != ""`: `containerSpec.SecretMounts = []runtime.SecretMount{{SecretName: harvest.GitCredSecretName(step.plan.Repo), MountPath: harvest.GitCredMountPath}}`. When `step.plan.Judge != nil`: `containerSpec.SecretEnv = map[string]vars.SecretRef{"CLAUDE_CODE_OAUTH_TOKEN": {Name: harvest.PlatformCredentialSecret, Key: harvest.PlatformCredentialSecretKey}}` (§8.2 — the platform credential, never the per-run user token). *Note (2026-07-09, F20):* this key is deliberately `SecretEnv`-ONLY — no matching literal env entry exists or is added; it reaches the pod because jetbridge `applySecretRefs` (07 Task 11B) **appends** a secretKeyRef EnvVar for `SecretEnv` keys with no literal counterpart. The literal-placeholder workaround is forbidden (§8.2 Consumption).
  7. **dev-mcp sidecar:** when `step.plan.DevMCP != nil`: `sidecars, err := loadSidecarConfigs(ctx, logger, state.ArtifactRepository(), step.streamer, []atc.SidecarSource{*step.plan.DevMCP})`; `resolveSidecarImages(ctx, logger, state, step.imageResolver, sidecars)`; *(2026-07-09, F21/§8.5 CWD convention)* for each loaded sidecar whose `WorkingDir == ""`, set `sc.WorkingDir = artifactPath(step.containerMetadata.WorkingDirectory, step.plan.Workspace, "")` in-place before assigning — dev-mcp images ship a bare-binary ENTRYPOINT with CWD-relative flag defaults (no hardcoded `/workspace`), so the exec must point the sidecar's CWD at the workspace mount; then `containerSpec.Sidecars = sidecars`; `delegate.EmitSidecarPlans(logger, sidecars)`; append `"DEV_MCP_URL=http://127.0.0.1:7780/mcp"` to `containerSpec.Env` (§8.1 fixed port).
  8. **Placement + timeout + process:** identical to the agent step — `tracing.Inject(ctx, &containerSpec)`; `owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)`; `delegate.BeforeSelectWorker`; `step.workerPool.FindOrSelectWorker(...)`; `MaybeTimeout(ctx, step.plan.Timeout, step.defaultTimeout)`; `delegate.SelectedWorker`; `worker.FindOrCreateContainer(...)`; `delegate.Starting(logger)`; `process, err := attachOrRun(ctx, container, runtime.ProcessSpec{ID: harvestProcessID, Path: "harvest-runner", Dir: step.containerMetadata.WorkingDirectory}, sidecarProcessIO(delegate, containerSpec.Sidecars))`; `result, runErr := process.Wait(ctx)`.
  9. **Output registration:** register the `flight` volume mount as an artifact (`worker.ArtifactFromVolume` wrap, `repository.RegisterArtifact(build.ArtifactName("flight"), artifact, false)` — same loop the agent step uses).
  10. **Ingest + record + transition:** `step.ingestAndRecord(ctx, logger, wkr, volumeMounts, time.Since(start))` — Task 13; called on every path where the container ran, including `DeadlineExceeded`, before returning.
  11. **Exit handling** identical to the agent step: `DeadlineExceeded` → `delegate.Errored(logger, TimeoutLogMessage)`, `(false, nil)`; other runErr → return it; else `delegate.Finished(logger, ExitStatus(result.ExitStatus))`, `(result.ExitStatus == 0, nil)`.

  In this task, stub `ingestAndRecord` as a no-op method (real body in Task 13) so the specs above compile and pass.
- [ ] Run `ginkgo --focus="HarvestStep" ./atc/exec/` — expect pass.
- [ ] Run `ginkgo ./atc/exec/` — full package green.
- [ ] Commit: `git add atc/exec && git commit -m "feat(exec): harvest step execution - config env, dev-mcp sidecar, isolated secrets, resumable process"`

---

### Task 13: `exec.HarvestStep` — ingestion, evidence upsert, ticket transition

Synchronous before `Run` returns (same GC-race guarantee as the agent step): metrics row, evidence row with linkage, judge ledger record, ticket transition through the single-writer. All tolerant: missing/corrupt flight files degrade to a `status=error` row and an `errored` ticket; recording failures are logged, never fail the step.

**Files:**
- Modify: `atc/exec/harvest_step.go` (replace the `ingestAndRecord` stub)
- Test: `atc/exec/harvest_step_test.go` (ingestion contexts)

**Steps:**

- [ ] Add ingestion specs to `atc/exec/harvest_step_test.go`, with `fakeMetricsStore *metricsfakes.FakeStore`, `fakeTicketStore *ticketsfakes.FakeStore`, and the reviews `MemoryStore` (or a counterfeiter fake if `reviewsfakes` exists) passed via the options. `fakeStreamer.StreamFileStub` returns fixture readers keyed on the requested path:
  - `results.json` → `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"1 gate run(s) passed; judge 7.5/10; pushed agent/ticket-42","artifacts":[],"metadata":{"pushed_branch":"agent/ticket-42","head_sha":"abc123"}}`
  - `events.ndjson` → NDJSON lines: `step.start`, `gate.result` (`{"gate":"test","scope":"full","status":"ok","duration_seconds":3}`), `judge.score` (`{"rubric_hash":"h","dimensions":[],"total":7.5,"max_total":10,"model":"m1","cost_usd":0.2}`), `cost.record` (`{"source":"harvest_judge","provider":"anthropic","model":"m1","input_tokens":10,"output_tokens":5,"turns":1,"cost_usd":0.2}`), `push.done`, `step.end` (`{"step_name":"verify-and-push","status":"ok","summary":"done","wall_time_seconds":42,"cost_usd":0.2,"turns":1}`)
  - `review.json` → `{"schema_version":"harvest/1","metadata":{"repo":"tdmtrader/concourse","commit":"abc123","branch":"agent/ticket-42","agent_model":"m1","duration_seconds":42},"score":{"value":7.5,"max":10,"pass":true},"proven_issues":[],"observations":[{"id":"judge-correctness-1","title":"nit","category":"judge"}],"summary":"ok","gates":[],"judge":{"rubric_hash":"h","total":7.5,"max_total":10,"pass":true}}`

```go
	Context("ingestion, evidence, and ticket transition", func() {
		It("upserts a RunMetrics row before Run returns", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(fakeMetricsStore.UpsertCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertArgsForCall(0)
			Expect(rm.Status).To(Equal("ok"))
			Expect(rm.BuildID).To(Equal(stepMetadata.BuildID))
			Expect(rm.PlanID).To(Equal(string(planID)))
			Expect(rm.StepName).To(Equal("verify-and-push"))
			Expect(*rm.TicketID).To(Equal(42))
			Expect(rm.CostUSD).To(BeNumerically("~", 0.2, 1e-9))
			Expect(rm.EventCounts).To(HaveKeyWithValue("gate.result", 1))
			Expect(rm.EventCounts).To(HaveKeyWithValue("judge.score", 1))
		})

		It("upserts the evidence review row with ticket/run linkage", func() {
			step.Run(ctx, state)
			got, err := reviewsStore.ListByTicket(42)
			Expect(err).ToNot(HaveOccurred())
			Expect(got).To(HaveLen(1))
			Expect(got[0].BuildID).To(Equal(stepMetadata.BuildID))
			Expect(got[0].Repo).To(Equal("tdmtrader/concourse"))
			Expect(got[0].CommitSha).To(Equal("abc123"))
			Expect(got[0].Branch).To(Equal("agent/ticket-42"))
			Expect(got[0].Score).To(BeNumerically("~", 7.5, 1e-9))
			Expect(got[0].Pass).To(BeTrue())
			Expect(*got[0].PipelineRunID).To(Equal(7))
		})

		It("transitions running -> needs_review with the branch on pass", func() {
			step.Run(ctx, state)
			Expect(fakeTicketStore.TransitionCallCount()).To(Equal(1))
			id, from, to, meta := fakeTicketStore.TransitionArgsForCall(0)
			Expect(id).To(Equal(42))
			Expect(from).To(Equal(tickets.StateRunning))
			Expect(to).To(Equal(tickets.StateNeedsReview))
			Expect(meta.Branch).To(Equal("agent/ticket-42"))
		})

		It("transitions to needs_review WITHOUT a branch on gate failure", func() {
			// results.json fixture status "fail", no pushed_branch; process exit 1
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())
			_, _, to, meta := fakeTicketStore.TransitionArgsForCall(0)
			Expect(to).To(Equal(tickets.StateNeedsReview))
			Expect(meta.Branch).To(BeEmpty())
		})

		It("transitions to errored with detail on platform error", func() {
			// results.json fixture status "error", summary "gate tooling error..."
			step.Run(ctx, state)
			_, _, to, meta := fakeTicketStore.TransitionArgsForCall(0)
			Expect(to).To(Equal(tickets.StateErrored))
			Expect(meta.ErrorDetail).To(ContainSubstring("gate tooling error"))
		})

		It("records an error metrics row and errored ticket when the flight files are missing", func() {
			// fakeStreamer returns an error for every path
			step.Run(ctx, state)
			Expect(fakeMetricsStore.UpsertArgsForCall(0).Status).To(Equal("error"))
			_, _, to, _ := fakeTicketStore.TransitionArgsForCall(0)
			Expect(to).To(Equal(tickets.StateErrored))
		})

		It("skips ticket writes entirely for ticketless (pure-CI) harvests", func() {
			// plan.TicketID = 0
			step.Run(ctx, state)
			Expect(fakeTicketStore.TransitionCallCount()).To(BeZero())
		})

		It("records the judge ledger entry fire-and-forget", func() {
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
			entry := fakeChecker.RecordArgsForCall(0)
			Expect(entry.Source).To(Equal("harvest_judge"))
			Expect(entry.CostUSD).To(BeNumerically("~", 0.2, 1e-9))
		})

		It("never fails the step when any recording write errors", func() {
			fakeMetricsStore.UpsertReturns(errors.New("db down"))
			fakeTicketStore.TransitionReturns(errors.New("db down"))
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
		})
	})
```

- [ ] Run `ginkgo --focus="ingestion, evidence" ./atc/exec/` — expect failure (stub no-op).
- [ ] Replace the stub in `atc/exec/harvest_step.go`:

```go
// ingestAndRecord reads the flight output (results.json, events.ndjson,
// review.json) and records everything server-side before Run returns:
// agent_run_metrics upsert, agent_reviews evidence upsert with linkage,
// harvest_judge ledger record, and the single-writer ticket transition
// (shared-contracts §2.8.1). Tolerant by design: missing/corrupt inputs
// degrade to status=error; write failures are logged, never returned.
func (step *HarvestStep) ingestAndRecord(
	ctx context.Context,
	logger lager.Logger,
	wkr runtime.Worker,
	volumeMounts []runtime.VolumeMount,
	wallTime time.Duration,
) {
	flightPath := ensureTrailingSlash(artifactPath(step.containerMetadata.WorkingDirectory, "flight", ""))
	var flightArtifact runtime.Artifact
	eventsHandle := ""
	for _, mount := range volumeMounts {
		if filepath.Clean(mount.MountPath) == filepath.Clean(flightPath) {
			flightArtifact = wkr.ArtifactFromVolume(mount.Volume)
			eventsHandle = mount.Volume.Handle()
		}
	}

	status := schema.RunStatusError
	summary := "flight recorder output missing"
	var resultsRaw json.RawMessage
	var resultsMeta map[string]any

	if flightArtifact != nil {
		if rc, err := step.streamer.StreamFile(ctx, flightArtifact, "results.json"); err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(rc, 5<<20))
			rc.Close()
			var results schema.Results
			if readErr == nil && json.Unmarshal(raw, &results) == nil && results.Validate() == nil {
				st, _ := schema.ThreeWayStatus(results.Status)
				status = st
				summary = results.Summary
				resultsRaw = json.RawMessage(raw)
				resultsMeta = results.Metadata
			} else {
				summary = "results.json missing or malformed"
			}
		}
	}

	// --- metrics + judge cost from events ---
	rm := schema.RunMetrics{
		BuildID:         step.metadata.BuildID,
		PlanID:          string(step.planID),
		StepName:        step.plan.Name,
		Status:          status,
		Summary:         summary,
		WallTimeSeconds: int(wallTime.Seconds()),
		Results:         resultsRaw,
		EventsArtifact:  eventsHandle,
	}
	if step.plan.TicketID > 0 {
		tid := step.plan.TicketID
		rm.TicketID = &tid
	}
	// step.pipelineRunID is the EFFECTIVE run id resolved in run() (plan
	// field, else the AGENT_PIPELINE_RUN_ID env row — F30/§7 fallback).
	if step.pipelineRunID > 0 {
		prid := step.pipelineRunID
		rm.PipelineRunID = &prid
	}

	if flightArtifact != nil {
		if rc, err := step.streamer.StreamFile(ctx, flightArtifact, "events.ndjson"); err == nil {
			counts := map[string]int{}
			reader := schema.NewEventReader(rc)
			for {
				event, err := reader.Read()
				if err != nil {
					break
				}
				counts[string(event.Type)]++
				switch event.Type {
				case schema.EventCostRecord:
					var c schema.CostRecordData
					if json.Unmarshal(event.Data, &c) == nil {
						rm.Usage.InputTokens += c.InputTokens
						rm.Usage.OutputTokens += c.OutputTokens
						rm.Turns += c.Turns
						rm.CostUSD += c.CostUSD
						if c.Model != "" {
							rm.Model = c.Model
						}
					}
				case schema.EventStepEnd:
					var e schema.StepEndData
					if json.Unmarshal(event.Data, &e) == nil && e.WallTimeSeconds > 0 {
						rm.WallTimeSeconds = e.WallTimeSeconds
					}
				}
			}
			rc.Close()
			rm.EventCounts = counts
		}
	}

	if step.metricsStore != nil {
		if err := step.metricsStore.Upsert(&rm); err != nil {
			logger.Error("failed-to-ingest-harvest-metrics", err)
		}
	}

	if step.budgetChecker != nil && rm.CostUSD > 0 {
		entry := budget.LedgerEntry{
			TicketID:      rm.TicketID,
			PipelineRunID: rm.PipelineRunID,
			BuildID:       rm.BuildID,
			StepName:      rm.StepName,
			Source:        budget.SourceHarvestJudge,
			Provider:      "anthropic",
			Model:         rm.Model,
			InputTokens:   rm.Usage.InputTokens,
			OutputTokens:  rm.Usage.OutputTokens,
			Turns:         rm.Turns,
			CostUSD:       rm.CostUSD,
		}
		if err := step.budgetChecker.Record(entry); err != nil {
			logger.Error("failed-to-record-judge-cost", err) // fire-and-forget
		}
	}

	// --- evidence row (agent_reviews with linkage, §1.10/§6.4.1) ---
	if step.reviewsStore != nil && flightArtifact != nil {
		if rc, err := step.streamer.StreamFile(ctx, flightArtifact, "review.json"); err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(rc, 5<<20))
			rc.Close()
			var payload reviews.ReviewPayload
			if readErr == nil && json.Unmarshal(raw, &payload) == nil && payload.Metadata.Commit != "" {
				rec := &reviews.StoredReview{
					BuildID:          step.metadata.BuildID,
					BuildName:        step.metadata.BuildName,
					TeamName:         step.metadata.TeamName,
					PipelineName:     step.metadata.PipelineName,
					JobName:          step.metadata.JobName,
					Repo:             step.plan.Repo,
					CommitSha:        payload.Metadata.Commit,
					Branch:           payload.Metadata.Branch,
					Score:            payload.Score.Value,
					MaxScore:         payload.Score.Max,
					Pass:             payload.Score.Pass,
					ProvenCount:      len(payload.ProvenIssues),
					ObservationCount: len(payload.Observations),
					Summary:          payload.Summary,
					AgentModel:       payload.Metadata.AgentModel,
					DurationSeconds:  payload.Metadata.DurationSec,
					Review:           json.RawMessage(raw),
					TicketID:         rm.TicketID,
					PipelineRunID:    rm.PipelineRunID,
				}
				if err := step.reviewsStore.Upsert(rec); err != nil {
					logger.Error("failed-to-publish-harvest-evidence", err)
				}
			} else {
				logger.Info("harvest-evidence-missing-or-malformed")
			}
		}
	}

	// --- ticket transition (single writer, §1.7) ---
	if step.ticketStore == nil || step.plan.TicketID <= 0 {
		return
	}
	var to tickets.State
	meta := tickets.TransitionMeta{} // ticket-core §2.1: no actor field — actor is carried by flight events
	switch status {
	case schema.RunStatusOK:
		to = tickets.StateNeedsReview
		if b, ok := resultsMeta["pushed_branch"].(string); ok {
			meta.Branch = b
		}
	case schema.RunStatusFailed:
		to = tickets.StateNeedsReview
	default:
		to = tickets.StateErrored
		meta.ErrorDetail = summary
	}
	if err := step.ticketStore.Transition(step.plan.TicketID, tickets.StateRunning, to, meta); err != nil {
		logger.Error("failed-to-transition-ticket", err, lager.Data{
			"ticket": step.plan.TicketID, "to": string(to),
		})
	}
}
```

  (Constant names `schema.RunStatusOK/Failed/Error` and the `ThreeWayStatus` signature are as landed by agent-step Task 4 — if the landed spelling differs, e.g. plain strings `"ok"/"failed"/"error"`, follow the landed code; the wire values are the contract.)
- [ ] Run `ginkgo --focus="ingestion, evidence" ./atc/exec/` then `ginkgo ./atc/exec/` — expect pass.
- [ ] Commit: `git add atc/exec && git commit -m "feat(exec): harvest server-side ingestion - metrics, evidence with linkage, judge ledger, ticket transition"`

---

### Task 14: Engine wiring — container type, `CoreStepFactory.HarvestStep`, builder dispatch

**Files:**
- Modify: `atc/db/container_metadata.go:26` (constants block — add `ContainerTypeHarvest`; parse case in `ContainerTypeFromString` at :33)
- Modify: `atc/engine/builder.go:20` (`CoreStepFactory` interface), `:138` region (dispatch — after agent-step's `plan.Agent` check), after `buildAgentStep` (follows `buildRunStep` at :358)
- Modify: `atc/engine/step_factory.go:18` region (fields), `:39` region (options), after the landed `AgentStep` constructor
- Modify: `atc/engine/enginefakes/` (regenerate)
- Test: `atc/engine/builder_test.go`

**Steps:**

- [ ] Add a builder test case in `atc/engine/builder_test.go` next to the agent-step context (which mirrors the run-step context at :558): a plan built from `planFactory.NewPlan(atc.HarvestPlan{Name: "verify-and-push", Workspace: "workspace", Repo: "o/r"})`; assert `fakeCoreStepFactory.HarvestStepCallCount()` is 1 and the received plan / stepMetadata / containerMetadata match (containerMetadata `Type: db.ContainerTypeHarvest`, `StepName: "verify-and-push"`). Copy the surrounding `expectedPlan`/`ArgsForCall` assertions verbatim from the agent case, substituting names.
- [ ] Run `ginkgo ./atc/engine/` — expect compile failure (`HarvestStep` not on `CoreStepFactory`, fake missing).
- [ ] In `atc/db/container_metadata.go`: add `ContainerTypeHarvest ContainerType = "harvest"` to the constants block (:26 region) and a `case "harvest": return ContainerTypeHarvest, nil` arm in `ContainerTypeFromString` (:33).
- [ ] In `atc/engine/builder.go`:
  - Add to `CoreStepFactory` (after agent-step's `AgentStep` entry): `HarvestStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, DelegateFactory) exec.Step`
  - Add dispatch after the `plan.Agent` check:

```go
	if plan.Harvest != nil {
		return factory.buildHarvestStep(build, plan)
	}
```

  - Add after `buildAgentStep`:

```go
func (factory *stepperFactory) buildHarvestStep(build db.Build, plan atc.Plan) exec.Step {
	containerMetadata := factory.containerMetadata(
		build,
		db.ContainerTypeHarvest,
		plan.Harvest.Name,
		plan.Attempts,
	)

	stepMetadata := factory.stepMetadata(
		build,
		factory.externalURL,
		false,
	)

	return factory.coreFactory.HarvestStep(
		plan,
		stepMetadata,
		containerMetadata,
		factory.buildDelegateFactory(build, plan),
	)
}
```

- [ ] In `atc/engine/step_factory.go`:
  - Add fields to `coreStepFactory` (:18 region, next to agent-step's `agentStepImage`/`agentMetricsStore`/`agentBudgetChecker`): `harvestTicketStore tickets.Store`, `harvestReviewsStore reviews.Store` (imports `"github.com/concourse/concourse/agent/api/tickets"`, `"github.com/concourse/concourse/agent/api/reviews"`).
  - Add options next to `WithAgentBudgetChecker`:

```go
// WithHarvestTicketStore sets the single-writer ticket store the harvest
// step transitions tickets through.
func WithHarvestTicketStore(s tickets.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.harvestTicketStore = s }
}

// WithHarvestReviewsStore sets the evidence store the harvest step
// publishes agent_reviews rows through.
func WithHarvestReviewsStore(s reviews.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.harvestReviewsStore = s }
}
```

  - Add the constructor after `AgentStep`, mirroring its working-dir hash:

```go
func (factory *coreStepFactory) HarvestStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	sum := sha256.Sum256([]byte(plan.Harvest.Name))
	containerMetadata.WorkingDirectory = filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))

	var opts []exec.HarvestStepOption
	if factory.imageResolver != nil {
		opts = append(opts, exec.WithHarvestImageResolver(factory.imageResolver))
	}
	if factory.agentMetricsStore != nil {
		opts = append(opts, exec.WithHarvestMetricsStore(factory.agentMetricsStore))
	}
	if factory.agentBudgetChecker != nil {
		opts = append(opts, exec.WithHarvestBudgetChecker(factory.agentBudgetChecker))
	}
	if factory.harvestTicketStore != nil {
		opts = append(opts, exec.WithHarvestTicketStore(factory.harvestTicketStore))
	}
	if factory.harvestReviewsStore != nil {
		opts = append(opts, exec.WithHarvestReviewsStore(factory.harvestReviewsStore))
	}

	harvestStep := exec.NewHarvestStep(
		plan.ID,
		*plan.Harvest,
		factory.defaultLimits,
		factory.defaultRequests,
		stepMetadata,
		containerMetadata,
		factory.pool,
		factory.streamer,
		delegateFactory,
		factory.defaultTaskTimeout,
		factory.agentStepImage, // shared image carries both runners (§2.8.1)
		opts...,
	)

	harvestStep = exec.LogError(harvestStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		harvestStep = exec.RetryError(harvestStep, delegateFactory)
	}
	return harvestStep
}
```

- [ ] Regenerate engine fakes: `go generate ./atc/engine/...`
- [ ] Run `ginkgo ./atc/engine/` — expect pass.
- [ ] Commit: `git add atc/db/container_metadata.go atc/engine && git commit -m "feat(engine): route harvest plans to exec.HarvestStep with ticket/reviews store options"`

---

### Task 15: atccmd wiring — stores into the engine

**Files:**
- Modify: `atc/atccmd/command.go:1990` region (the `engine.NewCoreStepFactory(...)` call — agent-step appended its options after `engine.WithCoreImageResolver(resolver)` at :2004)
- Test: `go build ./atc/...` + one engine-construction smoke check

**Steps:**

- [ ] In the `engine.NewCoreStepFactory` option list (after agent-step's `engine.WithAgentBudgetChecker(...)`), append:

```go
				engine.WithHarvestTicketStore(db.NewAgentTicketsFactory(dbConn)),
				engine.WithHarvestReviewsStore(db.NewAgentReviewsFactory(dbConn)),
```

  (`dbConn` reaches `constructEngine` the same way agent-step threaded it for `db.NewAgentRunMetricsFactory(dbConn)` — reuse that parameter; `db.NewAgentTicketsFactory` landed with ticket-core Task 4 at `atc/db/agent_tickets_factory.go:715` of that plan.)
- [ ] Run `go build ./atc/... && go vet ./atc/atccmd/` — expect pass.
- [ ] Run `go run ./cmd/concourse web --help 2>&1 | grep agent-step-image` — the shared flag is still present and its description covers harvest (update the flag's description string in `atc/atccmd/command.go:218` region to: `"Container image for agent: and harvest: step main containers (must contain the claude CLI, agent-runner, and harvest-runner). These steps error at runtime when unset."`).
- [ ] Commit: `git add atc/atccmd && git commit -m "feat(web): wire ticket and reviews stores into the harvest step engine path"`

---

### Task 16: Elm build-page rendering for the harvest step

The plan decoder (`web/elm/src/Concourse.elm:666` region) is a strict `oneOf` — a build containing a `harvest` plan otherwise fails to render. Mirror the landed `BuildStepAgent` precedent end to end.

**Files:**
- Modify: `web/elm/src/Concourse.elm` (`BuildStep` union :500 region — add `BuildStepHarvest StepName`; decoder list :679 region — add the `harvest` field; add `decodeBuildStepHarvest`)
- Modify: `web/elm/src/Build/StepTree/StepTree.elm` (every case site the compiler flags — render like `BuildStepAgent`/`BuildStepTask` with the step name and label `"harvest:"`)
- Test: Elm compile (exhaustiveness) + existing elm test suite

**Steps:**

- [ ] Add `| BuildStepHarvest StepName` to the `BuildStep` union (next to `BuildStepAgent`) and satisfy every `case` the compiler flags — Elm's exhaustiveness checking is the failing test here.
- [ ] Add the decoder entry after the `agent` field:

```elm
                , Json.Decode.field "harvest" <|
                    lazy (\_ -> decodeBuildStepHarvest)
```

  and:

```elm
decodeBuildStepHarvest : Json.Decode.Decoder BuildStep
decodeBuildStepHarvest =
    Json.Decode.succeed BuildStepHarvest
        |> andMap (Json.Decode.field "name" Json.Decode.string)
```

- [ ] In `Build/StepTree/StepTree.elm`, handle `Concourse.BuildStepHarvest name` at each flagged site exactly as `Concourse.BuildStepAgent name` is handled (init → leaf step with name; view label `"harvest:"`).
- [ ] Compile + test + rebuild the bundle the way the repo does (see commit `6f16d19a` "rebuild frontend bundle" for the exact command; typically `cd web && yarn && yarn build`; elm tests under `web/elm` via `npx elm-test` if present). Expect clean compile.
- [ ] Commit: `git add web && git commit -m "feat(web-ui): render harvest steps in the build plan view"`

---

### Task 17: Fixture-workspace end-to-end suite — gates pass / fail / flaky

The charter's trust-foundation suite, exercising the REAL runner binary path (`harvest.Run` with a real git workspace, a scripted dev-mcp fake, a stub claude, and a local bare remote) — one spec per posture, plus the flaky case that proves the retry stance surfaces rather than hides flakiness. Task 10 covered unit-level orchestration; this task locks the postures together against regressions and asserts the full flight-dir contract each time. *(Amended 2026-07-09, F33: a fourth DIRTY posture pins the worktree-cleanliness stance — uncommitted changes fail the harvest before any gate runs, with no auto-discard.)*

**Files:**
- Create: `agent/harvest/fixture_e2e_test.go`
- Test: `ginkgo --focus="fixture workspace" ./agent/harvest/`

**Steps:**

- [ ] Write `agent/harvest/fixture_e2e_test.go` (reuses the Task 7/9/10 helpers in package `harvest_test`):

```go
package harvest_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
	"github.com/concourse/concourse/agent/devmcp/devmcpfakes"
	"github.com/concourse/concourse/agent/harvest"
	schema "github.com/concourse/concourse/agent/schema"
)

// fixture posture: {workspace with 2 committed changes} + gate script per case.
var _ = Describe("fixture workspace postures", func() {
	var (
		tmp, ws, bare, flight string
		client                *devmcpfakes.FakeClient
		cfg                   harvest.Config
		deps                  harvest.RunDeps
	)

	pass := &devmcp.ToolResult{Status: devmcp.StatusOK, Summary: "all green", DurationSeconds: 1}
	fail := &devmcp.ToolResult{Status: devmcp.StatusFailed, Summary: "1 spec failed", OutputTail: "--- FAIL: TestX"}

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		ws, bare = fixtureWorkspace(tmp)
		flight = filepath.Join(tmp, "flight")
		Expect(os.MkdirAll(flight, 0o755)).To(Succeed())

		client = new(devmcpfakes.FakeClient)
		client.AffectedComponentsReturns([]string{"app"}, nil)

		cfg = harvest.Config{
			StepName: "verify-and-push", Workspace: "workspace",
			Repo: "tdmtrader/concourse", TargetBranch: "main",
			TicketID: 7, Branch: "agent/ticket-7", Push: true,
			GatePolicy: harvest.GatePolicy{Gates: []harvest.Gate{
				{Gate: "build", Scope: "affected"},
				{Gate: "test", Scope: "affected_then_full", Retries: 1},
			}},
		}
		deps = harvest.RunDeps{Client: client, GitCredDir: filepath.Join(tmp, "none")}
	})

	flightContract := func(expectStatus schema.Status) schema.Results {
		var res schema.Results
		raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(raw, &res)).To(Succeed())
		Expect(res.Validate()).To(Succeed())
		Expect(res.Status).To(Equal(expectStatus))

		_, err = os.Stat(filepath.Join(flight, "review.json"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(flight, "events.ndjson"))
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	It("PASS posture: everything green, branch delivered", func() {
		client.BuildReturns(pass, nil)
		client.RunTestsReturns(pass, nil)

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(harvest.ExitGatesPassed))

		res := flightContract(schema.StatusPass)
		Expect(res.Metadata["pushed_branch"]).To(Equal("agent/ticket-7"))
		head := git(ws, "rev-parse", "HEAD")
		Expect(git(bare, "rev-parse", "refs/heads/agent/ticket-7")).To(Equal(head))
	})

	It("FAIL posture: deterministic red gate, nothing pushed, evidence cites the gate", func() {
		client.BuildReturns(pass, nil)
		client.RunTestsReturns(fail, nil)

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(harvest.ExitGatesFailed))

		flightContract(schema.StatusFail)
		Expect(git(bare, "for-each-ref", "refs/heads/agent")).To(BeEmpty())

		evidence, _ := os.ReadFile(filepath.Join(flight, "review.json"))
		Expect(string(evidence)).To(ContainSubstring(`"gate-test`))
		Expect(string(evidence)).To(ContainSubstring("--- FAIL: TestX"))
	})

	It("DIRTY posture: uncommitted changes fail the harvest before any gate runs (F33)", func() {
		client.BuildReturns(pass, nil)
		client.RunTestsReturns(pass, nil)
		Expect(os.WriteFile(filepath.Join(ws, "wip.txt"), []byte("uncommitted"), 0o644)).To(Succeed())

		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(harvest.ExitGatesFailed)) // agent failure, NOT platform error

		flightContract(schema.StatusFail)

		By("no gate ran, nothing was pushed")
		Expect(client.BuildCallCount()).To(BeZero())
		Expect(client.RunTestsCallCount()).To(BeZero())
		Expect(git(bare, "for-each-ref", "refs/heads/agent")).To(BeEmpty())

		By("no auto-discard: the agent's uncommitted work survives")
		_, statErr := os.Stat(filepath.Join(ws, "wip.txt"))
		Expect(statErr).NotTo(HaveOccurred())

		evidence, _ := os.ReadFile(filepath.Join(flight, "review.json"))
		Expect(string(evidence)).To(ContainSubstring(`"workspace-dirty"`))
	})

	It("FLAKY posture: fails once then passes - delivered, but visibly flaky", func() {
		client.BuildReturns(pass, nil)
		client.RunTestsReturnsOnCall(0, fail, nil) // affected attempt 1
		client.RunTestsReturnsOnCall(1, pass, nil) // affected attempt 2 (retry)
		client.RunTestsReturnsOnCall(2, pass, nil) // full suite
		exit, err := harvest.Run(context.Background(), cfg, flight, filepath.Dir(ws), deps)
		Expect(err).NotTo(HaveOccurred())
		Expect(exit).To(Equal(harvest.ExitGatesPassed))

		flightContract(schema.StatusPass)

		By("flakiness is surfaced in the evidence, not hidden")
		evidence, _ := os.ReadFile(filepath.Join(flight, "review.json"))
		Expect(string(evidence)).To(ContainSubstring(`"flaky":true`))

		By("and on the gate.result event stream")
		f, _ := os.Open(filepath.Join(flight, "events.ndjson"))
		defer f.Close()
		sawFlaky := false
		r := schema.NewEventReader(f)
		for {
			e, err := r.Read()
			if err != nil {
				break
			}
			if e.Type == schema.EventGateResult {
				var d schema.GateResultData
				Expect(json.Unmarshal(e.Data, &d)).To(Succeed())
				if d.Flaky {
					sawFlaky = true
					Expect(d.Attempt).To(Equal(2))
				}
			}
		}
		Expect(sawFlaky).To(BeTrue())
	})
})
```

- [ ] Run `ginkgo --focus="fixture workspace" ./agent/harvest/` — expect pass (any failure here is a real semantics bug in Tasks 7–10; fix the implementation, not the fixtures).
- [ ] Run `ginkgo ./agent/harvest/` — full package green.
- [ ] Commit: `git add agent/harvest && git commit -m "test(harvest): fixture-workspace e2e postures - pass, fail, and surfaced-flaky"`

---

### Task 18: Live theborg security test — credentials exist only in the harvest pod

The §5/§8.3 posture proof the whole credential story rests on: agent pods can never see the git credentials or the platform token; inside the harvest pod, sidecars can't see the git-cred mount either. Plain Go test, `//go:build live`, `kubeClient(t)` + throwaway namespace per CLAUDE.md (`KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=<sandbox-ns>`, NEVER `cicd`/`concourse`). Scaffolding copied from `atc/worker/jetbridge/live_secret_env_test.go:25` (secret creation, `dbfakes.FakeWorker`, `jetbridge.NewWorker` + `SetExecutor`, pod-spec assertions).

**Files:**
- Create: `atc/worker/jetbridge/live_harvest_credentials_test.go`
- Test: `KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=<sandbox-ns> go test -tags live -run '^TestLiveHarvestCredentialIsolation$' -v -count=1 -timeout 5m ./atc/worker/jetbridge/`

**Steps:**

- [ ] Write `atc/worker/jetbridge/live_harvest_credentials_test.go`:

```go
//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveHarvestCredentialIsolation proves the §8.3 credential posture on
// a real cluster: the git-cred secret is readable in the harvest pod's MAIN
// container, invisible to its sidecar, and entirely absent from an
// agent-shaped pod in the same namespace.
func TestLiveHarvestCredentialIsolation(t *testing.T) {
	clientset, cfg := kubeClient(t)
	ctx := context.Background()
	ns := cfg.Namespace

	stamp := time.Now().Format("150405")

	// The two secrets the harvest pod consumes (§8.2, §8.3).
	gitSecret := "agent-harvest-git-live-" + stamp
	platSecret := "agent-platform-credential-live-" + stamp
	for name, data := range map[string]map[string][]byte{
		gitSecret:  {"username": []byte("bot"), "token": []byte("sekret-token")},
		platSecret: {"anthropic-token": []byte("sk-ant-live-test")},
	} {
		_, err := clientset.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       data,
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("creating secret %s: %v", name, err)
		}
		name := name
		t.Cleanup(func() {
			_ = clientset.CoreV1().Secrets(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
		})
	}

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}

	newWorker := func() *jetbridge.Worker {
		fakeDBWorker := new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("live-k8s-worker")
		w := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
		w.SetExecutor(jetbridge.NewSPDYExecutor(clientset, restConfig))
		return w
	}

	execInContainer := func(pod, container string, cmd []string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := jetbridge.NewSPDYExecutor(clientset, restConfig).Exec(
			ctx, ns, pod, container, cmd, nil, &stdout, &stderr, false,
		)
		return stdout.String() + stderr.String(), err
	}

	// --- harvest-shaped pod: git-cred SecretMount + platform-token SecretEnv + dev sidecar.
	// CLAUDE_CODE_OAUTH_TOKEN is a SecretEnv-ONLY key (no literal env entry):
	// this exercises applySecretRefs' APPEND path (07 Task 11B, F20) live —
	// with replace-only semantics the main container would silently get no var.
	harvestHandle := "live-harvest-" + stamp
	cleanupPod(t, clientset, ns, harvestHandle)
	{
		fakeDBWorker := new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("live-k8s-worker")
		setupFakeDBContainer(fakeDBWorker, harvestHandle)
		w := newWorker()
		_ = w

		worker := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
		worker.SetExecutor(jetbridge.NewSPDYExecutor(clientset, restConfig))
		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(harvestHandle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID: 1, TeamName: "main", Dir: "/tmp",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				SecretEnv: map[string]vars.SecretRef{
					"CLAUDE_CODE_OAUTH_TOKEN": {Namespace: ns, Name: platSecret, Key: "anthropic-token"},
				},
				SecretMounts: []runtime.SecretMount{
					{SecretName: gitSecret, MountPath: "/var/run/agent/git"},
				},
				Sidecars: []atc.SidecarConfig{
					{Name: "dev", Image: "docker:///busybox", Command: []string{"sh", "-c", "sleep 3600"}},
				},
			},
			&noopDelegate{},
		)
		if err != nil {
			t.Fatalf("harvest pod: %v", err)
		}
		if _, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh", Args: []string{"-c", "sleep 3600"}, Dir: "/tmp",
		}, runtime.ProcessIO{}); err != nil {
			t.Fatalf("harvest run: %v", err)
		}
	}

	// --- agent-shaped pod: NO harvest secrets at all
	agentHandle := "live-agent-" + stamp
	cleanupPod(t, clientset, ns, agentHandle)
	{
		fakeDBWorker := new(dbfakes.FakeWorker)
		fakeDBWorker.NameReturns("live-k8s-worker")
		setupFakeDBContainer(fakeDBWorker, agentHandle)
		worker := jetbridge.NewWorker(fakeDBWorker, clientset, *cfg)
		worker.SetExecutor(jetbridge.NewSPDYExecutor(clientset, restConfig))
		container, _, err := worker.FindOrCreateContainer(
			ctx,
			db.NewFixedHandleContainerOwner(agentHandle),
			db.ContainerMetadata{Type: db.ContainerTypeTask},
			runtime.ContainerSpec{
				TeamID: 1, TeamName: "main", Dir: "/tmp",
				ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
				Env:       []string{"AGENT_STEP_NAME=write-spec"},
			},
			&noopDelegate{},
		)
		if err != nil {
			t.Fatalf("agent pod: %v", err)
		}
		if _, err := container.Run(ctx, runtime.ProcessSpec{
			Path: "/bin/sh", Args: []string{"-c", "sleep 3600"}, Dir: "/tmp",
		}, runtime.ProcessIO{}); err != nil {
			t.Fatalf("agent run: %v", err)
		}
	}

	// wait for both pods running
	for _, h := range []string{harvestHandle, agentHandle} {
		deadline := time.Now().Add(2 * time.Minute)
		for {
			pod, err := clientset.CoreV1().Pods(ns).Get(ctx, h, metav1.GetOptions{})
			if err == nil && pod.Status.Phase == corev1.PodRunning {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("pod %s never became running", h)
			}
			time.Sleep(2 * time.Second)
		}
	}

	// 1. harvest MAIN reads the git token
	out, err := execInContainer(harvestHandle, "main", []string{"cat", "/var/run/agent/git/token"})
	if err != nil || !strings.Contains(out, "sekret-token") {
		t.Errorf("harvest main should read git token, got err=%v out=%q", err, out)
	}

	// 2. harvest MAIN sees the platform token via env
	out, err = execInContainer(harvestHandle, "main", []string{"sh", "-c", "echo $CLAUDE_CODE_OAUTH_TOKEN"})
	if err != nil || !strings.Contains(out, "sk-ant-live-test") {
		t.Errorf("harvest main should have CLAUDE_CODE_OAUTH_TOKEN, got err=%v out=%q", err, out)
	}

	// 3. harvest SIDECAR cannot see the git-cred mount
	out, _ = execInContainer(harvestHandle, "dev", []string{"cat", "/var/run/agent/git/token"})
	if strings.Contains(out, "sekret-token") {
		t.Errorf("dev sidecar must NOT see git credentials, read: %q", out)
	}

	// 4. agent pod has neither mount nor env
	out, _ = execInContainer(agentHandle, "main", []string{"cat", "/var/run/agent/git/token"})
	if strings.Contains(out, "sekret-token") {
		t.Errorf("agent pod must NOT see git credentials, read: %q", out)
	}
	out, _ = execInContainer(agentHandle, "main", []string{"sh", "-c", "echo -n x$CLAUDE_CODE_OAUTH_TOKEN"})
	if strings.TrimSpace(out) != "x" {
		t.Errorf("agent pod must NOT carry the platform token, got %q", out)
	}

	// 5. spec-level assertion: no secret refs anywhere in the agent pod spec
	agentPod, err := clientset.CoreV1().Pods(ns).Get(ctx, agentHandle, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get agent pod: %v", err)
	}
	for _, v := range agentPod.Spec.Volumes {
		if v.Secret != nil {
			t.Errorf("agent pod declares secret volume %q", v.Secret.SecretName)
		}
	}
	for _, c := range agentPod.Spec.Containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				t.Errorf("agent pod container %s has secretKeyRef env %s", c.Name, e.Name)
			}
		}
	}
}
```

  (Adapt the two helper call-shapes to what `live_sidecar_test.go`/`live_secret_env_test.go` actually export: `cleanupPod`, `setupFakeDBContainer`, and the executor `Exec` signature — the file at `live_secret_env_test.go:25` is the canonical template; if the SPDY executor has no exported per-container `Exec` helper, use `clientset.CoreV1().RESTClient().Post()...` exec the way `live_security_test.go` does.)
- [ ] Create a throwaway namespace on theborg (`kubectl --context theborg create ns harvest-live-<date>`), run:

```bash
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=harvest-live-<date> \
  go test -tags live -run '^TestLiveHarvestCredentialIsolation$' -v -count=1 -timeout 5m \
  ./atc/worker/jetbridge/
```

  — expect PASS with all five assertions green; then `kubectl --context theborg delete ns harvest-live-<date>`.
- [ ] Commit: `git add atc/worker/jetbridge && git commit -m "test(jetbridge): live credential-isolation proof - harvest-only git creds and platform token (contracts s8.3)"`

---

### Task 19: Full verification and workstream close-out

**Files:**
- Modify: none (verification only; fix regressions in place)

**Steps:**

- [ ] Run the workstream's Go suites end to end:

```bash
pg_isready
ginkgo ./agent/api/reviews/ ./agent/harvest/
(cd agent/schema && go test ./...)
ginkgo ./atc/ ./atc/builds/ ./atc/configvalidate/ ./atc/exec/ ./atc/engine/ ./atc/worker/jetbridge/
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/
ginkgo ./atc/db/
go build ./... && go build ./cmd/harvest-runner
```

  — everything green.
- [ ] Run `make test-unit` (~3 min, needs PostgreSQL; never `--race` per CLAUDE.md) — expect the same pass rate as the branch baseline (record any pre-existing failures before starting; `atc/exec/artifact_input_step_test.go` vet issue and the gardenruntime BeforeSuite port conflict are known).
- [ ] Validate a hand-written harvest pipeline parses and validates (the wave-3 loop-closing template consumes this step next):

```bash
go run ./cmd/concourse web --help >/dev/null  # binary sanity
cat > /tmp/harvest-demo-pipeline.yml <<'YAML'
jobs:
- name: demo
  plan:
  - task: make-workspace
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      outputs: [{name: workspace}]
      run: {path: "true"}
  - harvest: verify-and-push
    workspace: workspace
    repo: tdmtrader/concourse
    ticket_id: 1
    branch: agent/ticket-1
    push: true
    dev_mcp:
      name: dev
      image: ghcr.io/tdmtrader/mcp-dev-concourse:v0.1.0
    gate_policy:
      gates:
      - {gate: build, scope: affected}
      - {gate: test, scope: affected_then_full, retries: 1}
    judge:
      rubric:
      - {name: correctness, weight: 3, guidance: does it satisfy the spec}
      pass_threshold: 6.5
YAML
go run ./fly validate-pipeline -c /tmp/harvest-demo-pipeline.yml
```

  — expect `looks good`.
- [ ] Confirm the Task 1 addendum sections exist and match the code (grep names): `grep -n "2.8.1\|6.4.1\|Retries int" docs/superpowers/plans/agentic-platform/00-shared-contracts.md`.
- [ ] Commit any verification fixes: `git add -A && git commit -m "chore(harvest): close-out fixes from full verification"` (skip if clean).

---

## Execution notes

**Full test suite for this workstream** (PostgreSQL required for `atc/db` and migrations — check `pg_isready`):

```bash
ginkgo ./agent/api/reviews/ ./agent/harvest/          # contract types, gates, judge, runner, fixtures
go test ./agent/devmcp/ -run TestWaitHealthy           # shared sidecar-readiness helper (F34)
(cd agent/schema && go test ./...)                     # nested module (attempt/flaky additions)
ginkgo ./atc/ ./atc/builds/ ./atc/configvalidate/      # step parse/validate/plan
ginkgo ./atc/exec/ ./atc/engine/                       # exec step + engine wiring
ginkgo ./atc/worker/jetbridge/                         # SecretMounts pod construction (fake clientset)
ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/
ginkgo ./atc/db/                                       # factories (~1007 specs, ~90s)
make test-unit                                         # full tier before handing off
```

Never use `--race` (parallel compilation failures, per CLAUDE.md). If `atc/db` reports `database "testdb_template" already exists`, another test process is running — wait.

**Live-test requirements (theborg):** Task 18 needs a real cluster. Pattern per CLAUDE.md: plain Go tests behind `//go:build live`; connect via `kubeClient(t)` (`KUBECONFIG=~/.kube/config`, kube-context `theborg` → https://theborg.home:6443); ALWAYS a throwaway namespace (`kubectl --context theborg create ns harvest-live-<date>`), never `cicd`/`concourse`; `t.Cleanup` deletes pods, then delete the namespace. Colima/Docker is usually down on this machine — testcontainers is not an option; use theborg. Command:

```bash
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=harvest-live-<date> \
  go test -tags live -run '^TestLiveHarvestCredentialIsolation$' -v -count=1 -timeout 5m \
  ./atc/worker/jetbridge/
```

Before any real push test against a live repo: the per-repo secret `agent-harvest-git-<slug>` is created **manually by an admin** (§8.3 — no API writes it); use a scratch repo, never a live one, and a PAT restricted to `agent/*` branches where the host supports it.

**Rollback notes for the risky diffs:**
- **Migration 1773106080** is additive (nullable columns + partial indexes); `.down.sql` drops exactly what it created. Existing review publishing is untouched (upsert key unchanged; linkage preserved via COALESCE).
- **jetbridge `SecretMounts` (Task 11)** is this plan's only change on the shared pod-construction path (the `applySecretRefs` append, `SidecarEnv`/`SidecarSecretEnv`, and the pause loop land earlier via agent-step 07 Task 11B — narrowed 2026-07-09). It is inert when `SecretMounts` is empty (every existing caller). To roll back: revert the `buildPod` hunk and the `runtime` type — no data migration involved. The main-container-only invariant is pinned by the Task 11 spec asserting sidecars carry no secret mount.
- **Step-union changes (Task 6)** follow the exhaustive `StepVisitor` pattern — a revert is a clean removal of the `harvest` detector + visitor methods; pipelines containing `harvest:` steps then fail config validation loudly rather than misparsing.
- **Ticket transitions (Task 13)** go exclusively through `tickets.Store.Transition` with `from=running` guards — a double-running harvest (retry/resume) makes the second transition fail `ErrInvalidTransition` and logs, never corrupts state. Evidence upsert is idempotent on `(build_id, repo, commit_sha)`.
- **The shared image change (Task 10)** adds a second binary to `deploy/agent-runner/Dockerfile`; the live theborg review job pins its image by tag, so nothing changes for it until a new tag is built and referenced. If the image build breaks, the previous tag keeps working.
- **Judge spend**: capped by `JudgeConfig.BudgetUSD` post-hoc and funded by the platform credential — if the judge misbehaves in cost terms, remove the `judge:` block from the workflow definition/pipeline (config-only rollback); gates and push are unaffected.

## Amendment log (this plan)

- **2026-07-17 (harvest v0 pulled forward, owner-approved — landed on `jetbridge` ahead of wave 3):** the deliverable-unstranding core exists: §2.8.1 `atc.HarvestStep`/`HarvestPlan` + visitors (Task 6 shape), `agent/harvest` policy/Config types + `GitCredSecretName` (Task 5 subset), a v0 `harvest-runner` in the agent-step image (verify committed clean workspace per F33, push-by-sha `--force-with-lease`, §2.8.1 exit taxonomy — Task 7/10 subset), `runtime.ContainerSpec.SecretMounts` with jetbridge main-container-only mounting (Task 11), `exec.HarvestStep` with the linkage-guarded ticket transition (Tasks 12/13 subset — no metrics/reviews/evidence ingestion yet), engine/atccmd wiring (Tasks 14/15), and renderer emission from dispatch. NOT built (this plan's remaining scope): the gates engine + dev-mcp invocation (Task 8), judge (Task 9), reviews/feedback ticket linkage + migration 1773106080 (Tasks 2–4), flight-recorder evidence outputs, Elm rendering (Task 16), fixture e2e (Task 17). Deviations to reconcile at execution: v0 REFUSES gate/judge/dev_mcp configs at exec and runner (replace refusals with real execution); the runner is exec-assembled config via HARVEST_CONFIG env exactly as §2.8.1 specifies but writes results to stdout only (no flight dir yet); worktree cleanliness is enforced but records no evidence payload.
