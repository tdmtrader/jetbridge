# Agentic Platform — UX Assessment

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. This UX assessment was written against the pre-build ticket-centric foundation; see the later UX audits (Nos. 3-5) for assessments of the shipped platform.

> Read after `ROADMAP.md`. Written at the moment the foundation (identity, budget, credentials) is merged and **no UI exists yet** — the cheapest possible point to fix user experience, because every finding here is a plan edit or a small net-new surface, not a rebuild. Sourced from three review lenses (web surfaces, fly CLI surfaces, vocabulary/labels). Owner ask: "make sure we're on board with a clean UI and clear user experience before more of the platform is built."

## 1. The assembled user journey

The north star is one arc: **file → dispatch → run → review → merge → outcome → learn.** Here is that arc mapped to the surface that renders each step and where it is defined. Reading it as one line exposes where the seams are.

| Step | What the user does | Surface | Defined in |
|---|---|---|---|
| **File** | Create a ticket (title/body/repo/workflow/budget) | `fly agent tickets create` **only** — no web form | 06 Task 11 |
| **(view)** | Open the ticket | Ticket detail `/agent-tickets/:id` — URL-only, no nav | 06 Task 13 |
| **Dispatch** | Ticket is claimed and rendered to a `template:` pipeline `agent-ticket-<id>` | dispatcher (no user surface); hand-dispatch via `fly run-pipeline` | 11; 03 Tasks 18–19 |
| **Run** | Watch agent steps execute; 5s-polling task list | Ticket page task list; `fly runs`/`fly watch` | 06 Task 13; 03 Task 19 |
| **Answer** | Respond when the agent parks on `ask_human` | Open-question banner + amber "AWAITING HUMAN" chip on ticket page | 08 Tasks 17/17b |
| **Review** | Read diff + proof-carrying evidence + judge score + cost | PR view on ticket page (reuses `Build.AgentReview.view`); per-team `/teams/:team/agent-reviews` | 12 Tasks 14/15 |
| **Merge** | Human merges the branch | GitHub (outside platform) | — |
| **Outcome** | See merged / merged-with-fixes / human-touch delta; six-verdict feedback; disposition | Outcome + merge badge, disposition UI on ticket page | 12 Tasks 14/15 |
| **Metrics** | "Where did the turns go" per step | Metrics panel on ticket page | 13 Task 8 |
| **Learn** | Compare workflow versions; experiments/analytics | Scorecard `/workflows/:name/scorecard?versions=3,4`; experiments/analytics Elm views | 13 Task 9; 14 Task 17 |
| **Cost** | Spend by group/time | `fly agent costs` (CLI); SQL view for Grafana/psql | 02 Tasks 10/17 |

**The journey does not close in the web product.** Step 1 (file) is CLI-only. There is no home, no ticket index, no nav entry — the ticket page, scorecard, experiments, and analytics are each reachable **only by typing a URL with the right id/params.** The web experience begins at step 2, and only if you already know the integer id. The mental model also breaks at the file→run handoff: nothing links a ticket to the pipeline run actually executing it, in web *or* fly.

## 2. Coherence verdict

**Fragmented on the web surface; minor-drift on CLI and on vocabulary.** Honest read: the *data model* is coherent and the backend is largely ready — every gap below is a presentation gap. But as a **product a human navigates**, the web side is a set of disconnected URL-islands with no entry point, no cross-links, no shared status vocabulary, and no breadcrumbs. That is fragmented, and it is fragmented in the cheap-to-fix direction: no UI is built yet, so these are plan edits and one new small surface, not rework.

- **Web (lens 1): fragmented.** No platform home, no web "file" step, islands not cross-linked, status badges reinvented per plan, blank breadcrumbs, one overloaded ticket page.
- **fly CLI (lens 2): minor-drift.** Grouping is already consistent (`fly agent <noun> <verb>`); residual: positional-vs-`--id` split, no ticket↔run cross-link, expiry nag on the wrong command.
- **Vocabulary/labels (lens 3): minor-drift.** The same concepts render raw-token in one place and humanized inches away on the same page; snake_case enums leak into dropdowns and badges.

## 3. Findings, ranked

### Blockers — the product has no front door

**B1. No platform home / web entry point.** Every agentic surface is a URL-only island; nobody can reach anything by clicking. *(06-ticket-core.md:4489 "reachable only via direct URL"; SideBar/TopBar unchanged by 06/08/12/13/14.)*
→ **Net-new surface.** Add a platform-home/ticket-dashboard Elm page at `/agent-tickets` over the already-registered `ListAgentTickets` route, plus one SideBar/TopBar entry. Make it the anchor everything else breadcrumbs back to. See §4.

