# FLOWS — Workflow Shapes & the Spec→Plan→Implement Coupling

**Status:** program doc, 2026-07-10. Companion to `00-shared-contracts.md` and the wave plans (05, 06, 08, 09, 11–14).
**Question answered:** is the superpowers spec→plan→implement flow a *requirement* or an *option*?
**Verdict:** it is already mechanically an option. The coupling is rhetorical and soft, concentrated in seed content and doc framing — plus one under-specified render edge. Six small edits make spec-less/plan-less workflows first-class and *tested*, not merely tolerated.

> **Amendment (2026-07-09):** E1–E6 APPLIED to their owning files (status column, §2). The `concluded` terminal-state decision is DECIDED + APPLIED — enum amended pre-freeze (§2, §4). `direct-one-shot` and `test-first-contract` are now shipped seeds (§3, §5). Fix-loop's evidence-read gap is unchanged.
>
> **Amendment (2026-07-09, verifier follow-up):** E3's status corrected — it landed in **05-workflow-store.md only** (Task 13 seeds `direct-dev` / `test-first-dev`); the `00-shared-contracts.md` §6 half of its original target was deliberately dropped per plan 05's "no contract change — both seeds are pure §6 data" rationale. See the E3 scope note under the §2 table.

---

## 1. Coupling verdict

Three audits (contracts, runtime path, grammar) converged. Classification:

### HARD couplings — none to spec/plan; two to *shape*

No schema constraint, state-machine transition, dispatcher check, tool mandate, or harvest gate requires a spec or plan to exist:

- `agent_ticket_specs` / `agent_ticket_tasks` are FK-child tables that may have zero rows; the ticket's only required input is `agent_tickets.body` (contracts §1.7).
- `Store.LatestSpec` returns `(*Spec, bool, error)` with an explicit not-found bool; `ActivePlan` returns an empty task list on no rows (contracts §2.1).
- `read_ticket` types `spec` as `["object","null"]`; `list_tasks` returns `{"tasks": []}` — not errors (contracts §3.2; 08-platform-mcp lines ~3589–3616, fake fixture ~3764 pins exactly `{spec:null, tasks:[]}`).
- `draft→queued` is gated only by the state matrix (`validTransitions`, 06 Task 3 ~line 342); `CreateRequest` has no spec field; harvest consumes workspace + gates + rubric + diff only — `buildJudgePrompt` never reads a spec (09 lines 2109–2119).
- The §6.1 grammar has exactly two step kinds (`agent`, `checkpoint`), no spec/plan step kind, no minimum step count. A one-step implement-only definition is an explicit accept case (`TestValidateAcceptsMinimalDefinition`, 05 lines 584–606) and renders, dispatches, and harvests today.

The genuine HARD couplings are **shape-level, not phase-level** — they assume "linear, merge-intent code change", not "spec first":

