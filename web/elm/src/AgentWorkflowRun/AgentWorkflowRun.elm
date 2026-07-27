module AgentWorkflowRun.AgentWorkflowRun exposing
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

import AgentPage.Chrome as Chrome
import AgentSnapshot.RepositoryChange as RepositoryChange
import Application.Models exposing (Session)
import Build.AgentReview as AgentReviewView
import Concourse.Agent as Agent
import Concourse.AgentReview exposing (BuildReview)
import Concourse.Snapshot as Snapshot
import Concourse.Transcript as Transcript
import Concourse.WorkflowRun as WorkflowRun
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, disabled, href, placeholder, style, type_, value)
import Html.Events exposing (onClick, onInput)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription)
import Polling
import Routes
import Set exposing (Set)
import Tooltip
import UserState exposing (UserState(..))


type alias Model =
    Login.Model
        { workflowName : String
        , workflowRunId : String
        , detail : Maybe WorkflowRun.Detail
        , waits : List WorkflowRun.Wait
        , outcomes : List WorkflowRun.Outcome
        , repositoryChanges : Dict String WorkflowRun.RepositoryChange
        , metrics : List Agent.RunMetric
        , transcripts : List Transcript.Ref
        , transcriptEntries : Dict String (List Transcript.Entry)
        , expandedTranscripts : Set String
        , expandedTranscriptEntries : Set String
        , transcriptErrors : Set String
        , transcriptIndexError : Bool
        , answerSnapshots : Dict String String
        , loadError : Bool
        , actionError : Bool
        , agentReviews : List BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Maybe Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
        , expandedDescriptions : Set String
        , terminalProjectionPollsRemaining : Int
        }


init : { workflowName : String, id : String } -> ( Model, List Effect )
init { workflowName, id } =
    ( { workflowName = workflowName
      , workflowRunId = id
      , detail = Nothing
      , waits = []
      , outcomes = []
      , repositoryChanges = Dict.empty
      , metrics = []
      , transcripts = []
      , transcriptEntries = Dict.empty
      , expandedTranscripts = Set.empty
      , expandedTranscriptEntries = Set.empty
      , transcriptErrors = Set.empty
      , transcriptIndexError = False
      , answerSnapshots = Dict.empty
      , loadError = False
      , actionError = False
      , agentReviews = []
      , agentReviewLoadError = False
      , agentReviewPanelExpanded = True
      , expandedFindings = Set.empty
      , showObservations = Nothing
      , agentReviewNotes = Dict.empty
      , verdictErrors = Set.empty
      , expandedDescriptions = Set.empty
      , terminalProjectionPollsRemaining = terminalProjectionPollLimit
      , isUserMenuExpanded = False
      }
    , fetchAll workflowName id
    )


fetchAll : String -> String -> List Effect
fetchAll workflowName id =
    [ FetchAgentWorkflowRun workflowName id
    , FetchAgentWorkflowWaits workflowName id
    , FetchAgentWorkflowOutcomes workflowName id
    , FetchAgentWorkflowReviews workflowName id
    , FetchAgentWorkflowRunMetrics workflowName id
    , FetchAgentWorkflowRunTranscripts workflowName id
    ]


