module AgentPlatform.AgentPlatform exposing
    ( Model
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

{-| The agent-platform OPERATIONS console at `/agent`: the workflow catalogue,
the execution ledger, and the operations/admin drawer (costs and credentials).

It used to live in `Agent.Agent`, where the module name claimed the whole agent
domain for one page — every other agent page (tickets, reviews, runs,
snapshots, experiments) is just as much "the agent". The name now says which
page this is.

-}

import AgentBadge
import AgentPage.Chrome as Chrome
import Application.Models exposing (Session)
import Colors
import Concourse.Agent as Agent
import Concourse.Experiment as Experiment
import Concourse.WorkflowRun as WorkflowRun
import DateFormat
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, disabled, href, id, style, title, type_)
import Html.Events exposing (onClick)
import Html.Lazy
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.ScrollDirection as ScrollDirection
import Message.Subscription
    exposing
        ( Delivery
        , Interval(..)
        , Subscription
        )
import Polling
import Routes
import Set exposing (Set)
import Time
import Tooltip
import Views.Prose


type alias Model =
    Login.Model
        { runs : Maybe (List Agent.RunMetric)
        , runsError : Maybe String
        , workflows : Maybe (List Agent.WorkflowSummary)
        , workflowRuns : Dict String (List WorkflowRun.Summary)
        , workflowRunStatusCounts : Dict String (Dict String Int)
        , workflowRunsErrors : Dict String String
        , workflowRunStatusCountsErrors : Dict String String
        , experiments : Maybe (List Experiment.Experiment)
        , experimentsError : Maybe String
        , costByWorkflow : Maybe (Dict String Float)
        , workflowCostsError : Maybe String
        , costRollup : Maybe Agent.CostRollup
        , workflowsError : Maybe String
        , costError : Maybe String
        , credentials : Maybe (List Agent.CredentialStatus)
        , credentialsError : Maybe String
        , platformCredentials : Maybe (List Agent.CredentialStatus)
        , platformCredentialsError : Maybe String
        , platformCredentialsForbidden : Bool
        , expandedRuns : Set String
        }


init : ( Model, List Effect )
init =
    ( { runs = Nothing
      , runsError = Nothing
      , workflows = Nothing
      , workflowRuns = Dict.empty
      , workflowRunStatusCounts = Dict.empty
      , workflowRunsErrors = Dict.empty
      , workflowRunStatusCountsErrors = Dict.empty
      , experiments = Nothing
      , experimentsError = Nothing
      , costByWorkflow = Nothing
      , workflowCostsError = Nothing
      , costRollup = Nothing
      , workflowsError = Nothing
      , costError = Nothing
      , credentials = Nothing
      , credentialsError = Nothing
      , platformCredentials = Nothing
      , platformCredentialsError = Nothing
      , platformCredentialsForbidden = False
      , expandedRuns = Set.empty
      , isUserMenuExpanded = False
      }
    , [ FetchAgentRunMetrics
      , FetchAgentWorkflows
      , FetchAgentCostRollup
      , FetchAgentWorkflowCosts
      , FetchAgentExperiments
      , FetchAgentCredentials
      , FetchAgentPlatformCredentials
      ]
    )


