# triage-jb-001 — curation record

## Provenance walk

Backed out of a scoping document on the `jetbridge` line of this repo. Every SHA
below was resolved and read; nothing was taken on the mining pass's word.

| Role | SHA | Committer date | Subject |
|---|---|---|---|
| pre_state (parent of the terminal) | `6188b2a8c1e3b954434a82ae8c90423cb469c199` | 2026-07-19T08:25:56-07:00 | `Merge agent-elm-perf: stop re-doing heavy agent-page work on every 5s render` |
| terminal artifact (the routing) | `05ef24ec6972e3c28a7711122e60dae0a4ba24cc` | 2026-07-19T11:25:07-07:00 | `docs(agentic): UX audit №4 scoping — ops runbook, loop tickets 41-46, elm wave ux4, structural tracks S1-S8, workflow improvements` |
| terminal companion (outcome log) | `644184e3f011369f3da77dc82caee200bd8fd196` | 2026-07-19T13:46:04-07:00 | `docs(agentic): UX4 live execution log — A0-1 validated, dispatch findings` |
| tag the stale deployed image resolves to | `5715e7db2d762ab92ebaa2beee0ba05741879699` (`v0.2.167`) | 2026-07-18T09:07:17-07:00 | `feat(deploy): skills-smoke workflow source dir for the slice-b live smoke` |
| commit that introduced the flight recorder | `8457f107c9126acc37392cc08c19b756fc66c19c` | 2026-07-18T15:00 PDT | `feat(harvest): flight recorder + evidence payload types with failing flight tests` |

Verification performed:

- `git rev-parse 05ef24ec69^` → `6188b2a8c1…`. Direct parent confirmed;
  `git rev-list --count 6188b2a8c1..05ef24ec69` = 1, so nothing else landed
  between the audited state and the routing.
- `git show --stat 05ef24ec69` → exactly one file, +196 lines, no code
  companions. The routing is the whole commit.
- The terminal document was read in full. It says what the mining pass claimed:
  a five-class executor legend at its head (`ops` / `loop` / `wave` / `plan` /
  `decision`), five numbered coordination constraints, per-item routing across
  five tables, self-contained draft ticket bodies for the loop items, and a
  seven-step execution order.
- **The pre_state is the audited state, not merely its parent.** The document's
  own header states `v0.2.195-rc = HEAD 6188b2a8c1, verified via vcs.revision`,
  and adds that "the audit saw current code, so every finding below survives the
  latest landings". This is unusually good provenance for an audit case: there is
  no drift between what the auditor saw and what the triager's checkout held.
- `git ls-tree 6188b2a8c1 -- bench/` is empty. The corpus is not reachable
  through the exposure manifest (schema §"Self-hosted corpus caveat").

Every claim the answer key makes about the pre-state tree was re-checked against
the tree rather than against the document:

- `6188b2a8c1:deploy/agent-runner/Dockerfile` — `node:20-bookworm-slim` runtime
  with the Claude CLI, `git`, `ca-certificates`, `curl` and a copied Go toolchain.
  **No Elm compiler.** (Node is present, which is a nice distractor: an agent that
  stops at "node is installed, so front-end work is possible" gets it wrong.)
- `6188b2a8c1:agent/harvest/gates.go:31-33` — the entire gate vocabulary:
  `"build": go build ./...`, `"test": go test ./...`, `"lint": go vet ./...`.
  Nothing can observe Elm breakage.
- `6188b2a8c1:web/public/elm.min.js` exists as a committed 373 KB bundle, and
  contains the string `harvest:` — i.e. the bundle is the served artifact and
  goes stale if not regenerated.
- `6188b2a8c1:deploy/concourse-pipeline.yml:502-510` — the image job pushes
  `registry.home/agent-runner:v${NEXT_VERSION}`, so an image tag is a repo
  version. `git rev-list -n1 v0.2.167` → `5715e7db2d` (2026-07-18T09:07), and
  `agent/harvest/flight.go` was added at `8457f107c9` (2026-07-18T15:00 PDT),
  after it. The stale-image root cause is mechanically checkable.
- `6188b2a8c1:atc/exec/harvest_step.go:598` and `atc/exec/agent_step.go:743` both
  set `rm.Summary = "flight recorder output missing"` on the degrade path — the
  finding text in the task is the literal string in the pre-state source.
