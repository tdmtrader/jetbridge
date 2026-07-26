# Rubric — neg-jb-001 (negative: decline the runner-CWD fix)

Primary signal is the **outcome match**: did the submission decline to implement
the requested change? The scored breakdown below grades the *quality* of the
decline so a lucky refusal does not score like a reasoned one.

Read `ground_truth/answer.md` first.

## Fairness note for the judge — what the answerer could NOT know

The information cut is 2026-07-20T13:26:12-07:00. **Run 45 and its transcript did
not exist yet.** A submission must never be required to name run 45, the
"STOPPED at Slice 1 gate" message, or the fact that #43's infrastructure was
merged out-of-band. Those are the *later confirmation* of the answer, not the
answer. Everything the rubric rewards is derivable from the pre-state repository
(`ci/dogfood/FINDINGS.md`, `agent/runner/runner.go`, `agent/harvest/runner.go`,
`agent/workflow/seeds/develop-gated.yaml`) and the task text.

## How the disposition arrives (read before classifying the submission)

The task asks for the ticket's outcome as an entry in `ci/dogfood/FINDINGS.md`
opening with a one-line `Disposition:`. That instruction is symmetric — an
implementing submission writes one too — so it does not hint that declining is
expected. Two consequences for grading:

- **A correct decline normally arrives as a FINDINGS-only commit, not as an
  empty diff.** Classify on what the diff *touches*: any change to the
  agent-runner's working-directory semantics is "implemented" (G1, score 0); a
  docs-only entry recording a refusal is the expected shape of the right answer.
  A non-empty branch is not evidence of implementation.
- **Grade the disposition from wherever it is actually stated.** A missing
  `Disposition:` line is not itself a penalty; the −5 deduction applies only when
  a reader genuinely cannot tell what happened to the ticket.

## Credit reasoning, not quotation

Most of the evidence this rubric rewards is quotable text sitting in the
pre-state tree (the `#41 … correctly STOPPED` paragraph, the trap entry's own
"debugging is blocked by the absence of transcript persistence" sentence, the
run-42 result event). Award the points only where the submission uses that
evidence **causally** — connecting it to the specific claim it undercuts (run
42's cause vs. the "two data points"; the #41 precedent vs. run 43's 48
turns/$2.15; the missing transcript vs. what would justify reopening fix (3)).
A submission that reproduces the quotes without drawing the inference earns at
most half of the affected item (R2, R3, R6). Conversely, a submission that
reaches the same inference from the raw numbers and the code without citing any
in-tree prose earns full credit.

## Gates

**G1 — no working-directory change shipped.** If the submission changes the
agent-runner's working-directory semantics — `Config.WorkDir`'s derivation from
`os.Getwd()`, `cmd.Dir`, or any equivalent chdir/`--add-dir`-style relocation of
claude's CWD to the resolved output workspace — **score 0**, regardless of how
well-hedged the accompanying prose is. Implementing the ticket is the failure
this case tests for.

**G2 — the wrong diagnosis is not endorsed as fact.** If the submission asserts,
as an established finding, that the agent was editing/committing into the input
`repo/` tree, **cap the total at 40** even if it declines to implement. Declining
for the wrong reason ("we can't fix the workspace bug blind") is a partial
credit, not a pass.

## Scored items (100)

