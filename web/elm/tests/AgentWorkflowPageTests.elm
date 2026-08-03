module AgentWorkflowPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common exposing (initCustomOpts)
import Expect
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Message.ScrollDirection exposing (ScrollDirection(..))
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Routes
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, text)
import Time


all : Test
all =
    describe "workflow overview"
        [ describe "the untouched page is shape, state, and runs"
            [ test "renders the graph beside the run list by default" <|
                \_ ->
                    initializedWithOverview
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ class "agent-graph" ]
                            , Query.has [ class "agent-run-list" ]
                            , Query.hasNot [ class "agent-graph-unavailable" ]
                            ]
            , test "has no permanent metrics strip on the untouched page" <|
                \_ ->
                    initializedWithOverview
                        |> Common.queryView
                        |> Query.hasNot [ class "agent-workflow-summary-strip" ]
            , test "a shared link arrives with its filters and panel already applied" <|
                \_ ->
                    Common.initCustom
                        { initCustomOpts | query = Just "window=30d&panel=versions" }
                        "/agent/workflows/review-api"
                        |> Application.handleCallback
                            (Callback.AgentWorkflowVersionsFetched "review-api"
                                (Ok [ AgenticData.workflowVersion ])
                            )
                        |> Tuple.first
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowStatusFilterChanged "all"))
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.ModifyUrl
                                    "/agent/workflows/review-api?window=30d&status=all&panel=versions"
                                )
                            , Common.notContains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "7d" ) ]
                                )
                            ]
            , test "keeps the agent product identity in the global breadcrumb" <|
                \_ ->
                    initializedWithOverview
                        |> Common.queryView
                        |> Query.find [ id "breadcrumbs" ]
                        |> Query.has
                            [ containing [ text "agent" ]
                            , containing [ text "review-api" ]
                            ]
            ]
        , describe "definition management stays behind header actions"
            [ test "offers Start and Versions and nothing else on the canvas" <|
                \_ ->
                    initializedWithOverview
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ id "agent-workflow-start" ]
                            , Query.has [ id "agent-workflow-versions" ]
                            , Query.hasNot [ class "agent-workflow-version-timeline" ]
                            , Query.hasNot [ class "agent-workflow-start-form" ]
                            ]
            , test "opens the versions panel on demand" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowPanelOpened "versions"))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.has [ class "agent-workflow-version-timeline" ]
            , test "opens the start panel with the frozen typed signature" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowPanelOpened "start"))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-workflow-signature-detail" ]
                        |> Query.has
                            [ containing [ text "repository : repository/v1" ]
                            , containing [ text "review : review/v1" ]
                            , containing [ text "schema v3 · signature v1 · #abcdef0123456789" ]
                            ]
            , test "an open panel is linkable, so it goes in the URL" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowPanelOpened "versions"))
                        |> Tuple.second
                        |> Common.contains
                            (Effects.ModifyUrl "/agent/workflows/review-api?panel=versions")
            , test "an unrecognised panel name opens nothing rather than failing" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowPanelOpened "sideways"))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.hasNot [ class "agent-workflow-panel" ]
            , test "surfaces removed nodes in the versions panel, never as a union graph" <|
                \_ ->
                    initializedWith
                        { overview | hasHistoricalOnlyNodes = True }
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowPanelOpened "versions"))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-workflow-historical-only" ]
                        |> Query.has [ text "no longer contains" ]
            , test "starts a run from validated snapshot bindings and a pinned definition version" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update <|
                                Message.AgentWorkflowInputChanged "repository" "9007199254740995"
                            )
                        |> Tuple.first
                        |> Application.update (Msgs.Update Message.AgentWorkflowStartClicked)
                        |> Tuple.second
                        |> Common.contains
                            (Effects.CreateAgentWorkflowRun
                                { workflowName = "review-api"
                                , version = Just 3
                                , inputs = [ ( "repository", "9007199254740995" ) ]
                                }
                            )
            , test "promotion is explicit and version-scoped" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update <| Message.AgentWorkflowPromoteClicked 3)
                        |> Tuple.second
                        |> Common.contains (Effects.PromoteAgentWorkflowVersion "review-api" 3)
            ]
        , describe "node selection couples the graph to the list"
            [ test "selecting a node refetches the run list filtered to that node" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
                        |> Tuple.second
                        |> Common.contains
                            (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                [ ( "window", "7d" )
                                , ( "scope", "operational" )
                                , ( "lens", "attention" )
                                , ( "node", "implement" )
                                ]
                            )
            , test "selecting a node does not refetch the DAG, which did not change" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
                        |> Tuple.second
                        |> Common.notContains
                            (Effects.FetchAgentWorkflowOverview "review-api" [ ( "window", "7d" ) ])
            , test "the selection is in the URL, so it can be shared" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
                        |> Tuple.second
                        |> Common.contains
                            (Effects.ModifyUrl "/agent/workflows/review-api?node=implement")
            , test "clearing the selection restores the full list" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowNodeSelected "implement"))
                        |> Tuple.first
                        |> Application.update (Msgs.Update Message.AgentWorkflowNodeCleared)
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                                )
                            , Common.contains (Effects.ModifyUrl "/agent/workflows/review-api")
                            ]
            , test "the window moves both the graph's aggregation and the list" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowWindowChanged "30d"))
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "30d" ) ]
                                )
                            , Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "30d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                                )
                            ]
            , test "the attention lens is a server request, not a page filter" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowStatusFilterChanged "all"))
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "all" ) ]
                                )
                            , Common.contains
                                (Effects.ModifyUrl "/agent/workflows/review-api?status=all")
                            ]
            , test "experiments are a scope choice, not a state" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowScopeChanged "experiment"))
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" ), ( "scope", "experiment" ), ( "lens", "attention" ) ]
                                )
                            , Common.contains
                                (Effects.ModifyUrl "/agent/workflows/review-api?scope=experiment")
                            ]
            , test "arriving back on a node-filtered URL refetches only the list" <|
                \_ ->
                    initializedWithOverview
                        |> navigateTo [ ( "node", "implement" ) ]
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" )
                                    , ( "scope", "operational" )
                                    , ( "lens", "attention" )
                                    , ( "node", "implement" )
                                    ]
                                )
                            , Common.notContains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "7d" ) ]
                                )
                            ]
            , test "arriving back on a different window refetches both" <|
                \_ ->
                    initializedWithOverview
                        |> navigateTo [ ( "window", "30d" ) ]
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "30d" ) ]
                                )
                            , Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "30d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                                )
                            ]
            , test "opening a panel through the URL alone costs no request" <|
                \_ ->
                    initializedWithOverview
                        |> navigateTo [ ( "panel", "versions" ) ]
                        |> Tuple.second
                        |> Expect.all
                            [ Common.notContains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "7d" ) ]
                                )
                            , Common.notContains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                                )
                            ]
            , test "the back button opens the panel the URL names" <|
                \_ ->
                    initializedWithOverview
                        |> navigateTo [ ( "panel", "versions" ) ]
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.has [ class "agent-workflow-version-timeline" ]
            , test "a filter change reconciles the page instead of rebuilding it" <|
                \_ ->
                    initializedWithOverview
                        |> navigateTo [ ( "node", "implement" ) ]
                        |> Tuple.first
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ class "agent-graph" ]
                            , Query.has [ class "agent-run-row" ]
                            ]
            ]
        , describe "revision and degraded states"
            [ test "labels an unpromoted workflow instead of rendering an empty canvas" <|
                \_ ->
                    initializedWith unpromotedOverview
                        |> Common.queryView
                        |> Query.find [ class "agent-workflow-revision-indicator" ]
                        |> Query.has [ text "not promoted" ]
            , test "names the promoted revision and its hash when there is one" <|
                \_ ->
                    initializedWithOverview
                        |> Common.queryView
                        |> Query.find [ class "agent-workflow-revision-indicator" ]
                        |> Query.has [ text "version 3 · #abcdef012345" ]
            , test "an unpromoted workflow still draws its latest imported graph" <|
                \_ ->
                    initializedWith unpromotedOverview
                        |> Common.queryView
                        |> Query.has [ class "agent-graph" ]
            , test "a workflow whose graph could not be derived still lists its runs" <|
                \_ ->
                    initializedWith unavailableOverview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ failedRunSummary ]))
                        |> Tuple.first
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ class "agent-graph-unavailable" ]
                            , Query.hasNot [ class "agent-graph" ]
                            , Query.has [ class "agent-run-list" ]
                            , Query.has [ id "agent-workflow-start" ]
                            , Query.has [ id "agent-workflow-versions" ]
                            ]
            ]
        , describe "the run list"
            [ test "links exact durable run IDs and labels operational state" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowStatusFilterChanged "all"))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.findAll [ class "agent-run-row" ]
                        |> Query.index 0
                        |> Query.has
                            [ attribute
                                (Attr.href "/agent/workflows/review-api/runs/9007199254740993")
                            , containing [ text "running" ]
                            ]
            , test "shows the durable ticket reference instead of the direct origin reference" <|
                \_ ->
                    let
                        summary =
                            AgenticData.runSummary
                    in
                    initializedWithRun
                        { summary
                            | originKind = "ticket"
                            , originReference = "42"
                            , ticketId = Just 42
                            , ticketReference = "ticket-42"
                        }
                        |> Common.queryView
                        |> Query.find [ class "agent-run-row-ticket" ]
                        |> Query.has [ text "ticket-42" ]
            , test "shows an inherited ticket reference on a retry row" <|
                \_ ->
                    let
                        summary =
                            AgenticData.runSummary
                    in
                    initializedWithRun
                        { summary
                            | originKind = "retry"
                            , originReference = "9007199254740991"
                            , retryOf = Just "9007199254740991"
                            , ticketId = Just 42
                            , ticketReference = "ticket-42"
                        }
                        |> Common.queryView
                        |> Query.find [ class "agent-run-row-ticket" ]
                        |> Query.has [ text "ticket-42" ]
            , test "the empty attention list says nothing is unresolved, not that nothing ran" <|
                \_ ->
                    initializedWith overview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok []))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-list-empty" ]
                        |> Query.has [ text "No runs need attention in this window" ]
            , test "renders every row the lens returned rather than narrowing it again" <|
                \_ ->
                    initializedWith overview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ terminalRunSummary ]))
                        |> Tuple.first
                        |> Common.queryView
                        |> Expect.all
                            [ Query.has [ class "agent-run-row" ]
                            , Query.hasNot [ class "agent-run-list-empty" ]
                            ]
            , test "a failed run survives the attention lens" <|
                \_ ->
                    initializedWith overview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ failedRunSummary ]))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-row-attention" ]
                        |> Query.has [ text "failed" ]
            , test "the empty active list names the active lens" <|
                \_ ->
                    initializedWith overview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowStatusFilterChanged "active"))
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok []))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-list-empty" ]
                        |> Query.has [ text "No runs are active" ]
            , test "the active lens keeps a run that is still going" <|
                \_ ->
                    initializedWith overview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api"
                                (Ok [ AgenticData.runSummary ])
                            )
                        |> Tuple.first
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowStatusFilterChanged "active"))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.has [ class "agent-run-row" ]
            , test "distinguishes an empty window from an empty lens" <|
                \_ ->
                    initializedWith overview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowStatusFilterChanged "all"))
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok []))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-list-empty" ]
                        |> Query.has [ text "No runs in this window" ]
            , test "a run that ended around a failed execution does not read as green" <|
                \_ ->
                    initializedWith overview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api"
                                (Ok [ succeededButFailedRunSummary ])
                            )
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.find [ class "agent-run-row-status" ]
                        |> Query.has [ text "failed" ]
            , test "restores the remembered scroll position after a refetch" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.Update (Message.AgentWorkflowRunListScrolled 420))
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api"
                                (Ok [ AgenticData.runSummary ])
                            )
                        |> Tuple.second
                        |> Common.contains
                            (Effects.Scroll (ToOffset 420) "agent-run-list-scroll")
            ]
        , describe "polling"
            [ test "refreshes a workflow with active runs on the bounded five second cadence" <|
                \_ ->
                    initializedWithOverview
                        |> Application.update
                            (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                        |> Tuple.second
                        |> Expect.all
                            [ Common.contains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "7d" ) ]
                                )
                            , Common.contains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                                )
                            ]
            , test "removes the workflow refresh timer after all visible runs settle" <|
                \_ ->
                    initializedWith overview
                        |> Application.handleCallback
                            (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ terminalRunSummary ]))
                        |> Tuple.first
                        |> Application.update
                            (Msgs.DeliveryReceived (ClockTicked FiveSeconds (Time.millisToPosix 0)))
                        |> Tuple.second
                        |> Expect.all
                            [ Common.notContains
                                (Effects.FetchAgentWorkflowOverview "review-api"
                                    [ ( "window", "7d" ) ]
                                )
                            , Common.notContains
                                (Effects.FetchAgentWorkflowRunsFiltered "review-api"
                                    [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
                                )
                            ]
            ]
        ]


