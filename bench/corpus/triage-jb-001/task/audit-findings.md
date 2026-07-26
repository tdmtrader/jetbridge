# UX audit №4 — findings

**Date:** 2026-07-19 · **Target:** live JetBridge agentic UI on `concourse.home`
**Method:** browser tour as an admin operator, following a ticket's whole life —
dashboard → ticket queue → a ticket → its runs → a run's build page → reviews →
disposition. Auditor was instructed not to read prior audits.

**Not covered:** live in-flight streaming (nothing was running and the auditor did
not dispatch anything, to avoid spend); the Merge action was not clicked.

Findings are grouped by where they were hit. The grouping carries no priority and
no ownership. Findings marked **P0** are the ones the auditor called out as
blocking the system's basic legibility.

---

## 1. P0 — the run record does not tell the truth

**F-01.** Every ticket run in the runs ledger shows status `error` with the summary
*"flight recorder output missing"*. Every one — including runs whose build finished
green and whose branch was pushed for review. It is not a subset and not a recent
regression; it holds for every ticket the ledger shows. The practical effect is
alarm fatigue: the ledger is a wall of orange that nobody reads, so a genuinely
failed run is indistinguishable from a healthy one.

**F-02.** A single execution presents three contradictory truths at the same time:
the build page renders green and complete; the ticket page's run row says
*"Errored"*; and the runs ledger shows the implement step OK and the harvest step
Errored. Nothing on any page reconciles them, and there is no rule a user could
learn about which one to believe.

**F-03.** There is no way to retrieve what an agent actually did once its run has
ended. The runner emits a stream of events while it works; none of it is retained
anywhere an API can serve it back.

**F-04.** Reading an agent's work means scrolling raw JSON blobs in the harvest
step's build log. There is no turn-by-turn view — no timeline of tool calls, file
edits, gate results, judge verdict — and no way to see how a run spent its budget
or its turns.

## 2. P0 — a dispatched ticket is opaque

**F-05.** A dispatched ticket renders as a single grey box labelled `run:1`. The
workflow that ran, the steps it contains, which gates ran and what they returned,
the judge's verdict, and per-step cost and duration are all invisible from the
ticket. The ticket page is where you go to find out what happened, and it tells
you almost nothing.

**F-06.** Every ticket group on the dashboard also shows a phantom *"no instance
vars"* card, for a pipeline that has never run and never will.

**F-07.** Ticket run rows are identified by build number (*"build 571263"*). Which
attempt it was, when it ran, how it came out, and what it cost are not in the row.

**F-08.** On an errored ticket the *"Run error"* box is a dead end — it states the
error but does not link to the failing run's build page, which is where the error
can actually be read.

**F-09.** The rows of the runs table on `/agent` are not links. There is no way to
get from a run to its build without hand-assembling a URL.

## 3. P0 — naming soup

**F-10.** One ticket is referred to by at least six different identifiers across the
UI: `#40`, `agent-ticket-40`, instance `run:37`, a job called `run`, `run #1`,
`build 571263`, and a count reading *"2 runs"*. Nothing anywhere tells the reader
that these all denote the same thing, or what the relationship between a ticket, an
attempt, a run, a job and a build is.

**F-11.** Status vocabulary is inconsistent between surfaces: the same terminal
state reads *"OK"* in one place and *"Succeeded"* in another, and fused outcome
tokens are rendered raw rather than through any display vocabulary.

**F-12.** A harvest step's header renders as the literal text `step: step` rather
than naming the step, where the agent step above it correctly reads `agent: <name>`.

## 4. P0 — information architecture

**F-13.** `/agent` is a single mega-page with anchor-jump tabs. Agent pages have no
sidebar and no breadcrumbs, so once you are on one you have lost the rest of the
application's navigation.

**F-14.** The reviews index lives at `/teams/main/agent-reviews` and is linked from
nowhere in the UI — the auditor found it only by guessing. `/reviews` silently
redirects to the dashboard rather than to it.