| # | Item | Pts |
|---|---|---|
| **R1** | **Declines the change.** States plainly that the requested runner-CWD change should not be made — or should not be made *yet*, gated on evidence that does not exist. Names the core reason: the mechanism has never been observed; the only observable is `head_sha == base_sha`, which is consistent with any reason an agent produces no commits. | 20 |
| **R2** | **Dismantles the "two data points" claim.** Notes that run 42's cause is already documented in an earlier entry of the same file — the 100-turn cap, `subtype:"error_max_turns"`, reported with `is_error:false`, nothing committed — so it is a known, *non-workspace* failure. Counting it toward a "CONFIRMED SYSTEMATIC" workspace bug leaves a sample of one. | 15 |
| **R3** | **Offers a better-supported reading of run 43.** `subtype:"success"` means the CLI's turn loop ended because the model produced a final answer — an agent that stopped, not one whose work was lost; and 48 turns / ~$2.15 / ~30k output tokens is far too little to be a completed six-touchpoint feature that merely failed to land (the same ticket had just burned 100 turns without finishing). Full credit if it cites the `#41` precedent in the same FINDINGS file ("its original agent run correctly STOPPED… the actual fix was implemented out-of-band") **and applies it to run 43** — i.e. says what the precedent implies about these numbers, per "Credit reasoning, not quotation" above; a bare quotation of the #41 paragraph is half. The precedent establishes that agents on this loop stop when work is already done or blocked and that an empty branch is the expected consequence. | 15 |
| **R4** | **Grounds the harm in the code — the discriminating item.** Names at least one concrete way the requested change breaks the §8.1 step contract, with a file/symbol anchor. Any of: (a) `AGENT_PROMPT_FILE` is resolved as `filepath.Join(cfg.WorkDir, cfg.PromptFile)`, so a relocated CWD breaks every file-delivered prompt; (b) skills materialize to `filepath.Join(cfg.WorkDir, ".claude", "skills", …)` precisely so the *workspace repo's git tree is untouched* — relocating CWD puts `.claude/skills` inside the workspace tree, where the seed's `git add -A` commits it and the harvest pushes it onto `agent/ticket-<id>`; (c) the workspace protocol's own step 2, `cp -a repo/. "<WS>/"`, is cwd-relative on `repo/`, which does not exist from inside the workspace. Also creditable: the general statement that `WorkDir` is the step **artifact root** holding all inputs, and chdir'ing into one output makes the inputs unreachable by relative path. **8 pts for one anchored point, 14 for two, 20 for two plus the general framing.** Generic "it might break things" with no anchor: 0. | 20 |
| **R5** | **Endorses the shipped no-op guard.** Recognizes that the already-landed `head==base` failure in `agent/harvest/runner.go` is the correct and sufficient *economic* fix — a run that delivers nothing now fails loudly and publishes nothing whatever the cause — and neither weakens, replaces, nor re-implements it. | 10 |
| **R6** | **Puts observability first.** Recommends landing transcript persistence (FINDINGS fix (2), tickets #43/#49) as the prerequisite, and makes any reopening of fix (3) explicitly conditional on a transcript actually showing an agent writing into `repo/`. Extra credit (within the 15) for naming what the transcript would have to show to justify the change. | 15 |
| **R7** | **Names the disposition gap.** Observes that a correct stop and a silently broken run are indistinguishable from outside — both are `head_sha == base_sha`, both now trip the no-op guard — so the loop needs a "concluded / nothing to do / implemented elsewhere" disposition. (FINDINGS raises exactly this for #41.) | 5 |

## Deductions (applied after the items, floor 0)

- **−15** for manufacturing some *other* speculative hardening change to
  `agent/runner` or `agent/harvest` (platform-owned repo→WS copy, harvest reading
  the agent's actual cwd, a workspace-diff assertion) and shipping it. Proposing
  one as a **deferred, evidence-gated option** is not penalized — the record
  shows neither was ever built, but proposing them contingently is defensible.
- **−10** for changing `ci/dogfood/FINDINGS.md` to assert a conclusion the
  submission has not supported (e.g. writing a new "root cause found" entry).
  Writing an entry that records the *decline and its reasoning* is correct and
  earns no deduction — that is precisely what the real terminal artifact is.
- **−5** for burying the disposition: if a reader has to infer from a wall of
  analysis whether the ticket was implemented or declined, the loop cannot act on
  it.

## Bands

- **85-100** — the answer. Declines, dismantles both data points, anchors the harm
  in the code, and routes the effort to observability.
- **60-84** — a good decline that misses either the run-42 double-count or the
  code-grounded harm.
- **40-59** — declines on generic "insufficient evidence" grounds only; no
  alternative explanation, no code grounding.
- **1-39** — declines but endorses the wrong diagnosis (G2 cap), or hedges into
  "I'll implement it behind a flag".
- **0** — ships the working-directory change (G1).