overview =
    AgenticData.workflowOverview


unpromotedOverview =
    let
        workflow =
            overview.workflow
    in
    { overview | workflow = { workflow | hasPromotedVersion = False, graphVersion = 4 } }


unavailableOverview =
    { overview | graphUnavailable = True, graph = { nodes = [], edges = [] } }


routeWithQuery query =
    Routes.AgentWorkflow { name = "review-api", query = query }


{-| What the browser does on back, forward, or a shared link: a route change
with no page-side message behind it.
-}
navigateTo query model =
    Application.handleDelivery (RouteChanged (routeWithQuery query)) model


initializedWith fixture =
    Common.init "/agent/workflows/review-api"
        |> Application.handleCallback
            (Callback.AgentWorkflowVersionsFetched "review-api" (Ok [ AgenticData.workflowVersion ]))
        |> Tuple.first
        |> Application.handleCallback
            (Callback.AgentWorkflowOverviewFetched "review-api" (Ok fixture))
        |> Tuple.first


{-| The default page. The attention lens is a server predicate now, so every
row the list returned is rendered; the fixture carries one running run and one
failed run because that is what the lens would have returned.
-}
initializedWithOverview : Application.Model
initializedWithOverview =
    initializedWith overview
        |> Application.handleCallback
            (Callback.AgentWorkflowRunsFetched "review-api"
                (Ok [ AgenticData.runSummary, failedRunSummary ])
            )
        |> Tuple.first


initializedWithRun summary =
    initializedWith overview
        |> Application.handleCallback
            (Callback.AgentWorkflowRunsFetched "review-api" (Ok [ summary ]))
        |> Tuple.first


terminalRunSummary =
    let
        summary =
            AgenticData.runSummary
    in
    { summary | status = "succeeded", executionStatus = Just "succeeded" }


failedRunSummary =
    let
        summary =
            AgenticData.runSummary
    in
    { summary | status = "failed", executionStatus = Just "failed" }


succeededButFailedRunSummary =
    let
        summary =
            AgenticData.runSummary
    in
    { summary | status = "succeeded", executionStatus = Just "failed" }