**F-15.** Rows in the reviews index carry no instance identity, so three reviews of
three different runs all render identically as *"dogfood / run #1"*.

**F-16.** The web can mint principals but cannot create, queue or dispatch a ticket.
Every one of those actions is CLI-only, which means the UI is a read-only window
onto a system it cannot operate.

**F-17.** Reviewing a ticket's change means leaving the application for a GitHub
compare link. The link goes stale as soon as the branch moves, and the review
surface is therefore outside the product.

**F-18.** Workflows have no detail page. What steps a workflow runs, what prompts it
carries, what gate policy applies, what versions exist and how they differ, and how
a workflow has performed historically are not visible anywhere. There is also no way
to describe, hide or retire a workflow once it exists.

## 5. Presentation defects

**F-19.** Ticket titles wrap with roughly 70px of dead vertical space between lines.
The same string renders correctly in the ticket's edit form.

**F-20.** Copy defects on the ticket page: `budget$0.08` with no space; an errored
ticket described as *"completed"*; a run with no summary rendered as *"(no result)"*,
which reads as an assertion about the work rather than about the record.

**F-21.** Prose rendering: a fenced code block's language tag is rendered as a line
of body text, and run summaries shown in tables display raw `**bold**` markers
instead of being rendered.

**F-22.** The ticket queue lists the DRAFT section above the ERRORED section, so the
things that need attention sit below the things that do not. Section headers carry
a count but no spend rollup.

**F-23.** Timestamps mix absolute and relative formats across the agent pages — some
surfaces show a wall-clock time, others show "2 hours ago", with no consistency and
no way to get the other form.

**F-24.** Dashboard strip chips truncate at the end, which is exactly where two
otherwise-identical chips differ — the truncation removes the only distinguishing
part.

**F-25.** The costs section describes a `--agent-daily-budget-usd` flag as though it
were a control the reader could use. It is set at deploy time; nothing in the UI can
change it, and the text does not say so.

## 6. Spend

**F-26.** There is no spend view: no burn-down against a cap, no trend over time, no
per-workflow or per-ticket cost rollup. Related, and the reason the numbers matter:
there is no way to change the daily spend cap without a redeploy, so a runaway
cannot be stopped from the product.

## 7. Clutter and housekeeping

**F-27.** Every ticket leaves an `agent-ticket-<id>` pipeline on the dashboard
forever — including tickets that were merged, concluded or abandoned days ago. The
dashboard is now mostly dead pipelines.

**F-28.** The principals list mixes short-lived per-run principals (named
`agent-run-<id>`) with the long-lived operator principals. The run principals
dominate the list, grow without bound, and nothing distinguishes the two kinds.

## 8. Queue state observed during the tour

**F-29.** Tickets `#23` and `#24` are both `Errored`. Both errored on turn 1 with the
Claude CLI declining the task on usage-policy grounds — same task, filed twice, with
the prose reworded the second time. The scope they were filed for was subsequently
delivered by ticket `#25`, which is merged. Together they spent about $0.16 and two
dispatches for nothing.

**F-30.** Ticket `#2` is `Queued` with no workflow assigned, so it cannot dispatch.
Ticket `#12`, which is at `needs_review`, implements the same change.

**F-31.** Ticket `#1`, a `Draft` titled *"ticket-core live smoke"*, was a one-off
smoke test that ran on 2026-07-17 and is still sitting in the queue.

**F-32.** Ticket `#12` has been sitting at `needs_review` for 1 day 21 hours. It is
the oldest undispositioned item in the queue, and it blocks the two findings above
from being resolved.

**F-33.** Nothing in the UI surfaces how long the oldest needs-review ticket has been
waiting. The dashboard chip shows a count and no age.

**F-34.** A workflow named `badpolicy` appears in the workflow list with no
description and no indication of what it is for. There is no way to delete it or
hide it, so it reads as cruft that a user might dispatch by accident.