- `6188b2a8c1:atc/db/migration/migrations/1773106060_create_agent_run_metrics.up.sql:11`
  — `status TEXT NOT NULL CHECK (status IN ('ok','failed','error'))`, re-stated
  with `'parked'` at `1773106061_agent_run_metrics_parked.up.sql:5-7`. This is the
  constraint the recorded routing wrongly assumed away; it is visible at pre-state,
  which is what makes the X1 bonus fair.
- `6188b2a8c1:docs/superpowers/plans/agentic-platform/remainders/README.md` — all
  five coordination constraints the answer key credits are here (chokepoint 3
  `render.go`, chokepoint 4 migration spine, chokepoint 5 Elm bundle, the
  "What can loop concurrently" paragraph, and the dispatch cadence).
- `6188b2a8c1:agent/dispatch/render.go:247-255` — the harvest step the renderer
  emits is named `"harvest"`, and `6188b2a8c1:atc/public_plan.go:77-79,333-339`
  exposes it with its name. See "Open questions" — F-12 does not reproduce from
  source.

Materialize recipe: the ref-stripping script in `case.yaml` was **executed and
verified on a synthetic five-commit repository** (ancestor tag kept, descendant
tag and branch deleted, descendant commit gone after `gc --prune=now`, HEAD
correct). It was *not* run against the real 1.1 GB repository — the scratch
volume had 3 GB free, and a `--no-hardlinks` clone plus a repack would not have
fit. The reachability facts it depends on were checked directly in the source
instead: `git tag --contains 05ef24ec69` → `v0.2.196` and everything after
(so those tags must be deleted); `git tag --merged 6188b2a8c1` → 546 tags
including `v0.2.167` (so those must be kept). Running the recipe end-to-end is
the first item for the validation stage.

## Task derivation

The trigger for this triage was a live browser audit whose report is an external
`claude.ai` artifact (`https://claude.ai/code/artifact/ad17470d-…`). It is not in
the repository and does not resolve. **The finding list in
`task/audit-findings.md` is therefore reconstructed** — every item is the terminal
document's own description of a finding, restated symptom-first with the routing
removed. This is the case's principal fidelity caveat and it is recorded in
`curation.learnings` as well as here:

- The **decomposition** is the triager's, not the auditor's. The real task
  included "turn a prose report into routable items"; this case starts after that
  step. It grades routing, which is what the curator's rubric asked for, but it is
  a smaller task than the original.
- Two findings are **reconstructed motivations** rather than restated ones: F-27
  (dead ticket pipelines accumulating) and F-28 (principals list mixing run and
  operator principals) are the plainly-implied motivating observations behind two
  loop items whose descriptions in the document were written as solutions
  ("pipeline-archiver component", "principal kinds"). Both are true of the
  pre-state system; neither was invented to make a routing work.
- Item descriptions were rewritten in the auditor's register (what was seen on
  screen) wherever the document's version was written in the implementer's
  register (which files to change). No file path, function name, module name or
  budget figure from the document survives into the task.

## Leakage analysis

### Scrubbed from the task

1. **Executor tags and the ID scheme.** The document's IDs encode their own
   answer: `A0-*` = ops, `L-*` = loop, `W-*` = wave, `S-*` = plan. Every item was
   renumbered into a flat `F-01…F-34` space.