**B2. The first journey step — "file a ticket" — has no web UI.** The web ticket page only *edits*; creation is CLI/Jira/retrospective only. The web product starts at step 2. *(06-ticket-core.md:15 "read side only"; `CreateAgentTicket` route already supports `origin: web`.)*
→ **Net-new surface (small).** Add a create form (title/body/repo/workflow/budget) to the B1 dashboard, POSTing the existing `CreateAgentTicket` with origin `web`. Backend is ready; view-only addition, currently unplanned.

### Majors — islands don't connect, and status is reinvented everywhere

**M1. Islands are not cross-linked.** A ticket page never links to the build running it, its workflow scorecard, or (from an experiment) the tickets it spawned — even though `PipelineRunID`/`WorkflowName`/`WorkflowVersion`/`TicketID` all exist in the model. *(06:434-439; 14 §2.9; scorecard 13 Task 9.)*
→ **Plan edit (assign an owner).** Add view-only links on the ticket page ("View run" → the build for `pipeline_run_id`; "Workflow scorecard" → `/workflows/<name>/scorecard?versions=<version>`) and "tickets" links on experiment views. Small edits, but each plan currently assumes someone else connects them — name the owner.

**M2. Scorecard unreachable without hand-typed name + version params.** No workflows index; no way to discover which workflows/versions exist. *(13-scorecards.md:456.)*
→ **Plan edit / fold into B1.** Add a workflows index (or a section of the platform home) listing `workflow_name` + versions with scorecard links; `Versions(name)`/`Live(name)` already exist.

**M3. Status badges/colors reinvented in every plan — no shared component.** Ticket lifecycle badge (06), merge/outcome badge (12), amber AWAITING HUMAN chip (08), scorecard "live" badge (13), task-status glyphs (06): four+ color systems, no legend, no guarantee the same concept looks the same. Only enum *values* are coordinated, not the visual component. *(06:4497; 12 Task 14; 08 Task 17b `agent-awaiting-human-chip`; 13:997.)*
→ **Plan edit (wave-start contract).** Extract a shared `AgentBadge` / status-color Elm module (lifecycle states, task statuses, merge states, run status, one legend) and make it a wave-start contract the way enum coordination already is. 06/08/12/13 consume it. The `Build.AgentReview.view` reuse is the precedent to follow.

**M4. Chrome never orients the user — blank breadcrumbs on every agentic page.** Agentic pages wear standard Concourse top/side chrome (implying they belong to Concourse) but the breadcrumb bar is blank because no agentic route has a breadcrumb clause. Concourse chrome with no "you are here." *(TopBar.elm matches only Pipeline/Build/Resource/Job/Dashboard/Causality then `_ ->`.)*
→ **Plan edit (depends on B1).** Add breadcrumb clauses for AgentTicket / Scorecards / Experiments rooted at a platform-home crumb, mirroring the existing Pipeline clause.

**M5. Identity args split between positional and `--id` inside `fly agent`.** `tickets show --id 7` (flag) vs `workflows show standard-dev` / `set-live standard-dev 2` (positional). Fingers learn one idiom, it breaks at the neighbor. *(05 Task 10 L2354-2357/2446-2449; 06 Task 11 L3434.)*
→ **Plan edit.** Pick one convention for the whole group. Cleanest: positional identifiers for `show` everywhere (`fly agent tickets show 7`) — amend 06 Task 11 to drop `--id`, matching the already-written `workflows show`.

**M6. Ticket↔run vocabulary seam in fly.** The platform asks users to think in "tickets" but execution surfaces as a run `agent-ticket-<id>` via `fly runs`/`fly watch`, and neither side back-references the other. The mental model breaks exactly at the handoff the product is about. *(11:61, 5043-5047; 06 Task 11 L3439-3443; 03 Task 19.)*
→ **Plan edit (11 or 12).** Have `fly agent tickets show` print the dispatched run coordinates + a copy-pasteable `fly watch -j agent-ticket-<id>/<run>` line once dispatch exists. Additive output field, not new command surface.