documentTitle : String
documentTitle =
    "Agent"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentRunMetricsFetched (Ok fresh) ->
            ( { model
                | runs =
                    -- Keep the old list when the refetch decoded identical
                    -- data, so the lazy runs table below skips its render.
                    if model.runs == Just fresh then
                        model.runs

                    else
                        Just fresh
                , runsError = Nothing
              }
            , effects
            )

        AgentRunMetricsFetched (Err err) ->
            ( { model | runsError = Just (errorMessage "runs" err) }, effects )

        AgentWorkflowsFetched (Ok workflows) ->
            ( { model | workflows = Just workflows, workflowsError = Nothing }
            , effects
                ++ List.concatMap
                    (\workflow ->
                        [ FetchAgentWorkflowRuns workflow.name
                        , FetchAgentWorkflowRunOperationalStatusCounts workflow.name
                        ]
                    )
                    workflows
            )

        AgentWorkflowsFetched (Err err) ->
            ( { model | workflowsError = Just (errorMessage "workflows" err) }, effects )

        AgentWorkflowRunsFetched workflowName requestQuery (Ok runs) ->
            if requestQuery == [] then
                ( { model
                    | workflowRuns = Dict.insert workflowName runs model.workflowRuns
                    , workflowRunsErrors = Dict.remove workflowName model.workflowRunsErrors
                  }
                , effects
                )

            else
                ( model, effects )

        AgentWorkflowRunsFetched workflowName requestQuery (Err err) ->
            if requestQuery == [] then
                ( { model
                    | workflowRunsErrors =
                        Dict.insert workflowName (errorMessage "workflow runs" err) model.workflowRunsErrors
                  }
                , effects
                )

            else
                ( model, effects )

        AgentWorkflowRunOperationalStatusCountsFetched workflowName (Ok aggregate) ->
            if aggregate.workflowName /= workflowName then
                ( model, effects )

            else
                ( { model
                    | workflowRunStatusCounts =
                        Dict.insert workflowName aggregate.counts model.workflowRunStatusCounts
                    , workflowRunStatusCountsErrors =
                        Dict.remove workflowName model.workflowRunStatusCountsErrors
                  }
                , effects
                )

        AgentWorkflowRunOperationalStatusCountsFetched workflowName (Err err) ->
            ( { model
                | workflowRunStatusCountsErrors =
                    Dict.insert workflowName (errorMessage "workflow run status" err) model.workflowRunStatusCountsErrors
              }
            , effects
            )

        AgentExperimentsFetched (Ok experiments) ->
            ( { model | experiments = Just experiments, experimentsError = Nothing }, effects )

        AgentExperimentsFetched (Err err) ->
            ( { model | experimentsError = Just (errorMessage "experiments" err) }, effects )

        AgentCostRollupFetched (Ok costRollup) ->
            ( { model | costRollup = Just costRollup, costError = Nothing }, effects )

        AgentCostRollupFetched (Err err) ->
            ( { model | costError = Just (errorMessage "costs" err) }, effects )

        AgentWorkflowCostsFetched (Ok costRollup) ->
            if costRollup.groupBy == "workflow" then
                ( { model
                    | costByWorkflow =
                        costRollup.rows
                            |> List.map (\row -> ( row.key, row.costUsd ))
                            |> Dict.fromList
                            |> Just
                    , workflowCostsError = Nothing
                  }
                , effects
                )

            else
                ( { model | workflowCostsError = Just "couldn't load workflow costs" }, effects )

        AgentWorkflowCostsFetched (Err err) ->
            ( { model | workflowCostsError = Just (errorMessage "workflow costs" err) }, effects )

        AgentCredentialsFetched (Ok credentials) ->
            ( { model | credentials = Just credentials, credentialsError = Nothing }, effects )

        AgentCredentialsFetched (Err err) ->
            ( { model | credentialsError = Just (errorMessage "credentials" err) }, effects )

        AgentPlatformCredentialsFetched (Ok credentials) ->
            ( { model
                | platformCredentials = Just credentials
                , platformCredentialsError = Nothing
                , platformCredentialsForbidden = False
              }
            , effects
            )

        AgentPlatformCredentialsFetched (Err err) ->
            case err of
                Http.BadStatus { status } ->
                    if status.code == 403 then
                        -- Non-admins cannot inspect the shared platform slot;
                        -- only this exact authorization result hides it.
                        ( { model
                            | platformCredentials = Nothing
                            , platformCredentialsError = Nothing
                            , platformCredentialsForbidden = True
                          }
                        , effects
                        )

                    else
                        ( { model
                            | platformCredentialsError = Just (errorMessage "platform credentials" err)
                            , platformCredentialsForbidden = False
                          }
                        , effects
                        )

                _ ->
                    ( { model
                        | platformCredentialsError = Just (errorMessage "platform credentials" err)
                        , platformCredentialsForbidden = False
                      }
                    , effects
                    )

        _ ->
            ( model, effects )


