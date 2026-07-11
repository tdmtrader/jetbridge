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

---

## Part 2: The agent model — bounded batch steps, audited

**Status:** program doc, 2026-07-10. Companion question to Part 1: Part 1 asked whether the platform is coupled to the spec→plan→implement *flow*; Part 2 asks whether it is coupled to the original jetbridge *agent concept* — the agent as a bounded batch step — and whether that concept still makes sense.
**Question answered:** how tightly is the platform coupled to "agent = task-shaped function", and does that concept hold up?
**Verdict:** the coupling is deep and load-bearing — four of the five core platform promises are arithmetic over step boundaries — but it is coupling-as-the-bet, not coupling-as-defect. The concept survives audit everywhere except one workload: indefinite human-wait, where the batch model currently impersonates a session (ask_human park) and pays for it in F-series findings. The recommended posture is: keep the bet, ship four bounded accommodations, and explicitly refuse three foreign models.

### P2.1 The original concept, in its own words

There is no agent-concept prose in the `atc/worker/jetbridge` Go doc comments — the package is purely pods/volumes/exec. The founding claim lives in `JETBRIDGE.md`, "Origin and Goal of this project" (line 12):

> "At its core, I've always viewed concourse tasks as functions that take an input and produce an output. The composability of these functions is what gives concourse its power and flexibility, and also why I think it has great potential as an agent platform. The ability to tightly, consistently, and repeatably define exactly what gets fed to an agent and exactly what it produces allow the volatility of the AI Agents themselves to be more tightly bounded for repeatable workflows. The _results_ themselves may differ … but the _process_ of producing those results becomes versioned, repeatable, composable, _and_ transparent."

So the concept is explicit: **agent = a task-shaped function** — defined inputs, defined outputs, bounded volatility — and the four promised properties (versioned, repeatable, composable, transparent) are attributed to that shape. The 2026-07-07 end-state spec operationalizes it into contract:

- An agent step "finishes by leaving committed work in its workspace" (Vision, line 9); "The agent's job ends when its workspace contains committed work. Agents hold no credentials and cannot push." (§5, line 78).
- Long-lived sessions are consciously excluded: "Out of scope (end state): Interactive/reattachable agent sessions (pause-pod primitives remain available if wanted later)." (line 170).

### P2.2 Coupling classification

**Framing note, stated up front:** load-bearing coupling to a GOOD bet is not a defect. The question is not "is the platform coupled to boundedness" (it is, thoroughly) but "is boundedness the thing actually generating the promised properties" — and for four of five promises the answer is yes, mechanically. A platform whose core promises did *not* depend on its core concept would be the worrying finding.