2. **Section structure.** The document groups by executor ("Wave 0 — ops actions",
   "Wave 1 — loop tickets", "Wave 2 — human Elm wave", "Wave 3+ — structural
   tracks"). The task regroups by where the auditor hit the finding, and the
   ordering within groups was deliberately broken up so that same-class items are
   not adjacent.
3. **File pointers.** The document's `Where` column names the exact Elm module for
   each presentation fix. All removed — naming `web/elm/src/**` for sixteen items
   would hand over the Elm capability argument, which is the case's central
   discrimination.
4. **The "Elm rule" and the executor legend's own tells.** The document's legend
   defines `loop` as "server-side Go only" and states the Elm rule inline. The
   task's legend defines each class by *what the executor is*, never by which work
   qualifies — `loop` is described as "an agent in a pod, gates decide whether the
   branch is pushed", which invites the capability check without performing it.
5. **The root cause of F-01.** The document names the stale runner image as the
   cause in its first sentence. The task states only the symptom (in the finding
   list) and the deployed image tag (in the environment block), leaving the
   version→commit walk to the agent.
6. **The entire "Draft ticket specs" section** — four complete, self-contained
   ticket bodies, which are simultaneously the answer to routing item 5 and a
   template for the deliverable.
7. **Coordination constraints, execution order, done-criteria** — all outputs.
8. **The word "loop" in its domain sense.** This repo calls the whole
   dogfood/agent-execution apparatus "the loop", which is also the name of one of
   the five executor classes. Three occurrences in the finding list and one in the
   brief were reworded ("a ticket's whole life", "the system's basic legibility",
   "CLI-only", "the machinery") so that word-association cannot substitute for the
   capability judgement the `loop` class requires. The collision exists in the
   source document too, where it is harmless because the author already knew the
   answer.
9. **The emergent workflow improvements** (`WF-1`, `WF-2`, `WF-3`). These are
   conclusions of the triage, not audit findings: `WF-1` presupposes the image-skew
   root cause and `WF-2` presupposes the Elm capability gap. Including them would
   have leaked both. Only `WF-4` survives into the task, as F-33, because it is a
   plain observation about the dashboard. They are scored as bonus items instead.

### Withheld (in `ground_truth/`, never exposed)

- `reference.diff` — the scoping document as committed. The answer key.
- `outcome-log.diff` — the same-day live execution log. This is worse than an
  answer key: it says which routings held (the ops image refresh, three of the
  loop dispatches), which broke (the migration wall), and names a finding the
  triage missed. Exposing it would give away both the answer and its grading.
- `answer.md`, including the F-NN ↔ document-ID crosswalk.

### Deliberately exposed, and why

- **`docs/superpowers/plans/agentic-platform/remainders/README.md`** and the five
  remainder plans. These carry the coordination constraints *and* the sentence
  "no Elm toolchain in gates — do NOT dispatch this slice to the loop"
  (`2026-07-17-delivery-outcomes.md:105`, `:956`;
  `2026-07-17-platform-mcp-hitl.md:77`; `15-platform-home.md:28`). This is the
  closest thing to a leak in the case and it was kept on purpose: it is a standing
  repo convention that predates the audit by two days, a competent triager would
  be expected to know it, and the case would be *unfair* without it (rubric item
  B3 asks the agent to find constraints, which presumes they are findable). It
  does lower the difficulty of the sixteen presentation items from "derive" to
  "find and cite", which is why B1 awards full credit only for the capability
  argument and not for the citation alone.
- **`docs/superpowers/plans/2026-07-18-agentic-ux-wave-2-scope.md`** — the
  previous audit's scoping document, one day older. It models the *shape* of the
  deliverable (findings → slices → deferred-with-rationale) but uses a different
  taxonomy and shares no items with this audit. Exposed; it is the repo's own
  house style and withholding it would be artificial.
- **`ci/dogfood/FINDINGS.md`** — running log of loop friction, including the
  six-touchpoint add-a-route checklist. Useful for writing good loop ticket bodies
  (rubric B4). No routing content.
- **The two live facts in `task.md`'s environment block** (deployed web version,
  `CONCOURSE_AGENT_STEP_IMAGE=registry.home/agent-runner:v0.2.167`). These could
  not have been read from the UI at this commit — the endpoint that surfaces the
  step image was one of the fixes this very audit produced — so they are labelled
  as operator-supplied, which is exactly how the triager got them. Withholding
  them would make F-01's routing underivable rather than hard, which is a worse
  case. The residual tell is that handing over an image tag hints something is
  wrong with it; accepted, and B2 requires the version→commit check to be
  performed rather than guessed, so a hunch alone does not score.

### Leak vectors outside the repository

- **This machine's memory files.** `~/.claude/projects/…/memory/project_agentic_ux_audit_4.md`
  contains a paragraph-level summary of the audit's P0s, the stale-image root
  cause, the drafts filed, the "Elm items deliberately NOT loop tickets" decision,
  and the full downstream execution history. `MEMORY.md`'s index line for it leaks
  the P0 list on its own. **A replay run on this machine with memory enabled is
  invalid.** Run this case with memory disabled, or on a machine without these
  files.
- **Descendant commits.** `15e4027e50` adds nine plan documents named
  `2026-07-19-s{1..8}-*.md` + `wf2-elm-build-gate.md`; their existence alone
  reveals which items were routed `plan` and that an Elm gate was the answer to
  the capability gap. Unreachable after the materialize recipe (they descend from
  the terminal), but do not hand an agent a repo that still has branch tips.

## Open questions

