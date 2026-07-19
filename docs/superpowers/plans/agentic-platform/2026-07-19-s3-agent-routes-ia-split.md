# Agent IA Split — Real /agent/* Routes Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax.

**Track:** S-3 (UX Audit №4, Proposal E) · **Branch:** jetbridge · **Date:** 2026-07-19
**Depends on:** W-11 (reviews nav link) — coordinate the reviews promotion (see Task 7).

## Goal

Replace the single anchor-jump `/agent` mega-page with six real, bookmarkable routes — `/agent/tickets`, `/agent/runs`, `/agent/reviews`, `/agent/workflows`, `/agent/spend`, `/agent/admin` — each carrying the persistent sidebar, consistent breadcrumbs, and a shared agent sub-nav, with the mega-page module split into per-section view modules.

## Architecture

`Routes.Agent` becomes `Routes.Agent AgentSection` (`AgentRuns | AgentWorkflows | AgentSpend | AgentAdmin`); the pre-existing `AgentTickets`, `AgentTicket`, and `AgentReviews` route constructors are re-pathed under `/agent/*`. The four mega-page sections (runs, workflows, spend=costs, admin=credentials+principals) move into new view-only modules (`Agent/Runs.elm`, `Agent/Workflows.elm`, `Agent/Spend.elm`, `Agent/Admin.elm`); `Agent/Agent.elm` keeps its single `Model`/`init`/`update`/`handleCallback`/poll and renders exactly one section chosen by the URL, so `SubPage.genericUpdate`'s 13-arg signature is unchanged. A new shared `Agent/Nav.elm` renders the six-tab sub-nav (real `<a href>` links) that every agent page mounts, and `Views/TopBar.elm` gains breadcrumbs for each agent route.

## Tech Stack

- Elm 0.19.1 (`web/elm/src`), `elm-explorations/test` 2.2.0 (`web/elm/tests`, `elm-test`).
- Embedded bundle regeneration via `hack/build-web.sh` (elm.min.js) — MANDATORY final step; there is no elm-build CI gate today (WF-2 adds one), so a forgotten rebuild ships a stale UI.
- No Go, no server route, no migration. S-3 is pure client Elm. (The APIs the sections call already exist: `FetchAgentRunMetrics`, `FetchAgentWorkflows`, `FetchAgentCostRollup`, `FetchAgentTicketCosts`, `FetchAgentCredentials`, `FetchAgentPlatformCredentials`, `FetchAgentPrincipals`, `FetchTeamAgentReviews` — all in `web/elm/src/Message/Effects.elm`.)

## File Structure

| File | Create / Modify | Responsibility |
|---|---|---|
| `web/elm/src/Routes.elm` | Modify | Add `AgentSection` type; `Agent AgentSection` constructor; parse/build/toString/getGroups/withGroups for the 6 routes; re-path tickets/reviews. |
| `web/elm/src/Views/Styles.elm` | Modify | `pageBelowTopBar` layout branch: `Routes.Agent _` (was `Routes.Agent`). |
| `web/elm/src/Views/TopBar.elm` | Modify | Breadcrumbs for `Agent section`, `AgentTickets`, `AgentTicket`, `AgentReviews`. |
| `web/elm/src/Application/Application.elm` | Modify | `routeMatchesModel`: `( Routes.Agent _, SubPage.AgentModel _ ) -> True` so section switches urlUpdate (keep fetched data), not re-init. |
| `web/elm/src/SubPage/SubPage.elm` | Modify | `init` passes section to `Agent.init`; `urlUpdateValid` Agent slot calls `Agent.changeSection`. |
| `web/elm/src/SideBar/SideBar.elm` | Modify | `agentPlatformLink` → new IA entry points; reviews promotion (coordinate W-11). |
| `web/elm/src/Message/Message.elm` | Modify | Remove the now-dead `AgentSectionNavClicked String` constructor. |
| `web/elm/src/UserState.elm` | Modify | Add `isAdmin : UserState -> Bool` for the `/agent/admin` client gate. |
| `web/elm/src/Agent/Nav.elm` | Create | Shared six-tab agent sub-nav (`view : Routes.Route -> Html Message`). |
| `web/elm/src/Agent/Runs.elm` | Create | Runs section view (moved verbatim from `Agent/Agent.elm`). |
| `web/elm/src/Agent/Workflows.elm` | Create | Workflows section view. |
| `web/elm/src/Agent/Spend.elm` | Create | Costs/spend section view. |
| `web/elm/src/Agent/Admin.elm` | Create | Credentials + principals section view + admin gate. |
| `web/elm/src/Agent/Shared.elm` | Create | Shared table/time/color/error helpers the section modules share. |
| `web/elm/src/Agent/Agent.elm` | Modify | Shell: `section` field, per-section view dispatch, remove anchor-scroll nav. |
| `web/elm/src/AgentTickets/AgentTickets.elm` | Modify | Mount `Agent.Nav`. |
| `web/elm/src/AgentTickets/AgentTicket.elm` | Modify | Mount `Agent.Nav`. |
| `web/elm/src/AgentReviews/AgentReviews.elm` | Modify | Mount `Agent.Nav`; team defaulting via new route. |
| `web/elm/tests/RoutesTests.elm` | Modify | Roundtrip tests for all six routes + legacy aliases. |
| `web/elm/tests/AgentPageTests.elm` | Modify | Point each section's assertions at its section URL. |
| `web/public/elm.min.js` | Modify (generated) | Regenerated bundle — committed with the source. |

---

## Task 1 — Route model: `AgentSection` + six real paths

Introduce the section-parameterized `Agent` route and the re-pathed tickets/reviews paths, with roundtrip tests. This is the "worked template" enumeration of every `Routes.elm` site a new agent route touches, using the `AgentReviews` route (lines 61, 318-321, 618-620, 741-742, 784-785 in the current file) as the model.

**The six Routes.elm touchpoints for an agent route** (verified against the current `AgentReviews` route):
1. **Type constructor** — `type Route` (line 61 region).
2. **Parser** — a `Parser` helper (e.g. `agentReviews`, lines 318-321) added to the `sitemap` `oneOf` (lines 498-514).
3. **`toString`** — the URL builder branch (lines 618-632).
4. **`getGroups`** — the exhaustive `case` (lines 741-751).
5. **`withGroups`** — the exhaustive `case` (lines 784-794).
6. (No `extractPid` branch — agent routes have no pipeline id; they already fall through the `_ ->` at line 681.)

Files:
- Modify: `web/elm/src/Routes.elm`
- Test: `web/elm/tests/RoutesTests.elm`

Steps:

- [ ] Write the failing tests. Replace the two existing agent-route tests (currently `web/elm/tests/RoutesTests.elm:299-310`) and add section + reviews roundtrips. Insert this block in place of lines 299-310:

```elm
        , test "agent tickets queue" <|
            \_ ->
                ("http://example.com" ++ Routes.toString Routes.AgentTickets)
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just Routes.AgentTickets)
        , test "agent tickets queue path is /agent/tickets" <|
            \_ ->
                Routes.toString Routes.AgentTickets
                    |> Expect.equal "/agent/tickets"
        , test "agent ticket detail roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.AgentTicket { id = 12 }))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentTicket { id = 12 })
        , test "agent ticket detail path is /agent/tickets/12" <|
            \_ ->
                Routes.toString (Routes.AgentTicket { id = 12 })
                    |> Expect.equal "/agent/tickets/12"
        , test "agent runs section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentRuns))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentRuns)
        , test "agent workflows section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentWorkflows))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentWorkflows)
        , test "agent spend section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentSpend))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentSpend)
        , test "agent admin section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentAdmin))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentAdmin)
        , test "bare /agent legacy alias parses to runs" <|
            \_ ->
                "http://example.com/agent"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentRuns)
        , test "agent reviews team-less path roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.AgentReviews { teamName = "main" }))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentReviews { teamName = "main" })
        , test "agent reviews path is /agent/reviews" <|
            \_ ->
                Routes.toString (Routes.AgentReviews { teamName = "main" })
                    |> Expect.equal "/agent/reviews"
        , test "legacy /teams/main/agent-reviews still parses" <|
            \_ ->
                "http://example.com/teams/main/agent-reviews"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentReviews { teamName = "main" })
        ]
```