{-| Turn an Http.Error into a short human message. A 403 on these admin-only
endpoints is the common case (non-admin users), so it gets a specific note;
everything else falls back to a generic "couldn't load …".
-}
errorMessage : String -> Http.Error -> String
errorMessage what err =
    case err of
        Http.BadStatus { status } ->
            if status.code == 403 then
                "not authorized — the agent " ++ what ++ " API is admin-only"

            else
                "couldn't load " ++ what

        _ ->
            "couldn't load " ++ what


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        AgentSectionNavClicked anchorId ->
            ( model, effects ++ [ Scroll (ScrollDirection.ToId anchorId) agentContentId ] )

        AgentRunExpandToggled rowKey ->
            -- Toggle a single ledger row between its one-line summary and the
            -- full run summary. Keyed by build id + plan id (see runKey): a
            -- build carries one metric row per step, so the build id alone
            -- would toggle sibling rows together, and an
            -- ordinal would jump when the 5s refetch prepends a newer run.
            let
                expanded =
                    if Set.member rowKey model.expandedRuns then
                        Set.remove rowKey model.expandedRuns

                    else
                        Set.insert rowKey model.expandedRuns
            in
            ( { model | expandedRuns = expanded }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


{-| Self-healing refresh. These fetches only replace the fetched data (and
clear their own errors); the mint form and the one-time token box live
outside the poll, so a tick can't wipe them. One minute is plenty: this is
near-static admin data (the cost rollup alone is a 30-day ledger
aggregation), and mutations (mint/revoke/promote) already refetch explicitly.
-}
polls : List (Polling.Poll Model)
polls =
    [ { interval = OneMinute
      , fetch =
            \_ ->
                [ FetchAgentRunMetrics
                , FetchAgentWorkflows
                , FetchAgentCostRollup
                , FetchAgentWorkflowCosts
                , FetchAgentExperiments
                , FetchAgentCredentials
                , FetchAgentPlatformCredentials
                ]
      }
    ]


handleDelivery : Delivery -> ET Model
handleDelivery =
    Polling.handleDelivery polls


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls



-- COLORS / STYLE HELPERS


mutedColor : String
mutedColor =
    "#b0b0b0"


subtleColor : String
subtleColor =
    "#7a7a7a"


rowBorder : String
rowBorder =
    "1px solid " ++ Colors.background


amberColor : String
amberColor =
    "#e0a44e"


{-| Round a dollar amount to two decimals via integer cents so the display
never leaks floating-point noise (e.g. 0.1 + 0.2).
-}
formatUsd : Float -> String
formatUsd amount =
    let
        cents =
            round (amount * 100)

        sign =
            if cents < 0 then
                "-"

            else
                ""

        absCents =
            abs cents

        dollars =
            absCents // 100

        remainder =
            modBy 100 absCents

        fraction =
            if remainder < 10 then
                "0" ++ String.fromInt remainder

            else
                String.fromInt remainder
    in
    sign ++ String.fromInt dollars ++ "." ++ fraction


sectionBlock : String -> String -> List (Html Message) -> Html Message
sectionBlock anchorId title children =
    Html.div
        [ id anchorId, style "margin-top" "24px" ]
        (Html.h2
            [ style "font-size" "15px"
            , style "margin" "0 0 8px 0"
            , style "color" Colors.text
            ]
            [ Html.text title ]
            :: children
        )


mutedLine : String -> Html Message
mutedLine content =
    Html.p
        [ style "color" mutedColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "margin" "4px 0"
        ]
        [ Html.text content ]


errorLine : String -> Html Message
errorLine content =
    Html.p
        [ class "agent-section-error"
        , style "color" amberColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "margin" "4px 0"
        ]
        [ Html.text content ]


{-| A visible warning shown above a section's content when a poll fails after
data has already loaded. Without it a broken refresh is invisible — the stale
data keeps rendering forever with no hint that it stopped updating.
-}
staleDataWarning : Maybe String -> List (Html Message)
staleDataWarning maybeError =
    case maybeError of
        Just message ->
            [ Html.div [ class "agent-section-stale" ]
                [ errorLine ("refresh failed — showing stale data: " ++ message) ]
            ]

        Nothing ->
            []


pill : String -> { bg : String, fg : String } -> String -> Html Message
pill className { bg, fg } labelText =
    Html.span
        [ class className
        , style "margin-left" "8px"
        , style "padding" "1px 8px"
        , style "border-radius" "3px"
        , style "font-size" "11px"
        , style "font-weight" "700"
        , style "background" bg
        , style "color" fg
        ]
        [ Html.text labelText ]



-- VIEW


view : Session -> Model -> Html Message
view session model =
    Chrome.view session
        model
        Routes.Agent
        "Agent workflows"
        "durable functions from typed snapshots to typed snapshots"
        [ sectionNav
        , workflowsSection model
        , runsSection session.timeZone model
        , operationsAdminSection session.timeZone model
        ]



-- SECTION NAV


{-| The shell's scrolling content element (see `AgentPage.Chrome.contentId`):
the section nav jumps by setting its scrollTop via the `scrollToId` port, which
needs a scrolling parent addressable by id.
-}
agentContentId : String
agentContentId =
    Chrome.contentId


{-| A slim in-page nav strip so the long single-column console can be jumped
around without scroll-hunting. Each entry scrolls to a section's `id` via the
`scrollToId` port — a plain `#fragment` href is dead here: `Browser.application`
intercepts every internal link click and re-navigates through `Routes`, which
carries no fragment for this page, so the browser never performs the jump.
-}
sectionNav : Html Message
sectionNav =
    Html.div
        [ class "agent-section-nav"
        , style "display" "flex"
        , style "flex-wrap" "wrap"
        , style "gap" "12px"
        , style "margin" "12px 0 0 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        ]
        (List.map navLink
            [ ( "agent-workflows", "workflows" )
            , ( "agent-runs", "execution ledger" )
            , ( "agent-operations", "operations / admin" )
            , ( "agent-costs", "costs" )
            , ( "agent-credentials", "credentials" )
            ]
        )


navLink : ( String, String ) -> Html Message
navLink ( anchorId, label ) =
    Html.button
        [ onClick (AgentSectionNavClicked anchorId)
        , type_ "button"
        , style "background" "transparent"
        , style "border" "none"
        , style "padding" "0"
        , style "font" "inherit"
        , style "color" "#7a9ac0"
        , style "cursor" "pointer"
        ]
        [ Html.text label ]



-- RUNS SECTION


runsSection : Time.Zone -> Model -> Html Message
runsSection zone model =
    sectionBlock "agent-runs" "Recent runs" <|
        case model.runs of
            Nothing ->
                case model.runsError of
                    Just message ->
                        [ errorLine message ]

                    Nothing ->
                        [ mutedLine "loading…" ]

            Just [] ->
                staleDataWarning model.runsError
                    ++ [ mutedLine "no agent runs recorded yet" ]

            Just runs ->
                staleDataWarning model.runsError
                    ++ [ mutedLine "showing the newest 100 runs (capped server-side, most recent first)"

                       -- Lazy so mint-form keystrokes (which rebuild the whole
                       -- model, and with it this page) stop re-rendering the
                       -- 100-row table; all three arguments are reference-
                       -- stable until the data actually changes.
                       , Html.Lazy.lazy3 runsTable zone model.expandedRuns runs
                       ]


runsTable : Time.Zone -> Set String -> List Agent.RunMetric -> Html Message
runsTable zone expandedRuns runs =
    Html.table
        [ class "agent-runs-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (runsHeaderRow :: List.map (runRow zone expandedRuns) runs)


runsHeaderRow : Html Message
runsHeaderRow =
    Html.tr []
        [ tableHeaderCell "left" "step"
        , tableHeaderCell "left" "workflow"
        , tableHeaderCell "left" "status"
        , tableHeaderCell "right" "cost"
        , tableHeaderCell "right" "tokens (in+out)"
        , tableHeaderCell "right" "turns"
        , tableHeaderCell "left" "run"
        , tableHeaderCell "left" "when (local)"
        ]


{-| Stable identity for a ledger row. (build id, plan id) is the metrics
table's unique key — a build id alone is shared by sibling step rows of the
same build, and a list ordinal changes when the refetch prepends a newer run.
-}
runKey : Agent.RunMetric -> String
runKey r =
    String.fromInt r.buildId ++ ":" ++ r.planId


runRow : Time.Zone -> Set String -> Agent.RunMetric -> Html Message
runRow zone expandedRuns r =
    Html.tr [ class "agent-run-row" ]
        [ runStepCell expandedRuns r
        , tableCell "left" (workflowRef r.workflowName r.workflowVersion)
        , runStatusCell r
        , tableCell "right" ("$" ++ formatUsd r.costUsd)
        , tableCell "right" (String.fromInt r.usage.inputTokens ++ "+" ++ String.fromInt r.usage.outputTokens)
        , tableCell "right" (String.fromInt r.turns)
        , runRefCell r
        , tableCell "left" (formatPosix zone (Just (secondsToPosix r.createdAt)))
        ]


{-| The step name plus its summary underneath it — omitted when the summary is
empty so the row does not carry a blank subtext line. The summary is
click-to-expand: collapsed it is a truncated one-liner; expanded it renders the
full run summary as prose (`AgentRunExpandToggled`, keyed by `runKey` so the
expanded row stays put when a 5s refetch prepends a newer run).
-}
runStepCell : Set String -> Agent.RunMetric -> Html Message
runStepCell expandedRuns r =
    let
        rowKey =
            runKey r

        expanded =
            Set.member rowKey expandedRuns

        summaryBlock =
            if r.summary == "" then
                []

            else if expanded then
                [ Html.div
                    [ class "agent-run-summary-full"
                    , onClick (AgentRunExpandToggled rowKey)
                    , style "max-width" "480px"
                    , style "margin-top" "2px"
                    , style "cursor" "pointer"
                    , title "click to collapse"
                    ]
                    [ Html.Lazy.lazy Views.Prose.view r.summary ]
                ]

            else
                [ Html.div
                    [ class "agent-run-summary"
                    , onClick (AgentRunExpandToggled rowKey)
                    , style "font-size" "11px"
                    , style "color" mutedColor
                    , style "max-width" "320px"
                    , style "white-space" "nowrap"
                    , style "overflow" "hidden"
                    , style "text-overflow" "ellipsis"
                    , style "cursor" "pointer"
                    , title r.summary
                    ]
                    [ Html.text ("▸ " ++ r.summary) ]
                ]
    in
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        , style "vertical-align" "top"
        ]
        (Html.div
            [ style "font-weight" "700", style "color" Colors.text ]
            [ Html.text r.stepName ]
            :: summaryBlock
        )


{-| Render the run's DISPLAY truth as an AgentBadge. The server-derived
`outcome` field carries the fused verdict (build status wins over step status,
U3), so a step that exited "ok" inside a failed build shows Failed, and an
"ok" step that delivered nothing shows "No output" — never a green OK on a
build that did not deliver. Servers that predate the field send no outcome;
the same fusion is then derived locally. Falls back to the raw step status
only when the badge can derive nothing.
-}
runStatusCell : Agent.RunMetric -> Html Message
runStatusCell r =
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ case AgentBadge.displayOutcome { outcome = r.outcome, buildStatus = r.buildStatus, runStatus = r.status, hasResult = r.summary /= "" } of
            Just badgeStatus ->
                AgentBadge.view badgeStatus

            Nothing ->
                Html.text r.status
        ]


{-| "name@version", or just the name when the version is unknown, or "—" when
there is no workflow at all (an ad-hoc / CI run).
-}
workflowRef : String -> Maybe Int -> String
workflowRef name version =
    case ( name, version ) of
        ( "", _ ) ->
            "—"

        ( n, Just v ) ->
            n ++ "@" ++ String.fromInt v

        ( n, Nothing ) ->
            n


{-| A linked "#N function" back to the durable workflow run that produced this
step, or a plain "CI" when the build ran no workflow (an unbound CI invocation,
never joined back to a ticket).
-}
runRefCell : Agent.RunMetric -> Html Message
runRefCell r =
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ case r.workflowRunId of
            Just runId ->
                let
                    label =
                        if r.functionId /= "" then
                            "#" ++ runId ++ " " ++ r.functionId

                        else
                            "#" ++ runId
                in
                if r.workflowName /= "" then
                    Html.a
                        [ href (Routes.toString (Routes.AgentWorkflowRun { workflowName = r.workflowName, id = runId }))
                        , title ("Durable workflow run #" ++ runId)
                        , style "color" "#7a9ac0"
                        , style "text-decoration" "none"
                        ]
                        [ Html.text label ]

                else
                    Html.span [ style "color" subtleColor ] [ Html.text label ]

            Nothing ->
                Html.span
                    [ title "Continuous-integration run — not tied to a durable workflow run"
                    , style "color" subtleColor
                    , style "cursor" "help"
                    ]
                    [ Html.text "CI" ]
        ]