**M7. Same lifecycle vocabulary raw on one badge, humanized on another — same page.** Lifecycle badge renders the literal DB token `needs_review`/`merged_with_fixes`/`sent_back`; inches away the outcome badge renders the same concept as "merged with fixes." The primary badge looks like leaked internal state. *(06 Task 13 `[ Html.text t.state ]` ~L4245; 12 `mergeStateLabel` ~L3926.)*
→ **Plan edit (06 Task 13).** Add a `stateLabel : String -> String` humanizer mirroring `mergeStateLabel`; render `Html.text (stateLabel t.state)`. Canonical user-facing spelling: lowercase words with spaces.

**M8. Six-verdict taxonomy spelled out in the review panel, cryptic in the scorecard.** Review panel shows "false positive"/"overly strict"; the scorecard collapses the same six to a positional header `Verdicts (acc/fp/noisy/strict/partial/missed)` and cells like `2/1/0/0/0/0` the reader must decode by slash position. *(AgentReview.elm:77 `verdictLabel`; 13 metricRow ~L1903/1938.)*
→ **Plan edit (13 Task 9).** Reuse `Concourse.AgentReview.verdictLabel` in the scorecard — one labeled sub-row per verdict, or per-count labels/tooltips.

**M9. Disposition/reason dropdowns show raw snake_case tokens.** Users choose from `sent_back`/`abandoned`/`concluded` and `wrong_approach`/`not_needed`/`research_complete`/`superseded` verbatim — and `concluded` vs `abandoned` is exactly the pair the spec worried users would confuse. *(12 Task 14 `optionFor` ~L4132.)*
→ **Plan edit (12 Task 14).** Map each token to a plain label in `optionFor` (e.g. `sent_back` → "Send back for rework", `concluded` → "Conclude — no merge intended (spike/research)"); keep the token as the option `value`.

### Minors — layout, duplication, naming, and one misleading badge

**m1. Ticket page overloaded — four plans stack sections with no IA.** `/agent-tickets/:id` accumulates title/body edit, task list, question banner, PR diff, evidence, judge score, cost, disposition, and a metrics panel — each plan independently choosing a vertical slot. This is the de-facto hub; the diff or open question gets buried in a long scroll. *(06 Task 13; 08 Task 17 "top of page body"; 12 Task 14 "below task list"; 13 Task 8.)*
→ **Plan edit (06, consumed by 08/12/13).** Define a sectioned/tabbed layout (Overview / Run / Diff & Review / Metrics) as a shared contract in 06 so each later plan targets a named section, not a raw position.

**m2. Scorecard page and experiment-delta view duplicate the comparison UI.** 14 reuses scorecards' SQL recipe and field names for `GetAgentExperimentDelta`, but the Elm is built twice — two different-looking tables of the same data. *(14 §1.12.2 ~L91; 13 Task 9 vs 14 Task 17.)*
→ **Plan edit (coordinate 13/14).** Share the column-rendering Elm component; 14 already reuses the numeric shape — reuse the presentation too.

**m3. `fly agent` is fly's only nested group — write the rule down.** Grouping is already consistent (go-flags nesting verified); the residual risk is a later workstream adding a flat `fly agent-foo` and re-fragmenting. *(05 Task 10 L2218; run-pipeline/runs correctly kept flat.)*
→ **Convention (one line in 00 or ROADMAP).** "Agent-scoped commands nest under `fly agent <noun> <verb>`; general-CI commands stay flat verb-noun." No code change.

**m4. Credential expiry nag lives on `fly status`, not under `fly agent`.** Users vault tokens with `fly agent auth`, so that's where they'll check "is my token still good?" — but there's no read-back; `fly agent auth` with no token just errors. *(02 L17, Task 17 L4893; agent_auth.go L5092-5144.)*
→ **Plan edit (02 Task 17).** Make `fly agent auth` with no `--token` (or a `--status` flag) print stored kind + expiry. Keep the `fly status` nag too.

**m5. `dispatch.sh` name collides with the platform "dispatch" domain.** The Phase-0 dogfood wrapper (set-pipeline + unpause + trigger-job) is correctly *not* a fly command, but its name collides with plan 11 (the ticket dispatcher), so readers conflate "run the dogfood build" with "dispatch an agent ticket." *(ci/dogfood/dispatch.sh; 11-dispatch.md.)*
→ **Rename (not a plan edit).** Rename to `dogfood-run.sh` + one README line: build self-host path, not ticket-dispatch. Keep it a script.

**m6. Lifecycle spelled three ways across spec / enums / UI.** Spec prose uses hyphens (`needs-review`), API/fly/DB use snake_case (`needs_review`), UI uses spaces. Someone filtering `fly agent tickets --state` after reading the spec guesses the hyphenated form and it won't match. *(Spec §1/§9; 00 §1.7/§2.1; 06 ~L3496.)*
→ **Doc edit.** Declare snake_case the one canonical machine token in spec §1 (e.g. "needs-review (`needs_review`)") and derive every human label from a single humanizer.