| Coupling | Location | Kind | What breaks if agents become sessions |
|----------|----------|------|----------------------------------------|
| Founding metaphor: tasks as input→output functions; four properties attributed to boundedness | JETBRIDGE.md line 12 | **rhetorical** | Nothing mechanical; but a session model must re-argue where repeatability and transparency come from — "exactly what gets fed to an agent" stops being definable |
| Agent step defined as terminating ("job ends when workspace contains committed work") | spec Vision line 9 + §5 lines 74–78 | **load-bearing** | The back-of-contract work product (pushed branch + patch manifest + evidence + score) has no trigger point — nothing tells harvest when the workspace is final |
| Verify-state: harvest re-runs gates against "the final workspace state"; F33 fails on dirty tree | spec principle 4; 09-harvest Goal + F33 (lines 98, 3966–3982) | **load-bearing** | "Final workspace state" is undefined for a live session; verification becomes TOCTOU against a moving target — exactly the trust gap verify-state exists to close. This is the platform's core trust promise and it depends directly on agent termination |
| Render-time-literal AgentStep (Prompt, Model, MaxTurns, BudgetSliceUSD); "reproducible from config alone" | 00-shared-contracts §2.8 (754–790), §6.2 | **load-bearing** | Session behavior depends on accumulated, unhashed conversational state → two runs of the same workflow version stop being the comparison unit; provenance and scorecards lose their denominator |
| Flight-recorder bracketing: stream missing `step.end` = platform error; step.end carries the whole rollup | 00-shared-contracts §5 (1262–1288) | **load-bearing** | A non-terminating session never emits step.end; its observability record is absent or permanently classified as platform error |
| Synchronous ingestion after process.Wait; cost ledger deduped on UNIQUE (build_id, plan_id) | 07-agent-step line 7 + Task 13 (2221–2228); F3/F24 | **load-bearing** | Tokens/cost never land while a session runs; the one-row dedup key has no representation for "same session, more spend"; F12's admission + post-hoc reconciliation degenerates to no reconciliation ever |
| Budget arithmetic is step-shaped: StepSlice admission, per-step --max-turns + timeout bounds | 00-shared-contracts §2.7 (720–752); F12 amendment | **load-bearing** | Step admission is the only enforcement point for the main agent's spend; without step boundaries, "every dollar is budgeted and attributed" survives only for gateway sub-calls |
| Grammar atoms ARE bounded steps; harvest implicitly appended as terminal | §6.1 (1387–1393); 05-workflow-store | **load-bearing** | A session is not expressible; "step with an end, followed by harvest" is the grammar's *semantics*, not a v1 simplification |
| Scorecards aggregate per-step rows per workflow version; promotion mechanism | 13-scorecards (15–55); §1.8 agent_run_metrics | **load-bearing** | No per-step rows to aggregate, no version-frozen behavior to compare, no clean ticket_id cost join — the comparison economy that replaces regression gates collapses |
| Rework = fresh bounded run force-pushing one stable branch; per-attempt branches forbidden | §2.8.1 (792–800); 09 rework loop | **load-bearing** | A persistent session mutates in place; outcome watcher merge detection and human-touch-delta accounting lose their attempt boundaries |
| Run-scoped ephemeral secret + principal expiry; RunSecretReaper | §8.1/§8.2 (1526–1558); F22 | **incidental** | Needs re-keying (lease/renewal) but the mechanism is parameterized — the 72h park expiry already exists as a knob. Bounded change, not a broken promise. (§8.3 harvest-only push isolation is orthogonal and survives either model) |
| Supervisor, attachOrRun, F31 pause loop, ask_human park protocol | 07 Task 11B F18/F31 (1635–1876); spec lines 141, 170 | **incidental — counter-evidence** | Nothing. The jetbridge *runtime* substrate already keeps an agent process alive for days across web restarts and severed connections. The coupling to boundedness lives entirely in the platform layer above (ingestion, budget, harvest, scorecards), not in the runtime |
| "Code for mechanics, agents for judgment, humans for approval"; sidecar litmus test | spec principle 3 (17) + §6 (91) | **rhetorical** | Transfers unchanged to a session model — a long-lived agent could equally hold no terminal tools |

**Reading:** the runtime could host long-lived agent sessions today; what cannot is everything the platform *promises* about them — verification, budgets, metrics, provenance comparison — all of which are arithmetic over step boundaries that sessions erase.

### P2.3 Strain points — where the batch model is already bent

Audited against the F-series findings as the empirical record. The bends cluster exactly where the plans *pretend* conversational continuity exists inside batch semantics — not where continuity is honestly absent.