1. **F-12 does not reproduce from the pre-state source.** The finding is that a
   harvest step's header renders as `step: step`. But at `6188b2a8c1` the Elm
   handles it correctly (`StepTree.elm:1385-1386` → `simpleHeader "harvest:"`),
   the decoder has a `harvest` branch (`Concourse.elm:735`), the server exposes
   the step with its name (`public_plan.go:77-79, 333-339`), the renderer names it
   `"harvest"` (`render.go:248`), and the committed bundle contains the string
   `harvest:`. Either the audit hit a plan shape none of that covers, or it was
   observing something the pre-state source contradicts. The routing does not
   depend on the mechanism — a header rendering defect is presentation work either
   way — but a grader should not penalise a submission that investigates F-12 and
   reports it as not-reproducible. Kept in the item list because it is part of the
   recorded routing; weight it low.
2. **Is F-01's ops/loop pair one item or two?** The record splits it (refresh the
   image; then a code-side status tier) and this case follows the record by making
   the code-side half F-02. A submission that treats them as one item and routes
   the pair to `ops`+`loop` should be scored on both rows, not penalised for the
   different cut.
3. **The `decision` class is nearly empty** — exactly one graded item (inside
   F-26). That is faithful to the record but it makes one fifth of the taxonomy
   almost unscoreable. If a second triage case is built, prefer a source with more
   genuinely-undecided items, or fold `decision` into the rubric as a
   "did-not-manufacture-certainty" check rather than a class.
4. **Part A is not really an `outcome` rubric as the schema means it** — it is a
   34-way per-item taxonomy match, closer to `reference` scoring against
   `expected_findings.yaml` than to the single-decision match the schema
   describes. Flagged in `curation.learnings`; the schema may want a per-item
   variant.

## Validation

*(stub — to be filled by the validation stage)*

Not yet validated. No mechanical transition exists for this case (the deliverable
is a document), so validation here means:

1. **Run the materialize recipe against the real repository** and confirm all four
   assertions in `case.yaml` (`05ef24ec69` gone, `644184e3f0` gone, `v0.2.167`
   resolves to `5715e7db2d`, no `v0.2.19[6-9]` tags). Needs ~3 GB of scratch;
   the recipe itself is verified on a synthetic repo only.
2. **Confirm the exposure manifest is answer-free** — grep the materialized tree
   for `ux4`, `A0-1`, `flight recorder output missing` *as a diagnosis* (the
   string itself is legitimately in the source), and for any file whose path
   contains `2026-07-19`.
3. **Sanity-run the rubric** on the terminal document itself: scoring
   `reference.diff` against `rubric.md` must yield ~100 on Part A and full marks
   on B1–B5, minus X1 (which the record fails by construction). If it does not,
   the rubric and the answer key have drifted apart.
4. **Two independent leakage audits** per `bench/README.md`, with particular
   attention to whether `remainders/2026-07-17-delivery-outcomes.md:105` should be
   moved from "deliberately exposed" to `withheld`. My judgement is exposed; a
   dissent here is exactly the borderline the two-auditor rule exists to catch.

## Fixup 2026-07-25

Curator-fixup pass over the dual audit (opus `borderline`, sonnet `fail`). Every
audit item resolved; residual verdict **pass**; the `BORDERLINE` header on
`case.yaml` was replaced with a pointer to this section.

### Dissolved by the exposure contract — no edit

- **Sonnet's entire FAIL**, and opus's second item: both are about
  `case.yaml`'s `curation.learnings` stating the governing rule ("presentation work
  cannot go to the autonomous loop"), quantifying it at sixteen of 34, naming the
  three evidence artifacts, quoting `remainders/2026-07-17-delivery-outcomes.md:105`,
  and disclosing the post-cut CHECK-constraint outcome. Under
  `bench/schema/benchmark-case-v1.md` §"The exposure contract", the solver sees
  `pre_state − withheld + task/` and nothing else; `case.yaml`, `notes.md`,
  `ground_truth/` and the case id/path are harness-side and never exposed, and
  titles and grading configs "may state the answer freely". Nothing renamed, nothing
  retitled, nothing removed from `curation.learnings` — the analysis there is the
  point of the corpus. The one operational consequence (a by-hand run must
  materialize `task/` into a neutrally-named directory) is already the schema's
  standing rule, not a per-case defect.

### Known leak channel — declared

- `case.yaml` gained `known_leak_channels: [project-auto-memory]` with a comment
  naming what leaks: this machine's `MEMORY.md` index line for
  `project_agentic_ux_audit_4.md` and that memory file itself state the v0.2.167
  stale-image root cause, the A0-1 ops item, the P0 list and the "Elm items
  deliberately NOT loop tickets" decision. Memory was not touched and cannot be
  fixed from inside the case; a hand-run on this machine with memory enabled is
  invalid (this repeats, in the manifest, what "Leak vectors outside the repository"
  above already said in prose).