secondsToPosix : Int -> Time.Posix
secondsToPosix seconds =
    Time.millisToPosix (seconds * 1000)



-- WORKFLOWS SECTION


workflowStatusCount : String -> String -> Model -> Int
workflowStatusCount workflowName status model =
    model.workflowRunStatusCounts
        |> Dict.get workflowName
        |> Maybe.andThen (Dict.get status)
        |> Maybe.withDefault 0


workflowsSection : Model -> Html Message
workflowsSection model =
    sectionBlock "agent-workflows" "Workflows" <|
        case model.workflows of
            Nothing ->
                case model.workflowsError of
                    Just message ->
                        [ errorLine message ]

                    Nothing ->
                        [ mutedLine "loading…" ]

            Just [] ->
                staleDataWarning model.workflowsError
                    ++ [ mutedLine "no workflow definitions — import one with: fly agent workflows import" ]

            Just workflows ->
                staleDataWarning model.workflowsError
                    ++ (case model.experiments of
                            Just _ ->
                                staleDataWarning model.experimentsError

                            Nothing ->
                                []
                       )
                    ++ [ Html.div [ class "agent-workflows" ] (List.map (workflowRow model) workflows) ]


workflowRow : Model -> Agent.WorkflowSummary -> Html Message
workflowRow model w =
    let
        maybeRuns =
            Dict.get w.name model.workflowRuns

        runs =
            maybeRuns
                |> Maybe.withDefault []

        maybeStatusCounts =
            Dict.get w.name model.workflowRunStatusCounts

        queued =
            workflowStatusCount w.name "admitting" model

        running =
            workflowStatusCount w.name "running" model
                + workflowStatusCount w.name "canceling" model

        operational =
            List.filter (\run -> run.originKind /= "experiment") runs

        latestStatus =
            case maybeRuns of
                Nothing ->
                    if Dict.member w.name model.workflowRunsErrors then
                        "unavailable"

                    else
                        "loading…"

                Just _ ->
                    operational
                        |> List.head
                        |> Maybe.map .status
                        |> Maybe.withDefault "no operational runs"

        attention =
            workflowStatusCount w.name "failed" model
                + workflowStatusCount w.name "errored" model

        needsAttention =
            maybeStatusCounts /= Nothing && attention > 0

        statusCountsSummary =
            case maybeStatusCounts of
                Nothing ->
                    if Dict.member w.name model.workflowRunStatusCountsErrors then
                        "status counts unavailable"

                    else
                        "status counts loading…"

                Just _ ->
                    String.fromInt queued
                        ++ " queued · "
                        ++ String.fromInt running
                        ++ " running · "
                        ++ String.fromInt attention
                        ++ " attention"

        staleSummary =
            (if maybeRuns /= Nothing && Dict.member w.name model.workflowRunsErrors then
                " · recent runs stale"

             else
                ""
            )
                ++ (if maybeStatusCounts /= Nothing && Dict.member w.name model.workflowRunStatusCountsErrors then
                        " · status counts stale"

                    else
                        ""
                   )

        experimentLabel =
            case model.experiments of
                Nothing ->
                    case model.experimentsError of
                        Just _ ->
                            "unavailable"

                        Nothing ->
                            "loading…"

                Just experiments ->
                    let
                        states =
                            experiments
                                |> List.filter
                                    (\experiment ->
                                        List.any
                                            (\variant -> variant.target.workflowName == w.name)
                                            experiment.definition.variants
                                    )
                                |> List.map (.definition >> .state)
                    in
                    case states of
                        [] ->
                            "no experiments"

                        _ ->
                            String.join ", " states

        costLabel =
            case model.costByWorkflow of
                Nothing ->
                    case model.workflowCostsError of
                        Just _ ->
                            "cost unavailable"

                        Nothing ->
                            "cost loading…"

                Just costs ->
                    "cost $"
                        ++ formatUsd (Dict.get w.name costs |> Maybe.withDefault 0)
                        ++ (case model.workflowCostsError of
                                Just _ ->
                                    " (stale)"

                                Nothing ->
                                    ""
                           )
    in
    Html.div
        [ class "agent-workflow-row"
        , style "display" "flex"
        , style "align-items" "baseline"
        , style "gap" "12px"
        , style "padding" "8px 0"
        , style "border-bottom" rowBorder
        ]
        [ Html.div [ style "flex" "1", style "min-width" "0" ]
            [ Html.div []
                (Html.a
                    [ href (Routes.toString (Routes.AgentWorkflow { name = w.name, query = [] }))
                    , class "agent-workflow-link"
                    , style "font-weight" "700"
                    , style "color" "#7a9ac0"
                    , style "text-decoration" "none"
                    ]
                    [ Html.text w.name ]
                    :: workflowPills w
                )
            , Html.div
                [ style "font-size" "12px", style "color" mutedColor ]
                [ Html.text w.description ]
            , Html.div
                [ class "agent-workflow-annotation"
                , style "font-size" "12px"
                , style "color" subtleColor
                , style "font-style" "italic"
                ]
                [ Html.text w.annotation ]
            , Html.div
                [ class "agent-workflow-signature"
                , style "font-size" "11px"
                , style "font-family" "monospace"
                , style "color" subtleColor
                , style "margin-top" "4px"
                ]
                [ Html.text
                    ("schema v"
                        ++ String.fromInt w.schemaVersion
                        ++ " · signature v"
                        ++ String.fromInt w.signatureVersion
                    )
                ]
            , Html.div
                [ class "agent-workflow-operational-state"
                , style "font-size" "12px"
                , style "color" mutedColor
                , style "margin-top" "4px"
                ]
                ([ Html.text
                    ("latest operational: "
                        ++ latestStatus
                        ++ " · "
                        ++ statusCountsSummary
                        ++ staleSummary
                    )
                 ]
                    ++ (if needsAttention then
                            [ pill "agent-workflow-needs-attention"
                                { bg = "#5a3d24", fg = "#f0c078" }
                                "needs attention"
                            ]

                        else
                            []
                       )
                )
            , Html.div
                [ class "agent-workflow-experiment-state"
                , style "font-size" "11px"
                , style "color" subtleColor
                , style "margin-top" "3px"
                ]
                [ Html.text
                    ("experiments: "
                        ++ experimentLabel
                        ++ " · "
                        ++ costLabel
                    )
                ]
            ]
        , Html.div
            [ style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" subtleColor
            , style "text-align" "right"
            , style "white-space" "nowrap"
            ]
            [ liveVersionLine w
            , Html.div [] [ Html.text ("latest v" ++ String.fromInt w.latestVersion) ]
            , Html.div [] [ Html.text ("#" ++ String.left 12 w.contentHash) ]
            ]
        ]


