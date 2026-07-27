# Ticket-detail layout contract (ws15 Task 4, Step 1)

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This ticket-detail layout contract governed the abandoned ticket-centric UI; the live ticket compatibility UI (see `docs/agentic/README.md`) may differ.

Status: **frozen** (wave-start deliverable). Downstream plans render INTO the
named sections below; none adds a new top-level page.

The agent-ticket detail is **one page** with a **fixed section order**:

1. **Overview** — title, repo, branch, `AgentBadge` status, primary actions
   (Merge / Send-back). Filled by ticket-core (06). Parked state (08) surfaces
   here and in Run.
2. **Run** — pipeline-run link, live plan-task progress, parked/awaiting-human
   state. Run link is live today (pipeline-runs runs API); progress + parked
   state come from agent-step (07) and platform-mcp-hitl (08).
3. **Diff & Review** — diff stat, the existing `Build/AgentReview.elm` evidence
   panel reused verbatim, judge score, six-verdict feedback in plain language.
   Filled by delivery-outcomes (12); reviews surface is live today.
4. **Metrics** — cost / turns / wall-time / judge score. Cost from the ledger
   (live), turns/wall-time from the agent-step runs table (07), judge score
   from agent-reviews (live). Filled by scorecards (13).

Rules:
- Section order is fixed; a plan fills its named section, never a new page.
- Status is rendered exclusively through `AgentBadge` (web/elm/src/AgentBadge.elm)
  — no raw enum tokens shown to users. The badge reads open-questions OR run
  status `awaiting_human`, so a parked ticket reads "Waiting on you", not
  "Running" (UX m8).
- The shell (`AgentTickets/Detail.elm`) exposes slots for Run and Diff&Review;
  Overview and Metrics render in the shell.

Buildable today: the `AgentBadge` module (shipped) and this contract. The
`Detail.elm` shell and the ticket dashboard are blocked on ticket-core (06)
routes (`ListAgentTickets`/`GetAgentTicket`/`CreateAgentTicket`), which do not
exist yet.
