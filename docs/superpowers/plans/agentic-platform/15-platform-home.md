# Platform Home Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. This document preserves the abandoned ticket-centric roadmap only. **Explicit superseded block:** every section below this banner, including migration reservations at `1773106100+`, `step_kind`, ticket/build/plan keys, restore runner/stub, and `primaryMetric` references, is historical and must not be implemented. **Keep:** fixtures, repetitions, evaluators, controls, and scorecards. **Supersede:** `step_kind`, ticket/build/plan keys, restore runner/stub, and the primary-metric switch.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the agentic platform a coherent web front door — a ticket dashboard you can reach from the nav and file tickets from — plus the two shared UI contracts (`AgentBadge` status language and the sectioned ticket layout) that every other agent surface builds against.

**Architecture:** One new Elm page (`AgentTickets` dashboard) over the existing `ListAgentTickets` route, a web ticket-create form over the existing `CreateAgentTicket` route, a nav entry + breadcrumb root, and two shared modules — `AgentBadge` (status → humanized label + semantic color) and the sectioned ticket-detail layout (Overview / Run / Diff & Review / Metrics) — that ticket-core (06), delivery-outcomes (12), and scorecards (13) consume instead of each reinventing status colors and page structure.

**Tech Stack:** Elm (web/elm/src), following existing `Build/AgentReview.elm` and `AgentReviews/AgentReviews.elm` patterns; the Concourse SideBar/TopBar nav; existing `agent/api` routes (no new backend).

**Visual anchor:** the approved mockup (`docs/superpowers/plans/agentic-platform/UX.md` references it) — Concourse-native dark shell, one restrained indigo accent, semantic status colors distinct from the accent, humanized labels. Lock it before 06/12/13 render their UI.

---

## Context

**Origin:** the 2026-07-11 UX review (`UX.md`). Verdict: backend coherent, web experience fragmented (no front door, no web create, per-plan badge systems, raw enum tokens shown to users). Owner approved adding this workstream and locked the mockup direction (2026-07-11).

**Wave:** UI wave (after the data-bearing plans 06/12/13 exist as backend + routes; this plan supplies the shell they render into). **Build order within the program:** ship the `AgentBadge` module and the sectioned-layout contract FIRST, as wave-start deliverables, so 06/12/13 append sections instead of inventing pages.

**PRODUCES (shared contracts other plans consume):**
- `AgentBadge` Elm module — §Task 1. Consumers: 06 (lifecycle badge), 12 (merge/outcome badge), 13 (live badge), 08 (parked badge).
- Sectioned ticket-detail layout contract — §Task 4. Consumers: 06 (Overview), 12 (Diff & Review), 13 (Metrics section links), 08 (parked state in Overview).
- Web ticket-create form + front-door route conventions — §Tasks 2–3.

**CONSUMES:** `agent/api` routes `ListAgentTickets`, `GetAgentTicket`, `CreateAgentTicket` (06); `agent_outcomes` + scorecard rollup read APIs (12/13) for cross-links only (view-only, no new backend).

**Dogfoodability note (a real finding):** this workstream is **not dogfoodable with the current Go-only dogfood gate** — the loop runs `go test`, not `elm make` / `elm-test`, and Elm UI needs visual verification. Build it human-driven, verifying in-browser, OR first extend the dogfood pipeline with an Elm build/test capability (tracked as a dogfood-loop enhancement, `notes/dogfood-elm-gate.md`). The `AgentBadge` module (pure, unit-testable) is the one piece that becomes dogfoodable once an Elm gate exists.

---

## Task 1: `AgentBadge` — the shared status language (build first)

**Files:**
- Create: `web/elm/src/AgentBadge.elm`
- Test: `web/elm/tests/AgentBadgeTests.elm`

The single module every agent surface imports for status display. It maps each state to a **humanized label** (fixes M7/M9 — no raw `needs_review` shown to users) and a **semantic color token** (fixes M3 — one color system, not four).

- [ ] **Step 1: Define the status union and its render contract.** One `Status` type covering the ticket lifecycle and outcome/run states, each with `label : Status -> String` (human text), `tone : Status -> Tone` (semantic color, distinct from the app accent), and `view : Status -> Html msg` (the pill: LED dot + label; running pulses unless `prefers-reduced-motion`). Canonical mapping:

```elm
type Status
    = Draft | Queued | Running (Maybe String)   -- optional current step, e.g. "implement"
    | AwaitingHuman | NeedsReview
    | Merged | MergedWithFixes | SentBack | Concluded | Abandoned
    | Failed | Errored

label : Status -> String
label s = case s of
    Draft -> "Draft"
    Queued -> "Queued"
    Running step -> "Running" ++ (step |> Maybe.map (\x -> " · " ++ x) |> Maybe.withDefault "")
    AwaitingHuman -> "Waiting on you"
    NeedsReview -> "Needs your review"
    Merged -> "Merged"
    MergedWithFixes -> "Merged with fixes"
    SentBack -> "Sent back"
    Concluded -> "Concluded"
    Abandoned -> "Abandoned"
    Failed -> "Failed"
    Errored -> "Errored"

type Tone = Neutral | Info | Active | Attention | Good | GoodMuted | Warn | Calm | Bad | Error
```