workflowPills : Agent.WorkflowSummary -> List (Html Message)
workflowPills w =
    let
        deprecatedPill =
            if w.hidden then
                [ pill "agent-workflow-deprecated"
                    { bg = "#5a3d24", fg = "#f0c078" }
                    "deprecated"
                ]

            else
                []

        livePill =
            if w.liveVersion > 0 then
                [ pill "agent-workflow-live"
                    { bg = "#2e4f2e", fg = "#9fdf9f" }
                    "live"
                ]

            else
                []

        candidatePill =
            if w.latestVersion > w.liveVersion then
                [ pill "agent-workflow-candidate"
                    { bg = Colors.background, fg = mutedColor }
                    ("candidate v" ++ String.fromInt w.latestVersion)
                ]

            else
                []
    in
    deprecatedPill ++ livePill ++ candidatePill


liveVersionLine : Agent.WorkflowSummary -> Html Message
liveVersionLine w =
    if w.liveVersion == 0 then
        Html.div [ style "color" subtleColor ] [ Html.text "no live version" ]

    else
        Html.div [] [ Html.text ("v" ++ String.fromInt w.liveVersion ++ " live") ]


operationsAdminSection : Time.Zone -> Model -> Html Message
operationsAdminSection zone model =
    Html.div
        [ id "agent-operations"
        , class "agent-operations-admin"
        , style "margin-top" "32px"
        , style "padding-top" "16px"
        , style "border-top" ("1px solid " ++ Colors.border)
        ]
        [ Html.h2
            [ style "font-size" "16px", style "margin" "0" ]
            [ Html.text "Operations / admin" ]
        , Html.p
            [ style "color" subtleColor, style "font-size" "12px" ]
            [ Html.text "Platform spend and credentials." ]
        , costsSection model
        , credentialsSection zone model
        ]



