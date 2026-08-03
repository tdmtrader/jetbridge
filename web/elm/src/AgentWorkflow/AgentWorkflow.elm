module AgentWorkflow.AgentWorkflow exposing
    ( Model
    , changeRoute
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

{-| The workflow overview.

The page is shape, state, and runs: the promoted revision's semantic graph
beside the chronological run list, and nothing else permanent. There is no
metrics strip, no KPI grid, and no per-node chart — those compete with the
graph for attention and answer questions the reader did not ask. Definition
management lives behind the Start and Versions header actions.

Selecting a node filters the run list and refetches **only** the run list: the
DAG does not change when the list narrows, so refetching it would buy a
visible flicker and nothing else.

Every filter and the open panel live in the URL, defaults omitted. Scroll
position deliberately does not — it is remembered in the model and restored
with `Browser.Dom.setViewportOf` after a refetch, so a shared link carries the
view and not the reader's scrollbar.

The graph may legitimately be missing: `graph_unavailable` says derivation
failed, most likely on a step kind the builder does not recognise. That is a
notice in place of the canvas, not a broken page — the run list and every
other affordance still work.

-}

import AgentGraph.Layout as Layout
import AgentGraph.Model as GraphModel
import AgentGraph.View as GraphView
import AgentPage.Chrome as Chrome
import AgentWorkflow.Filters as Filters
import AgentWorkflow.Panels as Panels
import AgentWorkflow.RunList as RunList
import Application.Models exposing (Session)
import Char
import Concourse.Agent as Agent
import Concourse.Timestamp as Timestamp
import Concourse.WorkflowOverview as Overview
import Concourse.WorkflowRun as WorkflowRun
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, classList, id, placeholder, selected, type_, value)
import Html.Events exposing (onClick, onInput)
import Json.Decode
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.ScrollDirection exposing (ScrollDirection(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Polling
import Routes
import Time
import Tooltip


{-| The DOM id of the scrolling run-list column. Scroll restoration needs a
stable handle, and this is it.
-}
runListId : String
runListId =
    "agent-run-list-scroll"


type alias Model =
    Login.Model
        { workflowName : String
        , filters : Filters.Filters
        , openPanel : Maybe Panels.Panel
        , overview : Maybe Overview.Overview
        , overviewDataQuery : Maybe (List ( String, String ))
        , runs : Maybe (List WorkflowRun.Summary)
        , runsDataQuery : Maybe (List ( String, String ))
        , versions : Maybe (List Agent.WorkflowVersion)
        , selectedVersion : String
        , inputValues : Dict String String
        , scrollTop : Float
        , now : Maybe Time.Posix
        , starting : Bool
        , startError : Bool
        , overviewLoadError : Bool
        , runsLoadError : Bool
        , versionsLoadError : Bool
        , promotionError : Bool
        }


init : { name : String, query : List ( String, String ) } -> ( Model, List Effect )
init { name, query } =
    let
        filters =
            Filters.fromQuery query
    in
    ( { workflowName = name
      , filters = filters
      , openPanel = Panels.fromQuery query
      , overview = Nothing
      , overviewDataQuery = Nothing
      , runs = Nothing
      , runsDataQuery = Nothing
      , versions = Nothing
      , selectedVersion = ""
      , inputValues = Dict.empty
      , scrollTop = 0
      , now = Nothing
      , starting = False
      , startError = False
      , overviewLoadError = False
      , runsLoadError = False
      , versionsLoadError = False
      , promotionError = False
      , isUserMenuExpanded = False
      }
    , fetchAll name filters
    )


fetchAll : String -> Filters.Filters -> List Effect
fetchAll name filters =
    [ FetchAgentWorkflowOverview name (Filters.overviewQuery filters)
    , FetchAgentWorkflowRunsFiltered name (Filters.runsQuery filters)
    , FetchAgentWorkflowVersions name
    ]


documentTitle : Model -> String
documentTitle model =
    model.workflowName ++ " workflow"



-- URL


route : Model -> Routes.Route
route model =
    Routes.AgentWorkflow
        { name = model.workflowName
        , query = Filters.toQueryPairs model.filters ++ Panels.toQueryPairs model.openPanel
        }


{-| Reconcile the page with the URL.

This runs both for back and forward navigation and for the page's own
`ModifyUrl`, so it has to be idempotent: a fetch is emitted only when the
query an endpoint would actually receive changed. Without that, every filter
change would fetch twice.

-}
changeRoute : List ( String, String ) -> ET Model
changeRoute query ( model, effects ) =
    let
        filters =
            Filters.fromQuery query

        overviewChanged =
            Filters.overviewQuery filters /= Filters.overviewQuery model.filters

        runsChanged =
            Filters.runsQuery filters /= Filters.runsQuery model.filters
    in
    ( { model
        | filters = filters
        , openPanel = Panels.fromQuery query
        , overviewLoadError =
            if overviewChanged then
                False

            else
                model.overviewLoadError
        , runsLoadError =
            if runsChanged then
                False

            else
                model.runsLoadError
        , scrollTop =
            if runsChanged then
                0

            else
                model.scrollTop
      }
    , effects
        ++ (if overviewChanged then
                [ FetchAgentWorkflowOverview model.workflowName (Filters.overviewQuery filters) ]

            else
                []
           )
        ++ (if runsChanged then
                [ FetchAgentWorkflowRunsFiltered model.workflowName (Filters.runsQuery filters) ]

            else
                []
           )
    )



-- UPDATE


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentWorkflowOverviewFetched workflowName requestQuery (Ok overview) ->
            if workflowName == model.workflowName && requestQuery == Filters.overviewQuery model.filters then
                ( { model
                    | overview = Just overview
                    , overviewDataQuery = Just requestQuery
                    , overviewLoadError = False
                  }
                , effects
                )

            else
                ( model, effects )

        AgentWorkflowOverviewFetched workflowName requestQuery (Err _) ->
            if workflowName == model.workflowName && requestQuery == Filters.overviewQuery model.filters then
                ( { model | overviewLoadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowVersionsFetched workflowName (Ok versions) ->
            if workflowName /= model.workflowName then
                ( model, effects )

            else
                let
                    preferred =
                        case versions |> List.filter .live |> List.head of
                            Just live ->
                                Just live

                            Nothing ->
                                latestVersion versions

                    current =
                        model.selectedVersion
                            |> String.toInt
                            |> Maybe.andThen
                                (\version ->
                                    versions
                                        |> List.filter (\candidate -> candidate.version == version)
                                        |> List.head
                                )

                    chosen =
                        case current of
                            Just existing ->
                                Just existing

                            Nothing ->
                                preferred

                    selected =
                        chosen
                            |> Maybe.map (.version >> String.fromInt)
                            |> Maybe.withDefault ""

                    inputValues =
                        chosen
                            |> Maybe.map (\version -> seedInputs version.signature model.inputValues)
                            |> Maybe.withDefault model.inputValues
                in
                ( { model
                    | versions = Just versions
                    , selectedVersion = selected
                    , inputValues = inputValues
                    , versionsLoadError = False
                  }
                , effects
                )

        AgentWorkflowVersionsFetched workflowName (Err _) ->
            if workflowName == model.workflowName then
                ( { model | versionsLoadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowRunsFetched workflowName requestQuery (Ok fetched) ->
            if workflowName == model.workflowName && requestQuery == Filters.runsQuery model.filters then
                -- The list re-renders from scratch on every poll, which would
                -- otherwise throw the reader back to the top mid-read.
                ( { model
                    | runs = Just fetched
                    , runsDataQuery = Just requestQuery
                    , runsLoadError = False
                  }
                , effects ++ [ Scroll (ToOffset model.scrollTop) runListId ]
                )

            else
                ( model, effects )

        AgentWorkflowRunsFetched workflowName requestQuery (Err _) ->
            if workflowName == model.workflowName && requestQuery == Filters.runsQuery model.filters then
                ( { model | runsLoadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowRunCreated workflowName (Ok detail) ->
            if workflowName == model.workflowName then
                ( { model | starting = False, startError = False }
                , effects
                    ++ [ NavigateTo
                            (Routes.toString
                                (Routes.AgentWorkflowRun
                                    { workflowName = workflowName
                                    , id = detail.summary.id
                                    }
                                )
                            )
                       ]
                )

            else
                ( model, effects )

        AgentWorkflowRunCreated workflowName (Err _) ->
            if workflowName == model.workflowName then
                ( { model | starting = False, startError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowVersionPromoted workflowName (Ok ()) ->
            if workflowName == model.workflowName then
                ( { model | promotionError = False }
                , effects
                    ++ [ FetchAgentWorkflowVersions workflowName
                       , FetchAgentWorkflowOverview workflowName (Filters.overviewQuery model.filters)
                       , FetchAgentWorkflows
                       ]
                )

            else
                ( model, effects )

        AgentWorkflowVersionPromoted workflowName (Err _) ->
            if workflowName == model.workflowName then
                ( { model | promotionError = True }, effects )

            else
                ( model, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update message ( model, effects ) =
    case message of
        AgentWorkflowNodeSelected nodeId ->
            -- The canvas no longer offers the click for an endpoint, so this
            -- guard is for the message arriving any other way. `?node=input:x`
            -- is URL-addressable and survives sharing, and the run-list filter
            -- is an EXISTS over agent_workflow_run_node_occurrences, which
            -- holds only execution nodes with bare ids. An endpoint id makes
            -- the predicate unsatisfiable rather than merely unmatched, so the
            -- list would report "nothing needs attention" about a population
            -- it never looked at.
            if GraphModel.isEndpointId nodeId then
                ( model, effects )

            else
                applyFilters
                    (\filters ->
                        if filters.selectedNode == Just nodeId then
                            { filters | selectedNode = Nothing, selectedNodeStatus = Nothing }

                        else
                            { filters | selectedNode = Just nodeId, selectedNodeStatus = Nothing }
                    )
                    ( model, effects )

        AgentWorkflowNodeCleared ->
            applyFilters
                (\filters -> { filters | selectedNode = Nothing, selectedNodeStatus = Nothing })
                ( model, effects )

        AgentWorkflowWindowChanged raw ->
            applyFilters
                (\filters -> { filters | window = (Filters.fromQuery [ ( "window", raw ) ]).window })
                ( model, effects )

        AgentWorkflowScopeChanged raw ->
            applyFilters
                (\filters -> { filters | scope = (Filters.fromQuery [ ( "scope", raw ) ]).scope })
                ( model, effects )

        AgentWorkflowStatusFilterChanged raw ->
            applyFilters
                (\filters -> { filters | status = (Filters.fromQuery [ ( "status", raw ) ]).status })
                ( model, effects )

        AgentWorkflowSearchChanged raw ->
            applyFilters (\filters -> { filters | search = raw }) ( model, effects )

        AgentWorkflowPanelOpened raw ->
            navigate { model | openPanel = Panels.fromString raw } effects

        AgentWorkflowPanelClosed ->
            navigate { model | openPanel = Nothing } effects

        AgentWorkflowRunListScrolled offset ->
            ( { model | scrollTop = offset }, effects )

        AgentWorkflowInputChanged portName snapshotId ->
            ( { model | inputValues = Dict.insert portName snapshotId model.inputValues }, effects )

        AgentWorkflowVersionSelected selected ->
            let
                signature =
                    selectedVersion selected model
                        |> Maybe.map .signature
                        |> Maybe.withDefault (Agent.WorkflowSignature [] [])
            in
            ( { model
                | selectedVersion = selected
                , inputValues = seedInputs signature model.inputValues
              }
            , effects
            )

        AgentWorkflowStartClicked ->
            case selectedVersion model.selectedVersion model of
                Just version ->
                    if canStart version.signature model.inputValues && not model.starting then
                        ( { model | starting = True, startError = False }
                        , effects
                            ++ [ CreateAgentWorkflowRun
                                    { workflowName = model.workflowName
                                    , version = Just version.version
                                    , inputs =
                                        version.signature.inputs
                                            |> List.filterMap
                                                (\signaturePort ->
                                                    Dict.get signaturePort.name model.inputValues
                                                        |> Maybe.map String.trim
                                                        |> Maybe.andThen
                                                            (\snapshotId ->
                                                                if snapshotId == "" then
                                                                    Nothing

                                                                else
                                                                    Just ( signaturePort.name, snapshotId )
                                                            )
                                                )
                                    }
                               ]
                        )

                    else
                        ( model, effects )

                Nothing ->
                    ( model, effects )

        AgentWorkflowPromoteClicked version ->
            ( { model | promotionError = False }
            , effects ++ [ PromoteAgentWorkflowVersion model.workflowName version ]
            )

        _ ->
            ( model, effects )


{-| Apply a filter change: refetch what the change actually invalidates, and
push the new URL.

The overview is refetched only when the window moves, because the window is
the only thing the overview endpoint accepts. The run list is refetched only
when the query it would receive changes — which now includes the attention
lens, because the lens is a server-side predicate over the whole population
rather than a narrowing of one returned page.

-}
applyFilters : (Filters.Filters -> Filters.Filters) -> ET Model
applyFilters change ( model, effects ) =
    let
        filters =
            change model.filters

        overviewChanged =
            Filters.overviewQuery filters /= Filters.overviewQuery model.filters

        runsChanged =
            Filters.runsQuery filters /= Filters.runsQuery model.filters
    in
    navigate
        { model
            | filters = filters
            , overviewLoadError =
                if overviewChanged then
                    False

                else
                    model.overviewLoadError
            , runsLoadError =
                if runsChanged then
                    False

                else
                    model.runsLoadError
            , scrollTop =
                if runsChanged then
                    0

                else
                    model.scrollTop
        }
        (effects
            ++ (if overviewChanged then
                    [ FetchAgentWorkflowOverview model.workflowName (Filters.overviewQuery filters) ]

                else
                    []
               )
            ++ (if runsChanged then
                    [ FetchAgentWorkflowRunsFiltered model.workflowName (Filters.runsQuery filters) ]

                else
                    []
               )
        )


navigate : Model -> List Effect -> ( Model, List Effect )
navigate model effects =
    ( model, effects ++ [ ModifyUrl (Routes.toString (route model)) ] )


latestVersion : List Agent.WorkflowVersion -> Maybe Agent.WorkflowVersion
latestVersion versions =
    versions
        |> List.sortBy (negate << .version)
        |> List.head


selectedVersion : String -> Model -> Maybe Agent.WorkflowVersion
selectedVersion raw model =
    raw
        |> String.toInt
        |> Maybe.andThen
            (\version ->
                model.versions
                    |> Maybe.withDefault []
                    |> List.filter (\candidate -> candidate.version == version)
                    |> List.head
            )


seedInputs : Agent.WorkflowSignature -> Dict String String -> Dict String String
seedInputs signature existing =
    signature.inputs
        |> List.foldl
            (\signaturePort values ->
                if Dict.member signaturePort.name values then
                    values

                else
                    Dict.insert signaturePort.name "" values
            )
            existing


canStart : Agent.WorkflowSignature -> Dict String String -> Bool
canStart signature values =
    List.all
        (\signaturePort ->
            case Dict.get signaturePort.name values |> Maybe.map String.trim of
                Just snapshotId ->
                    if snapshotId == "" then
                        signaturePort.optional

                    else
                        canonicalId snapshotId

                Nothing ->
                    signaturePort.optional
        )
        signature.inputs


canonicalId : String -> Bool
canonicalId value =
    case String.uncons value of
        Just ( first, rest ) ->
            first /= '0' && Char.isDigit first && String.all Char.isDigit rest

        Nothing ->
            False



-- POLLING


{-| Two cadences, exactly one of which does anything on a given tick.

A settled page must keep looking, just slowly. Gating the subscription on
`hasActiveRuns` made the gate a one-way latch: nothing outside a poll can flip
it back to True, so the first fetch returning an empty or fully terminal list
stopped the page permanently. A run dispatched afterwards — from a ticket, from
fly, or by the dispatcher — never appeared, including for the reader watching
this page precisely to see the run they just triggered elsewhere.

The gate now lives in `fetch`, which `Polling` explicitly supports (return `[]`
to skip a beat), so it is re-evaluated on every tick instead of deciding once.

-}
polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch =
            \model ->
                if hasActiveRuns model then
                    fetchAll model.workflowName model.filters

                else
                    []
      }
    , { interval = OneMinute
      , fetch =
            \model ->
                if hasActiveRuns model then
                    []

                else
                    fetchAll model.workflowName model.filters
      }
    ]


hasActiveRuns : Model -> Bool
hasActiveRuns model =
    let
        overviewActive =
            model.overview
                |> Maybe.map
                    (.nodeState
                        >> List.any
                            (\state -> state.running > 0 || state.waiting > 0 || state.pending > 0)
                    )
                |> Maybe.withDefault False

        visibleRunActive =
            model.runs
                |> Maybe.map (List.any (\run -> isActiveStatus run.status))
                |> Maybe.withDefault False
    in
    model.overview
        == Nothing
        || overviewActive
        || visibleRunActive
        || filterDataStale model
        || model.overviewLoadError
        || model.runsLoadError
        || model.versionsLoadError


isActiveStatus : String -> Bool
isActiveStatus status =
    List.member status [ "admitting", "running", "canceling" ]


{-| The one-second clock is not a poll: it only moves "now" so relative times
and in-flight durations keep counting. It ticks whether or not anything is
running, because a settled page still says how long ago things happened.
-}
handleDelivery : Delivery -> ET Model
handleDelivery delivery (( model, effects ) as state) =
    case delivery of
        ClockTicked OneSecond time ->
            ( { model | now = Just time }, effects )

        _ ->
            Polling.handleDelivery polls delivery state


subscriptions : Model -> List Subscription
subscriptions _ =
    OnClockTick OneSecond :: Polling.subscriptions polls


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing



-- VIEW


view : Session -> Model -> Html Message
view session model =
    Chrome.view session
        model
        (route model)
        model.workflowName
        "versioned workflow function"
        (List.filterMap identity
            [ filterDataNotice model
            , if currentFilterLoadError model || model.versionsLoadError then
                Just (errorLine "Some workflow data could not be loaded; available durable data is still shown.")

              else
                Nothing
            , Just (headerBar model)
            , Just (filterBar model)
            , Maybe.map (viewPanel session model) model.openPanel
            , Just (body model)
            ]
        )


filterDataNotice : Model -> Maybe (Html Message)
filterDataNotice model =
    if filterDataStale model then
        Just <|
            Html.div [ class "agent-workflow-filter-stale" ]
                [ Html.text
                    (if staleFilterLoadError model then
                        "Current-filter workflow data could not be loaded; previous-filter data remains visible."

                     else
                        "Updating workflow data for the selected filters; previous results remain visible."
                    )
                ]

    else
        Nothing


filterDataStale : Model -> Bool
filterDataStale model =
    overviewDataStale model || runsDataStale model


overviewDataStale : Model -> Bool
overviewDataStale model =
    case ( model.overview, model.overviewDataQuery ) of
        ( Just _, Just dataQuery ) ->
            dataQuery /= Filters.overviewQuery model.filters

        _ ->
            False


runsDataStale : Model -> Bool
runsDataStale model =
    case ( model.runs, model.runsDataQuery ) of
        ( Just _, Just dataQuery ) ->
            dataQuery /= Filters.runsQuery model.filters

        _ ->
            False


staleFilterLoadError : Model -> Bool
staleFilterLoadError model =
    (overviewDataStale model && model.overviewLoadError)
        || (runsDataStale model && model.runsLoadError)


currentFilterLoadError : Model -> Bool
currentFilterLoadError model =
    (model.overviewLoadError && not (overviewDataStale model))
        || (model.runsLoadError && not (runsDataStale model))


headerBar : Model -> Html Message
headerBar model =
    Html.div [ class "agent-workflow-header-bar" ]
        [ revisionIndicator model
        , windowControl model
        , Html.div [ class "agent-workflow-header-actions" ]
            [ Html.button
                [ type_ "button"
                , id "agent-workflow-start"
                , onClick (AgentWorkflowPanelOpened (Panels.toString Panels.Start))
                ]
                [ Html.text "Start" ]
            , Html.button
                [ type_ "button"
                , id "agent-workflow-versions"
                , onClick (AgentWorkflowPanelOpened (Panels.toString Panels.Versions))
                ]
                [ Html.text "Versions" ]
            ]
        ]


{-| What revision the canvas is showing, and — when nothing is promoted — that
it is showing the latest import instead. Rendering the latest import silently
would be a lie about what is live.
-}
revisionIndicator : Model -> Html Message
revisionIndicator model =
    Html.div [ class "agent-workflow-revision-indicator" ]
        [ Html.text
            (case model.overview of
                Nothing ->
                    "loading revision…"

                Just overview ->
                    if overview.workflow.hasPromotedVersion then
                        "version "
                            ++ String.fromInt overview.workflow.graphVersion
                            ++ " · #"
                            ++ String.left 12 overview.workflow.contentHash

                    else
                        "not promoted — showing version "
                            ++ String.fromInt overview.workflow.graphVersion
            )
        ]


windowControl : Model -> Html Message
windowControl model =
    Html.label [ class "agent-workflow-window" ]
        [ Html.text "window "
        , Html.select [ onInput AgentWorkflowWindowChanged ]
            ([ Filters.TwentyFourHours, Filters.SevenDays, Filters.ThirtyDays ]
                |> List.map
                    (\window ->
                        Html.option
                            [ value (Filters.windowParam window)
                            , selected (window == model.filters.window)
                            ]
                            [ Html.text (Filters.windowParam window) ]
                    )
            )
        ]


filterBar : Model -> Html Message
filterBar model =
    Html.div [ class "agent-workflow-filter-bar" ]
        [ Html.input
            [ class "agent-workflow-search"
            , placeholder "run ID, ticket, or snapshot"
            , value model.filters.search
            , onInput AgentWorkflowSearchChanged
            ]
            []
        , Html.div [ class "agent-workflow-lens" ]
            [ lensChoice model Filters.Attention "attention"
            , lensChoice model Filters.Active "active"
            , lensChoice model Filters.All "all"
            , scopeChoice model
            ]
        , selectionChip model
        ]


lensChoice : Model -> Filters.Status -> String -> Html Message
lensChoice model status label =
    Html.button
        [ type_ "button"
        , classList
            [ ( "agent-workflow-lens-choice", True )
            , ( "agent-workflow-lens-choice--current", model.filters.status == status )
            ]
        , onClick (AgentWorkflowStatusFilterChanged (Filters.statusParam status))
        ]
        [ Html.text label ]


{-| Experiments are a scope, not a state: the server classifies them as a
separate population precisely so they cannot distort operational evaluation.
The choice sits beside the lens choices because that is where a reader looks
for "show me the other ones".
-}
scopeChoice : Model -> Html Message
scopeChoice model =
    let
        showing =
            model.filters.scope == Filters.Experiment
    in
    Html.button
        [ type_ "button"
        , classList
            [ ( "agent-workflow-lens-choice", True )
            , ( "agent-workflow-lens-choice--current", showing )
            ]
        , onClick
            (AgentWorkflowScopeChanged
                (Filters.scopeParam
                    (if showing then
                        Filters.Operational

                     else
                        Filters.Experiment
                    )
                )
            )
        ]
        [ Html.text "experiments" ]


selectionChip : Model -> Html Message
selectionChip model =
    case model.filters.selectedNode of
        Nothing ->
            Html.text ""

        Just nodeId ->
            Html.button
                [ type_ "button"
                , class "agent-workflow-selection-chip"
                , onClick AgentWorkflowNodeCleared
                ]
                [ Html.text (nodeId ++ " ✕") ]


viewPanel : Session -> Model -> Panels.Panel -> Html Message
viewPanel session model panel =
    case panel of
        Panels.Start ->
            Panels.viewStart
                { versions = model.versions
                , versionsLoadError = model.versionsLoadError
                , selectedVersion = selectedVersion model.selectedVersion model
                , selectedVersionRaw = model.selectedVersion
                , inputValues = model.inputValues
                , canStart =
                    selectedVersion model.selectedVersion model
                        |> Maybe.map (\version -> canStart version.signature model.inputValues)
                        |> Maybe.withDefault False
                , starting = model.starting
                , startError = model.startError
                }

        Panels.Versions ->
            Panels.viewVersions
                { zone = session.timeZone
                , versions = model.versions
                , versionsLoadError = model.versionsLoadError
                , promotionError = model.promotionError
                , hasPromotedVersion =
                    model.overview
                        |> Maybe.map (.workflow >> .hasPromotedVersion)
                        |> Maybe.withDefault True
                , graphVersion =
                    model.overview
                        |> Maybe.map (.workflow >> .graphVersion)
                        |> Maybe.withDefault 0
                , hasHistoricalOnlyNodes =
                    model.overview
                        |> Maybe.map .hasHistoricalOnlyNodes
                        |> Maybe.withDefault False
                }


body : Model -> Html Message
body model =
    Html.div [ class "agent-workflow-body" ]
        [ Html.div [ class "agent-workflow-canvas" ] [ canvas model ]
        , Html.div
            [ class "agent-workflow-runs"
            , id runListId
            , Html.Events.on "scroll" scrollTopDecoder
            ]
            [ runList model ]
        ]


scrollTopDecoder : Json.Decode.Decoder Message
scrollTopDecoder =
    Json.Decode.at [ "target", "scrollTop" ] Json.Decode.float
        |> Json.Decode.map AgentWorkflowRunListScrolled


{-| The canvas has four honest states. Collapsing any of them would tell the
reader something untrue: "no versions" is not "no nodes", and neither is
"could not derive the graph".
-}
canvas : Model -> Html Message
canvas model =
    case model.overview of
        Nothing ->
            if model.versions == Just [] then
                Html.div [ class "agent-workflow-no-versions" ]
                    [ Html.text "This workflow has no imported versions yet. Import one with fly to give it a graph." ]

            else
                note "loading workflow graph…"

        Just overview ->
            if overview.graphUnavailable then
                Html.div [ class "agent-graph-unavailable" ]
                    [ Html.text
                        ("The graph for version "
                            ++ String.fromInt overview.workflow.graphVersion
                            ++ " could not be derived, most likely from a step kind the builder does not recognise. Everything else on this page is unaffected."
                        )
                    ]

            else if List.isEmpty overview.graph.nodes then
                Html.div [ class "agent-graph-empty" ]
                    [ Html.text "This revision has no nodes to draw." ]

            else
                GraphView.view
                    { selected = model.filters.selectedNode
                    , nodeState = Overview.nodeStateLookup overview
                    , onSelect = AgentWorkflowNodeSelected
                    }
                    (Layout.layout overview.graph)


runList : Model -> Html Message
runList model =
    case model.runs of
        Nothing ->
            note "loading runs…"

        Just summaries ->
            RunList.view
                { now = Maybe.withDefault (Time.millisToPosix 0) model.now
                , emptyMessage = emptyMessage model.filters.status
                }
                (List.map (runRow model.workflowName) summaries)


{-| "Nothing ran" and "nothing matches this lens" are different facts and get
different sentences: a reader who sees "No runs" while a lens is on concludes
the workflow is idle.

The distinction now reports a server answer rather than a client one. The rows
are whatever the lens returned, so an empty list under `all` really is an empty
window, and an empty list under `attention` really is nothing unresolved —
across the whole population, not just the newest page.

-}
emptyMessage : Filters.Status -> String
emptyMessage status =
    case status of
        Filters.Attention ->
            "No runs need attention in this window"

        Filters.Active ->
            "No runs are active"

        Filters.All ->
            "No runs in this window"


runRow : String -> WorkflowRun.Summary -> RunList.Row
runRow workflowName summary =
    { id = summary.id
    , url =
        Routes.toString
            (Routes.AgentWorkflowRun { workflowName = workflowName, id = summary.id })
    , status = WorkflowRun.effectiveState summary
    , workflowVersion = summary.workflowVersion
    , startedAt = summary.startedAt |> Maybe.andThen Timestamp.fromIso8601
    , completedAt = summary.completedAt |> Maybe.andThen Timestamp.fromIso8601
    , ticketReference = summary.ticketReference
    , attentionCue = attentionCue summary
    }


{-| The cue is derived from what a run summary actually carries. Richer cues —
"waiting at approval" — need the wait projection, which this endpoint does not
return; inventing one from status alone would be a guess presented as a fact.

It reads `WorkflowRun.effectiveState`, which resolves the durable status
against the execution status by worst-truth-wins. A durably failed run whose
build exited zero is exactly the run this cue exists for, and the previous
"terminal execution wins" rule silenced it.
-}
attentionCue : WorkflowRun.Summary -> String
attentionCue summary =
    case WorkflowRun.effectiveState summary of
        "failed" ->
            "failed"

        "errored" ->
            "errored"

        _ ->
            ""


note : String -> Html Message
note message =
    Html.p [ class "agent-workflow-note" ] [ Html.text message ]


errorLine : String -> Html Message
errorLine message =
    Html.p [ class "agent-page-error" ] [ Html.text message ]
