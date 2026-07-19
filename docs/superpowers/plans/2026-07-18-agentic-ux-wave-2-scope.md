# Agentic UX Wave 2 — scope & sequencing

Branch `agentic-ux-wave-2` off `jetbridge@54b541a81e`. Findings U1–U24 from the
fresh-eyes audit. NOTE: the branch has advanced past the live-deployed
v0.2.180-rc — **re-verify each finding against the current tree before editing.**

Bundle rule: after any `web/elm/src` change, `hack/build-web.sh` rebuilds
`web/public/elm.min.js`. Compile-check each Elm slice with `elm make`; regenerate
the committed bundle once at the end.

## In scope (implement + verify + compile + test)

| Slice | Findings | Surface | Notes |
|---|---|---|---|
| 1 | U1 client | Concourse.elm decoder + StepTree render | keystone; server half already committed (4f64b29745) |
| 2 | U3 truth (Go) | schema DeliveredOutcome + runner empty-result guard + RunMetrics build_status LEFT JOIN + ParseSubmission guard | test-first |
| 3 | U3 render (Elm) | AgentBadge outcome, ledger + ticket runRow render build truth | depends on 2 |
| 4 | U4 U19 U18 U21 U22 U2 U6 | AgentTicket.elm (+Routes/Message) | mostly pure-Elm; review card already on page |
| 5 | U9 U10 U11 U23 | AgentTickets.elm, Dashboard.elm, agent-badge.less | copy Dashboard live-clock pattern |
| 6 | U13 U16 U17 U15-lite | Agent/Agent.elm (+shared prose) | cheap wins, no Route/Model/Effect variant |
| 7 | U24 batch | AgentReview.elm, AgentReviews.elm, Agent.elm, AgentBadge.elm, Effects.elm | micro-fixes |
| 8 | — | rebuild bundle, full build/test, adversarial review workflow | |

## Deferred (documented follow-up tickets, with rationale)

- **U7 full web ticket-create form** — `CreateAgentTicket` exists server-side, but a
  new create form is substantial UI. Ship U7-lite (dispatch-confirm shows
  workflow@version + model + budget); defer the create form.
- **U8 workflow detail/source/version pages** — new Route + SubPage.Model variant
  forces arms across Routes.toString/extractPid/getGroups and SubPage
  init/genericUpdate/view/tooltip/subscriptions; needs new Go wire fields. Own track.
- **U15 full IA split into sub-pages + real pagination** — needs new Go endpoints.
  Ship U15-lite (anchors, section nav, fenced mint form, "newest 100" caption).
- **U12 dashboard de-sprawl (stop minting bare template pipelines)** — dispatch
  backend behavior change; risky. Own track.

Sequential implementation (shared Elm modules — Message.elm, AgentTicket.elm,
AgentBadge.elm — preclude safe parallel worktrees). Foundational slices 1–3 by
orchestrator; page bundles 4–7 by sequential sub-agents in this worktree, each
re-verifying + compile-checking; adversarial review workflow at the end.