### Priced deflator — kept exposed, paid for in the rubric

- Opus's third item (the Elm rule is quotable in-tree, so Part A partly measures
  retrieval) is the priced-deflator case: authentic pre-cut history that partially
  reveals the answer. **Default kept.** `withheld` stays empty, now with a comment
  recording why: the two remainder plans are standing repo convention, they decide
  no per-item routing, no root cause and none of the six ops dispositions, and
  rubric B3 presumes constraints are findable. Verified at pre-state that the rule
  lives in exactly two files (`git grep -l "no Elm toolchain" 6188b2a8c1` →
  `remainders/2026-07-17-delivery-outcomes.md`, `remainders/2026-07-17-platform-mcp-hitl.md`);
  opus's third citation, `TICKETS.md:14`, does not exist at pre-state — there is no
  `TICKETS.md` in that tree — so the exposure is narrower than the audit assumed.
- `ground_truth/rubric.md` now instructs the judge to credit the causal chain rather
  than the citation, in two places: a new "How to weigh evidence — reasoning, not
  quotation" preamble, and a reworked **B1** that requires at least one grounding to
  be a mechanical artifact (`deploy/agent-runner/Dockerfile`, `agent/harvest/gates.go`,
  `web/public/elm.min.js`), treats the written rule as corroboration only, and caps a
  rule-quotation-only argument at 4/10. B1 also now asks the grader to report which
  groundings were used, so retrieval-vs-derivation stays visible across runs.

### Real defect fixed — missing delivery channel

- The task asked for "a scoping document" and never said where to put it, leaving
  the graded artifact undefined. `task/task.md` now names it: a single new
  `TRIAGE.md` at the checkout root, no other file touched (header block plus the
  section heading "The document you produce (`TRIAGE.md`) should contain").
  `ground_truth/rubric.md` gained a matching "Where to find the submission"
  paragraph — grade `TRIAGE.md`; if absent, grade the routing wherever it appears
  and report the delivery miss instead of deducting for it twice — and the Reporting
  section now asks for that fact. `case.yaml`'s `grading` block records
  `deliverable: TRIAGE.md`. No trigger content changed: the deliverable was always a
  routing document, only its address was missing.

### Checked and left alone

- **Leading / answer-stating text in `task/`:** re-read both exposed files. Neither
  names the stale image as a cause, names an Elm module, uses an executor-encoded ID,
  or groups items by class. The environment block's image tag and the `loop` class
  description are the two residual tells; both are documented above as deliberate,
  and B2 still requires the version→commit walk to be performed rather than guessed.
  No softening needed.
- **Grading collisions:** none possible — `fail_to_pass` and `pass_to_pass` are
  empty and no overlay is applied. Added instead a `grading` comment that the record
  is known-wrong on F-02's migration assumption (bonus X1 rewards contradicting it),
  so a grader does not "correct" a submission toward the record there.
- **Manifest consistency:** Part A's class lists sum to 34 (wave 16 / plan 8 / ops 6
  / loop 4) and match `answer.md` row for row. `information_cut`
  (2026-07-19T08:25:56-07:00) is the pre_state commit timestamp and stays that; the
  task's internal dates ("Opened: 2026-07-19", "this morning", the finding list's
  2026-07-19 header) sit consistently at that instant — the trigger arrives *at* the
  cut, and the findings describe runtime behaviour of pre-cut code, not post-cut
  repo content. `git ls-tree -r 6188b2a8c1` has no path matching `2026-07-19|ux4`,
  and `.claude/` in the tree is generic skills/commands with no session or memory
  content.
- **Difficulty:** held at `moderate`, with the reasoning recorded inline in
  `case.yaml`. The deflator lowers sixteen items to find-and-cite, but the other
  eighteen, the version→commit walk, the self-contained ticket bodies and the
  execution order keep it above trivial, and the absence of any live-system work
  keeps it below hard.

### Residual

Pass. One caveat that is a *validation* gap rather than an exposure one, already
first on the validation list above: the ref-stripping materialize recipe has only
been executed against a synthetic repository. Its four assertions (`05ef24ec69`
gone, `644184e3f0` gone, `v0.2.167` → `5715e7db2d`, no `v0.2.19[6-9]` tags) are the
thing that keeps the answer key out of the solver's checkout, so a replay harness
must assert them, not assume them.
