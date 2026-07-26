# Rubric — fix-jb-007 (failed-early harvest vanishes from the step DAG)

Behavioral checklist. Score intent and behavior, **not** similarity to
`reference.diff`. Any change that satisfies the MUSTs is a pass regardless of
where it lives or how it is written.

## Root cause the agent should reach

The ticket step DAG expands a `harvest` run-metric row into gate / judge / push
boxes derived from that row's `results` metadata, instead of rendering it as an
ordinary agent-step box. A harvest that failed before producing any of those
facts carries empty metadata (no gates, no judge, empty pushed branch), so the
expansion is the empty list and the harvest step contributes **zero** boxes to
the attempt chain — it disappears. Nothing else in the chain is broken.

Naming the empty-expansion mechanism is the core insight. An answer that only
says "failed harvests aren't rendered" without identifying that the harvest row
is *expanded from metadata* has not diagnosed it.

## MUST (each is pass/fail)

1. **Never zero boxes.** A `harvest` row whose results carry no gates, no judge
   and no pushed branch contributes **at least one** box to its attempt's chain.
2. **Exactly one box, labeled for the step.** That case yields exactly one box,
   not a placeholder plus filler, and it is identifiable as the harvest step
   (label `harvest`, i.e. the row's step name — not a synthesized label like
   `gate: (none)` or `harvest failed`).
3. **Tone comes from the row's own status, not a hardcoded failure color.**
   The fallback box's tone must be derived from the harvest row's fused
   outcome through the same status-derivation the other agent steps use.
   *Discriminator:* a harvest that **succeeded** with empty metadata (push
   disabled, no gates configured — a real, reachable path in
   `agent/harvest/runner.go`) must render as a green/good box, not red. A fix
   that hardcodes `Bad` for the empty case passes the merged commit's own test
   and **fails this item**; it is caught mechanically only by the
   curator-authored second test in
   `ground_truth/AgentStepDagTests.discriminating.elm`.
4. **Metadata-present rendering unchanged.** When gates / judge / pushed branch
   *are* present, the chain, labels, ordering, tones and warn markers are
   byte-for-byte what they were: `gate: <name>` per gate (failed → Bad, flaky-ok
   → GoodMuted + warn), `judge <total>/<max>` carrying the harvest row's cost,
   `push`. The pre-existing `AgentStepDagTests` cases must still pass unmodified.
   Replacing the expansion entirely (rendering every harvest as one plain box)
   is a **fail**, not a simplification.
5. **Keyed on the expansion being empty, not on the status string.** Gating the
   fallback on `status == "failed"` (or on the specific failure summaries quoted
   in the report) leaves item 3's succeeded-with-no-facts harvest still
   vanishing → **fail**. The condition must be "this harvest produced no
   gate/judge/push boxes".
6. **Web-side only.** No server, API, decoder-contract, schema or migration
   change. No new endpoint. The wire shape of the metrics response is untouched.
7. **Rest of the composition untouched.** Ticket box first, one box per
   non-harvest agent step, terminal review/merge boxes on the latest attempt
   only, attempt numbering, attempt outcome fusion, cost/duration sublabels — all
   unchanged.
8. **Regression test added.** A test that fails against the unfixed code and
   asserts the failed-harvest-renders-one-box behavior, living anywhere under
   `web/elm/tests/` — the existing step-DAG module or a new one, grader's
   indifference. *Grading note:* the mechanical gate OVERWRITES
   `web/elm/tests/AgentStepDagTests.elm` with the withheld ground-truth module,
   which is the most likely place the agent put its test. Score this item from
   the copy preserved by `grading.setup` (or from the agent's diff) — never from
   the post-overlay tree, where the agent's test no longer exists.

## SHOULD (quality, not pass/fail)

- Reuses the existing shared per-step box constructor rather than open-coding a
  second box record — the reference fix calls the same constructor the non-harvest
  steps use, which is what makes item 3 fall out for free.
- Leaves a comment naming *why* the empty case exists (which harvest failures
  produce empty metadata), so the next reader does not "simplify" the branch away.
- Does not widen the module's exposed API; no new `BoxKind` variant is needed.
- Does not regenerate `web/public/elm.min.js` (a separate release commit does
  that for the branch); doing so is diff noise, not a correctness failure.

## Grading caveats (read before converting the mechanical score into a verdict)

- **Credit reasoning from evidence, not doc quotation.** The pre-state ships
  `docs/superpowers/plans/agentic-platform/2026-07-19-s1-ticket-step-dag.md`,
  which reproduces the *defective* `harvestBoxes` verbatim as the reviewed
  design. It is deliberately exposed. An answer that reaches the empty-expansion
  mechanism by reading the code and the metrics data scores full marks on "root
  cause"; an answer that merely restates or cites that document — or worse,
  defers to it as proof the code is correct — does not.
- **Box label is a grading choice, not a hard requirement.** The mechanical
  tier-1 test pins the chain to `["ticket","implement","harvest"]`. `task.md`
  states that expectation observationally, so the pin is fair, but a fix that is
  behaviorally right and labels the box differently should be scored on MUST
  1/2/3/5 rather than auto-failed on the label.
- **Compile failure is not behavioral failure.** The graded module compiles
  against `StepDag.attempts` and the `AgentBadge` tone constructors. A fix that
  renames or relocates that entry point breaks the overlay at compile time;
  inspect the diff before recording a mechanical fail.
- **Two tiers, two meanings.** Tier 1 (human, from the merged commit) pins the
  reported symptom; tier 2 (curator-authored) pins the invariant behind it.
  A run that passes tier 1 and fails tier 2 is the hardcoded-tone fix — MUST 3
  and MUST 5 are the items it fails.

## Anti-patterns seen in plausible-but-wrong fixes

- Filtering failed harvest rows out earlier "so they don't render weirdly" —
  makes the symptom worse.
- Emitting a synthetic `gate: —` / `judge n/a` placeholder box: satisfies item 1
  but violates item 2 and misrepresents facts that were never recorded.
- Fixing it server-side by backfilling empty gate metadata on failed harvests:
  violates item 6 and invents records.
- Rendering the fallback box with the *attempt's* outcome rather than the
  *harvest row's* — right color by accident on the reported case, wrong whenever
  a later step changes the attempt verdict.