**m7. "state" vs "status" vs "merge state" vs "verdict" vs "disposition" stack as near-synonyms on one page.** Six status-like axes co-render with nothing telling the user they're different axes. *(00 §1.7/§1.5/§1.8; 13 Task 8; AgentTicket.elm.)*
→ **Plan edit (presentation).** The contracts already fix the rule (`state` = lifecycle, `status` = run/step). Surface it: group badges with axis labels (Lifecycle / Run / Review) + a one-line glossary.

**m8. During a human park the badge still says "running."** Ticket state deliberately stays `running` on `ask_human`/checkpoint; awaiting-ness shows only via the separate amber chip, which can be scrolled past. The most-scanned signal actively misreports. *(08 Task 17b ~L7196.)*
→ **Plan edit (06/08 badge, presentation-only).** Drive the badge from the same derivation (open questions OR run status `awaiting_human`) so it reads "awaiting human" instead of "running." No enum change.

## 4. The biggest gap: yes, a platform home is missing — build it

**Call: YES.** The single largest UX gap is the absence of a unifying **platform home / ticket dashboard.** It is the root cause of B1, B2, M2, M4, and it's where M1's cross-links naturally anchor. Every other web surface is downstream of "how do I get here at all," and today the answer is "type a URL." Fixing this one surface converts a set of islands into a product.

**Sketch — `/agent-tickets` (platform home):**
- **Ticket list** over `ListAgentTickets`: id, title, lifecycle badge (via the shared M3 `AgentBadge`), workflow name+version, cost-so-far vs budget, last activity. Row → ticket detail.
- **Create form (B2):** title/body/repo/workflow/budget → `CreateAgentTicket` origin `web`. This is the web "file" step the journey is missing.
- **Workflows index (M2):** each `workflow_name` with its versions (live vs candidate) → scorecard links. Uses existing `Versions(name)`/`Live(name)`.
- **Nav entry:** one SideBar (or TopBar) item — the first click-path into the platform.
- **Breadcrumb root (M4):** the crumb every other agentic page roots to.

**Ownership.** This aggregates ticket-core + outcomes + scorecards, so it should **not** be bolted onto 06 (which is deliberately scoped to a single ticket's read/edit). Recommend a **new small workstream/plan** (call it `15-platform-home` or fold into a "web shell" track) that owns: the dashboard page, the create form, the workflows index, the nav entry, and the breadcrumb root — and declares the shared `AgentBadge` module (M3) and the sectioned ticket-layout contract (m1) as its wave-start deliverables, since it is the natural owner of cross-surface web conventions.

## 5. UI build order — anchor the visual language first

UI tasks are the **least dogfoodable** work in the program (per ROADMAP §dogfooding: Elm/UI is explicitly human-driven — a verbatim agent can't visually verify a page). So the first UI surface built and human-verified is doing double duty: it ships value *and* it fixes the visual vocabulary that every later page copies. Build the wrong one first and each subsequent plan re-invents badges and layout (exactly M3/m1).

**Recommended first UI surface: the platform home + ticket detail page, built together as one anchor, with the shared `AgentBadge` module extracted as step one.**

Order:
1. **`AgentBadge` / status-color module + a rendered legend page (M3).** Tiny, pure, human-verifiable in isolation, and it is the contract 06/08/12/13 all consume. Verify colors/labels in light and dark before anything renders them for real.
2. **Platform home `/agent-tickets` + create form + nav entry + breadcrumb root (§4 / B1, B2, M4).** This is the front door; verifying it also verifies the shared chrome integration every other page inherits.
3. **Ticket detail `/agent-tickets/:id` with the sectioned layout contract (06 + m1).** The hub. Lock the Overview/Run/Diff&Review/Metrics section scaffold here so 08/12/13 append into named slots.

Only after those three are human-verified should 08 (question banner), 12 (PR view/disposition), and 13 (scorecard/metrics) render their sections — each now targeting a named slot with shared badges, instead of choosing a vertical position and a color scheme independently. The scorecard/experiment comparison table (m2) is the natural fourth, built once and shared between 13 and 14.

**Why not the scorecard first?** It's the most self-contained page and tempting to start there — but it's a leaf, not an anchor. Nothing else reuses its layout, and building it first still leaves the product with no front door. Anchor the shell and the shared badge language first; leaves last.