| # | Coupling | Location | What it forecloses |
|---|----------|----------|--------------------|
| H1 | Harvest is appended unconditionally with `Push:true`, branch `agent/ticket-<id>`; no grammar knob | 11-dispatch Render (lines 1256–1260), RenderHarvestStep (Push at 611); §6.1 "harvest never declared in steps" | Review-only / spike workflows with no push intent (`HarvestStep` itself already supports `Push:false` — it's just unreachable from the grammar) |
| H2 | Flat, static, strictly-ordered step list; forward-only artifact dataflow | 05 Validate step loop (664–727, inputs-produced-earlier 697–701); charter decision 13 (05:17) | Fan-out (best-of-N), conditionals, intra-run loops — see §4 |
| H3 | Every render requires `Ticket.Repo`; dispatch errors tickets with `UserID == nil` | 11 RenderInput.Validate (199–216); 11 lines 2659–2661 | Repo-less research tickets; user-less tickets (the *actual* second-class citizens — credential-driven, not spec-driven) |

### SOFT couplings — degrade gracefully, deserve one normative sentence each

| # | Coupling | Location | Degradation today |
|---|----------|----------|-------------------|
| S1 | §6.2 render context frozen as `.Ticket .Spec .Tasks .Params` with **undefined nil semantics** — rendering happens at dispatch, *before* any agent can submit a spec, so nil `.Spec` is the normal case | contracts §6.2 (~1380) | A prompt template dereferencing `.Spec.Title` errors Go text/template execution and **fails the dispatch**. The one soft coupling with hard potential; bites even the seeded spec-first workflow. (11-dispatch's implementation notes already document `Spec *tickets.Spec // or nil` — the *contract* just never says so.) |
| S2 | `spec_delivery: files` materializes spec.md/plan.md; empty-render behavior unstated in contracts | contracts §6 (~1287–1294); rendered helpers 06 Task 9 | Already golden-tested to degrade: `RenderSpecMarkdown(t,nil)` → envelope + problem statement; `RenderPlanMarkdown(t,nil)` → "No plan submitted yet." (06:2941–2988). Make it normative; add a nil-input render test in 11 (current test only exercises the populated case, 11:1075–1103). |
| S3 | Seed content is uniformly spec-first: `standard-dev` definition + prompts ("…then submit a spec"), seed-prompt convention (read_ticket/list_tasks opening, 11:57), judge rubric "the spec's acceptance criteria", plan-approval checkpoint example | contracts §6 (~1282–1358) | Nothing breaks — all per-definition data. But it's the template every planner copies; spec-first propagates as de facto default. |
| S4 | No import-time check that some step outputs `workspace` (harvest's hard-coded input, 11:556) | 05 Validate (no rule) | Validate-clean / run-broken gap: a definition with no `outputs:[workspace]` passes import, then every run fails at the terminal harvest step. |
| S5 | Checkpoint requires a `platform` sidecar — enforced at render (F36 guard, 11:1329–1333), not at import | 05 Validate (no rule) | Second validate-clean / dispatch-broken gap. |
| S6 | `get_task` with no active plan → MCP tool error; `ActivePlan` empty-return not explicitly documented | contracts §2.1/§3.2 | Fine as designed (agents discover plan-lessness via `list_tasks`), but document the empty-plan returns so no implementer adds accidental error-on-absence. |

### RHETORICAL couplings — prose only, zero mechanism

| # | Statement | Location | Reality |
|---|-----------|----------|---------|
| R1 | "Constants vs. variables" declares **"Front: ticket → spec + plan"** a platform CONSTANT | design spec, line 26 | Flatly overstates the mechanism. Enforced constants are the ticket envelope (front) and pushed-branch/evidence/score (back). In a doc planners treat as gospel (contracts §0), this licenses workstreams to assume spec/plan rows exist. **The single most load-bearing edit.** |
| R2 | "the write path is submit_spec/submit_plan" | design spec §1, line 42 | The tools exist only if the workflow mounts the platform sidecar and its prompts call them. |
| R3 | Checkpoint example everywhere is "plan approval" | spec §10 (line 140), contracts §6 (~1331) | Mechanism is name/phase-agnostic; cosmetic. |
| R4 | Vision/UI prose assumes "live plan progress" on the ticket page | spec lines 9, 110 | Elm page already renders "No plan submitted yet." (06:4234); fly guards `detail.Spec != nil` (06:3575). Design the empty state; don't inherit it. |

**Bottom line:** a ticket that never gets a spec or plan flows through create → queue → dispatch → run → harvest → outcome → scorecard without breaking, *today, per the written contracts and pinned tests*. Spec-lessness is in fact the **normal entry state** — even in `standard-dev`, the spec is created mid-run by the first agent step, so every dispatch renders against a spec-less ticket.

---

## 2. Minimal decoupling edit list

The edits that convert "tolerated" into "first-class and tested". All are doc/seed/validation edits; **zero schema, state-machine, handler, or renderer code changes**.

| # | File | Edit | Size | Status |
|---|------|------|------|--------|
| E1 | `docs/superpowers/specs/2026-07-07-agentic-platform-end-state-design.md` line 26 | Reword the constant: "Front: **ticket** (with optional structured spec + plan storage available to any workflow that wants it)". Kills R1/R2 at the source. | S | **APPLIED 2026-07-09** (design spec) |
| E2 | `00-shared-contracts.md` §6.2 | Define nil-safe render semantics: `.Spec` may be nil / `.Tasks` may be empty at render time (always true at dispatch); either render as zero-value or document `{{if .Spec}}` guarding; add an import-validation or render-test check in workflow-store. Fixes S1, the only real breakage path. | S/M | **APPLIED 2026-07-09** (00-shared-contracts.md) |
| E3 | `05-workflow-store.md` seeds (original target also named `00-shared-contracts.md` §6 seed library; contracts half dropped — see note below) | Ship a second seeded definition — `direct-fix`: one agent step (read_ticket → implement from body → outputs `[workspace]`), no submit_spec/submit_plan, judge rubric worded against "the ticket body", no checkpoint. Equal-standing example so spec-first stops reading as the default. | S | **APPLIED 2026-07-09** (05-workflow-store.md ONLY — Task 13 ships seeds `direct-dev` = FLOWS "direct-one-shot" and `test-first-dev` = FLOWS "test-first-contract" — §3) |
| E4 | `11-dispatch.md` render tests + §2.8.2 | Add golden render test with `Spec=nil` / `Tasks=nil` in **both** delivery modes (files mode asserts the degraded goldens); amend the seed-prompt bullet: "read_ticket may return `spec:null`; the read-then-submit-spec opening is a standard-dev convention, not a renderer requirement." | S | **APPLIED 2026-07-09** (11-dispatch.md) |
| E5 | `00-shared-contracts.md` §1.7 + §2.1/§3.2 | One sentence each: "a ticket MAY have zero spec/task rows; all consumers must handle absence"; document `ActivePlan` → `([]Task{}, nil)` and `get_task`-with-no-plan → tool error; soften `read_ticket` description to "latest spec, if any". | S | **APPLIED 2026-07-09** (00-shared-contracts.md) |
| E6 | `05-workflow-store.md` Validate() | Close the two validate-clean/run-broken gaps: (a) warn/reject at import when no step outputs `workspace` (S4); (b) mirror the F36 platform-sidecar-required-for-checkpoints rule into import validation (S5). Pure additive validation. | S | **APPLIED 2026-07-09** (05-workflow-store.md) |

> **E3 scope note (2026-07-09 verifier follow-up):** E3's original target line named both `00-shared-contracts.md` §6 and the 05-workflow-store seeds. The contracts half was **deliberately dropped**, not forgotten: plan 05's E3 changelog records "no contract change — both seeds are pure §6 data" — seed definitions are per-definition data conforming to the §6 grammar, and adding a seed catalogue to the frozen contracts doc would couple the contract to content it explicitly does not govern. Accordingly 00-shared-contracts.md carries **no E3 edit** (its §11 flow-decoupling changelog entry lists only E2 + E5, plus the `concluded` decision), and the status cell above attributes E3 to 05-workflow-store.md alone. Seed naming: plan 05 ships the definitions as **`direct-dev`** and **`test-first-dev`**; this doc's flow names `direct-one-shot` / `test-first-contract` (§3, §5) are the flow-shape names those seeds implement.

Optional seventh, riding with §4's harvest knob: define the empty-render contract for `spec_delivery: files` (S2) — already golden-tested; just make it normative (S).

**`concluded` terminal state (2026-07-09): DECIDED + APPLIED** (owner-approved; enum amended pre-freeze in `00-shared-contracts.md` §1.7 + the 06 state matrix). New TERMINAL ticket state `concluded` — "run finished, human reviewed, no merge intended" (spike/research flows) — reachable from `needs_review` via explicit human disposition; the positive sibling of `abandoned`. Landing it now avoids the later enum migration §4 warned about.

---

## 3. Flow catalog

Eight flows, from the alternative-flow audit. "Native-today" = expressible as a new workflow definition with **zero platform changes**. "After edits" = needs §2's decoupling list. "Grammar v2" = needs §4.

| Flow | Use case | Support | Gap |
|------|----------|---------|-----|
| **direct-one-shot** | Chores, small fixes, dep bumps, retrospective-filed lint tickets — ticket body IS the spec | **native (seeded)** — shipped 2026-07-09 via E3 | — |
| **critic-loop** | Medium-risk changes: implement → gateway `request_review` → revise, looped inside one agent step | **native-today** | — (as-steps variant needs manual unrolling; no loop construct) |
| **heavy-hitl-pairing** | Trust-building: plan checkpoint + `ask_human` after every task | **native-today** | — (per-task gating must ride `ask_human`, not checkpoints — plan tasks are runtime data; each ask parks a live pod) |
| **superpowers-standard-dev** | Features/meaty fixes where prose spec + decomposed plan genuinely reduce risk. The seed — one option among peers, not the default | **native-today** | — |
| **test-first-contract** | Behavior-precise fixes: human approves the *tests* at a checkpoint, implementer graded by green | **native (seeded)** — E1–E6 applied + seed shipped 2026-07-09 | Checkpoint evidence surface still open: the approval row shows no mid-run workspace; the seed uses the workaround (tests mirrored into the spec body via `submit_spec`). Proper fix: attach a workspace-diff artifact to the checkpoint question row. |
| **fix-loop** | Sent-back tickets: read prior findings, fix only those, re-push same branch (§2.8.1 already anticipates the loop) | needs new primitive | (1) No platform-mcp read tool for prior review evidence — findings/judge cited-issues/disposition reason are unreachable from inside a run; (2) no attempt-aware dispatch (definition A on attempt 1, fix-definition B on attempt 2). Arguably the second-most-common real flow, and it can't read what it exists to fix. |
| **spike-research** | Feasibility/perf investigations; deliverable is findings, explicitly no merge intent | needs grammar ext (push knob only) | **ONLY** the harvest push knob remains (H1 — `HarvestStep` already supports `Push:false`, unreachable from grammar). The terminal-state half is closed: `concluded` DECIDED + APPLIED 2026-07-09 (enum pre-freeze, §2) — a finished spike no longer rots in `needs_review`. |
| **best-of-n-candidates** | High-ambiguity, high-value tickets: N parallel attempts, judge ranks, push the winner | needs grammar ext | Fan-out (H2), squarely. Cannot be faked with N runs: single-writer ticket state machine + §2.8.1's one-branch-per-ticket rule means N concurrent harvests would clobber each other. Needs parallel group + select/reduce step. |

### Definition sketches

- **direct-one-shot** — steps: one agent step, prompt "read_ticket; implement exactly what the body asks; run dev-mcp affected tests; commit", sidecars `[dev, platform]`, budget ~$3, outputs `[workspace]`. Gates build+test `affected_then_full`; small rubric against the ticket body. Implicit harvest pushes `agent/ticket-N`.
- **test-first-contract** — [1] agent write-tests (commit failing tests; `submit_spec` embedding the test list so the reviewer can approve from the ticket page) → [2] checkpoint tests-approval (`on_reject: send_back`) → [3] agent implement-to-green ("make the committed tests pass; do not weaken them"). Judge dimension: "tests unmodified since checkpoint".
- **fix-loop** — separate `fix` definition: one agent step "read_ticket; read prior findings + disposition; base on `agent/ticket-N`; address each finding; commit". Same gate policy; harvest force-with-lease re-pushes the branch.
- **spike-research** — one agent step, generous `max_turns`, sidecars `[dev, platform, gateway]`; `gates: []` (validates — prototype need not build); rubric {question-answered, evidence-quality, reproduction-clarity}; harvest judge-only, no push.
- **best-of-n** — parallel group of N implement steps (varying `model:`/seed), each → `workspace-i`; select step (judge scores each diff, optional cross-provider review, argmax); harvest gates + pushes the winner. N× slice under a hard ticket cap.
- **critic-loop** — one agent step: "implement; `request_review(diff, rubric)`; address confirmed findings; re-review; stop after 3 iterations or a clean pass" — bounded by `max_turns` + `budget_slice_usd` + gateway per-call cutoff. Each review call is individually metered in the flight recorder, so iterations are observable without being steps.
- **heavy-hitl-pairing** — spec-and-plan → checkpoint plan-approval (`send_back`) → implement whose prompt mandates `ask_human` after every `update_task_status(done)`. `hitl: {ask_timeout: default, ask_timeout_seconds: 14400}`.
- **standard-dev** — the shipped seed: write-spec (`submit_spec`) → plan-approval checkpoint → implement (`submit_plan`, per-task status, affected tests), sidecars `[dev, platform, gateway]`, rubric {correctness-vs-spec, tests, scope-discipline}.

### What the tooling quietly privileges

Five of platform-mcp's seven tools serve the spec→plan shape; the ticket page's marquee panels (spec, live plan progress) render empty for flows that never call them; scorecard plan/spec signals accrue only to spec-producing flows and merge-rate metrics assume merge intent (penalizing spikes). The constants-vs-variables principle holds **at the grammar layer** for linear merge-intent flows — but seeds, judge wording, UI, and scorecards all lean toward the one seeded workflow. E3 (second seed) plus a scorecard note that spec/plan panels are per-flow instrumentation, not health signals, is the cheap correction.

---

## 4. What the grammar deliberately cannot express (v2 seams)

Grammar v1 = linear ordered list of `agent|checkpoint` steps, forward-only artifact dataflow, implicit pushing harvest (charter decision 13, 05:17). What's out, the seam for adding it, and whether deferring is still right:

| Capability | Smallest extension | Size | Threatens linear v1? | Defer? |
|------------|--------------------|------|----------------------|--------|
| **Harvest opt-out / advisory mode** (spike, review-only) | Top-level `harvest: {push: bool}` or `mode: push\|advisory\|none`; one Validate case in 05 + a conditional around Render's append (11:1256). `HarvestStep` already supports it below the grammar. Pair with relaxing the repo requirement (H3) as one "no-code workflow" change. `concluded` terminal state: **DECIDED + APPLIED 2026-07-09** (enum pre-freeze, §2). | S/M | No — orthogonal to step ordering | State-enum decision **done** (2026-07-09); the knob itself rides the first spike-flow request. |
| **Render-time conditionals** (`when: ticket.has_spec` etc.) | Render-time pruning on ticket fields — the renderer already holds the Ticket; no run-time machinery, consistent with the render-time-resolution rule (11 §2.8). | M | No, if render-time only. An expression language would. | Defer until a concrete definition needs it; keep the door explicitly render-time-only. |
| **Parallel / fan-out (best-of-N)** | `parallel:` step group mapping onto atc's existing `in_parallel` — mechanically small, but N workspace names + a join/select step + per-branch budget slices + relaxing §2.8.1's one-branch rule are real design work. | L | **Yes** — this is the one that genuinely threatens linear-v1 YAGNI. | Defer. Note batch-level fan-out already exists at another layer (process-intel experiments create N runs reusing this renderer, 11:19) — adjacent machinery, wrong semantics for shipping one ticket, but proof the renderer composes. |
| **Loops / dynamic step counts** | None small. Contradicts pure-render/self-contained-pipeline. Sanctioned substitutes: agent-internal `max_turns` (critic-loop) and whole-run `send_back` re-dispatch capped by `MaxAttempts` (11:70, Task 11b). | L+ | Yes, by construction | Defer indefinitely; the two substitutes cover observed needs. |
| **Per-step `spec_delivery`** | `spec_delivery` on `workflow.Step` overriding the Config-level value; per-step check in Render/mount logic. | S | No | Defer until a mixed-mode definition exists. |
| **Attempt-aware dispatch** (fix-loop) | Not a grammar change: either a `fix_workflow:` ref on the definition or dispatch convention keyed on attempt>1; plus a platform-mcp `read_reviews`-style tool for prior findings. | M | No | **Don't defer long** — fix-loop is the flow the lifecycle already promises (`sent_back → queued`, force-with-lease re-push) but can't deliver. |

**Is deferring still right?** Yes for parallel/loops — no live flow needs them, both are L, and both attack the linear-v1 charter head-on. The honest exceptions, updated 2026-07-09: (1) ~~the harvest push knob + `concluded` state~~ — the enum half is resolved (`concluded` landed pre-freeze, §2); only the push knob defers, safely, since it's grammar-additive; (2) fix-loop's evidence-read tool, **unchanged and still open** — the platform already advertises the sent-back loop it feeds.

---

## 5. Recommended first seeds alongside standard-dev

For dogfooding Phase 0/1 A/B on the scorecards. **Both shipped as seeds 2026-07-09** (E3 + companion; §3):

1. **direct-one-shot** — zero platform changes, immediately exercisable on retrospective-filed chores; the cheapest possible A/B against standard-dev on cost-per-merged-ticket, and its existence alone (E3) fixes the strongest soft coupling. *Seeded 2026-07-09.*
2. **test-first-contract** — §2's edits are applied; its checkpoint-evidence gap keeps the documented workaround (§3). It A/Bs the *interesting* question — does approving a test contract beat approving a prose plan on rework rate and human review minutes — while exercising checkpoints, `send_back`, and judge rubrics on a non-spec shape. *Seeded 2026-07-09.*

Fix-loop is the third priority but is gated on the evidence-read tool; file it as a tracked gap, not a seed.