documentTitle : Model -> String
documentTitle model =
    model.workflowName ++ " run " ++ model.workflowRunId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentWorkflowRunFetched runId (Ok detail) ->
            if runId == model.workflowRunId then
                ( { model | detail = Just detail, loadError = False }
                , effects
                    ++ (detail.outputs
                            |> List.filter (\output -> output.snapshot.typeRef == "repository-change/v1")
                            |> List.map
                                (\output ->
                                    FetchAgentSnapshotRepositoryChange "main" output.snapshot.id
                                )
                       )
                )

            else
                ( model, effects )

        AgentWorkflowRunFetched runId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowWaitsFetched runId (Ok waits) ->
            if runId == model.workflowRunId then
                ( { model | waits = waits }, effects )

            else
                ( model, effects )

        AgentWorkflowWaitsFetched runId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowWaitResolved _ (Ok _) ->
            ( { model | actionError = False }
            , effects ++ [ FetchAgentWorkflowWaits model.workflowName model.workflowRunId ]
            )

        AgentWorkflowWaitResolved _ (Err _) ->
            ( { model | actionError = True }, effects )

        AgentWorkflowOutcomesFetched runId (Ok outcomes) ->
            if runId == model.workflowRunId then
                ( { model | outcomes = outcomes }, effects )

            else
                ( model, effects )

        AgentWorkflowOutcomesFetched runId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowReviewsFetched runId (Ok reviews) ->
            if runId == model.workflowRunId then
                ( { model | agentReviews = reviews, agentReviewLoadError = False }, effects )

            else
                ( model, effects )

        AgentWorkflowReviewsFetched runId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | agentReviewLoadError = True }, effects )

            else
                ( model, effects )

        AgentSnapshotRepositoryChangeFetched snapshotId (Ok projection) ->
            ( { model
                | repositoryChanges = Dict.insert snapshotId projection model.repositoryChanges
              }
            , effects
            )

        AgentSnapshotRepositoryChangeFetched _ (Err _) ->
            ( { model | loadError = True }, effects )

        AgentWorkflowRunCanceled runId (Ok detail) ->
            if runId == model.workflowRunId then
                ( { model | detail = Just detail, actionError = False }, effects )

            else
                ( model, effects )

        AgentWorkflowRunCanceled runId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | actionError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowRunRetried runId (Ok detail) ->
            if runId == model.workflowRunId then
                ( { model | actionError = False }
                , effects
                    ++ [ NavigateTo
                            (Routes.toString
                                (Routes.AgentWorkflowRun
                                    { workflowName = model.workflowName
                                    , id = detail.summary.id
                                    }
                                )
                            )
                       ]
                )

            else
                ( model, effects )

        AgentWorkflowRunRetried runId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | actionError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowRunMetricsFetched runId (Ok metrics) ->
            -- run-qualified: a second open run page must not accept this run's
            -- metrics (the global recent-metrics feed is the operator dashboard's)
            if runId == model.workflowRunId then
                ( { model | metrics = metrics }, effects )

            else
                ( model, effects )

        AgentWorkflowRunTranscriptsFetched runId (Ok refs) ->
            -- run-qualified for the same reason metrics are: a second open run
            -- page must not accept this run's transcript index
            if runId == model.workflowRunId then
                ( { model | transcripts = refs, transcriptIndexError = False }, effects )

            else
                ( model, effects )

        AgentWorkflowRunTranscriptsFetched runId (Err _) ->
            -- supplemental: a failed index must not blank the whole run page,
            -- but it must not read as "this run captured nothing" either
            if runId == model.workflowRunId then
                ( { model | transcriptIndexError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowRunTranscriptFetched runId planId (Ok ndjson) ->
            if runId == model.workflowRunId then
                ( { model
                    | transcriptEntries =
                        Dict.insert planId (Transcript.parse ndjson) model.transcriptEntries
                    , transcriptErrors = Set.remove planId model.transcriptErrors
                  }
                , effects
                )

            else
                ( model, effects )

        AgentWorkflowRunTranscriptFetched runId planId (Err _) ->
            if runId == model.workflowRunId then
                ( { model | transcriptErrors = Set.insert planId model.transcriptErrors }, effects )

            else
                ( model, effects )

        AgentReviewVerdictSubmitted findingId (Ok ()) ->
            ( { model | verdictErrors = Set.remove findingId model.verdictErrors }
            , effects ++ [ FetchAgentWorkflowReviews model.workflowName model.workflowRunId ]
            )

        AgentReviewVerdictSubmitted findingId (Err _) ->
            ( { model | verdictErrors = Set.insert findingId model.verdictErrors }, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update message ( model, effects ) =
    case message of
        AgentWaitAnswerChanged waitId snapshotId ->
            ( { model | answerSnapshots = Dict.insert waitId snapshotId model.answerSnapshots }, effects )

        AgentWaitResolveClicked waitId ->
            case Dict.get waitId model.answerSnapshots |> Maybe.map String.trim of
                Just snapshotId ->
                    if snapshotId /= "" then
                        ( { model | actionError = False }
                        , effects
                            ++ [ ResolveAgentWorkflowWait
                                    model.workflowName
                                    model.workflowRunId
                                    waitId
                                    snapshotId
                               ]
                        )

                    else
                        ( model, effects )

                Nothing ->
                    ( model, effects )

        AgentWorkflowRunCancelClicked ->
            ( { model | actionError = False }
            , effects ++ [ CancelAgentWorkflowRun model.workflowName model.workflowRunId ]
            )

        AgentWorkflowRunRetryClicked ->
            ( { model | actionError = False }
            , effects ++ [ RetryAgentWorkflowRun model.workflowName model.workflowRunId ]
            )

        AgentTranscriptToggled planId ->
            let
                expanded =
                    toggleSet planId model.expandedTranscripts

                -- fetch the ndjson body the first time a transcript is opened;
                -- the index is polled, the (up to 512KiB) bodies never are
                fetch =
                    if Set.member planId expanded && not (Dict.member planId model.transcriptEntries) then
                        [ FetchAgentWorkflowRunTranscript model.workflowName model.workflowRunId planId ]

                    else
                        []
            in
            ( { model | expandedTranscripts = expanded }, effects ++ fetch )

        AgentTranscriptEntryToggled planId index ->
            ( { model
                | expandedTranscriptEntries =
                    toggleSet (transcriptEntryKey planId index) model.expandedTranscriptEntries
              }
            , effects
            )

        ToggleAgentReviewPanel ->
            ( { model | agentReviewPanelExpanded = not model.agentReviewPanelExpanded }, effects )

        ToggleAgentReviewFinding findingId ->
            ( { model | expandedFindings = toggleSet findingId model.expandedFindings }, effects )

        ToggleAgentReviewFindingBody findingId ->
            ( { model | expandedDescriptions = toggleSet findingId model.expandedDescriptions }, effects )

        ToggleAgentReviewObservations open ->
            ( { model | showObservations = Just open }, effects )

        AgentReviewNoteChanged findingId note ->
            ( { model | agentReviewNotes = Dict.insert findingId note model.agentReviewNotes }, effects )

        AgentReviewVerdictClicked params ->
            ( model
            , effects
                ++ [ SubmitAgentReviewVerdict
                        { reviewSnapshotId = params.reviewSnapshotId
                        , findingId = params.findingId
                        , verdict = params.verdict
                        , notes = Dict.get params.findingId model.agentReviewNotes |> Maybe.withDefault ""
                        , reviewer = params.reviewer
                        }
                   ]
            )

        _ ->
            ( model, effects )


toggleSet : comparable -> Set comparable -> Set comparable
toggleSet value values =
    if Set.member value values then
        Set.remove value values

    else
        Set.insert value values


polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch = \model -> fetchAll model.workflowName model.workflowRunId
      }
    ]


terminalProjectionPollLimit : Int
terminalProjectionPollLimit =
    12


runIsActive : Model -> Bool
runIsActive model =
    case model.detail of
        Nothing ->
            True

        Just detail ->
            List.member detail.summary.status [ "admitting", "running", "canceling" ]


hasPendingOutputProjection : Model -> Bool
hasPendingOutputProjection model =
    case model.detail of
        Nothing ->
            False

        Just detail ->
            List.any (outputProjectionIsPending model) detail.outputs


outputProjectionIsPending : Model -> WorkflowRun.OutputBinding -> Bool
outputProjectionIsPending model output =
    case output.snapshot.typeRef of
        "repository-change/v1" ->
            case Dict.get output.snapshot.id model.repositoryChanges of
                Nothing ->
                    True

                Just projection ->
                    projection.status == "pending"

        "review/v1" ->
            not <|
                List.any
                    (\review -> review.info.snapshotId == Just output.snapshot.id)
                    model.agentReviews

        _ ->
            False


shouldPoll : Model -> Bool
shouldPoll model =
    runIsActive model
        || (model.terminalProjectionPollsRemaining
                > 0
                && hasPendingOutputProjection model
           )


handleDelivery : Delivery -> ET Model
handleDelivery delivery (( model, _ ) as state) =
    if shouldPoll model then
        let
            ( polledModel, polledEffects ) =
                Polling.handleDelivery polls delivery state

            nextModel =
                case delivery of
                    ClockTicked FiveSeconds _ ->
                        if runIsActive model then
                            polledModel

                        else
                            { polledModel
                                | terminalProjectionPollsRemaining =
                                    max 0 (model.terminalProjectionPollsRemaining - 1)
                            }

                    _ ->
                        polledModel
        in
        ( nextModel, polledEffects )

    else
        state


subscriptions : Model -> List Subscription
subscriptions model =
    if shouldPoll model then
        Polling.subscriptions polls

    else
        []


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentWorkflowRun
                { workflowName = model.workflowName, id = model.workflowRunId }
    in
    Chrome.view session
        model
        route
        (model.workflowName ++ " run #" ++ model.workflowRunId)
        "immutable admission, execution, outputs, and interventions"
        [ if model.loadError then
            errorLine "Some run projections could not be loaded. The durable run identity remains available."

          else
            Html.text ""
        , case model.detail of
            Nothing ->
                loading "loading durable run…"

            Just detail ->
                runContent session model detail
        ]


runContent : Session -> Model -> WorkflowRun.Detail -> Html Message
runContent session model detail =
    Html.div []
        [ identityCard detail.summary
        , executionCard model detail.summary
        , bindingsCard model detail
        , waitsCard model
        , outcomesCard model
        , telemetryCard model detail.summary
        , transcriptsCard model
        , if List.isEmpty model.agentReviews && not model.agentReviewLoadError then
            Html.text ""

          else
            Html.section [ class "agent-run-review", style "margin-bottom" "20px" ]
                [ heading "Review projection"
                , AgentReviewView.view (reviewer session) model
                ]
        ]


identityCard : WorkflowRun.Summary -> Html Message
identityCard run =
    Html.section [ class "agent-run-identity", cardStyle ]
        [ heading "Frozen definition"
        , definitionRows
            [ ( "workflow run ID", run.id )
            , ( "definition", run.workflowName ++ "@" ++ String.fromInt run.workflowVersion )
            , ( "schema/signature", String.fromInt run.schemaVersion ++ "/" ++ String.fromInt run.signatureVersion )
            , ( "definition content", run.definitionContentHash )
            , ( "parameterized config", run.parameterizedConfigHash )
            , ( "instance config", Maybe.withDefault "not materialized" run.instanceConfigHash )
            , ( "actual plan", Maybe.withDefault "not materialized" run.actualPlanHash )
            , ( "origin", run.originKind ++ originReference run.originReference )
            , ( "creator", run.createdBy )
            , ( "created", run.createdAt )
            ]
        ]


originReference : String -> String
originReference reference =
    if reference == "" then
        ""

    else
        " · " ++ reference


executionCard : Model -> WorkflowRun.Summary -> Html Message
executionCard model run =
    Html.section [ class "agent-run-execution", cardStyle ]
        [ heading "Execution"
        , Html.div [ style "display" "flex", style "gap" "12px", style "align-items" "center" ]
            [ Html.strong [ class "agent-run-status" ] [ Html.text run.status ]
            , Html.span [] [ Html.text (Maybe.withDefault "not started" run.executionStatus) ]
            , case run.plannedBuildId of
                Just buildId ->
                    Html.a
                        [ class "agent-run-build-link"
                        , href
                            (Routes.toString
                                (Routes.OneOffBuild
                                    { id = buildId, highlight = Routes.HighlightNothing }
                                )
                            )
                        ]
                        [ Html.text ("Concourse execution detail · build " ++ String.fromInt buildId) ]

                Nothing ->
                    Html.span [ style "color" "#8a8a8a" ] [ Html.text "Concourse execution not yet planned" ]
            ]
        , Html.div [ style "margin-top" "10px", style "display" "flex", style "gap" "8px" ]
            [ Html.button
                [ type_ "button"
                , onClick AgentWorkflowRunCancelClicked
                , disabled (List.member run.status [ "succeeded", "failed", "errored", "aborted" ])
                ]
                [ Html.text "cancel" ]
            , Html.button
                [ type_ "button"
                , onClick AgentWorkflowRunRetryClicked
                , disabled (not (List.member run.status [ "succeeded", "failed", "errored", "aborted" ]))
                ]
                [ Html.text "retry from frozen inputs" ]
            ]
        , if model.actionError then
            errorLine "The requested durable transition could not be applied."

          else
            Html.text ""
        , if List.member run.status [ "failed", "errored" ] then
            Html.p [ class "agent-run-contract-diagnostic", style "color" "#e0a44e" ]
                [ Html.text
                    "Execution failed. Private task output remains redacted here; inspect the linked Concourse build and typed output projections."
                ]

          else
            Html.text ""
        ]


bindingsCard : Model -> WorkflowRun.Detail -> Html Message
bindingsCard model detail =
    Html.section [ class "agent-run-bindings", cardStyle ]
        [ heading "Snapshot bindings and lineage"
        , Html.div [ style "display" "grid", style "grid-template-columns" "1fr 1fr", style "gap" "20px" ]
            [ Html.div []
                [ Html.h3 [] [ Html.text "Inputs" ]
                , if List.isEmpty detail.inputs then
                    loading "no inputs"

                  else
                    Html.ul [] (List.map inputBinding detail.inputs)
                ]
            , Html.div []
                [ Html.h3 [] [ Html.text "Outputs" ]
                , if List.isEmpty detail.outputs then
                    loading "outputs have not materialized"

                  else
                    Html.div [] (List.map (outputBinding model) detail.outputs)
                ]
            ]
        ]


inputBinding : WorkflowRun.InputBinding -> Html Message
inputBinding binding =
    Html.li []
        [ Html.text (binding.portName ++ " ← ")
        , snapshotRef binding.snapshot
        ]


outputBinding : Model -> WorkflowRun.OutputBinding -> Html Message
outputBinding model binding =
    Html.div [ class "agent-run-output", style "margin-bottom" "12px" ]
        [ Html.div []
            [ Html.text (binding.portName ++ " → ")
            , snapshotRef binding.snapshot
            , Html.span [ style "color" "#8a8a8a" ]
                [ Html.text (" · " ++ binding.snapshot.contentState) ]
            ]
        , case Dict.get binding.snapshot.id model.repositoryChanges of
            Just projection ->
                RepositoryChange.view projection

            Nothing ->
                if binding.snapshot.typeRef == "repository-change/v1" then
                    loading "loading bounded repository-change projection…"

                else
                    Html.text ""
        ]


snapshotRef : { snapshot | id : String, typeRef : String } -> Html Message
snapshotRef snapshot =
    Html.a
        [ class "agent-snapshot-link"
        , href (Routes.toString (Routes.AgentSnapshot { id = snapshot.id }))
        ]
        [ Html.text (snapshot.typeRef ++ " #" ++ snapshot.id) ]


waitsCard : Model -> Html Message
waitsCard model =
    Html.section [ class "agent-run-waits", cardStyle ]
        [ heading "Human waits"
        , if List.isEmpty model.waits then
            loading "no human intervention points"

          else
            Html.div [] (List.map (waitView model) model.waits)
        ]


waitView : Model -> WorkflowRun.Wait -> Html Message
waitView model wait =
    let
        questionContent =
            [ Html.div []
                [ Html.strong [] [ Html.text wait.questionName ]
                , Html.text
                    (" · "
                        ++ wait.status
                        ++ " · expects "
                        ++ wait.expectedType
                        ++ " · deadline "
                        ++ wait.deadline
                    )
                ]
            , Html.div []
                [ Html.text "question snapshot: ", snapshotRef wait.question ]
            , Html.p
                [ class "agent-run-wait-prompt"
                , style "margin" "8px 0 4px"
                ]
                [ Html.text wait.prompt ]
            , Html.p
                [ class "agent-run-wait-context"
                , style "margin" "0 0 8px"
                , style "color" "#b5b5b5"
                ]
                [ Html.text wait.context ]
            ]

        resolutionContent =
            if wait.status == "waiting" then
                [ if List.isEmpty wait.options then
                    Html.input
                        [ placeholder "answer"
                        , value (Dict.get wait.id model.answerSnapshots |> Maybe.withDefault "")
                        , onInput (AgentWaitAnswerChanged wait.id)
                        ]
                        []

                  else
                    Html.div
                        [ class "agent-run-wait-options"
                        , style "display" "flex"
                        , style "gap" "8px"
                        , style "flex-wrap" "wrap"
                        ]
                        (List.map (waitOption model wait) wait.options)
                , Html.button
                    [ type_ "button"
                    , style "margin-top" "8px"
                    , onClick (AgentWaitResolveClicked wait.id)
                    , disabled
                        (Dict.get wait.id model.answerSnapshots
                            |> Maybe.map (String.trim >> String.isEmpty)
                            |> Maybe.withDefault True
                        )
                    ]
                    [ Html.text "submit answer" ]
                ]

            else
                case wait.answer of
                    Just answer ->
                        [ Html.div []
                            [ Html.text "answer: "
                            , snapshotRef answer
                            , if wait.resolvedByDisplayName == "" then
                                Html.text ""

                              else
                                Html.text (" · answered by " ++ wait.resolvedByDisplayName)
                            ]
                        ]

                    Nothing ->
                        []
    in
    Html.div
        [ class "agent-run-wait"
        , style "padding" "10px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        (questionContent ++ resolutionContent)


waitOption : Model -> WorkflowRun.Wait -> String -> Html Message
waitOption model wait option =
    let
        selected =
            Dict.get wait.id model.answerSnapshots == Just option
    in
    Html.button
        [ type_ "button"
        , class
            (if selected then
                "agent-run-wait-option selected"

             else
                "agent-run-wait-option"
            )
        , onClick (AgentWaitAnswerChanged wait.id option)
        ]
        [ Html.text
            (if option == wait.default then
                option ++ " (default)"

             else
                option
            )
        ]


outcomesCard : Model -> Html Message
outcomesCard model =
    Html.section [ class "agent-run-outcomes", cardStyle ]
        [ heading "Outcomes and interventions"
        , if List.isEmpty model.outcomes then
            loading "no output dispositions have been recorded"

          else
            Html.div [] (List.map outcomeRow model.outcomes)
        ]


outcomeRow : WorkflowRun.Outcome -> Html Message
outcomeRow outcome =
    Html.div
        [ class "agent-run-outcome"
        , style "padding" "8px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        [ snapshotRef { id = outcome.outputSnapshotId, typeRef = "output" }
        , Html.text
            (" · "
                ++ outcome.disposition
                ++ " · publication "
                ++ outcome.publicationState
                ++ " · "
                ++ String.fromInt outcome.interventionCount
                ++ " human interventions"
            )
        , case outcome.modificationSnapshotId of
            Just snapshotId ->
                Html.span []
                    [ Html.text " · modified as "
                    , snapshotRef { id = snapshotId, typeRef = "human modification" }
                    ]

            Nothing ->
                Html.text ""
        ]


telemetryCard : Model -> WorkflowRun.Summary -> Html Message
telemetryCard model run =
    let
        relevant =
            model.metrics
                |> List.filter
                    (\metric ->
                        metric.workflowName
                            == run.workflowName
                            && (case run.plannedBuildId of
                                    Just buildId ->
                                        metric.buildId == buildId

                                    Nothing ->
                                        False
                               )
                    )

        cost =
            List.sum (List.map .costUsd relevant)

        turns =
            List.sum (List.map .turns relevant)

        tokens =
            relevant
                |> List.map (\metric -> metric.usage.inputTokens + metric.usage.outputTokens)
                |> List.sum
    in
    Html.section [ class "agent-run-telemetry", cardStyle ]
        [ heading "Invocation telemetry"
        , Html.p [ style "font-family" "monospace" ]
            [ Html.text
                (String.fromInt (List.length relevant)
                    ++ " steps · "
                    ++ String.fromInt turns
                    ++ " turns · "
                    ++ String.fromInt tokens
                    ++ " tokens · $"
                    ++ String.fromFloat cost
                )
            ]
        ]


{-| What the agent actually did, per step: the tool-call transcript the runner
captured. The index is polled with the rest of the run; a body is fetched only
when its step is opened.
-}
transcriptsCard : Model -> Html Message
transcriptsCard model =
    Html.section [ class "agent-run-transcripts", cardStyle ]
        [ heading "Agent transcripts"
        , if not (List.isEmpty model.transcripts) then
            Html.div [] (List.map (transcriptRow model) model.transcripts)

          else if model.transcriptIndexError then
            errorLine "The transcript index could not be loaded."

          else
            loading "no agent transcript captured for this run"
        ]


transcriptRow : Model -> Transcript.Ref -> Html Message
transcriptRow model ref =
    let
        expanded =
            Set.member ref.planId model.expandedTranscripts

        stepLabel =
            if ref.stepName == "" then
                ref.planId

            else
                ref.stepName

        functionLabel =
            if ref.functionId == "" then
                ""

            else
                " · " ++ ref.functionId
    in
    Html.div
        [ class "agent-run-transcript"
        , style "padding" "8px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        (Html.button
            (transcriptToggleStyle
                ++ [ class "agent-transcript-toggle"
                   , attribute "aria-expanded" (ariaBool expanded)
                   , onClick (AgentTranscriptToggled ref.planId)
                   ]
            )
            [ Html.text
                ((if expanded then
                    "▾ "

                  else
                    "▸ "
                 )
                    ++ stepLabel
                    ++ functionLabel
                    ++ " · "
                    ++ humanBytes ref.byteLen
                    ++ (if ref.truncated then
                            " (head-truncated)"

                        else
                            ""
                       )
                )
            ]
            :: (if not expanded then
                    []

                else if Set.member ref.planId model.transcriptErrors then
                    [ errorLine "The transcript for this step could not be loaded." ]

                else
                    case Dict.get ref.planId model.transcriptEntries of
                        Nothing ->
                            [ loading "loading transcript…" ]

                        Just [] ->
                            [ loading "transcript is empty" ]

                        Just entries ->
                            [ Html.div
                                [ class "agent-transcript"
                                , style "margin" "8px 0 0"
                                ]
                                (List.map (transcriptEntry model ref.planId) entries)
                            ]
               )
        )


transcriptEntry : Model -> String -> Transcript.Entry -> Html Message
transcriptEntry model planId entry =
    let
        bodyExpanded =
            Set.member (transcriptEntryKey planId entry.index) model.expandedTranscriptEntries

        body =
            Html.pre
                [ class "agent-transcript-body"
                , style "margin" "4px 0 0"
                , style "white-space" "pre-wrap"
                , style "overflow-x" "auto"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                ]
                [ Html.text entry.body ]
    in
    Html.div
        [ class ("agent-transcript-entry " ++ Transcript.kindClass entry.kind)
        , style "padding" "6px 0"
        , style "border-top" "1px solid #262525"
        ]
        (if entry.collapsible then
            [ Html.button
                (transcriptToggleStyle
                    ++ [ class "agent-transcript-entry-toggle"
                       , attribute "aria-expanded" (ariaBool bodyExpanded)
                       , onClick (AgentTranscriptEntryToggled planId entry.index)
                       ]
                )
                [ Html.text
                    ((if bodyExpanded then
                        "▾ "

                      else
                        "▸ "
                     )
                        ++ entry.label
                    )
                ]
            , if bodyExpanded then
                body

              else
                Html.text ""
            ]

         else
            [ Html.div
                [ class "agent-transcript-entry-label"
                , style "color" "#8a8a8a"
                , style "font-size" "12px"
                ]
                [ Html.text entry.label ]
            , body
            ]
        )


{-| The disclosure rows are buttons for keyboard and screen-reader reasons, but
must read as text: the same reset the review panel's toggles use.
-}
transcriptToggleStyle : List (Html.Attribute Message)
transcriptToggleStyle =
    [ type_ "button"
    , style "background" "transparent"
    , style "border" "none"
    , style "padding" "0"
    , style "margin" "0"
    , style "font-family" "monospace"
    , style "font-size" "12px"
    , style "color" "inherit"
    , style "text-align" "left"
    , style "cursor" "pointer"
    ]


ariaBool : Bool -> String
ariaBool value_ =
    if value_ then
        "true"

    else
        "false"


transcriptEntryKey : String -> Int -> String
transcriptEntryKey planId index =
    planId ++ "/" ++ String.fromInt index


humanBytes : Int -> String
humanBytes bytes =
    if bytes < 1024 then
        String.fromInt bytes ++ " B"

    else
        String.fromInt (bytes // 1024) ++ " KiB"


reviewer : Session -> String
reviewer session =
    case session.userState of
        UserStateLoggedIn user ->
            Login.userDisplayName user

        _ ->
            ""


definitionRows : List ( String, String ) -> Html Message
definitionRows rows =
    Html.dl [ style "display" "grid", style "grid-template-columns" "160px 1fr", style "gap" "6px 12px" ]
        (List.concatMap
            (\( label, content ) ->
                [ Html.dt [ style "color" "#8a8a8a" ] [ Html.text label ]
                , Html.dd [ style "margin" "0", style "font-family" "monospace" ] [ Html.text content ]
                ]
            )
            rows
        )


cardStyle : Html.Attribute Message
cardStyle =
    style "margin-bottom" "20px"


heading : String -> Html Message
heading label =
    Html.h2 [ style "font-size" "15px", style "margin" "0 0 10px" ] [ Html.text label ]


loading : String -> Html Message
loading message =
    Html.p [ style "color" "#8a8a8a", style "font-family" "monospace", style "font-size" "12px" ] [ Html.text message ]


errorLine : String -> Html Message
errorLine message =
    Html.p [ class "agent-page-error", style "color" "#e0a44e" ] [ Html.text message ]