- [ ] **Step 2: Failing test.** `AgentBadgeTests.elm` asserts (a) every `Status` has a non-empty label with no underscores (proves no raw enum leaks), (b) `NeedsReview`/`AwaitingHuman` map to the attention tone, (c) `fromApiToken : String -> Maybe Status` round-trips every wire token the API emits (`needs_review`, `merged_with_fixes`, `sent_back`, `awaiting_human`, …). Run: `elm-test tests/AgentBadgeTests.elm` — expect fail (module absent).
- [ ] **Step 3: Implement** the module: `label`, `tone`, `fromApiToken`, and `view` (pill markup matching the mockup — LED + label; `Running`/`AwaitingHuman` get the pulse class). Tone → concrete color lives in the stylesheet as CSS custom properties (`--s-*`), so the palette is themeable and owned in one place.
- [ ] **Step 4:** Run `elm-test` — expect pass. `elm make` the module in isolation — expect clean.
- [ ] **Step 5: Commit.** `feat(web): AgentBadge shared status language for agent surfaces`

## Task 2: Front-door route + nav entry + breadcrumb root

**Files:**
- Modify: the Concourse routing table (`web/elm/src/Routes.elm`) — add `AgentTickets` route at `/agent-tickets`
- Modify: `web/elm/src/SideBar` / `TopBar` — add the "Agent tickets" nav entry
- Test: routing round-trip test alongside existing route tests

- [ ] **Step 1:** Failing routing test: `/agent-tickets` parses to `AgentTickets` and back. Run the web route test suite — expect fail.
- [ ] **Step 2:** Add the route + the nav entry (active-state styling per the mockup) + breadcrumb root `main / agent tickets`. Fixes **B1** (no front door).
- [ ] **Step 3:** Run route tests — expect pass. **Commit.** `feat(web): agent-tickets route + nav entry`

## Task 3: Ticket dashboard + web create form

**Files:**
- Create: `web/elm/src/AgentTickets/AgentTickets.elm` (dashboard)
- Create: `web/elm/src/AgentTickets/CreateForm.elm` (create)

- [ ] **Step 1:** Dashboard page: fetch `ListAgentTickets`, render the table from the mockup — id, title (link to detail), repo, workflow (name + version), **`AgentBadge` status**, cost (tabular-nums), and per-row **cross-links** to build / diff / scorecard from ids already in the model (fixes **M1**). Filter chips (All / Needs you / Running / Done) are client-side over the fetched list. Empty state names the create action.
- [ ] **Step 2:** Create form posting `CreateAgentTicket` with `origin: "web"` (backend already supports it, 06): title, repo, workflow (name+version picker from the workflow list), budget, body (markdown). On success, route to the new ticket detail. Fixes **B2** (web could only edit, not file).
- [ ] **Step 3:** Wire the dashboard as the `AgentTickets` route's page; the "New ticket" button opens the create form.
- [ ] **Step 4:** Verify in-browser against the mockup (human visual check — this is the anchor page). **Commit.** `feat(web): agent-ticket dashboard + web create form`

## Task 4: Sectioned ticket-detail layout contract

**Files:**
- Create: `web/elm/src/AgentTickets/Detail.elm` (the shell)
- Create: `docs/superpowers/plans/agentic-platform/notes/ticket-layout-contract.md` (the contract 06/12/13 build against)

- [ ] **Step 1:** Write the layout contract note: the ticket detail is ONE page with a fixed section order — **Overview** (title, repo, branch, `AgentBadge`, primary actions Merge/Send-back), **Run** (pipeline-run link, live plan-task progress, parked state from 08), **Diff & Review** (diff stat, the existing `Build/AgentReview.elm` evidence panel reused verbatim, judge score, six-verdict feedback in plain language), **Metrics** (cost / turns / wall-time / judge score). Each downstream plan renders INTO its named section; none adds a new top-level page. This freezes the structure before 06/12/13 append.
- [ ] **Step 2:** Implement the `Detail.elm` shell: header (from `GetAgentTicket`) + the four section tabs + slots. Overview + Metrics render here (data already in the ticket payload); Diff & Review and Run expose slots the sibling plans fill.
- [ ] **Step 3:** Verify in-browser against the mockup. **Commit.** `feat(web): sectioned ticket-detail shell + layout contract`

## Task 5: Small fly + label seams (from UX M6/m4/M7)

**Files:**
- Modify: the relevant fly command files (owned by 02/06 plans; small additive edits recorded here)

- [ ] **Step 1:** `fly agent-ticket show <id>` prints the running build reference (link to `fly watch`) — fixes M6 (CLI ticket had no path to its build). Additive.
- [ ] **Step 2:** Move the token-expiry nag onto `fly agent auth` (from `fly status`) — fixes m4. Additive.
- [ ] **Step 3:** Audit fly/API/web for any user-facing raw enum token; route all through a shared humanizer (the CLI analog of `AgentBadge.label`). **Commit.** `feat(fly): ticket→build link + auth expiry nag + humanized status`

## Execution notes

- **Test suite:** web Elm tests via `elm-test` under `web/elm/`; route tests in the existing web test setup. The Go backend is untouched (view-only consumption of existing `agent/api` routes).
- **Build order (non-negotiable):** Task 1 (`AgentBadge`) and Task 4-step-1 (layout contract) are wave-start contract deliverables — land them before 06/12/13 render any UI, so those plans consume the shared badge + layout instead of reinventing. Then Tasks 2–3 (the anchor pages, human-verified), then Task 5.
- **Not dogfoodable on the Go gate:** see the Context note. Build human-driven with in-browser verification, or first add an Elm gate to the dogfood pipeline.
- **Rollback:** all additive — a new route, new Elm modules, a nav entry, small fly flags. Removing the nav entry + route hides the front door without affecting existing pages; the shared modules are pure and side-effect-free.
