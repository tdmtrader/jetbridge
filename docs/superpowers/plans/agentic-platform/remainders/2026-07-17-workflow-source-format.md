# Workflow Source Format + Skills — Remainder Plan (slice-a close-out + slice b)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Slice (a) — the entire committed plan `docs/superpowers/plans/2026-07-17-workflow-source-format.md` — is ALREADY LANDED (ten commits, `6ccb6c2162`..`187cad4926`); do NOT re-execute it. This plan carries the full text of everything that remains: the slice-(a) contract amendment and cutover runbook, and all of slice (b).

**Date:** 2026-07-17
**Status:** draft for review
**Depends-on:**
- Spec: `docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md` (approved at `b88d124540`; amended at `187cad4926` — grammar realization `prompt_files` map + the `1773106066` migration note)
- LANDED slice (a): plan `docs/superpowers/plans/2026-07-17-workflow-source-format.md` (commit `2b1c0e8dd5`), executed in full — see "Landed state" below
- Landed platform base: workflow store v1 (plan 05), manual dispatch + fly UX round, harvest v0 + gates v0.5, ticket-core (all on `jetbridge` as of 2026-07-17)
- Normative: `00-shared-contracts.md` (§1.6, §2.2, §2.8, §6, §8.1, §11), `CONVENTIONS.md` (C1/C2/C3), `ROADMAP.md`

**Goal:** Close out the landed slice (a) — append the missing shared-contracts amendment and run the one-time theborg hash-cutover — then land slice (b) materialization end-to-end: system-prompt/context plumbing, skill materialization into agent pods, runner discovery mapping with shadow logging, the example deploy pipeline, the slice-(b) contract amendment, and a live skill-bearing dispatch on theborg.

**Architecture:** Slice (a) (landed) made a `Manifest` (path→content map) the import/hash unit with `workflow.Compile` producing a self-contained `Config` (`ContextFiles`/`SkillFiles` resolutions, `prompt_files` inlined), stored in the nullable `source_manifest JSONB` column with compile-on-read; `dispatch.Render` refuses every source-format surface via `Config.SourceFormatField()`. Slice (b) moves that refusal boundary surface-by-surface, never before the consumer exists: the renderer resolves system-prompt layers and concatenates context into literal `atc.AgentStep` fields (§2.8 render-time-resolution preserved — the step exec still never reads workflow tables), materializes skill trees via a base64 write-task (same mechanism family as `writeTicketTask`), and the runner maps skills into claude's cwd discovery location (`.claude/skills/`), appends the system-prompt layer, and injects context at session start.