| # | Strain | Evidence | Verdict |
|---|--------|----------|---------|
| S1 | **ask_human park is a live interactive session smuggled inside a blocked tool call** — a claude process parked in a blocked MCP tools/call for up to 72h; park+0 = wait indefinitely | F13 (BLOCKER: CLI silently abandons a progress-free tools/call at exactly 60s, v2.1.77 empirical; fix = SSE heartbeats across all three sidecars + mirrored drift-guarded port); F31 (major, 3 legs: 24h sleep kill, ~4h SPDY severance, 6h principal expiry → silent forever-park); F18 (BLOCKER: park built on resume semantics that didn't exist for ContainerTypeAgent). Steady-state: idle pod pinned to a node for days, SSE 15s heartbeats, `running` build occupying concurrency. **Park buys zero token savings** — Anthropic prompt-cache TTL is 5min (1h max), so after any multi-hour park the next turn is a full cache-miss re-send whether the process lived or died | **needs-different-model** (for long waits). Terminate-and-respawn via `claude -p --resume <session-id>` — verified locally at v2.1.77; session_id already parsed-and-discarded (ci-agent/llm/result.go:51). Hybrid: SSE-park under ~30–60min, exit-and-respawn beyond. Note: the review explicitly did not require this ("nothing needs redesign", REVIEW:364) — this verdict is about which model carries less permanent complexity |
| S2 | **Supervisor "resume" conflates re-attach with re-execute** — happy path genuinely resumes a severed exec; reaped-pod path re-executes the ENTIRE claude session from the original prompt through the same code path and dedup key | F18 (agent ran unsupervised; web restart SIGHUP-killed claude mid-flight, tokens spent twice, silently); F24 (degraded {error, cost 0} row clobbered the real row; reaped-pod rerun's real cost intentionally unledgered — accepted under-count) | **accommodate-within-steps.** Landed fixes (supervised() Task‖Agent, xmax UpsertReturningInserted, InsertIfAbsent, live resume test) are sound. Cheap later improvement: persist session JSONL to the flight volume; agent-runner attempts `--resume` before cold re-run — rerun becomes true continuation with its own metrics row |
| S3 | **Cold context per step** — every ci-agent phase step is a fresh `claude -p`, zero conversational carry-over; continuity reconstructed via rendered templates | No F-finding indicts this seam. Prompt cache wouldn't survive between steps anyway (5-min TTL); cold context is load-bearing for determinism, per-step provenance, bounded context windows, and independently re-runnable steps | **acceptable-as-is** — the step model's genuine strength, not a bend. Opt-in later: within-phase `--resume` chaining (steps seconds apart may land the cache window); measure re-establishment cost via flight-recorder events first |
| S4 | **No observation or steering of a running agent step** — `--output-format json` means stdout is only the final envelope; a 6-hour step is a black box until exit; the only mid-flight input channel is agent-initiated ask_human | Scope hole, not a defect — no F-finding because the plans never promised observability. Operational cost: stuck agents (failing-test loops) burn turns invisibly until --max-turns or timeout; only recourse is abort-and-redispatch, forfeiting all spend | **accommodate-within-steps.** Watching is nearly free: `--output-format stream-json` teed to stdout makes `fly watch` a live transcript over the existing build-event fabric. Steering is genuinely a session — correctly out of scope |
| S5 | **Parked runs distort lifecycle vocabulary** — `running` means waiting; timeout policy forked into park/default/fail; principals forked into 6h run vs 72h park policies; each fork needs its own loud-failure guard | F7 (config cross-validation), F30 (run-number/run-id conflation), F31 leg 3 found "documented but unimplemented" in a follow-up sweep (REVIEW:408) — even the authors lost track of one fork. A run showing `running` for 3 days is indistinguishable from a hung agent | **accommodate-within-steps** as landed (coherent, guarded). Real simplification is downstream of S1: exit-and-respawn makes `parked`/`awaiting-human` a first-class run state (zero pods, no live principal) and most forks dissolve. Minimum now: surface "parked" distinctly in the UI so `running` stops lying |

**Net: 1 needs-different-model (bounded hybrid), 3 accommodate-within-steps, 1 acceptable-as-is.** The batch model survives the audit everywhere except indefinite human-wait — the one workload it structurally cannot represent without impersonating a session.

### P2.4 Alternative agent models — support classification

What the current architecture quietly forecloses: (1) the one-envelope-at-exit ingestion contract (anything continuous/streaming needs a second ingestion path); (2) the per-run per-triggering-user credential model (no seam for system-triggered or shared agents — "whose token, whose budget"); (3) the pure-render self-contained-pipeline charter (dynamic step materialization permanently out); (4, softer) minutes-scale latency floor (dispatcher poll + pod schedule + harvest). Notably NOT foreclosed: interactive sessions (pause-pod/supervisor primitives deliberately kept) and verify-state for externally-produced work (harvest trusts workspace state, not transcripts).

| Model | What it buys | What breaks | Support | Extension name |
|-------|--------------|-------------|---------|----------------|
| **Long-lived interactive sessions** (Claude Code style, reattachable — the explicitly-deferred option) | Human pairing, mid-flight steering, exploratory work, warm in-context state, hijack-style reattach | StepSlice budget admission (no bounded invocation to admit; reconciliation point never fires); flight recorder (no exit envelope); scorecards confounded by human keystrokes mid-run. Verify-state and harvest survive cleanly — whenever the human ends the session, committed workspace state is verified/judged/pushed exactly as today | **bounded-extension** | **"Session step"** — completes on explicit human end-session disposition rather than process exit; needs streaming ingestion, time+turn metering, an attach surface, and a scorecard human-touched exclusion flag |
| **Session-continuity batch steps** (each step `--resume`s the prior step's session from a workspace artifact) | Conversational memory across steps without long-lived pods; kills re-priming cost; pods stay ephemeral | Almost nothing mechanically — slices, envelopes, verify-state, harvest all hold. The break is philosophical: the *transcript* becomes the inter-step channel, so a step-1 hallucination is durable unreviewed context for every later step, bypassing the schema-constrained submit_spec/submit_plan write path | **native-compatible** | **"Session artifact"** — session is just another named artifact in the grammar; agent-runner needs one flag (AGENT_SESSION_FILE → `--resume`) + one §8.1 contract row. Pair with a judge-rubric note: inter-step *facts* must still land in spec/plan rows |
| **Persistent agent-as-service** (daemon per repo/team, claims tickets, warm memory across tickets) | Warm compute + accumulated memory; sub-second pickup; behavior improves with tenure | Nearly every promise, structurally: self-claiming bypasses budget admission; per-user credential attribution dies or user tokens co-reside in one long process; spend attribution trusts the transcript (principle 4 forbids); scorecards collapse (non-stationary daemon, no reproducible cold starts); memory poisoning propagates across tickets. Only verify-state survives | **foreign** | **Don't build.** Decompose the buys: warm memory → **"memory-as-artifact"** (versioned per-repo memory file as workflow input, updated via harvest/retrospective — auditable, poisoning diffable); warm compute → DaemonSet artifact cache + pre-warmed base image; fast pickup → poll-interval tuning |
| **Orchestrator-agent** (agent spawns/coordinates sub-agents dynamically) | Adaptive decomposition, dynamic review loops, best-of-N judgment, cross-provider ensembles — without grammar v2 | In its in-sidecar form, remarkably little — half-shipped already: the gateway IS this model (request_review/ask_agent, per-call metering, the one mid-call dollar cutoff). Degrades: step-level scorecard resolution (orchestration collapses to one row). Breaks at edges: sub-agents share the parent pod; true dynamic step materialization is foreign by charter | **bounded-extension** | **"Platform-scheduled-pod gateway backend"** (already named in the spec) — sub-agents get their own jetbridge pods behind the same tool contract; plus a flight-recorder sub-agent-span event convention so scorecards can decompose an orchestrated step |
| **Event-driven micro-agents** (review-comment responder, CI-failure triager — no ticket, no workflow) | Second-scale latency, zero ceremony, real-time "catches migrate leftward" | As stated, breaks the platform's one real front invariant (the ticket envelope) and the user-less-ticket credential gap (H3); latency buy structurally unreachable anyway (minutes-scale floor) | **bounded-extension** | **"Event→ticket adapter"** — the pattern the platform already dogfoods (retrospective files origin-tagged tickets): a poll-backed watcher component files `origin:ci-failure` / `origin:review-comment` tickets into cheap direct-one-shot definitions; needs a system principal + repo-level budget line; sub-minute reactions stay foreclosed |
| **Remote/hosted agents** (work happens off-cluster; platform orchestrates + verifies) | Zero cluster compute; provider sandboxes; hosted-only agent products; system-of-record verification for work produced anywhere | The entire left half bypassed: sidecars are pod-local by design, credential attach can't reach a remote runtime, budget/recorder numbers become provider-reported (trusting the transcript), supervisor/park are pod primitives. The back half survives BECAUSE of principle 4: harvest can fetch a remote branch and independently re-verify, judge, and outcome-track | **bounded-extension** | **"Verification-harness mode"** — a definition whose step materializes the remote branch, then unchanged harvest re-verifies/judges/annotates; sold with losses explicit (budgets, recorder, HITL, credential isolation N/A for the remote span). Remote agents consuming platform-mcp over the network = security redesign = foreign |

### P2.5 Recommendations

**Bounded accommodations worth planning** (all preserve the batch-step bet):

1. **Live watching via stream-json** (S4) — **S**. Switch agent-runner to `--output-format stream-json`, tee to stdout; `fly watch` becomes a live agent transcript over existing build-event fabric. No new API, no lifecycle change. The cheapest observability win the platform can buy; do it in the agent-runner task before first dogfood.
2. **Session JSONL into the flight output** (enables S1/S2/S3 follow-ons) — **S**. Capture `~/.claude/projects/<cwd-slug>/<id>.jsonl` alongside results.json/events.ndjson (it is NOT captured today, 07:61–63), and stop discarding the parsed session_id (result.go:51). Pure additive output contract; unlocks resume without committing to it.
3. **Exit-and-respawn for long parks** (S1) — **M**, and the one recommendation that would amend the shipped design. Hybrid: SSE-park under a threshold (~30–60min, checkpoint approvals in work hours); beyond it, agent exits, question row persists in agent_run_questions, answer arrival dispatches a continuation step (`claude -p --resume <session-id> "<answer>"` against a restored session dir). Eliminates F31 wholesale (zero pods during waits), shrinks the F13 surface, frees nodes, and makes `awaiting-human` a first-class run state (dissolving most S5 forks). **Contract note:** touches the §1.5 parked-run contract and run-state vocabulary — the enum lesson (see Part 1's `concluded` pre-freeze amendment) says decide the state-vocabulary question BEFORE the freeze even if implementation defers. Requires one F13-style empirical pin test first: does `--resume` behave headlessly with a restored session dir + fresh MCP sidecars.
4. **"Parked" surfaced in the UI** (S5 minimum) — **S**. Mirror the ticket-page question banner as a parked badge on the run/pipeline view so `running` stops meaning "maybe waiting for a human for 3 days." Do now regardless of #3.
5. **Deferred but named: session-artifact step chaining** (S3 opt-in) — **S/M** when wanted. Measure first: flight-recorder events already capture per-step turns/tokens, and process-intel's friction mining explicitly targets context re-reads — quantify re-establishment cost per workflow version before spending the complexity.

**Explicitly do NOT build:**

- **Persistent agent-as-service.** Foreign to every promise except verify-state; it is a second platform with its own credential, metering, and observability stories. Every buy decomposes onto native seams (memory-as-artifact, DaemonSet cache, poll tuning).
- **Mid-conversation steering.** Bidirectional input into a running step is a session by another name; the out-of-scope call (spec:170) is right. Closest step-shaped approximation: abort-and-redispatch-with-amended-prompt, which recommendation #2/#3's resume machinery makes non-wasteful.
- **Agent-authored dynamic pipeline steps.** Contradicts the pure-render charter (decision 13); orchestration belongs inside a step via the gateway (in-sidecar today, platform-scheduled-pod backend later), never as runtime step materialization.
- **Run-less execution paths** for sub-minute event reactions. The dispatcher/pod/harvest floor is minutes by construction; honest answer is the event→ticket adapter, not a bypass of the run lifecycle.

### P2.6 Bottom line

The platform is very tightly coupled to the original jetbridge concept — and that is the finding, not the problem. The founding claim (bounded inputs/outputs are what tame agent volatility) is not decoration on top of the architecture; it IS the architecture: verify-state, budget honesty, flight-recorder taxonomy, provenance-hashed workflow versions, and the scorecard promotion economy are all arithmetic over step boundaries. The F-series empirically vindicates the shape in both directions: nothing indicts the honestly-batch seams (S3), while every confirmed blocker clusters where the design smuggles session semantics into batch clothing (S1/S2/S5). The concept makes sense to apply — with one amendment: stop impersonating a session for indefinite human-waits and make exit-and-respawn (with the session file as a first-class artifact) the long-wait model, which is *more* faithful to "agent = bounded function", not less.
