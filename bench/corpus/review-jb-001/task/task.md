# Merge gate: adversarial review of `agentic-ux-wave-2` before it lands on `jetbridge`

**Type:** code review (pre-merge gate)
**Branch:** `agentic-ux-wave-2`
**Target:** `jetbridge` (the mainline; the self-build pipeline deploys it)
**Requested:** 2026-07-19

## What the branch is

`agentic-ux-wave-2` implements the U1–U24 findings from the fresh-eyes UX audit
of the agentic surfaces. The branch plan, sequencing and explicit deferrals are
in `AGENTIC_UX_WAVE_2_SCOPE.md` at the repo root — read it first; it is the
statement of intent this change should be judged against.

The wave touches, roughly in order of weight:

- **`/agent-tickets/:id`** (`web/elm/src/AgentTickets/AgentTicket.elm`) — the
  ticket page, rebuilt to be navigable and evidence-first: run ledger, review
  digest, inline edit, transition/dispatch controls, live refresh.
- **`/agent-tickets`** (`AgentTickets.elm`) and the dashboard agent strip
  (`Dashboard/Dashboard.elm`) — live queue, elapsed-time labels, per-ticket
  spend, errored tickets surfaced.
- **`/agent`** (`Agent/Agent.elm`) — the operator console: prose rendering, an
  expandable run ledger, local time, labeled credentials, in-page section nav.
- **Build page** — agent/harvest steps now decode and render (`Concourse.elm`,
  `Build/StepTree/*`), and the agent-review panel picked up polish
  (`Build/AgentReview.elm`).
- **Status truth** (`AgentBadge.elm`, plus the Go half in `agent/schema`,
  `agent/api/metrics`, `atc/db/agent_run_metrics_factory.go`,
  `atc/public_plan.go`) — run metric rows now carry a server-derived
  `build_status` so the UI can render what the *build* did, not only what the
  agent step said about itself.

## What is being asked

This is the last gate before merge. Review the branch's own contribution —
supplied as `task/change.diff`, equivalently `git diff <merge-base>..HEAD` with
the generated bundles excluded — and report every defect you can **verify** in
the code, ranked by severity.

Be adversarial, and budget for depth over breadth. The wave is large, it was
implemented in sequential slices by several sub-agents against shared Elm
modules, and it has already been through one lighter review round, so the cheap
bugs are gone. Judge the code as written, not the intent stated in comments,
commit messages or the scope doc — a claim about behavior is only as good as the
lines that implement it.

## Ground rules

- **Verify before you report.** A finding must name `file:line`, state a
  concrete failure scenario (inputs/state → wrong behavior a user would see),
  and be checkable against the code as written. Anything you could not confirm
  goes in a separate "unverified / worth a look" list, not in the findings.
- **Do not review generated artifacts.** `web/public/elm.min.js`,
  `web/public/elm.js` and `web/public/main.css` are build outputs of
  `hack/build-web.sh`; they are excluded from the supplied diff. Comments about
  how bundles are *managed* are in scope; their contents are not.
- Style, naming and formatting preferences are out of scope unless they cause a
  defect. So is re-litigating the audit's own scope decisions — the deferrals in
  `AGENTIC_UX_WAVE_2_SCOPE.md` are deliberate and already argued.
- The mainline is deployed by the self-build pipeline shortly after merge, and
  the agentic surfaces are the operator's only view of runs that spend real
  money. Weigh severity by what an operator would be misled into believing, or
  be unable to do, on the live UI.
- Findings that require a change outside the wave's own files are still
  findings — say so explicitly.

## Deliverable

A ranked list of verified findings. For each: severity, `file:line`, the failure
scenario, and what correct behavior would be. A one-paragraph verdict at the top
(merge / merge after fixes / do not merge) and, at the bottom, the candidates you
investigated and refuted, with the reason each was refuted.

Write the review to `MERGE_GATE_REVIEW.md` at the repository root — that file is
this gate's output. The deliverable is the review, not the fix; if you also
sketch code, keep it out of the report and say what you changed.