**Tech stack:** Go, goccy/go-yaml, plain `testing` in `agent/dispatch` + `agent/runner`, testify/suite table-driven in `atc/builds` + `atc` steps tests, Ginkgo-with-fakes in `atc/exec` (no postgres in any slice-(b) suite), fly against concourse.home for the live legs. No Elm anywhere in this item. No migration remains (slice (a)'s `1773106066` is landed; slice (b) is DB-free).

---

## Landed state (do NOT rebuild any of this)

Verified against the working tree at `187cad4926` (current HEAD; the shared-ground-state "HEAD `b88d124540`, no plan, no code" is two generations stale — the slice-(a) plan landed at `2b1c0e8dd5` and its execution landed the same evening):

**Slice (a) — ENTIRELY landed, one commit per committed-plan task:**

| Committed-plan task | Commit | What is in the tree now |
|---|---|---|
| T1 Manifest type | `6ccb6c2162` | `agent/workflow/manifest.go` — files map, `Validate`/`Canonical`/`Hash`/`Paths`, caps 512 files / 1 MiB file / 10 MiB total, path rules incl. dot-segment refusal |
| T2 schema_version-2 grammar | `9890b56b7a` | `agent/workflow/config.go:31-47` (`PromptFiles`, `Skills`, `SystemPrompt(File)`, `Context`, compile-populated `ContextFiles`/`SkillFiles`), step-level fields `:86-89`, `SourceFormatField()` `:138`; hooks rejected top-level AND step-level; v1-doc gate `parse.go:74` |
| T3 Compile | `3e40ae5390` | `agent/workflow/compile.go` — manifest→self-contained Config, prompt-file template validation, unreferenced files allowed |
| T4 dirload | `0ad01deabc` | `agent/workflow/dirload.go` — `ManifestFromDir` (hidden-skip + symlink deref), `DiscoverWorkflowDirs` |
| T5 Store.ImportManifest | `80dd045c2e` | `Store.ImportManifest`, `Definition.SourceManifest`, MemoryStore, manifest-hash identity (raw `Import` = single-file wrap — the hash-scheme change) |
| T6 migration + factory | `ac9347c9aa` | `1773106066_add_agent_workflow_source_manifest.{up,down}.sql` (nullable JSONB, no backfill, clean down); BOTH dual constants bumped and verified: `atc/db/migration/legacy_upgrade_test.go:37` = `1773106066`, `docs/migration/migrate-preflight.sh:38` = `1773106066`; factory compile-on-read with legacy Parse path (`atc/db/agent_workflows_factory.go` — `source_manifest` in the INSERT `:81` and SELECT `:139`, C3 add-alongside honored) |
| T7 JSON import body | `25ceb6e7fa` | `agent/api/workflows/handler.go:142-163` — content-type switch on the EXISTING route, 12 MiB envelope (`maxManifestRequestBytes` `:19`), raw-YAML path preserved. No new routes; C1 never fired |
| T8 render refusal | `2db630c3e9` | `agent/dispatch/render.go:163-170` — `SourceFormatField()` refusal after the judge refusal, refuse-don't-drop family; test `render_test.go:384` `TestRenderRefusesSourceFormatSurfaces` |
| T9 fly | `550a8dbd7a` | `fly/commands/agent_workflows.go` — dir/file import via `DiscoverWorkflowDirs`/`ManifestFromDir` (`:207`, `:222`), `--set-live` (`:190`), manifest summary in `show` |
| T10 spec amendment | `187cad4926` | Spec records the `prompt_files:` sibling-map grammar realization + the migration note (the old open question D2 is now a committed decision — owner reviews the commit, not a proposal) |

**Also landed and relevant:**
- **Agent step chain (slice-(b) change surface, still WITHOUT SystemPrompt/Context/Skills):** `atc.AgentStep` (`atc/steps.go:403-422`; `OutputSchema` field at `:410`) / `atc.AgentPlan` (`atc/plan.go:415-430`; `OutputSchema` at `:422`) / planner `VisitAgent` (`atc/builds/planner.go:105-124`) / exec §8.1 env assembly (`atc/exec/agent_step.go:346-383`; prompt block ends `:371`, `AGENT_FLIGHT_DIR` at `:372`) / runner `Config` (`agent/runner/runner.go:49-75`, has `Stderr io.Writer` at `:61`), `FromEnv` (`:82-130`), `Run` (`:147`; prompt guard `:181-183`; claude args `:244-257`). Verified: none of these carry SystemPrompt/Context/Skills.
- **Dispatch renderer mechanics slice (b) extends:** `RenderAgentStep` (`render.go:55-114`; model/maxTurns defaults `:82-89`; env-map build `:91-101`; returned `atc.AgentStep{}` literal `:103-113`), `Render` (`:124`; refusal chain `spec_delivery`/gate_policy/hitl/judge/source-format `:125-170`), `writeTicketTask` base64 write-task (`:326`), `renderPrompt` §6.2 template execution. `render_test.go` is plain Go with the `renderInput()` helper at `:15`; `agent/runner/runner_test.go` has `writeStubClaude` at `:21`.
- **Unrelated same-evening work:** `0866d89fc9` (agentic-UI waves E+F, Elm) — no interaction with this item.

**NOT landed (the remainder this plan covers):**
- The shared-contracts amendment for slice (a) — `git log` on `00-shared-contracts.md` still ends at the harvest-v0.5 entry; §6/§1.6/§2.2 do not mention schema_version 2, `source_manifest`, or `ImportManifest`. Every landed 2026-07-17 slice appended its §11 entry; slice (a) landed without one (the committed plan's only docs task amended the SPEC).
- The one-time theborg hash-scheme cutover (operational; needs the slice-(a) web image deployed first).
- All of slice (b): `SourceFormatField()` still refuses everything at render; no `AGENT_SYSTEM_PROMPT`/`AGENT_CONTEXT`/`AGENT_SKILL_DIRS` anywhere; runner has no skills install, no `--append-system-prompt`, no context injection.
- The example deploy pipeline (spec §6) and the live skill-bearing smoke (spec §9).

## Scope

**In:**
- Slice (a) close-out: the contract amendment (Task 1) and the theborg hash-cutover runbook (Task 2).
- Slice (b): system_prompt + context — schema + exec transport, inert until the renderer populates (Task 3a) then renderer resolution + runner consumption + the first refusal relaxation (Task 3b); runner skills mapping + schema/exec plumbing (Task 4); renderer skill materialization + final refusal removal (Task 5); example deploy pipeline (Task 6); slice-(b) contract amendment (Task 7); live theborg smoke (Task 8).

**Out (deferred, with where it lives):**
- Hooks (`hooks:` stays an import-time rejection; own future design — spec §7).
- Import-by-git-reference (documented alternate body if ever wanted — spec §2).
- Binary (non-UTF-8) files in skills; OCI artifacts; multi-env overlays (spec §7).
- The authenticated **fetch-by-version materialization endpoint** — named fallback if the base64 write-task path hits arg-size limits (spec §4). NOT built in this plan; slice (b) instead REFUSES oversized skill sets at render (Task 5). If it is ever built, CONVENTIONS **C1's six-touchpoint add-a-route checklist fires** (two panicking switches included) — the base64 path avoids C1 entirely, which is why we try it first.
- Any change to promotion semantics, the store's versioning model, or how the render path consumes compiled Definitions (spec §7).
- Elm/web UI surfaces: none exist for this item (fly `show` summary is terminal text, already landed).

## Slices

### Slice (a-close) — contract amendment + cutover (Tasks 1–2)

Docs and operations only; zero code. Landing discipline debt: the slice-(a) code merged without its §11 entry.

Verification story:
- **Gate-verifiable:** nothing (no Go changes).
- **Local-verify:** grep checks inside Task 1 (amendment text present at all five anchor points: §1.1, §6, §1.6, §2.2, §11).
- **Live-verify (theborg):** Task 2 — one-time re-import mints exactly one new version per live workflow (hash scheme moved from raw-YAML bytes to canonical single-file manifest); re-set-live; dispatch still works.

### Slice (b) — materialization (Tasks 3–8)

Renderer materializes; runner maps; refusals relax surface-by-surface (never before their consumer is end-to-end); example pipeline published; live-proven. Ordering is load-bearing: Task 3a → Task 3b → Task 4 → Task 5 strictly sequential. Task 3a lands the schema + exec transport INERT (renderer does not populate the fields, refusal unchanged); Task 3b lands the renderer producer + runner consumer AND narrows the refusal in the same commit (never a window where a relaxed surface has no end-to-end consumer). Task 4 lands the skills CONSUMER before Task 5 emits skills and lifts the last refusal.

Verification story:
- **Gate-verifiable** (pure Go; the loop's `go build ./...` + `go test ./...` + `go vet ./...` cover it): Tasks 3a–5 in full — plain `testing` in `agent/dispatch`/`agent/runner`, testify/suite table-driven in `atc/builds`/`atc` steps tests, Ginkgo-with-fakes in `atc/exec`; no suite touched needs postgres.
- **Local-verify:** closing `make test-quick` per merge (PostgreSQL required for the full unit tier even though the touched suites don't need it); Task 6's `fly validate-pipeline` run.
- **Live-verify (theborg):** Task 8 — dispatch a skill-bearing `analyze` variant; observe skill installation + shadow log lines in the build log and skill-informed agent output. The Task 6 example deploy pipeline is only meaningfully exercised against the live cicd env (optional live leg noted in Task 6).

---

## Tasks

### Task 1 [slice a-close]: Contract amendment — §1.1 / §6 / §1.6 / §2.2 + the §11 entry

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`

Landing discipline: every landed slice to date appended an SS11 entry in (or immediately after) its landing commit; slice (a) merged without one. Land this before anything else in this plan — it is the record downstream workstreams (dispatch, platform-mcp, harvest) read.

- [ ] **Step 1: Amend §6 (workflow definition grammar, ~:1376-1554).** After the existing v1 grammar block, append:

```markdown
**schema_version 2 additions (2026-07-17 source-format slice (a), landed
`6ccb6c2162`..`187cad4926` — additive per the slot-shape freeze rule below;
v1 documents parse unchanged, and any document using a v2 field MUST declare
`schema_version: 2`):** top-level `prompt_files` (map prompt-key → manifest
path; a key may not appear in both `prompts` and `prompt_files`; content is
template-validated and inlined into `Prompts` at compile), `skills` (list of
names resolving to `skills/<name>/` trees, each requiring
`skills/<name>/SKILL.md`), `system_prompt` / `system_prompt_file` (mutually
exclusive; appended to the runner's provider baseline), `context` (list of
manifest paths); step-level `skills` (ADDITIVE to the workflow-global set),
`system_prompt` / `system_prompt_file` (REPLACES the workflow-level layer
only, never the baseline), `context` (additive). A `hooks:` key — top-level
or step-level — is an import-time ERROR (refuse-don't-drop; hooks are a
deferred design). Compile-populated resolutions `ContextFiles` / `SkillFiles`
are never authorable in YAML. Import transport: JSON files-map manifest
`{"files": {path: content}}` on the existing per-name versions route
(content-type switch, 12 MiB envelope); raw-YAML bodies wrap to the
single-file manifest `{"workflow.yml": <bytes>}`. Grammar realization note:
`prompt_files` is a sibling map, not a string-or-object union under
`prompts:` (implementation decision recorded in the spec at `187cad4926`).
```

- [ ] **Step 2: Amend §1.6 (`agent_workflow_definitions`, ~:185-210).** Append:

```markdown
**2026-07-17 source-format slice (a) (landed, migration `1773106066`):** the
version storage gains a nullable `source_manifest JSONB` column holding the
canonical imported files map. `content_hash` scheme changes from
`sha256(raw YAML bytes)` to `sha256(canonical manifest serialization)` —
uniform for raw-YAML imports via the single-file wrap. One-time consequence:
each pre-slice workflow mints exactly one new version on its next import;
**`source_manifest IS NOT NULL` is the row-level scheme lineage marker**
(NULL rows carry the legacy raw-bytes hash and keep the Parse-on-read path;
no backfill; clean down migration). Import idempotence on `content_hash` and
the per-name `pg_advisory_xact_lock` serialization are unchanged.
```

Then, matching the file's inline-DDL convention (every other migration entry
quotes its SQL — e.g. the §1.7/§1.14 `CREATE TABLE` blocks), append the
literal landed migration as its own fenced block immediately below the prose:

```sql
ALTER TABLE agent_workflow_definitions ADD COLUMN source_manifest JSONB;
```

- [ ] **Step 3: Amend §2.2 (workflow-store Go surface, ~:598).** Append to the frozen-surface list:

```markdown
**2026-07-17 source-format slice (a) additions (landed):**
`workflow.Manifest` (path→content map; `Validate`/`Canonical`/`Hash`/`Paths`;
caps 512 files, 1 MiB/file, 10 MiB total; path rules: relative, no
`..`/`.`/empty/hidden segments, no backslash, UTF-8 only),
`workflow.Compile(Manifest) (*Config, error)`, `workflow.ManifestFromDir` /
`DiscoverWorkflowDirs`, `Definition.SourceManifest` (populated by Get/Live,
empty in List/Versions, nil for legacy rows), and
`Store.ImportManifest(name, m, createdBy)` (idempotent on the
canonical-manifest hash); `Store.Import` becomes the degenerate single-file
wrap. Consumers compile-checked: `atc/api/handler.go` wiring,
`mcpserver.RegisterTools`; dispatch consumes only the `WorkflowResolver`
subset (Get/Live) and is unaffected.
```

- [ ] **Step 4: Append the §11 amendment-log entry** (at the current log tail — re-read it at commit time and append after whatever entry is now LAST; all five remainder plans append to this §11 tail, so treat it as single-writer per merge window and never pin to a fixed line/entry):

```markdown
- 2026-07-17 (workflow source format slice (a) — design
  `2026-07-17-workflow-source-format-and-skills-design.md`, landed
  `6ccb6c2162`..`187cad4926`; this entry appended post-landing — the slice
  merged without it, a discipline slip, not a design change; affects:
  workflow-store, dispatch, fly, agent-api): §6 gains the schema_version 2
  additive grammar (prompt_files sibling map, skills, system_prompt(_file),
  context; step-level additive skills/context and layer-replacing
  system_prompt; `hooks:` an import-time error at BOTH levels) — landed as a
  new schema_version per the slot-shape freeze rule, v1 untouched. §1.6:
  nullable `source_manifest JSONB` (migration `1773106066`; dual constants
  bumped per C2 in the same commit `ac9347c9aa`), content-hash scheme moves
  to sha256(canonical manifest) with `source_manifest IS NOT NULL` as the
  row-level lineage marker; import idempotence + advisory-lock serialization
  preserved (C3 add-alongside). §2.2: `Manifest`/`Compile`/`ManifestFromDir`,
  `Definition.SourceManifest`, `Store.ImportManifest` (raw `Import` =
  single-file wrap). Transport: JSON files-map body on the existing per-name
  import route (content-type switch, 12 MiB envelope) — NO new routes (C1
  did not fire). Render boundary: dispatch REFUSES definitions whose
  compiled Config uses any source-format surface (`SourceFormatField()`,
  same refuse-don't-drop rule as gate_policy/hitl/judge) until slice (b)
  materializes them; fly gains directory import (hidden-file exclusion +
  symlink deref at packaging), `--set-live`, and a manifest summary in
  `show`. Server-side Validate additionally REFUSES dot-prefixed path
  segments (hardening beyond the spec's fly-side exclusion — a skill cannot
  ship a dotfile asset in v1).
```

- [ ] **Step 5: Amend §1.1 (the frozen Wave/number allocation table, ~:38).** The `1773106060–69` row currently names occupants only through `1773106065` and does not record migration `1773106066` at all — leaving the canonical registry (the document a future workstream planner consults FIRST) stale about who owns `1773106066` and how much of the block is free. Replace that row so it records `source_manifest` and the true free range (occupants unchanged through `1773106065`):

```markdown
| 1773106060–69 | agent-step + ticket-core + workflow-store (interleaved per the 2026-07-17 renumbers, §11) | `agent_run_metrics` (`1773106060`), parked status + `session_id` (`1773106061`, PARK-V2), `agent_tickets` (`1773106062`), `agent_ticket_specs` (`1773106063`), `agent_ticket_tasks` (`1773106064`), `agent_run_step_state` (`1773106065`, PARK-V2, deferred/Task 25 — RESERVED, stranded below head), `source_manifest` on `agent_workflow_definitions` (`1773106066`, workflow-store slice (a), landed `ac9347c9aa`). **Free in this block: `1773106067–69` only** (next slot `1773106067`); `1773106070–79` is platform-mcp-hitl's reserved block. |
```

- [ ] **Step 6: Verify + commit**

```bash
grep -n "source_manifest IS NOT NULL" docs/superpowers/plans/agentic-platform/00-shared-contracts.md
# expect: two hits (§1.6 + §11)
grep -c "schema_version 2" docs/superpowers/plans/agentic-platform/00-shared-contracts.md
# expect: >= 2 (§6 + §11)
grep -c "1773106066" docs/superpowers/plans/agentic-platform/00-shared-contracts.md
# expect: >= 3 (§1.1 row + §1.6 + §11)
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(contracts): workflow source format slice (a) — §1.1 registry, §6 v2 grammar, §1.6 manifest column + hash scheme, §2.2 store surface, §11 entry"
```

---

### Task 2 [slice a-close]: theborg hash-cutover runbook (one-time, post-deploy)

**Files:** none (operational; findings go to project memory if anything surprises).

On the first import after slice (a) deploys, every existing workflow mints exactly one new version (hash scheme moved from raw-YAML bytes to canonical single-file manifest). Live workflows on theborg: `smoke`, `analyze`, `develop`.

- [ ] **Step 1: Confirm deployment** — the web image running on theborg contains slice (a) (check the release tag against the jetbridge pipeline; dispatch-timing rule: push → settle → then act).
- [ ] **Step 2: Snapshot current versions**

```bash
fly -t cicd agent workflows list
# note each name's version + live marker
```

- [ ] **Step 3: Re-import each live workflow from its current source** (unchanged bytes)

```bash
fly -t cicd agent workflows import <path-to>/smoke.yaml
fly -t cicd agent workflows import <path-to>/analyze.yaml
fly -t cicd agent workflows import <path-to>/develop.yaml
# expected: each mints exactly ONE new version (hash-scheme change), e.g. smoke v3 -> v4
# re-running the same import again returns the SAME new version (idempotence under the new scheme)
```

- [ ] **Step 4: Promote**

```bash
fly -t cicd agent workflows set-live smoke <new-version>
fly -t cicd agent workflows set-live analyze <new-version>
fly -t cicd agent workflows set-live develop <new-version>
fly -t cicd agent workflows show smoke   # expect: manifest summary section (workflow.yml, sizes)
```

- [ ] **Step 5: Prove dispatch still works** — queue + dispatch a cheap smoke ticket against the re-imported live version; expect a normal run (v1 documents carry no source-format surfaces, so no refusal fires).

```bash
fly -t cicd agent tickets create --repo tdmtrader/jetbridge --title "post-cutover smoke" --workflow smoke --queue --dispatch
fly -t cicd agent tickets watch <id>
# expected: run completes as before the deploy
```

- [ ] **Step 6: Record** — append the cutover date + minted versions to the ops note in project memory. Lineage needs no per-row action: `source_manifest IS NOT NULL` marks the new-scheme rows (Task 1 §1.6 text).

---

### Task 3a [slice b]: system_prompt + context — schema + exec transport (inert until Task 3b's renderer populates)

Lands FIRST in slice (b) (spec §8). Adds the `SystemPrompt`/`Context` fields to `AgentStep`/`AgentPlan` and the §8.1 exec env emission — but the renderer does NOT populate them yet and the render refusal is UNCHANGED (a `system_prompt`/`context`/`skills` workflow still refuses at render). So after 3a these fields are INERT plumbing: nothing sets them, so nothing is emitted and nothing is silently dropped. Task 3b lands the renderer producer + runner consumer + refusal narrowing atomically. This mirrors the Task 4→5 ordering (land the consumer before the refusal lifts). 7 files, within the ticket-#14 envelope.

**Files:**
- Modify: `atc/steps.go` (AgentStep, `:403-422`; `OutputSchema` at `:410`), `atc/plan.go` (AgentPlan, `:415-430`; `OutputSchema` at `:422`), `atc/builds/planner.go` (VisitAgent, `:105-124`)
- Modify: `atc/exec/agent_step.go` (§8.1 env assembly, insertion after `:371`)
- Test: `atc/steps_test.go` ("agent step" case `:278`), `atc/builds/planner_test.go` ("agent step" case `:1541`), `atc/exec/agent_step_test.go` (§8.1 spec `:221`)

- [ ] **Step 1: Schema — `atc/steps.go`** (append to `AgentStep` after `OutputSchema`, `:410`):

```go
	// SystemPrompt is the step's EFFECTIVE workflow system-prompt layer,
	// resolved by the renderer (step-level replaces the workflow layer;
	// design 2026-07-17 §1). The runner APPENDS it to its provider's
	// baseline system prompt — never replaces the baseline.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Context is the step's effective workflow context, concatenated
	// with per-file headers by the renderer; the runner injects it at
	// session start.
	Context string `json:"context,omitempty"`
```

`atc/plan.go` — append the same two fields to `AgentPlan` after `OutputSchema` (`:422`):

```go
	SystemPrompt string `json:"system_prompt,omitempty"`
	Context      string `json:"context,omitempty"`
```

`atc/builds/planner.go` `VisitAgent` (`:106-121`) — add to the `atc.AgentPlan{...}` literal:

```go
		SystemPrompt:   step.SystemPrompt,
		Context:        step.Context,
```

Extend the "agent step" table case in `atc/steps_test.go` (`:278`): add `system_prompt: be careful` and `context: "## x\n\nbody"` to the YAML and the matching fields on the expected `&atc.AgentStep{...}`. Extend the "agent step" planner case in `atc/builds/planner_test.go` (`:1541`): set both fields on the step config and expect them copied onto the `AgentPlan`.

- [ ] **Step 2: Exec env rows — `atc/exec/agent_step.go`** (after the `AGENT_PROMPT`/`AGENT_PROMPT_FILE` block closing at `:371`, before the `AGENT_FLIGHT_DIR` append at `:372`):

```go
	if step.plan.SystemPrompt != "" {
		env = append(env, "AGENT_SYSTEM_PROMPT="+step.plan.SystemPrompt)
	}
	if step.plan.Context != "" {
		env = append(env, "AGENT_CONTEXT="+step.plan.Context)
	}
```

Extend the existing `It("builds the container spec per the s8.1 env contract", ...)` (`atc/exec/agent_step_test.go:221`): set `SystemPrompt: "be careful"` and `Context: "## x\n\nbody"` on the agent plan fixture used by that spec, and add:

```go
			Expect(spec.Env).To(ContainElements(
				"AGENT_SYSTEM_PROMPT=be careful",
				"AGENT_CONTEXT=## x\n\nbody",
			))
```

(These env rows never fire until Task 3b's renderer sets the plan fields; the test sets them on the fixture directly, so 3a is self-contained.)

- [ ] **Step 3: Run + commit**

```bash
go test ./atc/ -run TestSteps -count=1 && go test ./atc/builds/ -count=1 && go test ./atc/exec/ -count=1 && go build ./...
git add atc/steps.go atc/steps_test.go atc/plan.go atc/builds/planner.go atc/builds/planner_test.go \
        atc/exec/agent_step.go atc/exec/agent_step_test.go
git commit -m "feat(agent): AgentStep system_prompt/context fields + exec env transport (inert; slice b)"
```
Expected: PASS / clean build. The landed `TestRenderRefusesSourceFormatSurfaces` still passes unchanged — the render refusal is untouched in 3a.

---

### Task 3b [slice b]: system_prompt + context — renderer resolution + runner consumption + the first refusal relaxation

Depends on Task 3a (the `AgentStep`/`AgentPlan` fields + exec env emission exist but are inert). This task lands the PRODUCER (the renderer resolves the system-prompt layering and concatenates context, populating the fields) and the CONSUMER (the runner reads `AGENT_SYSTEM_PROMPT`/`AGENT_CONTEXT`, appends `--append-system-prompt`, injects context at session start) and — in the SAME commit — narrows the render refusal so `prompt_files`/`system_prompt`/`context` dispatch. **Skills remain refused** (they still need Tasks 4–5). Because the refusal narrows in the same commit that completes the renderer→exec→runner chain end-to-end, there is no window where a relaxed surface silently drops authored behavior (refuse-don't-drop). This also retires the landed slice-(a) over-refusal of `prompt_files` (old open decision D1): compile fully inlines them, so once this task narrows the probe they dispatch. 4 files, within the ticket-#14 envelope.

**Files:**
- Modify: `agent/dispatch/render.go` (`RenderAgentStep` resolution after model/maxTurns `:82-89`, set on the returned struct `:103-113`; REPLACE the `SourceFormatField()` refusal `:163-170`; helpers at bottom)
- Modify: `agent/runner/runner.go` (Config `:49-75`, FromEnv `:82-130`, Run `:147+`, claude args `:244-249`)
- Test: `agent/dispatch/render_test.go` (reuse `renderInput()` at `:15`; REPLACE `TestRenderRefusesSourceFormatSurfaces` at `:384`), `agent/runner/runner_test.go` (add a stub alongside `writeStubClaude` at `:21`)

- [ ] **Step 1: Write the failing renderer tests** (append to `render_test.go`)

```go
func TestRenderAgentStepResolvesSystemPromptAndContext(t *testing.T) {
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.SystemPrompt = "workflow layer"
	in.Workflow.Context = []string{"context/conventions.md"}
	in.Workflow.ContextFiles = map[string]string{
		"context/conventions.md": "conventions body\n",
		"context/tdd.md":         "tdd body\n",
	}
	in.Workflow.Steps[0].Context = []string{"context/tdd.md", "context/conventions.md"} // dup dropped

	step, err := dispatch.RenderAgentStep(in, in.Workflow.Steps[0])
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if step.SystemPrompt != "workflow layer" {
		t.Fatalf("workflow system prompt not resolved: %q", step.SystemPrompt)
	}
	want := "## context/conventions.md\n\nconventions body\n\n## context/tdd.md\n\ntdd body"
	if step.Context != want {
		t.Fatalf("context concatenation:\ngot  %q\nwant %q", step.Context, want)
	}

	// step-level system prompt REPLACES the workflow layer (never the baseline)
	in.Workflow.Steps[0].SystemPrompt = "step layer"
	step, err = dispatch.RenderAgentStep(in, in.Workflow.Steps[0])
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if step.SystemPrompt != "step layer" {
		t.Fatalf("step system prompt must replace workflow layer: %q", step.SystemPrompt)
	}
}

func TestRenderAgentStepUnresolvedContextIsAnError(t *testing.T) {
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Context = []string{"context/missing.md"} // no ContextFiles resolution
	if _, err := dispatch.RenderAgentStep(in, in.Workflow.Steps[0]); err == nil ||
		!strings.Contains(err.Error(), "context/missing.md") {
		t.Fatalf("unresolved context must error, got %v", err)
	}
}

func TestRenderRefusesOversizedContext(t *testing.T) {
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Context = []string{"context/huge.md"}
	in.Workflow.ContextFiles = map[string]string{"context/huge.md": strings.Repeat("a", 256*1024+1)}
	if _, err := dispatch.RenderAgentStep(in, in.Workflow.Steps[0]); err == nil ||
		!strings.Contains(err.Error(), "context") {
		t.Fatalf("oversized context must refuse, got %v", err)
	}
}
```

Then REPLACE the landed `TestRenderRefusesSourceFormatSurfaces` (`render_test.go:384-406`) with the narrowed boundary:

```go
func TestRenderRefusesUnmaterializedSkills(t *testing.T) {
	// prompt_files/system_prompt/context are materialized as of the
	// system-prompt/context slice-b task; ONLY skills remain refused
	// until materialization + runner mapping land.
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.SystemPrompt = "sp"
	in.Workflow.PromptFiles = map[string]string{}
	if _, err := dispatch.Render(in); err != nil {
		t.Fatalf("system_prompt must now render: %v", err)
	}

	in = renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Skills = []string{"tdd"}
	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "skills") {
		t.Fatalf("workflow-level skills must refuse: %v", err)
	}

	in = renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Steps[0].Skills = []string{"extra"}
	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "skills") {
		t.Fatalf("step-level skills must refuse: %v", err)
	}
}
```

- [ ] **Step 2: Run to see them fail**

```bash
go test ./agent/dispatch/ -run 'TestRender' -v -count=1
```
Expected: FAIL — the new renderer tests fail because `RenderAgentStep` does not yet resolve `SystemPrompt`/`Context` (the fields exist from Task 3a but the renderer leaves them empty), and the replaced refusal test fails because `Render` still refuses ALL source-format surfaces.

- [ ] **Step 3: Renderer — `agent/dispatch/render.go`**

In `RenderAgentStep`, after the model/maxTurns defaults block (`:82-89`):

```go
	systemPrompt := step.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = in.Workflow.SystemPrompt
	}
	contextText, err := renderContext(in.Workflow, step)
	if err != nil {
		return atc.AgentStep{}, fmt.Errorf("agent step %q: %w", step.Agent, err)
	}
```

and set on the returned struct (`:103-113`): `SystemPrompt: systemPrompt, Context: contextText,`.

Add at the bottom of render.go:

```go
// maxContextBytes bounds the concatenated per-step context: it travels
// as a literal pod env var (§8.1 AGENT_CONTEXT) inside the pod spec, so
// an unbounded value would fail at the kubelet/etcd instead of at
// render. Refuse-don't-drop: over the bound is a render error, never a
// truncation.
const maxContextBytes = 256 * 1024

// renderContext concatenates the step's effective context files
// (workflow-global list then step additions — additive, duplicates
// dropped on first occurrence; design 2026-07-17 §1) into one markdown
// document with per-file `## <path>` headers, resolved from the
// compiled Config's ContextFiles (§2.8 render-time resolution: the
// step exec never reads workflow tables).
func renderContext(cfg workflow.Config, step workflow.Step) (string, error) {
	paths := append(append([]string{}, cfg.Context...), step.Context...)
	seen := map[string]bool{}
	var b strings.Builder
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		content, ok := cfg.ContextFiles[p]
		if !ok {
			// Compile guarantees resolution; a miss means a hand-built or
			// pre-compile definition reached the renderer.
			return "", fmt.Errorf("context file %q is not resolved in the compiled definition", p)
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", p, strings.TrimRight(content, "\n"))
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(out) > maxContextBytes {
		return "", fmt.Errorf("step context is %d bytes (max %d): trim the context files or split the step", len(out), maxContextBytes)
	}
	return out, nil
}
```

In `Render`, REPLACE the landed `SourceFormatField()` refusal block (`:163-170` — comment + `if` block) with:

```go
	// Slice-b partial relaxation (design 2026-07-17 §4/§8): prompt_files
	// (inlined at compile), system_prompt, and context are fully
	// materialized by the renderer above. Skills still need workspace
	// materialization + runner discovery mapping and stay refused —
	// rendering them now would silently drop authored behavior.
	if field := skillsInUse(in.Workflow); field != "" {
		return atc.Config{}, fmt.Errorf("workflow %q declares %s: render does not yet materialize skills into agent pods — remove them or wait for the skills-materialization slice", in.WorkflowName, field)
	}
```

with the helper:

```go
func skillsInUse(cfg workflow.Config) string {
	if len(cfg.Skills) > 0 {
		return "skills"
	}
	for _, s := range cfg.Steps {
		if len(s.Skills) > 0 {
			return fmt.Sprintf("step %q skills", s.Agent)
		}
	}
	return ""
}
```

(`Config.SourceFormatField()` keeps its OTHER consumer — the v1-doc gate at `agent/workflow/parse.go:74` — untouched. Only the render.go call site changes.)

- [ ] **Step 4: Run renderer tests to pass**

```bash
go test ./agent/dispatch/ -count=1
```
Expected: PASS (the schema + steps/planner/exec suites already passed in Task 3a).

- [ ] **Step 5: Runner — failing tests first** (append to `agent/runner/runner_test.go`; add an args-recording stub helper alongside `writeStubClaude` at `:21`):

```go
func writeArgStubClaude(t *testing.T, dir, envelope string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/claude-args.txt\"\necho '" + envelope + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFromEnvReadsSystemPromptAndContext(t *testing.T) {
	t.Setenv("AGENT_SYSTEM_PROMPT", "be careful")
	t.Setenv("AGENT_CONTEXT", "## x\n\nbody")
	cfg := runner.FromEnv()
	if cfg.SystemPrompt != "be careful" || cfg.Context != "## x\n\nbody" {
		t.Fatalf("system prompt/context not read: %+v", cfg)
	}
}

func TestRunAppendsSystemPromptAndInjectsContext(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude := writeArgStubClaude(t, dir,
		`{"type":"result","subtype":"success","result":"\"ok\"","is_error":false}`)

	cfg := runner.Config{
		Prompt:       "do it",
		SystemPrompt: "be careful",
		Context:      "## x\n\nbody",
		FlightDir:    flight,
		WorkDir:      dir,
		StepName:     "s",
		ClaudePath:   claude,
	}
	if exit, err := runner.Run(context.Background(), cfg); err != nil || exit != 0 {
		t.Fatalf("run: exit=%d err=%v", exit, err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "claude-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "--append-system-prompt\nbe careful") {
		t.Fatalf("system prompt not appended:\n%s", args)
	}
	if !strings.Contains(args, "<workflow-context>") || !strings.Contains(args, "## x\n\nbody") ||
		!strings.Contains(args, "do it") {
		t.Fatalf("context not injected ahead of the prompt:\n%s", args)
	}
	// context precedes the task prompt in the delivered -p argument
	if strings.Index(args, "## x") > strings.Index(args, "do it") {
		t.Fatalf("context must precede the prompt:\n%s", args)
	}
}
```

Run: `go test ./agent/runner/ -run 'SystemPrompt|Context' -v -count=1`. Expected: FAIL — `cfg.SystemPrompt undefined`.

- [ ] **Step 6: Runner implementation — `agent/runner/runner.go`**

`Config` gains (after `OutputSchema`, `:54`):

```go
	// SystemPrompt is appended to claude's baseline system prompt
	// (--append-system-prompt); the renderer resolved the workflow/step
	// layering, this is always the effective literal (§8.1).
	SystemPrompt string
	// Context is injected at session start: for the claude runner, a
	// delimited block prepended to the -p prompt (design 2026-07-17 §1 —
	// platform-side equivalent of a SessionStart hook).
	Context string
```

`FromEnv` gains (with the other `AGENT_*` reads, `:87-92`):

```go
		SystemPrompt: os.Getenv("AGENT_SYSTEM_PROMPT"),
		Context:      os.Getenv("AGENT_CONTEXT"),
```

In `Run`, immediately after the prompt is resolved (after the `if prompt == "" { return 2, ... }` guard at `:181-183`):

```go
	// Session-start context injection (design 2026-07-17 §1/§4): the
	// workflow's concatenated context precedes the task prompt inside the
	// single -p message — claude sees it before acting, and the system
	// prompt stays reserved for the system_prompt layer.
	if cfg.Context != "" {
		prompt = "<workflow-context>\nReference material provided by your workflow definition. Read it before acting.\n\n" +
			cfg.Context + "\n</workflow-context>\n\n" + prompt
	}
```

In the claude args assembly, after the `--max-turns` **if-block closes** at `:248` (i.e. before the `--dangerously-skip-permissions` append at `:249`, and NOT nested inside the `if cfg.MaxTurns > 0` block — system_prompt must apply regardless of MaxTurns):

```go
	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}
```

- [ ] **Step 7: Run all touched suites**

```bash
go test ./agent/runner/ ./agent/dispatch/ -count=1
go build ./... && go vet ./agent/dispatch/ ./agent/runner/
```
Expected: PASS / no output from vet.

- [ ] **Step 8: Commit** (schema + exec files were already committed in Task 3a; 3b touches only the renderer + runner)

```bash
git add agent/dispatch/render.go agent/dispatch/render_test.go \
        agent/runner/runner.go agent/runner/runner_test.go
git commit -m "feat(agent): renderer resolves system_prompt/context + runner consumes; render relaxes all but skills (slice b)"
```

---

### Task 4 [slice b]: Runner skills mapping + schema/exec plumbing (`AGENT_SKILL_DIRS`)

Everything DOWNSTREAM of the rendered plan, landed before the renderer emits it (so relaxation in Task 5 never precedes the consumer). The renderer does not emit `Skills` yet; render still refuses skills after this task.

**Files:**
- Modify: `atc/steps.go`, `atc/plan.go`, `atc/builds/planner.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `agent/runner/runner.go`
- Test: `atc/exec/agent_step_test.go`, `agent/runner/runner_test.go`, `atc/builds/planner_test.go`

- [ ] **Step 1: Failing runner tests** (append to `runner_test.go`; `writeStubClaude` exists at `:21`):

```go
func TestFromEnvParsesSkillDirs(t *testing.T) {
	t.Setenv("AGENT_SKILL_DIRS", `["workflow-skills/skills/tdd","workflow-skills/skills/idioms"]`)
	cfg := runner.FromEnv()
	if len(cfg.SkillDirs) != 2 || cfg.SkillDirs[0] != "workflow-skills/skills/tdd" {
		t.Fatalf("skill dirs not parsed: %v", cfg.SkillDirs)
	}

	t.Setenv("AGENT_SKILL_DIRS", `not-json`)
	cfg = runner.FromEnv()
	if cfg.SkillDirsInvalid == "" {
		t.Fatal("malformed AGENT_SKILL_DIRS must be recorded, not silently dropped")
	}
}

func TestRunInstallsWorkflowSkills(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	src := filepath.Join(dir, "workflow-skills", "skills", "tdd")
	os.MkdirAll(filepath.Join(src, "references"), 0o755)
	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# tdd"), 0o644)
	os.WriteFile(filepath.Join(src, "references", "red-green.md"), []byte("rg"), 0o644)

	claude := writeStubClaude(t, dir,
		`{"type":"result","subtype":"success","result":"\"ok\"","is_error":false}`)

	cfg := runner.Config{
		Prompt: "do it", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		SkillDirs: []string{"workflow-skills/skills/tdd"},
	}
	if exit, err := runner.Run(context.Background(), cfg); err != nil || exit != 0 {
		t.Fatalf("run: exit=%d err=%v", exit, err)
	}

	installed := filepath.Join(dir, ".claude", "skills", "tdd")
	if raw, err := os.ReadFile(filepath.Join(installed, "SKILL.md")); err != nil || string(raw) != "# tdd" {
		t.Fatalf("SKILL.md not installed: %v %q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(installed, "references", "red-green.md")); err != nil {
		t.Fatalf("skill tree not copied: %v", err)
	}
}

func TestRunLogsSkillShadowing(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	// workflow-provided skill
	src := filepath.Join(dir, "workflow-skills", "skills", "tdd")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("# workflow tdd"), 0o644)
	// pre-existing same-named skill in the discovery root (workspace-carried)
	pre := filepath.Join(dir, ".claude", "skills", "tdd")
	os.MkdirAll(pre, 0o755)
	os.WriteFile(filepath.Join(pre, "SKILL.md"), []byte("# old"), 0o644)
	// same-named skill inside an input (repo/.claude/skills/tdd)
	repoSkill := filepath.Join(dir, "repo", ".claude", "skills", "tdd")
	os.MkdirAll(repoSkill, 0o755)
	os.WriteFile(filepath.Join(repoSkill, "SKILL.md"), []byte("# repo"), 0o644)

	claude := writeStubClaude(t, dir,
		`{"type":"result","subtype":"success","result":"\"ok\"","is_error":false}`)
	var stderr bytes.Buffer
	cfg := runner.Config{
		Prompt: "do it", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		SkillDirs: []string{"workflow-skills/skills/tdd"}, Stderr: &stderr,
	}
	if exit, err := runner.Run(context.Background(), cfg); err != nil || exit != 0 {
		t.Fatalf("run: exit=%d err=%v", exit, err)
	}
	if !strings.Contains(stderr.String(), "shadows") {
		t.Fatalf("shadowing must be logged, got:\n%s", stderr.String())
	}
	raw, _ := os.ReadFile(filepath.Join(pre, "SKILL.md"))
	if string(raw) != "# workflow tdd" {
		t.Fatalf("workflow skill must win the collision, got %q", raw)
	}
}

func TestRunSkillDirEscapingWorkdirIsPlatformError(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude := writeStubClaude(t, dir, `{"type":"result","result":"\"ok\"","is_error":false}`)
	cfg := runner.Config{
		Prompt: "do it", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		SkillDirs: []string{"../outside"},
	}
	if exit, _ := runner.Run(context.Background(), cfg); exit != 2 {
		t.Fatalf("escaping skill dir must be a platform error, got exit %d", exit)
	}
}
```

Run: `go test ./agent/runner/ -run 'Skill' -v -count=1`. Expected: FAIL — `cfg.SkillDirs undefined`.

- [ ] **Step 2: Runner implementation** (`agent/runner/runner.go`)

`Config` gains (after `Context` from Task 3b):

```go
	// SkillDirs are workdir-relative materialized workflow-skill
	// directories (design 2026-07-17 §4), installed into the provider
	// discovery location before claude starts. SkillDirsInvalid records a
	// malformed AGENT_SKILL_DIRS value so Run can fail loudly instead of
	// silently dropping authored skills.
	SkillDirs        []string
	SkillDirsInvalid string
```

`FromEnv` gains (after the struct literal, before the `AGENT_BUDGET_SLICE_USD` block; add `"encoding/json"` to imports if absent):

```go
	if v := os.Getenv("AGENT_SKILL_DIRS"); v != "" {
		if err := json.Unmarshal([]byte(v), &cfg.SkillDirs); err != nil {
			cfg.SkillDirsInvalid = v
		}
	}
```

In `Run`, after the prompt/context resolution and before the flight recorder opens (`os.MkdirAll(cfg.FlightDir, ...)`):

```go
	if cfg.SkillDirsInvalid != "" {
		return 2, fmt.Errorf("AGENT_SKILL_DIRS is not a JSON string array: %q", cfg.SkillDirsInvalid)
	}
	if err := installSkills(cfg, stderr); err != nil {
		return 2, fmt.Errorf("install workflow skills: %w", err)
	}
```

New functions (bottom of runner.go; add `"io/fs"` to imports):

```go
// installSkills copies each materialized workflow-skill directory into
// claude's cwd discovery location (<workdir>/.claude/skills/<name>).
// Coexistence rule (design 2026-07-17 §4): the target repo's own .claude
// stays in effect for repo-scoped skills; on a NAME collision the
// workflow's copy wins and the shadowing is logged — never silent.
func installSkills(cfg Config, stderr io.Writer) error {
	if len(cfg.SkillDirs) == 0 {
		return nil
	}
	targetRoot := filepath.Join(cfg.WorkDir, ".claude", "skills")
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	for _, dir := range cfg.SkillDirs {
		src := filepath.Join(cfg.WorkDir, dir)
		if rel, err := filepath.Rel(cfg.WorkDir, src); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("skill dir %q escapes the workdir", dir)
		}
		name := filepath.Base(dir)
		dst := filepath.Join(targetRoot, name)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(stderr, "agent-runner: workflow skill %q shadows the existing %s (workflow wins)\n", name, dst)
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
		// Informational: same-named skills inside first-level inputs
		// (e.g. repo/.claude/skills/<name>) are outside claude's cwd
		// discovery while the workflow copy sits in the discovery root.
		matches, _ := filepath.Glob(filepath.Join(cfg.WorkDir, "*", ".claude", "skills", name))
		for _, m := range matches {
			fmt.Fprintf(stderr, "agent-runner: workflow skill %q shadows %s (workflow wins)\n", name, m)
		}
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("skill %q: %w", name, err)
		}
		fmt.Fprintf(stderr, "agent-runner: installed workflow skill %q\n", name)
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}
```

Run: `go test ./agent/runner/ -count=1`. Expected: PASS.

- [ ] **Step 3: Schema + planner + exec plumbing**

`atc/steps.go` `AgentStep` (after `Context` from Task 3a):

```go
	// Skills are workdir-relative materialized skill directories
	// (emitted by the renderer alongside the write-skills task; the
	// runner installs them into its provider's discovery location).
	Skills []string `json:"skills,omitempty"`
```

`atc/plan.go` `AgentPlan`: `Skills []string \`json:"skills,omitempty"\``. `atc/builds/planner.go` `VisitAgent`: `Skills: step.Skills,`. Extend the planner "agent step" case (`:1541`) accordingly.

`atc/exec/agent_step.go` (after the `AGENT_CONTEXT` block from Task 3a; ensure `"encoding/json"` is in the import block — add it if absent):

```go
	if len(step.plan.Skills) > 0 {
		// []string marshal cannot fail; JSON keeps arbitrary path bytes
		// unambiguous (paths may contain spaces).
		skillDirs, _ := json.Marshal(step.plan.Skills)
		env = append(env, "AGENT_SKILL_DIRS="+string(skillDirs))
	}
```

Extend the §8.1 exec spec (`agent_step_test.go:221`): set `Skills: []string{"workflow-skills/skills/tdd"}` on the plan fixture and add:

```go
			Expect(spec.Env).To(ContainElement(`AGENT_SKILL_DIRS=["workflow-skills/skills/tdd"]`))
```

- [ ] **Step 4: Run + commit**

```bash
go test ./agent/runner/ ./atc/builds/ -count=1 && go test ./atc/exec/ -count=1 && go build ./...
git add atc/steps.go atc/plan.go atc/builds/planner.go atc/builds/planner_test.go \
        atc/exec/agent_step.go atc/exec/agent_step_test.go agent/runner/runner.go agent/runner/runner_test.go
git commit -m "feat(agent): skills discovery mapping — AGENT_SKILL_DIRS, .claude/skills install, shadow logging"
```

---

### Task 5 [slice b]: Renderer skill materialization + final refusal removal

**Files:**
- Modify: `agent/dispatch/render.go`
- Test: `agent/dispatch/render_test.go`

- [ ] **Step 1: Failing tests** (append to `render_test.go`; delete `TestRenderRefusesUnmaterializedSkills` from Task 3b — it is superseded here):

```go
func skilledRenderInput() dispatch.RenderInput {
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Skills = []string{"tdd"}
	in.Workflow.Steps[0].Skills = []string{"extra"}
	in.Workflow.SkillFiles = map[string]string{
		"skills/tdd/SKILL.md":         "# tdd",
		"skills/tdd/references/rg.md": "red green",
		"skills/extra/SKILL.md":       "# extra",
	}
	return in
}

func TestRenderMaterializesSkills(t *testing.T) {
	in := skilledRenderInput()
	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("skills must now render: %v", err)
	}

	plan := cfg.Jobs[0].PlanSequence
	// write-skills task precedes the agent steps and outputs workflow-skills
	var task *atc.TaskStep
	for _, s := range plan {
		if ts, ok := s.Config.(*atc.TaskStep); ok && ts.Name == "write-skills" {
			task = ts
		}
	}
	if task == nil {
		t.Fatal("no write-skills task in the plan")
	}
	if len(task.Config.Outputs) != 1 || task.Config.Outputs[0].Name != "workflow-skills" {
		t.Fatalf("write-skills outputs: %+v", task.Config.Outputs)
	}
	script := task.Config.Run.Args[len(task.Config.Run.Args)-1]
	wantB64 := base64.StdEncoding.EncodeToString([]byte("# tdd"))
	if !strings.Contains(script, wantB64) || !strings.Contains(script, "workflow-skills/skills/tdd/SKILL.md") {
		t.Fatalf("script does not materialize skills/tdd/SKILL.md:\n%s", script)
	}

	// the agent step lists its effective skill dirs and gains the input
	var agent *atc.AgentStep
	for _, s := range plan {
		if as, ok := s.Config.(*atc.AgentStep); ok {
			agent = as
			break
		}
	}
	if agent == nil {
		t.Fatal("no agent step in the plan")
	}
	if len(agent.Skills) != 2 || agent.Skills[0] != "workflow-skills/skills/tdd" ||
		agent.Skills[1] != "workflow-skills/skills/extra" {
		t.Fatalf("effective skill dirs (workflow then step, deduped): %v", agent.Skills)
	}
	found := false
	for _, input := range agent.Inputs {
		if input == "workflow-skills" {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent step must receive the workflow-skills input: %v", agent.Inputs)
	}
}

func TestRenderSkillsUnresolvedInCompiledConfigIsAnError(t *testing.T) {
	in := skilledRenderInput()
	in.Workflow.SkillFiles = nil // hand-built / pre-compile definition
	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "tdd") {
		t.Fatalf("unresolved skills must error, got %v", err)
	}
}

func TestRenderRefusesOversizedSkillSet(t *testing.T) {
	in := skilledRenderInput()
	in.Workflow.SkillFiles["skills/tdd/references/big.md"] = strings.Repeat("a", 1<<20)
	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "skill") {
		t.Fatalf("oversized skill set must refuse (fetch-by-version endpoint is the designed fallback), got %v", err)
	}
}

func TestRenderShellQuotesSkillPaths(t *testing.T) {
	in := skilledRenderInput()
	in.Workflow.SkillFiles["skills/tdd/notes with spaces.md"] = "n"
	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, s := range cfg.Jobs[0].PlanSequence {
		if ts, ok := s.Config.(*atc.TaskStep); ok && ts.Name == "write-skills" {
			script := ts.Config.Run.Args[len(ts.Config.Run.Args)-1]
			if !strings.Contains(script, "'workflow-skills/skills/tdd/notes with spaces.md'") {
				t.Fatalf("paths must be single-quoted:\n%s", script)
			}
			return
		}
	}
	t.Fatal("no write-skills task")
}
```

(`render_test.go` gains import `encoding/base64` if absent.)

Run: `go test ./agent/dispatch/ -run 'TestRenderMaterializes|TestRenderSkills|TestRenderRefusesOversized|TestRenderShellQuotes' -v -count=1`
Expected: FAIL — render still refuses skills.

- [ ] **Step 2: Implementation** (`agent/dispatch/render.go`; add `"sort"`, `"path"`, and `"encoding/base64"` to imports as needed — base64 is already imported for `writeTicketTask`)

REMOVE the `skillsInUse` refusal block and the `skillsInUse` helper (Task 3b's temporary boundary — this task completes the chain). Add:

```go
// skillsArtifact is the renderer-owned artifact name carrying
// materialized workflow skills (never authorable as a step output —
// renderer-attached, like the ticket artifact).
const skillsArtifact = "workflow-skills"

// maxSkillScriptBytes bounds the write-skills task's script: it travels
// as a single sh -ec argument and must stay clearly under ARG_MAX
// (~2 MiB incl. env). Past the bound render REFUSES rather than
// emitting a task the kubelet cannot exec — the authenticated
// fetch-by-version endpoint is the designed fallback (spec §4), not yet
// built (building it fires CONVENTIONS C1's six-touchpoint checklist).
const maxSkillScriptBytes = 1 << 20

// effectiveSkills is workflow-global ∪ step-additional, workflow order
// first, duplicates dropped (design 2026-07-17 §1 — step lists never
// remove or replace).
func effectiveSkills(cfg workflow.Config, step workflow.Step) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range append(append([]string{}, cfg.Skills...), step.Skills...) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeSkillsTask materializes every skill file from the compiled
// Config into the workflow-skills artifact — same base64 write-task
// mechanism as writeTicketTask, so arbitrary content survives shell
// quoting byte-exact.
func writeSkillsTask(cfg workflow.Config) (*atc.TaskStep, error) {
	paths := make([]string, 0, len(cfg.SkillFiles))
	for p := range cfg.SkillFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var script strings.Builder
	script.WriteString("set -eu\n")
	for _, p := range paths {
		target := skillsArtifact + "/" + p
		fmt.Fprintf(&script, "mkdir -p %s\n", shellQuote(path.Dir(target)))
		fmt.Fprintf(&script, "echo %s | base64 -d > %s\n",
			base64.StdEncoding.EncodeToString([]byte(cfg.SkillFiles[p])), shellQuote(target))
	}
	if script.Len() > maxSkillScriptBytes {
		return nil, fmt.Errorf("workflow skill set encodes to %d bytes (max %d): trim the skill trees or wait for the fetch-by-version materialization endpoint", script.Len(), maxSkillScriptBytes)
	}

	return &atc.TaskStep{
		Name: "write-skills",
		Config: &atc.TaskConfig{
			Platform: "linux",
			ImageResource: &atc.ImageResource{
				Type:   "registry-image",
				Source: atc.Source{"repository": "busybox"},
			},
			Outputs: []atc.TaskOutputConfig{{Name: skillsArtifact}},
			Run: atc.TaskRunConfig{
				Path: "sh",
				Args: []string{"-ec", script.String()},
			},
		},
	}, nil
}
```

(Match `writeTicketTask` at `render.go:326` for the exact `TaskConfig` field spelling in this tree — the shape above mirrors it.)

In `RenderAgentStep`, after the systemPrompt/context resolution (Task 3b):

```go
	var skillDirs []string
	inputs := step.Inputs
	if names := effectiveSkills(in.Workflow, step); len(names) > 0 {
		for _, name := range names {
			if _, ok := in.Workflow.SkillFiles["skills/"+name+"/SKILL.md"]; !ok {
				return atc.AgentStep{}, fmt.Errorf("agent step %q: skill %q is not resolved in the compiled definition", step.Agent, name)
			}
			skillDirs = append(skillDirs, skillsArtifact+"/skills/"+name)
		}
		inputs = append(append([]string{}, step.Inputs...), skillsArtifact)
	}
```

and on the returned struct use `Inputs: inputs,` (replacing `Inputs: step.Inputs,`) plus `Skills: skillDirs,`.

In `Render`, after the `needsTicket` write-task append (`:195-197`):

```go
	needsSkills := false
	for _, step := range in.Workflow.Steps {
		if len(effectiveSkills(in.Workflow, step)) > 0 {
			needsSkills = true
			break
		}
	}
	if needsSkills {
		task, err := writeSkillsTask(in.Workflow)
		if err != nil {
			return atc.Config{}, err
		}
		plan = append(plan, atc.Step{Config: task})
	}
```

- [ ] **Step 3: Run to pass**

```bash
go test ./agent/dispatch/ -count=1 && go build ./... && go vet ./agent/dispatch/
```
Expected: PASS / no vet output.

- [ ] **Step 4: Commit**

```bash
git add agent/dispatch/render.go agent/dispatch/render_test.go
git commit -m "feat(dispatch): materialize workflow skills via write-skills task; skills refusal lifted (slice b complete)"
```

---

### Task 6 [slice b]: Example deploy pipeline (shipped as an example, not a component)

**Files:**
- Create: `docs/examples/agent-workflow-deploy-pipeline.yml`

- [ ] **Step 1: Write the example** (spec §6 — pipelines-that-deploy; auto-promote is the author's policy):

```yaml
# Example: continuous deployment for agent workflow definitions
# (docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md §6).
#
# The platform's server surface is exactly two verbs — import (mint a
# content-hashed version) and set-live (human/scripted promotion). This
# pipeline is the ordinary-user-authored deploy model: watch a repo of
# workflow directories, import on change. It is an EXAMPLE, not a
# platform component: fork it, point it at your repo, choose your
# promotion policy.
#
# Vars to provide (fly set-pipeline ... -v / credential manager):
#   workflows-repo-uri:  git URI of the repo holding workflow dirs
#   fly-image:           an image containing the fly CLI matching your web
#   concourse-url:       your ATC external URL
#   concourse-username / concourse-password: a local user with main-team access
resources:
- name: workflows-repo
  type: git
  source:
    uri: ((workflows-repo-uri))
    branch: main

jobs:
- name: deploy-workflows
  plan:
  - get: workflows-repo
    trigger: true
  - task: import
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: ((fly-image))
      inputs:
      - name: workflows-repo
      params:
        CONCOURSE_URL: ((concourse-url))
        CONCOURSE_USERNAME: ((concourse-username))
        CONCOURSE_PASSWORD: ((concourse-password))
      run:
        path: sh
        args:
        - -ec
        - |
          fly -t deploy login -c "$CONCOURSE_URL" \
            -u "$CONCOURSE_USERNAME" -p "$CONCOURSE_PASSWORD"
          # Directory import: every workflow.yml-bearing subdirectory is
          # imported in turn; each import is independent and idempotent
          # on the canonical-manifest hash, so re-runs converge and a
          # failure in one workflow leaves the others imported.
          #
          # Promotion policy — pick ONE:
          #   import-only (default): humans promote via
          #     fly agent workflows set-live <name> <version>
          #   auto-promote: add --set-live below.
          fly -t deploy agent workflows import workflows-repo/workflows/
```

- [ ] **Step 2: Verify it is valid pipeline YAML**

```bash
go run ./fly validate-pipeline -c docs/examples/agent-workflow-deploy-pipeline.yml
```
Expected: `looks good`. (If the local fly build lacks `validate-pipeline` flags for vars, `-l` with a dummy vars file also works; the example must parse.)

- [ ] **Step 3: Commit**

```bash
git add docs/examples/agent-workflow-deploy-pipeline.yml
git commit -m "docs(examples): agent workflow deploy pipeline (spec §6)"
```

(Optional live leg, owner's call: set it on theborg pointing at a workflows repo and watch one import cycle — this is the retrospective-loop closer but is not required to ship slice (b).)

---

### Task 7 [slice b]: Slice-(b) contract amendment — §2.8 / §8.1 + the §11 entry

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`

- [ ] **Step 1: Amend §2.8** (agent step config, ~:821): add to the AgentStep field list the three renderer-resolved literals with the rule restated:

```markdown
**2026-07-17 source-format slice (b) additions:** `SystemPrompt` (effective
workflow/step system-prompt layer, resolved by the renderer — appended by the
runner to its provider baseline, never replacing it), `Context` (effective
concatenated workflow context with per-file headers, renderer-resolved,
≤ 256 KiB or render refuses), `Skills` (workdir-relative materialized skill
directory paths under the renderer-owned `workflow-skills` artifact). The
render-time-resolution rule is UNCHANGED: all three are literals resolved from
the compiled definition at render; the step implementation never reads
workflow tables. The renderer emits a deterministic `write-skills` busybox
task (base64 write-task family, sorted paths, single-quoted; script bound
1 MiB encoded — past it render REFUSES, naming the not-yet-built
fetch-by-version endpoint as the fallback) and attaches the
`workflow-skills` input to each skill-bearing agent step.
```

- [ ] **Step 2: Amend the §8.1 env table** (~:1706, with the other agent-step-owned rows):

```markdown
| `AGENT_SYSTEM_PROMPT` | main | literal (from `AgentStep.SystemPrompt`) *(2026-07-17 slice b)* | effective workflow/step system-prompt layer; claude runner passes `--append-system-prompt` |
| `AGENT_CONTEXT` | main | literal (from `AgentStep.Context`) *(2026-07-17 slice b)* | effective concatenated workflow context; claude runner injects it at session start (delimited block ahead of the `-p` prompt) |
| `AGENT_SKILL_DIRS` | main | literal JSON string array (from `AgentStep.Skills`) *(2026-07-17 slice b)* | workdir-relative materialized skill dirs; the runner installs each into `<workdir>/.claude/skills/<name>` (copy; name collision → workflow wins, shadowing logged), errors (exit 2) on malformed JSON or a dir escaping the workdir |
```

- [ ] **Step 3: Append the §11 entry:**

```markdown
- 2026-07-17 (workflow source format slice (b) — materialization; relaxes the
  slice-(a) render refusal surface-by-surface, never before its consumer
  exists; affects: dispatch, agent-step, workflow-store consumers): §2.8
  `AgentStep`/`AgentPlan` gain renderer-resolved `SystemPrompt`/`Context`/
  `Skills` literals (render-time-resolution rule intact); §8.1 gains
  `AGENT_SYSTEM_PROMPT`/`AGENT_CONTEXT`/`AGENT_SKILL_DIRS` (main container,
  all literal). Renderer: resolves the system-prompt layering
  (step-replaces-workflow-layer), concatenates additive context with
  per-file headers (256 KiB refusal bound), materializes skill trees via the
  deterministic `write-skills` base64 task into the renderer-owned
  `workflow-skills` artifact (1 MiB encoded-script refusal bound;
  fetch-by-version endpoint remains the designed-but-unbuilt fallback — C1
  fires if it is ever built), and lifts the refusals in two steps:
  prompt_files/system_prompt/context at the slice-b front (text plumbing),
  skills only once runner mapping landed. Runner: `--append-system-prompt`,
  session-start context block, `.claude/skills/<name>` install with
  shadow-logging (workflow wins; repo `.claude` remains in effect for
  repo-scoped sessions). Trust boundary unchanged: workflow-authored content
  materializes into AGENT pods only — `cmd/harvest-runner` takes none of
  these inputs. Example deploy pipeline published at
  `docs/examples/agent-workflow-deploy-pipeline.yml` (example, not a
  component). Proven by a live skill-bearing dispatch on theborg.
```

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(contracts): workflow source format slice (b) — §2.8 step fields, §8.1 env rows, §11 entry"
```

---

### Task 8 [slice b]: Live theborg smoke — skill-bearing dispatch (closes the item, spec §9)

**Files:** a throwaway workflow directory (keep under the scratchpad or a local dir; do NOT commit it), no repo changes.

Native-only: needs fly against concourse.home, the deployed slice-(b) web image, and build-log access. Dispatch-timing rule applies: push → release → settle → then dispatch.

- [ ] **Step 1: Confirm the deployed web + agent-runner images contain slice (b)** (release tag from the jetbridge pipeline; the agent-step image flag `--agent-step-image` must point at the rebuilt agent-runner).
- [ ] **Step 2: Author a skill-bearing analyze variant** (directory form):

```
analyze-skilled/
  workflow.yml       # schema_version: 2, name: analyze-skilled, skills: [repo-analysis],
                     # system_prompt_file: system/base.md, context: [context/houserules.md],
                     # steps: the live analyze workflow's steps + outputs [workspace]
  system/base.md     # "You are running inside the jetbridge agentic platform..."
  context/houserules.md
  skills/repo-analysis/SKILL.md      # instructs a distinctive, checkable behavior, e.g.
                                     # "begin your final summary with the marker [skill:repo-analysis]"
```

- [ ] **Step 3: Import + promote + dispatch**

```bash
fly -t cicd agent workflows import ./analyze-skilled --set-live
# expected: analyze-skilled v1 imported and live
fly -t cicd agent workflows show analyze-skilled
# expected: compiled definition + manifest summary (4 files, per-skill file counts)
fly -t cicd agent tickets create --repo tdmtrader/jetbridge \
  --title "slice-b skills smoke" --workflow analyze-skilled --queue --dispatch
fly -t cicd agent tickets watch <id>
```

- [ ] **Step 4: Verify, in the build log:**
  - the `write-skills` task ran and listed `workflow-skills/skills/repo-analysis/SKILL.md`;
  - `agent-runner: installed workflow skill "repo-analysis"` on stderr;
  - claude was invoked (step succeeds) and the agent's output carries the skill's distinctive marker (proves discovery, not just installation);
  - no refusal fired; the run reaches harvest normally.
- [ ] **Step 5: Verify shadowing observability** (optional but cheap): add a same-named `.claude/skills/repo-analysis` to the target repo branch, re-dispatch, and confirm the `shadows ... (workflow wins)` stderr line.
- [ ] **Step 6: Record the outcome in project memory** (skill-tree size vs the 1 MiB script bound — this is the real-world datum open decision D3 wants).

---

## Migration allocations

**No migration remains in this plan.** The item's single migration is LANDED and verified in the tree:

| Number | What | Status |
|--------|------|--------|
| `1773106066` | `ALTER TABLE agent_workflow_definitions ADD COLUMN source_manifest JSONB` (nullable; no backfill; clean down) | **LANDED** (`ac9347c9aa`); BOTH C2 dual constants verified at `1773106066` (`atc/db/migration/legacy_upgrade_test.go:37`, `docs/migration/migrate-preflight.sh:38`) |

- `1773106065` remains RESERVED for PARK-V2 `agent_run_step_state` — never take it. The vacated `1773106050–59` block is never reused.
- **Slice (b) requires NO migration** — materialization reads the stored manifest/compiled Config; the (unbuilt) fetch-by-version fallback endpoint would also be DB-free.
- If any FUTURE follow-up to this item needs a migration, next free is `1773106067+`; re-confirm at execution time (`ls atc/db/migration/migrations | sort | tail -3`) and obey lowest-version-first merge ordering + C2.

## Risks & open decisions (owner input flagged)

- **D2 — grammar realization: RESOLVED BY LANDING.** The `prompt_files:` sibling map (vs the spec's original `prompts: {implement: {file: ...}}` union) is committed in both code (`9890b56b7a`) and the amended spec (`187cad4926`). Owner review of the landed commits is the remaining sign-off act; reverting now would be a breaking grammar change and needs its own plan.
- **D1 — `prompt_files` over-refusal window: accepted, closes at Task 3b.** The landed slice-(a) refusal (`render.go:168`) covers `prompt_files` even though compile fully inlines them — a prompt-files-only workflow imports but cannot dispatch until Task 3b lands. Zero risk (nothing is dropped, the error names the field); the window is exactly the gap between now and Task 3b's merge. If that window must close sooner, the one-line narrowing is: probe `skillsInUse`-style instead of `SourceFormatField()` at render (this is precisely Task 3b's renderer step, extractable on its own).
- **D3 (owner, decided-by-default): materialization transport.** This plan commits to base64 write-task with a 1 MiB encoded-script refusal (no C1 impact). The fetch-by-version endpoint stays unbuilt; if Task 8's live data shows real skill trees near the bound, plan the endpoint separately WITH the full C1 six-touchpoint checklist spelled out.
- **D4 (owner): hash-scheme lineage carrier.** Spec §3.3 says "lineage note in the version row"; the landed code records the scheme change only in the `ContentHash` doc comment. This plan realizes the row-level note as **`source_manifest IS NOT NULL` = new-scheme marker** (no extra column, no description pollution) + the Task 1 §1.6 text. If the owner insists on an explicit per-row note, the `description` suffix is the only existing carrier — decide before Task 1 lands.
- **D5 (recorded): hidden-file policy asymmetry.** Server-side Validate refuses dot-prefixed path segments (hardening beyond spec §3.1's fly-side exclusion) — a skill can never ship a dotfile asset in v1. Landed behavior; stated in the Task 1 §11 entry; acceptable, revisit only if a real skill needs one.
- **Contract-amendment debt is live NOW:** slice (a) is deployed-ready code whose contract surfaces (§6/§1.6/§2.2) are undocumented in the normative file. Any parallel workstream reading 00-shared-contracts today sees a pre-slice-(a) picture. Task 1 first.
- **Context/env size:** `AGENT_CONTEXT` rides the pod spec; the 256 KiB render bound (Task 3b) protects the kubelet/etcd path. If real workflows need more, the escape is materializing context as files (same write-task family) — a small follow-up, not a redesign.
- **Refusal-relaxation ordering is load-bearing:** Task 4 (runner consumer) MUST merge before or with Task 5 (renderer emission + refusal lift). Tasks 3a→3b→4→5 are sequential by design; do not parallelize them across loop tickets. Within Task 3, 3a lands the schema + exec transport INERT (refusal untouched) and 3b lands the renderer + runner AND narrows the refusal in one commit — never split 3b so that the refusal narrows without its end-to-end consumer.
- **Claude discovery assumption:** the runner installs into `<workdir>/.claude/skills/` and claude discovers cwd-level `.claude` — Task 8's marker check is the live proof; if discovery fails, the fallback (documented, not planned) is `--append-system-prompt` injection of skill index text.
- **Line-number drift:** every anchor in this plan was re-verified at `187cad4926`. This repo moves fast (slice (a) landed within hours of its plan); executors MUST re-grep anchors (`SourceFormatField`, `writeTicketTask`, `AGENT_PROMPT_FILE`, `--max-turns`) rather than trusting absolute line numbers if HEAD has advanced.
- **Parallel-session hazard:** four sibling remainder plans live in this directory (delivery-outcomes, dispatcher-budget-reconciler, judge-evidence, platform-mcp-hitl). Dispatch coordination rule applies. This plan's slice (b) contends over TWO surfaces, not one:
  1. **`atc/steps.go`/`atc/plan.go`/`atc/builds/planner.go`/`atc/exec/agent_step.go`** (Tasks 3a, 4, 5) — the same files the agent-step-adjacent plans touch. Do not run this plan's slice (b) concurrently with another plan that amends `AgentStep`/`AgentPlan`. **Specific co-editor of `atc/exec/agent_step.go`:** platform-mcp-hitl Task 25 ADDs `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` to the platform-sidecar planEnv pass-through list (~`agent_step.go:519`) — a different region from this plan's `AGENT_SYSTEM_PROMPT`/`AGENT_CONTEXT`/`AGENT_SKILL_DIRS` env appends (~`:370-372`), but the SAME file. Both are additive (C3); whoever lands second re-reads the file rather than patching cited lines.
  2. **`agent/dispatch/render.go` + `agent/dispatch/render_test.go`** (Tasks 3b and 5) — **TWO OTHER pending plans rewrite the SAME function; all three edit the same anchors.**
     - **platform-mcp-hitl's Task 25** edits `RenderAgentStep`'s returned-struct/env-map build (its `render.go:56-58` sidecar refusal and the env block adjacent to this plan's `:82-89`/`:91-101`/`:103-113` insertions) and the `Render` refusal chain (`spec_delivery`, hitl), and its plan text EXPLICITLY assumes the source-format refusal at `render.go:163-170` "stays byte-identical" — an assumption this plan's Task 3b NARROWS and Task 5 REMOVES. It ADDS `Sidecars:` to the same `RenderAgentStep` return literal this plan adds `SystemPrompt:`/`Context:`/`Skills:` to.
     - **judge-evidence's Task 11** DELETES the judge refusal (`render.go:158-162`, the `in.Workflow.Judge != nil` block that sits IMMEDIATELY ABOVE this plan's source-format refusal at `:163-170`) and removes the `"judge"` sub-case from the `render_test.go` refusal table. Because the judge and source-format refusals are adjacent, a stale-snapshot edit by either plan can clobber the other's removal.
     - **Do NOT run this plan's Task 3b / Task 5 concurrently with platform-mcp-hitl Task 25 or judge-evidence Task 11.** Suggested landing order for the three render.go edits: this plan's slice-b (3b then 5) → judge-evidence Task 11 → platform-mcp-hitl Task 25 (each off the concurrent-loop schedule). Whoever lands second/third RE-GREPS the whole refusal chain (`render.go:125-170` at current HEAD: `spec_delivery` → gate_policy → hitl → judge → source-format) and the `render_test.go` refusal table, re-pins its own line numbers, and PRESERVES the others' already-applied removals/additions (removed judge sub-case, added `Sidecars`/`SystemPrompt`/`Context`/`Skills` struct fields, narrowed/removed source-format refusal) rather than patching from the cited numbers.

## Complexity, risk, and recommended execution level

**Honest assessment:** the design AND all of slice (a)'s implementation risk are behind us — ten landed, individually-committed TDD tasks. What remains splits cleanly: two documentation/ops tasks (judgment about normative text + a live cluster session), three well-specced pure-Go code tasks with complete code and tests in this plan (gate-verifiable, postgres-free, each inside the proven ticket-#14 envelope), one YAML example, one docs amendment, one live smoke. The single genuine unknown left in the item is claude's cwd `.claude/skills` discovery inside the pod (Task 8 proves or falsifies it). No Elm. No migration. No new routes (C1 dormant unless the fetch fallback is ever built).

**Recommended level: split**

| Work | Level | Why |
|------|-------|-----|
| Task 1 (slice-a contract amendment) | **native-opus** | Normative-text judgment against a 1900-line contract file; must reconcile with five prior §11 entries; not worth a loop dispatch (docs-only, no gates apply). Do it FIRST — it is landing-discipline debt. |
| Task 2 (theborg cutover runbook) | **native-fable** (same session as the next deploy) | Live cluster, fly against cicd, dispatch-timing rule, judgment if version minting surprises. Cheap in a session that is already deploying. |
| Tasks 3a–5 (slice-b code) | **loop-opus** (one ticket per task, strictly sequential 3a→3b→4→5) | Full code + tests in this plan; every touched suite is postgres-free and gate-verifiable (`go build/test/vet`); each task within the #14 envelope (≤8 files; #14 was 8 files, +682/-41 — this is why Task 3 is SPLIT into 3a (7 files: schema+exec) and 3b (4 files: renderer+runner) rather than one 11-file ticket). Opus not sonnet: cross-package env-contract exactness and the refusal-boundary moves are where a faithful-but-literal executor can drift (e.g. replacing the LANDED refusal test, not appending a duplicate; or narrowing the refusal in 3a before the 3b consumer exists). Not fable: no open design decisions remain in the text. Human merges each ticket and runs `make test-quick` locally pre-merge (loop gates lack postgres for the full tier). |
| Tasks 6–7 (example YAML + slice-b contract amendment) | **native-sonnet** (or fold into the Task 8 session) | Two small doc/YAML commits; not worth a dispatch's budget from the shared rate-limit window. |
| Task 8 (live skills smoke) | **native-fable** | Live theborg dispatch, build-log forensics, and the item's one real unknown (claude cwd skill discovery in the pod) — needs cluster access and mid-flight judgment; also produces the D3 size datum. native-opus acceptable if fable budget is tight, but this is the item's only end-to-end proof. |

Budget note: every loop dispatch spends from the owner's shared Claude window — 4 tickets total (3a, 3b, 4, 5), sequential, each merged-and-settled before the next (dogfood dispatch-timing rule; self-upgrade restarts web and double-spends agents). If the owner prefers fewer coordination points, slice (b) is comfortably a single native-opus session (Tasks 3a–7) plus one native-fable live session (Tasks 2 + 8) at higher attention cost.