- [ ] Run it, expect FAIL (compile error — `Routes.AgentRuns` and `Routes.Agent Routes.AgentRuns` don't exist yet):

```
cd web/elm && elm-test tests/RoutesTests.elm
```
Expected: `-- NAMING ERROR` / `I cannot find a 'Routes.AgentRuns' variant` (compile failure).

- [ ] Minimal implementation — Routes.elm edits.

  (1) **Type** — replace `web/elm/src/Routes.elm:62` (`| Agent`) and add the section type. Change:
```elm
    | AgentReviews { teamName : String }
    | Agent
    | AgentTickets
    | AgentTicket { id : Int }
```
to:
```elm
    | AgentReviews { teamName : String }
    | Agent AgentSection
    | AgentTickets
    | AgentTicket { id : Int }


type AgentSection
    = AgentRuns
    | AgentWorkflows
    | AgentSpend
    | AgentAdmin
```
  Also export `AgentSection(..)` — extend the `module Routes exposing (...)` list (line 2-22) by adding `AgentSection(..)` after `Route(..)`.

  (2) **Parsers** — replace the current `agent` parser (`web/elm/src/Routes.elm:324-326`):
```elm
agent : Parser ((b -> Route) -> a) a
agent =
    map (always <| Agent) (s "agent")
```
with a section parser plus re-pathed tickets/reviews parsers. Replace lines 318-336 (`agentReviews`, `agent`, `agentTicket`, `agentTickets`) with:
```elm
agentReviews : Parser ((b -> Route) -> a) a
agentReviews =
    map (always <| AgentReviews { teamName = defaultAgentTeam })
        (s "agent" </> s "reviews")


agentReviewsLegacy : Parser ((b -> Route) -> a) a
agentReviewsLegacy =
    map (\teamName -> always <| AgentReviews { teamName = teamName })
        (s "teams" </> string </> s "agent-reviews")


agentSection : Parser ((b -> Route) -> a) a
agentSection =
    oneOf
        [ map (always <| Agent AgentRuns) (s "agent" </> s "runs")
        , map (always <| Agent AgentWorkflows) (s "agent" </> s "workflows")
        , map (always <| Agent AgentSpend) (s "agent" </> s "spend")
        , map (always <| Agent AgentAdmin) (s "agent" </> s "admin")

        -- Legacy bare /agent → the runs section (the mega-page opened on runs).
        , map (always <| Agent AgentRuns) (s "agent")
        ]


agentTicket : Parser ((b -> Route) -> a) a
agentTicket =
    map (\id -> always <| AgentTicket { id = id }) (s "agent" </> s "tickets" </> int)


agentTickets : Parser ((b -> Route) -> a) a
agentTickets =
    map (always <| AgentTickets) (s "agent" </> s "tickets")


defaultAgentTeam : String
defaultAgentTeam =
    -- JetBridge is single-agent-team; the team-less /agent/reviews path binds
    -- to "main". See Open Decisions for the multi-team follow-up.
    "main"
```

  (3) **sitemap** — update the `oneOf` (lines 498-514). Order matters: the more specific `agent/tickets/:int` must precede `agent/tickets`, and both precede the bare-`agent` alias. Replace the agent entries so the block reads:
```elm
        , agentReviews
        , agentReviewsLegacy
        , agentTicket
        , agentTickets
        , agentSection
```
  (Keep `agentSection` LAST of the agent parsers so `s "agent" </> s "runs"` etc. are tried before the bare `s "agent"` fallback inside `agentSection`; and keep `agentTicket`/`agentTickets` before `agentSection` so `/agent/tickets` is not swallowed by the bare-`agent` alias.)

  (4) **toString** — replace lines 618-632:
```elm
        AgentReviews _ ->
            ( [ "agent", "reviews" ], [] )
                |> RouteBuilder.build

        Agent section ->
            ( [ "agent", agentSectionPath section ], [] )
                |> RouteBuilder.build

        AgentTickets ->
            ( [ "agent", "tickets" ], [] )
                |> RouteBuilder.build

        AgentTicket { id } ->
            ( [ "agent", "tickets", String.fromInt id ], [] )
                |> RouteBuilder.build
```
  and add the helper (place next to `toString`, after line 633):
```elm
agentSectionPath : AgentSection -> String
agentSectionPath section =
    case section of
        AgentRuns ->
            "runs"

        AgentWorkflows ->
            "workflows"

        AgentSpend ->
            "spend"

        AgentAdmin ->
            "admin"
```

  (5) **getGroups** — replace `Agent ->` at lines 744-745:
```elm
        Agent _ ->
            []
```

  (6) **withGroups** — replace `Agent ->` at lines 787-788:
```elm
        Agent _ ->
            route
```

- [ ] Run it, expect PASS:
```
cd web/elm && elm-test tests/RoutesTests.elm
```
Expected: `TEST RUN PASSED` for the RoutesTests module (the whole-suite build still fails until Task 2 fixes the other call sites; running the single file compiles Routes + its test).

  Note: `elm-test tests/RoutesTests.elm` compiles the whole `src` tree because `Routes` is imported widely; if the isolated run reports downstream errors (e.g. `Views/Styles.elm`), that is expected and resolved in Task 2. Confirm the RoutesTests *assertions* themselves are green once Task 2 lands (re-run at the end of Task 2).

- [ ] Commit:
```
git add web/elm/src/Routes.elm web/elm/tests/RoutesTests.elm
git commit -m "feat(web): AgentSection route type + /agent/* real paths (S-3 Task 1)"
```

---

## Task 2 — Fix every non-view compile site of the route change

The `Agent` constructor now takes an argument, so every exhaustive `case` on `Route` that matched bare `Agent` must become `Agent _` (or `Agent section`), and the two page-mapping sites (`SubPage.init`, `SubPage.urlUpdateValid`) must thread the section. This task makes `elm make src/Main.elm` compile again. No new behavior yet.

Files:
- Modify: `web/elm/src/Views/Styles.elm`, `web/elm/src/Views/TopBar.elm`, `web/elm/src/Application/Application.elm`, `web/elm/src/SubPage/SubPage.elm`

Steps:

- [ ] Run the compiler to enumerate the breakages (this is the "failing test" for a pure refactor):
```
cd web/elm && elm make --output /dev/null src/Main.elm
```
Expected: a series of `-- MISSING PATTERNS` / `This `case` does not have branches for all possibilities` and `-- TOO FEW ARGS` errors pointing at `Views/Styles.elm:141`, `Views/TopBar.elm:171`, and `SubPage/SubPage.elm:142`.

- [ ] Fix `web/elm/src/Views/Styles.elm`. Replace the four agent branches (lines 135-157) with a single collapsed branch (they all produce identical style):
```elm
                Routes.AgentReviews _ ->
                    agentLayout

                Routes.Agent _ ->
                    agentLayout

                Routes.AgentTickets ->
                    agentLayout

                Routes.AgentTicket _ ->
                    agentLayout
```
  and add near the top of the `pageBelowTopBar` `let` (or as a module-level helper after the function):
```elm
agentLayout : List (Html.Attribute msg)
agentLayout =
    [ style "box-sizing" "border-box"
    , style "display" "flex"
    , style "height" "100%"
    ]
```
  (Minimal alternative if you prefer no helper: change only `Routes.Agent ->` at line 141 to `Routes.Agent _ ->`. The helper is optional cleanup; either compiles.)

- [ ] Fix `web/elm/src/Views/TopBar.elm` breadcrumbs. Replace the `Routes.Agent ->` branch (lines 171-172) and add breadcrumbs for the other three agent routes that currently fall through to the empty `_ ->` (line 174). Replace lines 171-172 with:
```elm
            Routes.Agent section ->
                ( [ agentBreadcrumb, breadcrumbSeparator, agentSectionBreadcrumb section ], False, False )

            Routes.AgentTickets ->
                ( [ agentBreadcrumb, breadcrumbSeparator, agentLeafBreadcrumb "tickets" ], False, False )

            Routes.AgentTicket { id } ->
                ( [ agentBreadcrumb
                  , breadcrumbSeparator
                  , agentLeafBreadcrumb "tickets"
                  , breadcrumbSeparator
                  , agentLeafBreadcrumb ("#" ++ String.fromInt id)
                  ]
                , False
                , False
                )

            Routes.AgentReviews _ ->
                ( [ agentBreadcrumb, breadcrumbSeparator, agentLeafBreadcrumb "reviews" ], False, False )
```
  and add these helpers next to `agentBreadcrumb` (after line 221):
```elm
agentSectionBreadcrumb : Routes.AgentSection -> (Bool -> Html Message)
agentSectionBreadcrumb section =
    agentLeafBreadcrumb <|
        case section of
            Routes.AgentRuns ->
                "runs"

            Routes.AgentWorkflows ->
                "workflows"

            Routes.AgentSpend ->
                "spend"

            Routes.AgentAdmin ->
                "admin"


agentLeafBreadcrumb : String -> Bool -> Html Message
agentLeafBreadcrumb label _ =
    Html.div
        (id ("breadcrumb-agent-" ++ label) :: Styles.clusterName)
        [ Html.text label ]
```
  Note: `breadcrumbs`' `buildBreadcrumbs` (lines 81-95) expects each list element to be a `Bool -> Html Message`; `breadcrumbSeparator` (line 203) already has that shape, and `agentBreadcrumb` (line 217) does too, so this list typechecks. Import guard: `Routes` is already imported in TopBar.elm; `Routes.AgentSection` resolves because Task 1 exported `AgentSection(..)`.

- [ ] Fix `web/elm/src/Application/Application.elm` `routeMatchesModel` (lines 517-539) so section switches call `urlUpdate` (preserving the fetched ledger/workflows/costs) instead of re-`init`. Add before the final `_ -> False`:
```elm
        ( Routes.Agent _, SubPage.AgentModel _ ) ->
            True
```

- [ ] Fix `web/elm/src/SubPage/SubPage.elm`. (a) `init` — replace `Routes.Agent ->` (lines 142-144):
```elm
        Routes.Agent section ->
            Agent.init section
                |> Tuple.mapFirst AgentModel
```
  (b) `urlUpdateValid` — the Agent function slot is the 11th positional argument, currently `identity` at SubPage.elm:455. It is the FIFTH bare `identity` in that argument list (the slots after `fCaus` are `fNF`, `fFS`, `dFly`, `fAR`, then `fAgent` — all `identity`), NOT the first. Edit line 455, not line 451. Replace that single `identity` with:
```elm
        (case routes.to of
            Routes.Agent section ->
                Agent.changeSection section

            _ ->
                identity
        )
```
  Count carefully against `genericUpdate`'s parameter order `fBuild fJob fRes fPipe fDash fCaus fNF fFS dFly fAR fAgent fATs fAT` (SubPage.elm:206): the slots after `fCaus` are `fNF` (identity), `fFS` (identity), `dFly` (identity), `fAR` (identity), then `fAgent` — this is the Agent slot to replace. Leave `fATs`/`fAT` (the last two `identity`s) untouched.
  `Agent.init` and `Agent.changeSection` are provided in Task 5; until then this file will not compile — that is expected. To keep Task 2 independently green, stub them now in `Agent/Agent.elm`: change `init` to take a section and add `changeSection` (real bodies land in Task 5). Minimal stubs:
```elm
init : Routes.AgentSection -> ( Model, List Effect )
init section =
    ( { -- existing record fields unchanged ...
        section = section
      , ...
      }
    , [ FetchAgentRunMetrics, ... ]
    )


changeSection : Routes.AgentSection -> ET Model
changeSection section ( model, effects ) =
    ( { model | section = section }, effects )
```
  Because the full shell rewrite is Task 5, and Task 2's only goal is "compiles again", it is acceptable to land Tasks 2 and 5 as one commit if the stub churn is awkward. Recommended: do Task 2's Styles/TopBar/Application edits, then proceed directly into Task 5's Agent.elm rewrite and compile once. The plan keeps them separate for reviewability but they may share a green-compile checkpoint.

- [ ] Run the compiler, expect PASS once Task 5's `Agent.init section` / `changeSection` exist:
```
cd web/elm && elm make --output /dev/null src/Main.elm
```
Expected: `Success!` (no output file written).

- [ ] Commit (fold with Task 5 if you shared the compile checkpoint):
```
git add web/elm/src/Views/Styles.elm web/elm/src/Views/TopBar.elm web/elm/src/Application/Application.elm web/elm/src/SubPage/SubPage.elm
git commit -m "refactor(web): thread AgentSection through layout/breadcrumbs/subpage (S-3 Task 2)"
```

---

## Task 3 — Shared agent sub-nav module (`Agent/Nav.elm`)

The six-tab strip every agent page mounts, replacing the anchor-jump `sectionNav`. Real `<a href>` links (so back/forward and bookmarking work), active tab highlighted from the current route.

Files:
- Create: `web/elm/src/Agent/Nav.elm`
- Test: `web/elm/tests/AgentPageTests.elm` (add a nav describe-block)

Steps:

- [ ] Add `id` to the `Test.Html.Selector` import FIRST. `web/elm/tests/AgentPageTests.elm:17` is currently:
```elm
import Test.Html.Selector exposing (attribute, class, containing, style, tag, text)
```
  `id` is NOT exposed. Every new test in Tasks 3, 6, and 7 uses `id "agent-subnav…"` / `id "sidebar-agent-tickets"` selectors and will fail to compile with a NAMING ERROR until `id` is added. Change line 17 to:
```elm
import Test.Html.Selector exposing (attribute, class, containing, id, style, tag, text)
```

- [ ] Write the failing test. Append to `web/elm/tests/AgentPageTests.elm` inside the top-level `describe "agent page"` list (before its closing `]`):
```elm
        , describe "agent sub-nav"
            [ test "renders all six section tabs" <|
                \_ ->
                    Common.init "/agent/runs"
                        |> Common.queryView
                        |> Query.find [ id "agent-subnav" ]
                        |> Query.findAll [ tag "a" ]
                        |> Query.count (Expect.equal 6)
            , test "runs tab links to /agent/runs" <|
                \_ ->
                    Common.init "/agent/runs"
                        |> Common.queryView
                        |> Query.find [ id "agent-subnav-runs" ]
                        |> Query.has [ attribute (Attr.href "/agent/runs") ]
            , test "reviews tab links to /agent/reviews" <|
                \_ ->
                    Common.init "/agent/runs"
                        |> Common.queryView
                        |> Query.find [ id "agent-subnav-reviews" ]
                        |> Query.has [ attribute (Attr.href "/agent/reviews") ]
            , test "the active section tab is marked current" <|
                \_ ->
                    Common.init "/agent/spend"
                        |> Common.queryView
                        |> Query.find [ id "agent-subnav-spend" ]
                        |> Query.has [ attribute (Attr.attribute "aria-current" "page") ]
            ]
```
  (`Common.queryView` and the `tag`/`attribute` selectors are already imported at the top of AgentPageTests.elm; `id` is NOT — it is added in the import step above (do that first or these tests won't compile). `Common` exposes `queryView` — confirm with `grep -n queryView web/elm/tests/Common.elm`. If absent, use `Application.view >> .body >> ... Query.fromHtml` per the existing tests' pattern at lines 202-210.)

- [ ] Run it, expect FAIL:
```
cd web/elm && elm-test tests/AgentPageTests.elm
```
Expected: `Query.find [ id "agent-subnav" ] always failed` (the nav does not exist yet) — but note the whole page still renders the mega-page until Task 5, so this will additionally fail to compile only if `Agent.Nav` is referenced; keep the nav test red on the missing element.

- [ ] Minimal implementation — create `web/elm/src/Agent/Nav.elm`:
```elm
module Agent.Nav exposing (view)

import Colors
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, href, id, style)
import Message.Message exposing (Message)
import Routes


{-| The persistent agent sub-nav mounted by every /agent/* page. Real links so
back/forward and bookmarking work; the tab matching the current route is marked
`aria-current="page"` and rendered in the active color.
-}
view : Routes.Route -> Html Message
view current =
    Html.div
        [ id "agent-subnav"
        , class "agent-subnav"
        , style "display" "flex"
        , style "flex-wrap" "wrap"
        , style "gap" "4px"
        , style "margin" "0 0 16px 0"
        , style "border-bottom" ("1px solid " ++ Colors.background)
        , style "font-family" "monospace"
        , style "font-size" "13px"
        ]
        (List.map (tab current) tabs)


tabs : List ( String, String, Routes.Route )
tabs =
    [ ( "tickets", "tickets", Routes.AgentTickets )
    , ( "runs", "runs", Routes.Agent Routes.AgentRuns )
    , ( "reviews", "reviews", Routes.AgentReviews { teamName = "main" } )
    , ( "workflows", "workflows", Routes.Agent Routes.AgentWorkflows )
    , ( "spend", "spend", Routes.Agent Routes.AgentSpend )
    , ( "admin", "admin", Routes.Agent Routes.AgentAdmin )
    ]


tab : Routes.Route -> ( String, String, Routes.Route ) -> Html Message
tab current ( slug, label, route ) =
    let
        active =
            isActive current route

        activeAttrs =
            if active then
                [ attribute "aria-current" "page"
                , style "color" Colors.text
                , style "border-bottom" "2px solid #7a9ac0"
                ]

            else
                [ style "color" "#7a9ac0"
                , style "border-bottom" "2px solid transparent"
                ]
    in
    Html.a
        ([ id ("agent-subnav-" ++ slug)
         , href (Routes.toString route)
         , style "padding" "8px 12px"
         , style "text-decoration" "none"
         ]
            ++ activeAttrs
        )
        [ Html.text label ]


{-| A tab is active when the current route is in its section family. The ticket
DETAIL page (`AgentTicket`) lights the "tickets" tab; the section routes match
by AgentSection; reviews matches any team.
-}
isActive : Routes.Route -> Routes.Route -> Bool
isActive current route =
    case ( current, route ) of
        ( Routes.AgentTickets, Routes.AgentTickets ) ->
            True

        ( Routes.AgentTicket _, Routes.AgentTickets ) ->
            True

        ( Routes.AgentReviews _, Routes.AgentReviews _ ) ->
            True

        ( Routes.Agent a, Routes.Agent b ) ->
            a == b

        _ ->
            False
```

- [ ] The nav test still fails until Task 5 mounts it. Note the sequencing: `Agent.Nav` is created here and consumed in Tasks 5 and 6. Run `elm make --output /dev/null src/Agent/Nav.elm`-equivalent by compiling the whole tree at the end of Task 5. For this task's checkpoint, confirm the module compiles in isolation:
```
cd web/elm && elm make --output /dev/null src/Agent/Nav.elm
```
Expected: `Success!`

- [ ] Commit:
```
git add web/elm/src/Agent/Nav.elm web/elm/tests/AgentPageTests.elm
git commit -m "feat(web): shared Agent.Nav six-tab sub-nav (S-3 Task 3)"
```

---

## Task 4 — Split the four section views into modules

Move the runs / workflows / costs / credentials+principals rendering out of `Agent/Agent.elm` into view-only modules, plus a shared-helpers module. These are **verbatim moves** of existing, already-tested render functions — no logic changes — so the "test" is a clean compile and the unchanged AgentPageTests (retargeted in Task 5). Reproducing ~800 lines here would be noise; instead each move is specified by exact function name and current line range in `web/elm/src/Agent/Agent.elm`.

First, create `Agent/Shared.elm` with the helpers the section modules share (currently private top-level functions in Agent.elm). Move these VERBATIM (keeping bodies byte-identical), changing only the module they live in:

- `mutedColor` (421-423), `subtleColor` (426-428), `rowBorder` (431-433), `amberColor` (436-438)
- `formatUsd` (444-473)
- `sectionBlock` (476-487), `mutedLine` (490-498), `errorLine` (501-510), `staleDataWarning` (517-527), `pill` (529-541)
- `tableHeaderCell` (1141-1150), `tableCell` (1153-1160)
- `formatPosix` (1169-1188), `secondsToPosix` (864-866)

Files:
- Create: `web/elm/src/Agent/Shared.elm`, `web/elm/src/Agent/Runs.elm`, `web/elm/src/Agent/Workflows.elm`, `web/elm/src/Agent/Spend.elm`, `web/elm/src/Agent/Admin.elm`
- Modify: `web/elm/src/UserState.elm`

Steps:

- [ ] Create `web/elm/src/Agent/Shared.elm`. Header + exposes; bodies are the verbatim moves listed above:
```elm
module Agent.Shared exposing
    ( amberColor
    , errorLine
    , formatPosix
    , formatUsd
    , mutedColor
    , mutedLine
    , pill
    , rowBorder
    , sectionBlock
    , secondsToPosix
    , staleDataWarning
    , subtleColor
    , tableCell
    , tableHeaderCell
    )

import Colors
import DateFormat
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Message.Message exposing (Message)
import Time

-- (verbatim bodies of the 14 helpers listed above, moved from Agent/Agent.elm)
```
  The moved functions reference only `Colors`, `DateFormat`, `Html`, `Html.Attributes`, `Message.Message`, `Time` — all imported above; no other change needed.

- [ ] Add `isAdmin` to `web/elm/src/UserState.elm`. Change the exposing line (line 1) to add `isAdmin`, and append:
```elm
isAdmin : UserState -> Bool
isAdmin userState =
    case userState of
        UserStateLoggedIn user ->
            user.isAdmin

        _ ->
            False
```
  (`Concourse.User` has `isAdmin : Bool` — verified at `web/elm/src/Concourse.elm:1634`.)

- [ ] Create `web/elm/src/Agent/Runs.elm`. Exposes `view`, imports `Agent.Shared`, `AgentBadge`, `Views.Prose`. Move VERBATIM from Agent.elm: `runsSection` (657-682), `runsTable` (685-694), `runsHeaderRow` (697-708), `runKey` (715-717), `runRow` (720-731), `runStepCell` (740-791), `runStatusCell` (802-815), `workflowRef` (821-831), `ticketRefCell` (837-861). Change `runsSection`'s signature so it takes the fields it needs from the Model rather than the whole Model, so the module does not depend on `Agent.Agent.Model`:
```elm
module Agent.Runs exposing (view)

import Agent.Shared as Shared
import AgentBadge
import Concourse.Agent as Agent
import Colors
import Html exposing (Html)
import Html.Attributes exposing (class, style, title)
import Html.Events exposing (onClick)
import Html.Lazy
import Message.Message exposing (Message(..))
import Routes
import Set exposing (Set)
import Time
import Views.Prose


view : Time.Zone -> Set String -> Maybe (List Agent.RunMetric) -> Maybe String -> Html Message
view zone expandedRuns maybeRuns runsError =
    Shared.sectionBlock "agent-runs" "Recent runs" <|
        case maybeRuns of
            Nothing ->
                case runsError of
                    Just message ->
                        [ Shared.errorLine message ]

                    Nothing ->
                        [ Shared.mutedLine "loading…" ]

            Just [] ->
                Shared.staleDataWarning runsError
                    ++ [ Shared.mutedLine "no agent runs recorded yet" ]

            Just runs ->
                Shared.staleDataWarning runsError
                    ++ [ Shared.mutedLine "showing the newest 100 runs (capped server-side, most recent first)"
                       , Html.Lazy.lazy3 runsTable zone expandedRuns runs
                       ]

-- (verbatim: runsTable, runsHeaderRow, runKey, runRow, runStepCell,
--  runStatusCell, workflowRef, ticketRefCell — with every bare `mutedColor`,
--  `subtleColor`, `rowBorder`, `tableCell`, `tableHeaderCell`, `formatPosix`,
--  `secondsToPosix` reference prefixed `Shared.`)
```
  Mechanical rule for all four section modules: any call that resolved to a now-moved Shared helper gets a `Shared.` prefix. `AgentBadge`, `Views.Prose`, `Routes`, `Concourse.Agent as Agent` imports carry over unchanged.

- [ ] Create `web/elm/src/Agent/Workflows.elm`. Exposes `view : Maybe (List Agent.WorkflowSummary) -> Maybe String -> Html Message`. Move VERBATIM: `workflowsSection` (873-891), `workflowRow` (894-926), `workflowPills` (929-952), `liveVersionLine` (955-961); change `workflowsSection` to take `(maybeWorkflows, workflowsError)` instead of `Model`; `Shared.`-prefix the shared helpers.

- [ ] Create `web/elm/src/Agent/Spend.elm`. Exposes `view : { costRollup : Maybe Agent.CostRollup, costError : Maybe String, unattributedUsd : Maybe Float } -> Html Message`. Move VERBATIM: `costsSection` (968-987), `costSummaryLine` (989-1026), `dailyCapGauge` (1033-1071), `unattributedLine` (1078-1096), `costTable` (1099-1112), `costHeaderRow` (1115-1123), `costRow` (1126-1134); change `costsSection` to take the record above; `Shared.`-prefix.

- [ ] Create `web/elm/src/Agent/Admin.elm`. Exposes `view : UserState -> Time.Zone -> AdminData -> Html Message` where `AdminData` is a record of the credential/principal/mint fields. Add the admin gate at the top of `view`:
```elm
view : UserState -> Time.Zone -> AdminData -> Html Message
view userState zone data =
    if UserState.isAdmin userState then
        adminBody zone data

    else
        Html.div [ id "agent-admin-denied" ]
            [ Shared.errorLine "not authorized — the agent admin surface (credentials + principals) is admin-only" ]
```
  Move VERBATIM into `adminBody` and its helpers: `credentialsSection` (1195-1203), `credentialSlotLabel` (1209-1218), `platformCredentialsBlock` (1226-1254), `personalCredentialsBlock` (1261-1281), `credentialsTable` (1284-1299), `credentialRow` (1302-1308), `principalsSection` (1315-1322), `mintFence` (1329-1348), `revokeErrorLine` (1355-1362), `mintForm` (1365-1395), `mintTextField` (1398-1413), `scopeCheckbox` (1416-1432), `expiresField` (1435-1469), `expiresHint` (1476-1488), `mintButton` (1491-1536), `mintErrorLine` (1539-1546), `mintedTokenBox` (1549-1593), `principalsBody` (1596-1623), `isEphemeralPrincipal` (1631-1638), `ephemeralPrincipals` (1641-1681), `principalsTable` (1684-1703), `principalRow` (1706-1753). Also move the module-level `mintScopeVocabulary` (49-57), `canMint` (272-277), and `expiresIsValid` (283-296) — but note `canMint`/`expiresIsValid`/`mintScopeVocabulary` are also read by `Agent.update` (`AgentMintSubmitted`, lines 321-342). Keep the CANONICAL copies in `Agent/Agent.elm` (update still owns them) and have `Agent.Admin` import them: add `mintScopeVocabulary`, `canMint`, `expiresIsValid` to `Agent/Agent.elm`'s `exposing` list and `import Agent.Agent as Agent` in Admin.elm. This avoids duplicating validation logic across two modules.
  `AdminData` shape:
```elm
type alias AdminData =
    { credentials : Maybe (List CredAgent.CredentialStatus)
    , credentialsError : Maybe String
    , platformCredentials : Maybe (List CredAgent.CredentialStatus)
    , principals : Maybe (List CredAgent.Principal)
    , principalsError : Maybe String
    , revokeError : Maybe String
    , showEphemeralPrincipals : Bool
    , mintName : String
    , mintDescription : String
    , mintScopes : Set String
    , mintExpiresDays : String
    , mintedToken : Maybe String
    , mintError : Maybe String
    , minting : Bool
    }
```
  (Field types match `Agent.Agent.Model` lines 66-83; `CredAgent` = `import Concourse.Agent as CredAgent` — same module aliased.)
  Circular-import guard: `Agent.Admin` importing `Agent.Agent` while `Agent.Agent` imports `Agent.Admin` is a cycle Elm rejects. Resolve by moving `mintScopeVocabulary`/`canMint`/`expiresIsValid` into `Agent/Shared.elm` instead (they are pure, no Model dependency once `canMint`/`expiresIsValid` take the raw fields). `canMint` currently takes `Model`; change it to `canMint : { name : String, scopes : Set String, expiresDays : String } -> Bool`. Update the one caller in `Agent.update` accordingly. This keeps a single copy in `Shared` with no cycle. **Prefer this (Shared) placement over the exposing-from-Agent option above.**

- [ ] Compile the new modules in isolation:
```
cd web/elm && elm make --output /dev/null src/Agent/Admin.elm src/Agent/Runs.elm src/Agent/Spend.elm src/Agent/Workflows.elm
```
Expected: `Success!` (the modules are self-contained view functions; they don't import `Agent.Agent`).

- [ ] Commit:
```
git add web/elm/src/Agent/Shared.elm web/elm/src/Agent/Runs.elm web/elm/src/Agent/Workflows.elm web/elm/src/Agent/Spend.elm web/elm/src/Agent/Admin.elm web/elm/src/UserState.elm
git commit -m "refactor(web): extract agent section views into modules (S-3 Task 4)"
```

---

## Task 5 — Rewrite the Agent shell + migrate AgentPageTests

Turn `Agent/Agent.elm` into a thin shell: the `Model` gains `section : Routes.AgentSection`, `init`/`changeSection` set it, `view` renders the shared chrome + `Agent.Nav` + exactly the active section (delegating to the four new modules), and the anchor-jump nav (`sectionNav`, `navLink`, `agentContentId` scroll, `AgentSectionNavClicked`) is deleted. Then retarget AgentPageTests so each section's assertions load that section's URL.

Files:
- Modify: `web/elm/src/Agent/Agent.elm`, `web/elm/src/Message/Message.elm`, `web/elm/tests/AgentPageTests.elm`

Steps:

- [ ] Add the `section` field to `Model` (`web/elm/src/Agent/Agent.elm:60-84`): add `, section : Routes.AgentSection` to the record.

- [ ] Replace `init` (lines 87-121) so it takes a section and seeds it. Keep the same fetch list (fetch-all keeps section switches instant with already-loaded data; per-section fetch is a deferred optimization — see Open Decisions):
```elm
init : Routes.AgentSection -> ( Model, List Effect )
init section =
    ( { runs = Nothing
      , runsError = Nothing
      , workflows = Nothing
      , costRollup = Nothing
      , workflowsError = Nothing
      , costError = Nothing
      , credentials = Nothing
      , credentialsError = Nothing
      , platformCredentials = Nothing
      , unattributedUsd = Nothing
      , principals = Nothing
      , principalsError = Nothing
      , mintName = ""
      , mintDescription = ""
      , mintScopes = Set.empty
      , mintExpiresDays = ""
      , mintedToken = Nothing
      , mintError = Nothing
      , minting = False
      , revokeError = Nothing
      , showEphemeralPrincipals = False
      , expandedRuns = Set.empty
      , isUserMenuExpanded = False
      , section = section
      }
    , [ FetchAgentRunMetrics
      , FetchAgentWorkflows
      , FetchAgentCostRollup
      , FetchAgentTicketCosts
      , FetchAgentCredentials
      , FetchAgentPlatformCredentials
      , FetchAgentPrincipals
      ]
    )


changeSection : Routes.AgentSection -> ET Model
changeSection section ( model, effects ) =
    ( { model | section = section }, effects )
```

- [ ] Update `documentTitle` (lines 124-126) to be section-aware so the browser tab distinguishes sections. Change from a constant `String` to a function:
```elm
documentTitle : Routes.AgentSection -> String
documentTitle section =
    case section of
        Routes.AgentRuns ->
            "Agent runs"

        Routes.AgentWorkflows ->
            "Agent workflows"

        Routes.AgentSpend ->
            "Agent spend"

        Routes.AgentAdmin ->
            "Agent admin"
```
  and update the caller in `web/elm/src/SubPage/SubPage.elm` `view` (line 524): `( Agent.documentTitle model.section, Agent.view session model )`.

- [ ] Delete the anchor nav machinery: remove `AgentSectionNavClicked anchorId ->` from `update` (lines 356-357), remove `sectionNav` (617-635), `navLink` (638-650), and the `agentContentId` scroll usages. Also drop the now-unused imports (`Message.ScrollDirection`, `Scroll` effect usage) if nothing else references them — run the compiler to confirm which imports go unused.

- [ ] Rewrite `view` (lines 548-599) to render the shared chrome once and dispatch the active section. Note the shell keeps the top-bar + sidebar + breadcrumbs (persistent sidebar requirement) and mounts `Agent.Nav`:
```elm
view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.Agent model.section
    in
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            [ Html.div
                [ style "display" "flex", style "align-items" "center" ]
                (SideBar.sideBarIcon session
                    :: TopBar.breadcrumbs session route
                )
            , Login.view session.userState model
            ]
        , Html.div
            (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar route)
            [ SideBar.view session Nothing
            , Html.div
                [ id "agent-content"
                , style "padding" "16px"
                , style "width" "100%"
                , style "box-sizing" "border-box"
                , style "overflow-y" "auto"
                ]
                [ Agent.Nav.view route
                , sectionView session model
                ]
            ]
        ]


sectionView : Session -> Model -> Html Message
sectionView session model =
    case model.section of
        Routes.AgentRuns ->
            Agent.Runs.view session.timeZone model.expandedRuns model.runs model.runsError

        Routes.AgentWorkflows ->
            Agent.Workflows.view model.workflows model.workflowsError

        Routes.AgentSpend ->
            Agent.Spend.view
                { costRollup = model.costRollup
                , costError = model.costError
                , unattributedUsd = model.unattributedUsd
                }

        Routes.AgentAdmin ->
            Agent.Admin.view session.userState session.timeZone
                { credentials = model.credentials
                , credentialsError = model.credentialsError
                , platformCredentials = model.platformCredentials
                , principals = model.principals
                , principalsError = model.principalsError
                , revokeError = model.revokeError
                , showEphemeralPrincipals = model.showEphemeralPrincipals
                , mintName = model.mintName
                , mintDescription = model.mintDescription
                , mintScopes = model.mintScopes
                , mintExpiresDays = model.mintExpiresDays
                , mintedToken = model.mintedToken
                , mintError = model.mintError
                , minting = model.minting
                }
        ]
```
  Add imports to `Agent/Agent.elm`: `import Agent.Nav`, `import Agent.Runs`, `import Agent.Workflows`, `import Agent.Spend`, `import Agent.Admin`. Remove the now-unused section render functions and helpers that moved to the section/Shared modules (they will be reported as unused / referenced-from-nowhere; delete them). `handleCallback`, `update` (minus the deleted branch), `polls`, `handleDelivery`, `subscriptions`, `canMint`/`expiresIsValid` caller all stay — but `canMint`'s caller in `AgentMintSubmitted` now calls `Shared.canMint { name = model.mintName, scopes = model.mintScopes, expiresDays = model.mintExpiresDays }`.

- [ ] Remove the dead message. In `web/elm/src/Message/Message.elm` delete line 71 (`| AgentSectionNavClicked String`). Recompile to confirm no remaining reference.

- [ ] Migrate `web/elm/tests/AgentPageTests.elm`. Every existing case uses `Common.init "/agent"`; after the split, `/agent` (and `/agent/runs`) render ONLY the runs section, so assertions about other sections must load their section URL. Change each `Common.init "/agent"` to the URL matching what the case asserts, per this mapping (grep the assertion body for the section keyword):

  | Asserts about | New URL |
  |---|---|
  | runs table / run rows / `agent-runs` / status badge / ticket ref | `/agent/runs` |
  | workflows / `agent-workflows` / live/candidate pills | `/agent/workflows` |
  | costs / `agent-costs` / daily cap gauge / unattributed | `/agent/spend` |
  | credentials / `agent-credentials` / platform credential | `/agent/admin` |
  | principals / mint form / `agent-principals` / ephemeral / minted token | `/agent/admin` |

  **In-body `/agent-tickets/...` href literals must ALSO be re-pathed — not just the `Common.init` URLs.** Task 1 re-paths tickets and `ticketRefCell` now renders `Routes.toString (Routes.AgentTicket { id })` = `/agent/tickets/42`, so any test asserting the OLD `/agent-tickets/...` literal breaks. Concretely, the runs-section case at `AgentPageTests.elm:255` currently asserts:
```elm
                Common.init "/agent"
                    ...
                        [ tag "a"
                        , attribute (Attr.href "/agent-tickets/42")
                        , containing [ text "#42" ]
                        ]
```
  Change BOTH: the `Common.init "/agent"` → `Common.init "/agent/runs"` (per the mapping — this case asserts about the runs table / ticket ref), AND the href literal `Attr.href "/agent-tickets/42"` → `Attr.href "/agent/tickets/42"`. Grep the whole test file for any remaining `/agent-tickets/` string literal and re-path each to `/agent/tickets/`.

  Concrete example — the workflows case (currently around AgentPageTests.elm:395):
```elm
        -- before
        , test "lists workflow definitions" <|
            \_ ->
                Common.init "/agent"
                    |> ... (asserts agent-workflows content)
        -- after
        , test "lists workflow definitions" <|
            \_ ->
                Common.init "/agent/workflows"
                    |> ... (unchanged body)
```
  Do the same substitution for every case. The admin-gated cases are those matching credentials/principals/mint above.

  **Admin-login recipe (Task 4 introduces the `isAdmin` gate, so `/agent/admin` renders `agent-admin-denied` for a non-admin session).** There is NO `Common.initWithAdmin` helper and NO admin user fixture in `tests/Data.elm` — do not reference either. `Concourse.User` is `{ id, userName, name, email, isAdmin, teams, displayUserId }` (verified `src/Concourse.elm:1629`). Build an admin user locally and feed it through `Application.update` via `Callback.UserFetched`, exactly as `TopBarTests.elm:209` drives `sampleUser` (which has `isAdmin = False`). For each admin-content case, either:
  - **(preferred) an explicit admin-session case:** define
```elm
adminUser : Concourse.User
adminUser =
    { id = "1"
    , userName = "admin"
    , name = "Admin"
    , email = "admin@example.com"
    , isAdmin = True
    , teams = Dict.empty
    , displayUserId = "admin"
    }
```
    (add `import Concourse` if not already imported; `Dict` is already imported at AgentPageTests.elm:6), then
```elm
        , test "admin sees the credentials section" <|
            \_ ->
                Common.init "/agent/admin"
                    |> Application.handleCallback (Callback.UserFetched (Ok adminUser))
                    |> Tuple.first
                    -- then re-drive the section fetches this case needs, e.g.
                    |> Application.handleCallback (Callback.AgentCredentialsFetched (Ok [ ... ]))
                    |> Tuple.first
                    |> Common.queryView
                    |> ... (unchanged assertion body)
```
    Order matters: apply `UserFetched (Ok adminUser)` BEFORE (or interleaved with) the credential/principal `handleCallback`s so the gate is open when the section data lands; re-drive the fetches the original case relied on.
  - **OR the anonymous-denied path:** for at least one case, keep `Common.init "/agent/admin"` with no `UserFetched` and assert `Query.has [ id "agent-admin-denied" ]` to pin the gate, and add one explicit admin-session case (above) that proves the content renders when `isAdmin = True`.

- [ ] Run the full agent + routes + subpage tests, expect PASS:
```
cd web/elm && elm-test tests/AgentPageTests.elm tests/RoutesTests.elm tests/SubPageTests.elm
```
Expected: `TEST RUN PASSED`.

- [ ] Full tree compile, expect PASS:
```
cd web/elm && elm make --output /dev/null src/Main.elm
```
Expected: `Success!`

- [ ] Commit:
```
git add web/elm/src/Agent/Agent.elm web/elm/src/SubPage/SubPage.elm web/elm/src/Message/Message.elm web/elm/tests/AgentPageTests.elm
git commit -m "refactor(web): Agent shell renders one section per route; drop anchor nav (S-3 Task 5)"
```

---

## Task 6 — Mount `Agent.Nav` on tickets & reviews pages

The ticket queue, ticket detail, and reviews index are their own SubPage modules; give them the same sub-nav so the IA is consistent across all six routes. Breadcrumbs for these routes already landed in Task 2 (TopBar). Minimal, surgical view edits — do NOT touch these modules' Model/update (S-4/S-5 also edit `AgentTicket.elm`; keep the diff to the view's content wrapper).

Files:
- Modify: `web/elm/src/AgentTickets/AgentTickets.elm`, `web/elm/src/AgentTickets/AgentTicket.elm`, `web/elm/src/AgentReviews/AgentReviews.elm`

Steps:

- [ ] Write the failing test. Append to `web/elm/tests/AgentPageTests.elm` (or the relevant page test file) inside a describe block:
```elm
        , test "ticket queue mounts the agent sub-nav with tickets active" <|
            \_ ->
                Common.init "/agent/tickets"
                    |> Common.queryView
                    |> Query.find [ id "agent-subnav-tickets" ]
                    |> Query.has [ attribute (Attr.attribute "aria-current" "page") ]
        , test "reviews index mounts the agent sub-nav with reviews active" <|
            \_ ->
                Common.init "/agent/reviews"
                    |> Common.queryView
                    |> Query.find [ id "agent-subnav-reviews" ]
                    |> Query.has [ attribute (Attr.attribute "aria-current" "page") ]
```

- [ ] Run it, expect FAIL:
```
cd web/elm && elm-test tests/AgentPageTests.elm
```
Expected: `Query.find [ id "agent-subnav-tickets" ] always failed`.

- [ ] Minimal implementation. In each of the three views, add `import Agent.Nav` and insert `Agent.Nav.view route` as the first child of the page-content container (the `div` after `SideBar.view session Nothing`). Each module already binds `route` in its `view` `let` (AgentTickets:189, AgentTicket:458, AgentReviews:99). Example for `web/elm/src/AgentTickets/AgentTickets.elm` (the content div begins right after `SideBar.view session Nothing` at line 205):
```elm
            [ SideBar.view session Nothing
            , Html.div
                [ ... existing content-container attrs ... ]
                (Agent.Nav.view route :: existingChildren)
            ]
```
  If the content children are a `[ ... ]` literal, prepend `Agent.Nav.view route ::` to that list. Apply the identical one-line insertion to `AgentTicket.elm` (after line 474) and `AgentReviews.elm` (after line 115). For `AgentReviews`, `route` is `Routes.AgentReviews { teamName = model.teamName }` (line 99) — `Agent.Nav.isActive` matches any team, so the reviews tab lights regardless of team.

- [ ] Run it, expect PASS:
```
cd web/elm && elm-test tests/AgentPageTests.elm
```
Expected: `TEST RUN PASSED`.

- [ ] Commit:
```
git add web/elm/src/AgentTickets/AgentTickets.elm web/elm/src/AgentTickets/AgentTicket.elm web/elm/src/AgentReviews/AgentReviews.elm web/elm/tests/AgentPageTests.elm
git commit -m "feat(web): mount agent sub-nav on tickets & reviews pages (S-3 Task 6)"
```

---

## Task 7 — Sidebar IA + reviews promotion (coordinate W-11)

Update the sidebar's agent links to the new IA and promote the reviews index. **W-11 dependency:** W-11 adds a reviews nav link; run S-3 AFTER W-11 lands, re-grep `agentPlatformLink` at HEAD, and reconcile rather than reverting W-11's edit. The current block (`web/elm/src/SideBar/SideBar.elm:344-348`) has two links (`Agent platform` → `Routes.Agent`, `Ticket queue` → `Routes.AgentTickets`). Both compile-break in Task 1 because `Routes.Agent` now needs a section.

Files:
- Modify: `web/elm/src/SideBar/SideBar.elm`

Steps:

- [ ] Write the failing test. Add to `web/elm/tests/SideBar/` (match the existing SideBar test file layout — grep for a SideBar feature test that asserts `sidebar-agent-tickets`). If none targets these links, add to `web/elm/tests/AgentPageTests.elm`:
```elm
        , test "sidebar agent link targets the new /agent/tickets IA" <|
            \_ ->
                Common.init "/agent/runs"
                    |> Common.queryView
                    |> Query.find [ id "sidebar-agent-tickets" ]
                    |> Query.has [ attribute (Attr.href "/agent/tickets") ]
```
  (The sidebar renders only when `hasVisiblePipelines` and the sidebar is open — see `SideBar.view` guard at SideBar.elm:223-226. The existing SideBar tests set that up; reuse their fixture. If pipelines aren't seeded in `Common.init`, follow the SideBarFeature.elm recipe to open the sidebar with a visible pipeline.)

- [ ] Run it, expect FAIL (compile error from Task 1's `Routes.Agent` arity change if not yet fixed, or missing href):
```
cd web/elm && elm-test tests/AgentPageTests.elm
```

- [ ] Minimal implementation. Replace `agentPlatformLink` (SideBar.elm:344-348):
```elm
agentPlatformLink : List (Html Message)
agentPlatformLink =
    [ agentNavLink "sidebar-agent-tickets" Routes.AgentTickets "Agent"
    ]
```
  Rationale: the in-page `Agent.Nav` now carries section switching, so the sidebar needs only ONE entry into the agent IA. Land on the ticket queue (the loop's primary surface). The old `Routes.Agent` link must change regardless (arity). Reviews is promoted via the in-page nav (Task 3/6); if W-11 additionally added a dedicated sidebar reviews link, KEEP it here as a second `agentNavLink "sidebar-agent-reviews" (Routes.AgentReviews { teamName = "main" }) "Agent reviews"` rather than deleting it — reconcile with whatever W-11 shipped.

- [ ] Run it, expect PASS:
```
cd web/elm && elm-test tests/AgentPageTests.elm
```
Expected: `TEST RUN PASSED`.

- [ ] Commit:
```
git add web/elm/src/SideBar/SideBar.elm web/elm/tests/AgentPageTests.elm
git commit -m "feat(web): single agent sidebar entry into /agent IA; reviews promoted via sub-nav (S-3 Task 7)"
```

---

## Task 8 — Full suite, bundle regen, final commit

The served UI is `web/public/elm.min.js`; every Elm change above is invisible in the deployed web until the bundle is rebuilt. There is NO elm-build CI gate today (WF-2 adds one), so this step is mandatory and easy to forget.

Files:
- Modify (generated): `web/public/elm.min.js`

Steps:

- [ ] Run the FULL Elm test suite, expect PASS:
```
cd web/elm && elm-test
```
Expected: `TEST RUN PASSED` — Passed count at or above the pre-change baseline (the delivery-outcomes remainder records ~3090 green; the exact number moves with added tests). No failures.

- [ ] Full production compile, expect PASS:
```
cd web/elm && elm make --optimize --output /dev/null src/Main.elm
```
Expected: `Success!` (the `--optimize` flag is what `hack/build-web.sh` uses; catches optimize-only errors like `Debug.*`).

- [ ] Regenerate the embedded bundle:
```
cd /Users/tdmtrader/concourse/concourse && ./hack/build-web.sh
```
Expected: `built web/public/elm.min.js (<N> bytes)`. Requires `elm` 0.19.1 and `uglifyjs` (`npm i -g uglify-js`) on PATH.

- [ ] Manual verification (there is no automated route-render integration test for the running web). Boot the web locally or open the dev server and click through: `/agent/tickets` → `/agent/runs` → `/agent/reviews` → `/agent/workflows` → `/agent/spend` → `/agent/admin`. Confirm for each: (a) the sidebar is present, (b) breadcrumbs read `agent / <section>`, (c) the sub-nav highlights the active tab, (d) browser back/forward moves between sections, (e) `/agent` (bare) lands on runs, (f) `/agent/admin` as a non-admin shows the denied notice. Grep-level pre-check that the built bundle contains the new paths:
```
grep -c "agent/tickets" web/public/elm.min.js
```
Expected: `>= 1` (the path string survives minification).

- [ ] Commit the bundle WITH nothing else outstanding (spine-serialization rule: the bundle is one commit; no parallel Elm sessions):
```
git add web/public/elm.min.js
git commit -m "chore(web): rebuild elm.min.js bundle for agent IA split (S-3 Task 8)"
```

---

## Self-Review

**Spec coverage.** Every S-3 requirement is mapped: real routes `/agent/{tickets,runs,reviews,workflows,spend,admin}` (Task 1); persistent sidebar on each (Task 5 shell keeps `SideBar.view`; Tasks 6 keep it on tickets/reviews); breadcrumbs for each (Task 2 TopBar); mega-page split into per-section modules (Task 4 `Agent/{Runs,Workflows,Spend,Admin,Shared}.elm`); admin-gated `/agent/admin` (Task 4 `UserState.isAdmin` + Task 5 dispatch + Admin gate); orphaned reviews promoted into nav (Task 3 sub-nav "reviews" tab + Task 7 sidebar reconcile with W-11); every `Routes.elm` touchpoint enumerated against the `AgentReviews` template (Task 1: type/parser/sitemap/toString/getGroups/withGroups) plus the downstream exhaustive-`case` sites the audit's "add-a-route six-touchpoint trap" memory warns about (`Views/Styles.elm`, `Views/TopBar.elm`, `SubPage.elm` init+urlUpdate, `Application.routeMatchesModel`). The mandatory bundle rebuild is Task 8.

**Placeholder scan.** No TODO/TBD/"similar to Task N". Verbatim function moves in Task 4 are specified by exact name + current line range rather than re-pasting ~800 unchanged lines — this is a deliberate, unambiguous specification of a mechanical move, not a placeholder; the NEW logic (route parsers, `Agent.Nav`, section field, view dispatch, `isAdmin`, tests) is shown in full.

**Type consistency.** `AgentSection` is exported `AgentSection(..)` so `Views/TopBar.elm`, `Agent/Nav.elm`, `Agent/Agent.elm`, and tests can pattern-match it. `SubPage.genericUpdate` keeps its 13-arg signature (Agent stays one `AgentModel` variant), so no fan-out edit to its callers. `documentTitle` changes from `String` to `AgentSection -> String` with its single caller (`SubPage.view`) updated. The `Agent.Admin` import cycle is explicitly avoided by placing `canMint`/`expiresIsValid`/`mintScopeVocabulary` in `Agent/Shared.elm` and re-signing `canMint` to take raw fields. `routeMatchesModel` gains `Agent _` so intra-agent section switches `urlUpdate` (data-preserving) instead of re-`init`.

**Risk notes.** Task 5 is the largest single edit (shell rewrite + test migration); it may share a compile checkpoint with Task 2's stubs. AgentPageTests migration is mechanical but broad (~35 cases retargeted by section keyword). The reviews route change is low blast-radius (only self-reference + forthcoming W-11 link inbound, per grep).

## Open Decisions

1. **Team backing the team-less `/agent/reviews`.** Reviews are team-scoped server-side (`/api/v1/teams/:team/agent-reviews`), but the flat IA wants a team-less path. **Recommendation:** hardwire `defaultAgentTeam = "main"` (JetBridge is single-agent-team; confirmed by the `concourse-main`/team `main` deployment in memory) and keep the legacy `/teams/:team/agent-reviews` parser as a back-compat alias for other teams' deep links. File a follow-up to add an in-nav team switcher IF multi-team agent reviews ever ship. Owner: product/UX.

2. **Fetch-all vs. fetch-per-section on the Agent shell.** Task 5 keeps the existing behavior (init fetches all seven endpoints; render one section) so section switches are instant against already-loaded data and the 5s/1min poll is unchanged. The cost is that visiting `/agent/runs` still fetches credentials/principals (admin-only; 403 for non-admins, already handled silently). **Recommendation:** ship fetch-all now (zero behavior regression, simplest), and defer per-section fetch to a follow-up only if the admin 403 noise or payload size proves to matter. Owner: whoever picks up S-3.

3. **Landing target of the single sidebar agent link.** Task 7 collapses the two sidebar links to one. **Recommendation:** point it at `/agent/tickets` (the loop's primary surface and the natural entry) with section switching via the in-page sub-nav; keep any dedicated reviews link W-11 added. If ops would rather land on `/agent/runs` (the old `/agent` behavior), that's a one-line change. Owner: product/UX + W-11 author (coordinate to avoid a double reviews link).

4. **Non-admin `/agent/admin` behavior.** Task 5 renders a client-side "not authorized" notice (the server is the real boundary — the credential/principal endpoints are admin-only and 403). **Recommendation:** keep the explicit notice (clearer than an empty page) rather than 404-ing or hiding the tab; optionally hide the "admin" sub-nav tab for non-admins in a follow-up (needs `session.userState` threaded into `Agent.Nav.view`, currently it isn't). Owner: whoever picks up S-3.