-- COSTS SECTION


costsSection : Model -> Html Message
costsSection model =
    sectionBlock "agent-costs" "Costs" <|
        case model.costRollup of
            Nothing ->
                case model.costError of
                    Just message ->
                        [ errorLine message ]

                    Nothing ->
                        [ mutedLine "loading…" ]

            Just rollup ->
                staleDataWarning model.costError
                    ++ [ costSummaryLine rollup.summary
                       , dailyCapGauge rollup.summary
                       , costTable rollup.rows
                       ]


costSummaryLine : Agent.CostSummary -> Html Message
costSummaryLine summary =
    let
        spent =
            "today (UTC day): $" ++ formatUsd summary.dailySpentUsd ++ " spent"

        cap =
            if summary.dailyCapUsd > 0 then
                " / $"
                    ++ formatUsd summary.dailyCapUsd
                    ++ " cap ($"
                    ++ formatUsd summary.dailyRemainingUsd
                    ++ " left)"

            else
                ""

        exhausted =
            if summary.dailyExhausted then
                [ Html.span
                    [ class "agent-budget-exhausted"
                    , style "margin-left" "8px"
                    , style "color" amberColor
                    , style "font-weight" "700"
                    ]
                    [ Html.text "budget exhausted" ]
                ]

            else
                []
    in
    Html.div
        [ style "margin" "0 0 12px 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.span [] [ Html.text (spent ++ cap) ] :: exhausted)


{-| A slim gauge of today's spend against the daily cap, mirroring the ticket
budget bar. Rendered only when a cap is configured; turns amber once the cap is
exhausted. Complements — does not replace — the exact-text summary line above.
-}
dailyCapGauge : Agent.CostSummary -> Html Message
dailyCapGauge summary =
    if summary.dailyCapUsd <= 0 then
        -- An uncapped ledger deserves an explicit statement, not silence:
        -- otherwise "no gauge" is indistinguishable from "under the cap".
        Html.div
            [ class "agent-daily-cap-none"
            , style "margin" "0 0 12px 0"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" subtleColor
            ]
            [ Html.text "no daily cap set — spend is unbounded (web flag: --agent-daily-budget-usd)" ]

    else
        let
            pct =
                min 100 (summary.dailySpentUsd / summary.dailyCapUsd * 100)
        in
        Html.div
            [ class "agent-daily-cap-gauge"
            , style "max-width" "320px"
            , style "margin" "0 0 12px 0"
            , style "height" "6px"
            , style "background" "#3d3c3c"
            ]
            [ Html.div
                [ style "height" "6px"
                , style "width" (String.fromFloat pct ++ "%")
                , style "background"
                    (if summary.dailyExhausted then
                        amberColor

                     else
                        "#7aa37a"
                    )
                ]
                []
            ]


costTable : List Agent.CostRow -> Html Message
costTable rows =
    if List.isEmpty rows then
        mutedLine "no cost records yet"

    else
        Html.table
            [ class "agent-costs-table"
            , style "border-collapse" "collapse"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" Colors.text
            ]
            (costHeaderRow :: List.map costRow rows)


costHeaderRow : Html Message
costHeaderRow =
    Html.tr []
        [ tableHeaderCell "left" "day (UTC)"
        , tableHeaderCell "right" "entries"
        , tableHeaderCell "right" "tokens (in+out)"
        , tableHeaderCell "right" "turns"
        , tableHeaderCell "right" "cost"
        ]


costRow : Agent.CostRow -> Html Message
costRow r =
    Html.tr [ class "agent-cost-row" ]
        [ tableCell "left" r.key
        , tableCell "right" (String.fromInt r.entries)
        , tableCell "right" (String.fromInt r.inputTokens ++ "+" ++ String.fromInt r.outputTokens)
        , tableCell "right" (String.fromInt r.turns)
        , tableCell "right" ("$" ++ formatUsd r.costUsd)
        ]



-- SHARED TABLE + TIME HELPERS


tableHeaderCell : String -> String -> Html Message
tableHeaderCell align content =
    Html.th
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "color" mutedColor
        , style "font-weight" "700"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


tableCell : String -> String -> Html Message
tableCell align content =
    Html.td
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


{-| Humanize an optional timestamp as a compact absolute time in the viewer's
own time zone (from `session.timeZone`), e.g. "Jul 18, 2026 14:30", or "—"
when absent. Showing local time is what an operator expects for "which of
today's runs came first"; the minutes matter on an ops console. The
server-aggregated cost buckets stay labelled as UTC days separately.
-}
formatPosix : Time.Zone -> Maybe Time.Posix -> String
formatPosix zone maybe =
    case maybe of
        Nothing ->
            "—"

        Just posix ->
            DateFormat.format
                [ DateFormat.monthNameAbbreviated
                , DateFormat.text " "
                , DateFormat.dayOfMonthNumber
                , DateFormat.text ", "
                , DateFormat.yearNumber
                , DateFormat.text " "
                , DateFormat.hourMilitaryFixed
                , DateFormat.text ":"
                , DateFormat.minuteFixed
                ]
                zone
                posix



-- CREDENTIALS SECTION (read-only status)


credentialsSection : Time.Zone -> Model -> Html Message
credentialsSection zone model =
    -- Two distinct slots, each behind its own labelled sub-header (U17): the
    -- vaulted PLATFORM credential dispatched runs authenticate with, and the
    -- viewer's OWN interactive credential. Keeping them apart stops an empty
    -- personal slot from reading as "the platform auth is missing".
    sectionBlock "agent-credentials" "Credentials" <|
        platformCredentialsBlock zone model
            ++ personalCredentialsBlock zone model


{-| A bold, muted sub-header naming one of the two credential slots so the
platform slot and the personal slot can never be mistaken for one another.
-}
credentialSlotLabel : String -> Html Message
credentialSlotLabel labelText =
    Html.div
        [ style "color" mutedColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "font-weight" "700"
        , style "margin" "10px 0 2px 0"
        ]
        [ Html.text labelText ]


{-| The vaulted platform credential dispatched runs actually authenticate
with. Fetched with `?user=platform` (admin-only; a 403 hides the block), so
the section no longer claims "no credentials stored" while dispatch works.
Rendered under its own "Platform credential" header.
-}
platformCredentialsBlock : Time.Zone -> Model -> List (Html Message)
platformCredentialsBlock zone model =
    if model.platformCredentialsForbidden then
        []

    else
        credentialSlotLabel "Platform credential (used by dispatched runs)"
            :: (case model.platformCredentials of
                    Nothing ->
                        case model.platformCredentialsError of
                            Just message ->
                                [ errorLine message ]

                            Nothing ->
                                [ mutedLine "loading…" ]

                    Just [] ->
                        staleDataWarning model.platformCredentialsError
                            ++ [ errorLine "no platform credential stored — dispatched runs cannot authenticate" ]

                    Just credentials ->
                        staleDataWarning model.platformCredentialsError
                            ++ [ Html.div
                                    [ class "agent-platform-credential"
                                    , style "font-family" "monospace"
                                    , style "font-size" "12px"
                                    , style "color" Colors.text
                                    , style "margin" "0 0 8px 0"
                                    ]
                                    (List.map
                                        (\credential ->
                                            Html.div []
                                                [ Html.text
                                                    (credential.kind
                                                        ++ " (expires "
                                                        ++ formatPosix zone credential.expiresAt
                                                        ++ ") — active"
                                                    )
                                                ]
                                        )
                                        credentials
                                    )
                               ]
               )


{-| The viewer's own credential, set via `fly agent auth`. Kept under its own
header so an empty personal slot reads as "you have no personal credential",
not "the platform auth is missing" (U17).
-}
personalCredentialsBlock : Time.Zone -> Model -> List (Html Message)
personalCredentialsBlock zone model =
    credentialSlotLabel "Your credential (interactive fly login)"
        :: mutedLine "set or rotate with: fly agent auth"
        :: (case model.credentials of
                Nothing ->
                    case model.credentialsError of
                        Just message ->
                            [ errorLine message ]

                        Nothing ->
                            [ mutedLine "loading…" ]

                Just [] ->
                    staleDataWarning model.credentialsError
                        ++ [ mutedLine "no personal credential stored — run: fly agent auth" ]

                Just creds ->
                    staleDataWarning model.credentialsError
                        ++ [ credentialsTable zone creds ]
           )


credentialsTable : Time.Zone -> List Agent.CredentialStatus -> Html Message
credentialsTable zone creds =
    Html.table
        [ class "agent-credentials-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.tr []
            [ tableHeaderCell "left" "kind"
            , tableHeaderCell "left" "expires"
            ]
            :: List.map (credentialRow zone) creds
        )


credentialRow : Time.Zone -> Agent.CredentialStatus -> Html Message
credentialRow zone c =
    Html.tr [ class "agent-credential-row" ]
        [ tableCell "left" c.kind
        , tableCell "left" (formatPosix zone c.expiresAt)
        ]
