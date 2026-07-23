module AgentExperiment.AgentExperiment exposing
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

import AgentExperiment.Scorecard as Scorecard
import AgentPage.Chrome as Chrome
import Application.Models exposing (Session)
import Concourse.Experiment as Experiment
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, disabled, href, style, type_)
import Html.Events exposing (onClick)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import Tooltip


type alias Model =
    Login.Model
        { experimentId : String
        , experiment : Maybe Experiment.Experiment
        , cells : Maybe (List Experiment.StoredCell)
        , scorecard : Maybe Experiment.Scorecard
        , scorecardUnavailable : Bool
        , loadError : Bool
        , actionError : Bool
        }


init : { id : String } -> ( Model, List Effect )
init { id } =
    ( { experimentId = id
      , experiment = Nothing
      , cells = Nothing
      , scorecard = Nothing
      , scorecardUnavailable = False
      , loadError = False
      , actionError = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentExperiment id
      , FetchAgentExperimentCells id
      , FetchAgentExperimentScorecard id
      ]
    )


documentTitle : Model -> String
documentTitle model =
    "Experiment " ++ model.experimentId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentExperimentFetched experimentId (Ok experiment) ->
            if experimentId == model.experimentId then
                ( { model | experiment = Just experiment, loadError = False }, effects )

            else
                ( model, effects )

        AgentExperimentFetched experimentId (Err _) ->
            if experimentId == model.experimentId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentExperimentCellsFetched experimentId (Ok cells) ->
            if experimentId == model.experimentId then
                ( { model | cells = Just cells }, effects )

            else
                ( model, effects )

        AgentExperimentCellsFetched experimentId (Err _) ->
            if experimentId == model.experimentId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentExperimentScorecardFetched experimentId (Ok scorecard) ->
            if experimentId == model.experimentId then
                ( { model | scorecard = Just scorecard, scorecardUnavailable = False }, effects )

            else
                ( model, effects )

        AgentExperimentScorecardFetched experimentId (Err (Http.BadStatus { status })) ->
            if experimentId == model.experimentId then
                if status.code == 409 then
                    ( { model | scorecardUnavailable = True }, effects )

                else
                    ( { model | scorecardUnavailable = False, loadError = True }, effects )

            else
                ( model, effects )

        AgentExperimentScorecardFetched experimentId (Err _) ->
            if experimentId == model.experimentId then
                ( { model | scorecardUnavailable = False, loadError = True }, effects )

            else
                ( model, effects )

        AgentExperimentCanceled experimentId (Ok experiment) ->
            if experimentId == model.experimentId then
                ( { model | experiment = Just experiment, actionError = False }
                , effects
                    ++ [ FetchAgentExperimentCells experimentId
                       , FetchAgentExperimentScorecard experimentId
                       ]
                )

            else
                ( model, effects )

        AgentExperimentCanceled experimentId (Err _) ->
            if experimentId == model.experimentId then
                ( { model | actionError = True }, effects )

            else
                ( model, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update message ( model, effects ) =
    case ( message, model.experiment ) of
        ( AgentExperimentCancelClicked, Just experiment ) ->
            ( { model | actionError = False }
            , effects ++ [ CancelAgentExperiment experiment.id experiment.revision ]
            )

        _ ->
            ( model, effects )


polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch =
            \model ->
                [ FetchAgentExperiment model.experimentId
                , FetchAgentExperimentCells model.experimentId
                , FetchAgentExperimentScorecard model.experimentId
                ]
      }
    ]


experimentIsActive : Model -> Bool
experimentIsActive model =
    case model.experiment of
        Nothing ->
            True

        Just experiment ->
            List.member experiment.definition.state [ "running", "canceling" ]


handleDelivery : Delivery -> ET Model
handleDelivery delivery (( model, _ ) as state) =
    if experimentIsActive model then
        Polling.handleDelivery polls delivery state

    else
        state


subscriptions : Model -> List Subscription
subscriptions model =
    if experimentIsActive model then
        Polling.subscriptions polls

    else
        []


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


view : Session -> Model -> Html Message
view session model =
    Chrome.view session
        model
        (Routes.AgentExperiment { id = model.experimentId })
        (experimentTitle model)
        "frozen variants, fixtures, evaluator, cells, and evidence"
        [ if model.loadError then
            errorLine "Some durable experiment data could not be loaded."

          else
            Html.text ""
        , case model.experiment of
            Nothing ->
                muted "loading frozen experiment definition…"

            Just experiment ->
                experimentContent model experiment
        ]


experimentTitle : Model -> String
experimentTitle model =
    model.experiment
        |> Maybe.map (.definition >> .name)
        |> Maybe.withDefault ("Experiment #" ++ model.experimentId)


experimentContent : Model -> Experiment.Experiment -> Html Message
experimentContent model experiment =
    let
        definition =
            experiment.definition
    in
    Html.div []
        [ identityCard model experiment
        , signatureCard definition
        , variantsCard definition
        , fixturesCard definition
        , evaluatorCard definition
        , cellsCard definition model.cells
        , case model.scorecard of
            Just scorecard ->
                Scorecard.view scorecard

            Nothing ->
                if model.scorecardUnavailable then
                    Html.section [ class "agent-scorecard-unavailable" ]
                        [ Html.h2 [ style "font-size" "15px" ] [ Html.text "Scorecard" ]
                        , muted "Scorecard is unavailable until enough immutable cells have completed; raw lifecycle cells remain above."
                        ]

                else
                    muted "loading scorecard…"
        ]


identityCard : Model -> Experiment.Experiment -> Html Message
identityCard model experiment =
    let
        definition =
            experiment.definition

        expected =
            List.length definition.variants
                * List.length definition.fixtures
                * definition.repetitions
    in
    Html.section [ class "agent-experiment-identity", cardStyle ]
        [ heading "Experiment identity"
        , definitionRows
            [ ( "ID", experiment.id )
            , ( "state", definition.state )
            , ( "revision", String.fromInt experiment.revision )
            , ( "created by", experiment.createdBy )
            , ( "created", experiment.createdAt )
            , ( "updated", experiment.updatedAt )
            , ( "matrix", String.fromInt expected ++ " expected cells" )
            , ( "budget", "$" ++ String.fromFloat definition.budget.totalUsd ++ " total · $" ++ String.fromFloat definition.budget.perCellUsd ++ " per cell" )
            ]
        , Html.div [ style "display" "flex", style "gap" "8px" ]
            [ Html.button
                [ type_ "button"
                , class "agent-experiment-cancel"
                , onClick AgentExperimentCancelClicked
                , disabled (not (List.member definition.state [ "running", "canceling" ]))
                ]
                [ Html.text "cancel experiment" ]
            ]
        , if model.actionError then
            errorLine "Experiment cancellation conflicted with a newer revision or terminal state."

          else
            Html.text ""
        ]


signatureCard : Experiment.Definition -> Html Message
signatureCard definition =
    Html.section [ class "agent-experiment-signature", cardStyle ]
        [ heading "Pinned candidate signature"
        , signatureView definition.signature
        ]


signatureView : Experiment.Signature -> Html Message
signatureView signature =
    Html.div [ style "display" "grid", style "grid-template-columns" "1fr 1fr", style "gap" "20px" ]
        [ portList "inputs" signature.inputs
        , portList "outputs" signature.outputs
        ]


portList : String -> List Experiment.Port -> Html Message
portList label ports =
    Html.div []
        [ Html.h3 [ style "font-size" "13px" ] [ Html.text label ]
        , Html.ul []
            (List.map
                (\signaturePort ->
                    Html.li [ style "font-family" "monospace" ]
                        [ Html.text
                            (signaturePort.name
                                ++ " : "
                                ++ signaturePort.typeRef
                                ++ (if signaturePort.optional then
                                        "?"

                                    else
                                        ""
                                   )
                            )
                        ]
                )
                ports
            )
        ]


variantsCard : Experiment.Definition -> Html Message
variantsCard definition =
    Html.section [ class "agent-experiment-variants", cardStyle ]
        [ heading "Frozen variants"
        , Html.div [] (List.map variantRow definition.variants)
        ]


variantRow : Experiment.Variant -> Html Message
variantRow variant =
    let
        target =
            variant.target
    in
    Html.div
        [ class "agent-experiment-variant"
        , style "padding" "8px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        [ Html.strong []
            [ Html.text
                (variant.label
                    ++ (if variant.control then
                            " · control"

                        else
                            ""
                       )
                )
            ]
        , Html.span [ style "margin-left" "12px", style "font-family" "monospace" ]
            [ Html.text
                (target.kind
                    ++ " "
                    ++ target.workflowName
                    ++ "@"
                    ++ String.fromInt target.version
                    ++ (case target.functionId of
                            Just functionId ->
                                "#" ++ functionId

                            Nothing ->
                                ""
                       )
                    ++ " · signature #"
                    ++ String.left 12 variant.signatureHash
                )
            ]
        , Html.a
            [ class "agent-experiment-promotion-link"
            , href (Routes.toString (Routes.AgentWorkflow { name = target.workflowName }))
            , style "margin-left" "12px"
            ]
            [ Html.text "inspect operational runs / promote explicitly" ]
        ]


fixturesCard : Experiment.Definition -> Html Message
fixturesCard definition =
    Html.section [ class "agent-experiment-fixtures", cardStyle ]
        [ heading "Immutable fixtures"
        , Html.div [] (List.map fixtureRow definition.fixtures)
        ]


fixtureRow : Experiment.Fixture -> Html Message
fixtureRow fixture =
    Html.div
        [ class "agent-experiment-fixture"
        , style "padding" "8px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        [ Html.strong [] [ Html.text (fixture.label ++ " · " ++ fixture.role) ]
        , Html.ul []
            (fixture.inputs
                |> List.map
                    (\( portName, snapshotId ) ->
                        Html.li []
                            [ Html.text (portName ++ " ← ")
                            , snapshotLink snapshotId
                            ]
                    )
            )
        , if List.isEmpty fixture.assertions then
            Html.text ""

          else
            Html.p [ style "font-family" "monospace", style "font-size" "12px" ]
                [ Html.text
                    (fixture.assertions
                        |> List.map
                            (\assertion ->
                                assertion.metric
                                    ++ " "
                                    ++ assertion.comparator
                                    ++ " "
                                    ++ String.join "," (List.map String.fromFloat assertion.thresholds)
                            )
                        |> String.join " · "
                    )
                ]
        ]


evaluatorCard : Experiment.Definition -> Html Message
evaluatorCard definition =
    let
        evaluator =
            definition.evaluator
    in
    Html.section [ class "agent-experiment-evaluator", cardStyle ]
        [ heading "Pinned evaluator"
        , Html.p [ style "font-family" "monospace" ]
            [ Html.text
                (evaluator.target.workflowName
                    ++ "@"
                    ++ String.fromInt evaluator.target.version
                    ++ " · measurements → "
                    ++ evaluator.measurementsPort
                )
            ]
        , signatureView evaluator.signature
        , Html.ul []
            (List.map
                (\mapping ->
                    Html.li []
                        [ Html.text
                            (mapping.evaluatorPort
                                ++ " ← "
                                ++ mapping.sourceDirection
                                ++ ":"
                                ++ mapping.sourcePort
                            )
                        ]
                )
                evaluator.mappings
            )
        ]


cellsCard : Experiment.Definition -> Maybe (List Experiment.StoredCell) -> Html Message
cellsCard definition maybeCells =
    Html.section [ class "agent-experiment-cells", cardStyle ]
        [ heading "Cell matrix"
        , case maybeCells of
            Nothing ->
                muted "loading cells…"

            Just [] ->
                muted "no cells have been materialized"

            Just cells ->
                Html.table [ style "width" "100%" ]
                    [ Html.thead []
                        [ Html.tr []
                            (List.map tableHeader
                                [ "cell", "variant", "fixture", "role", "rep", "status", "candidate", "evaluator", "measurement", "failure" ]
                            )
                        ]
                    , Html.tbody [] (List.map (cellRow definition) cells)
                    ]
        ]


cellRow : Experiment.Definition -> Experiment.StoredCell -> Html Message
cellRow definition cell =
    let
        workflowName =
            definition.variants
                |> List.filter (\variant -> variant.label == cell.variantLabel)
                |> List.head
                |> Maybe.map (.target >> .workflowName)
                |> Maybe.withDefault ""
    in
    Html.tr [ class "agent-experiment-cell" ]
        [ tableCell cell.id
        , tableCell cell.variantLabel
        , tableCell cell.fixtureLabel
        , tableCell cell.fixtureRole
        , tableCell (String.fromInt cell.repetition)
        , tableCell cell.status
        , tableLinkCell (workflowRunLink workflowName cell.candidateWorkflowRunId)
        , tableLinkCell (workflowRunLink definition.evaluator.target.workflowName cell.evaluatorWorkflowRunId)
        , tableLinkCell (Maybe.map snapshotLink cell.measurementSnapshotId)
        , tableCell cell.candidateFailureCategory
        ]


workflowRunLink : String -> Maybe String -> Maybe (Html Message)
workflowRunLink workflowName maybeId =
    maybeId
        |> Maybe.map
            (\runId ->
                Html.a
                    [ href
                        (Routes.toString
                            (Routes.AgentWorkflowRun
                                { workflowName = workflowName, id = runId }
                            )
                        )
                    ]
                    [ Html.text ("run #" ++ runId) ]
            )


snapshotLink : String -> Html Message
snapshotLink snapshotId =
    Html.a
        [ href (Routes.toString (Routes.AgentSnapshot { id = snapshotId })) ]
        [ Html.text ("snapshot #" ++ snapshotId) ]


tableHeader : String -> Html Message
tableHeader label =
    Html.th [ style "text-align" "left", style "padding" "6px" ] [ Html.text label ]


tableCell : String -> Html Message
tableCell content =
    Html.td
        [ style "padding" "6px"
        , style "border-top" "1px solid #302f2f"
        , style "font-family" "monospace"
        , style "font-size" "11px"
        ]
        [ Html.text content ]


tableLinkCell : Maybe (Html Message) -> Html Message
tableLinkCell maybeContent =
    Html.td
        [ style "padding" "6px", style "border-top" "1px solid #302f2f" ]
        [ Maybe.withDefault (Html.text "—") maybeContent ]


definitionRows : List ( String, String ) -> Html Message
definitionRows rows =
    Html.dl [ style "display" "grid", style "grid-template-columns" "130px 1fr", style "gap" "6px 12px" ]
        (List.concatMap
            (\( label, content ) ->
                [ Html.dt [ style "color" "#8a8a8a" ] [ Html.text label ]
                , Html.dd [ style "margin" "0", style "font-family" "monospace" ] [ Html.text content ]
                ]
            )
            rows
        )


heading : String -> Html Message
heading label =
    Html.h2 [ style "font-size" "15px", style "margin" "0 0 10px" ] [ Html.text label ]


cardStyle : Html.Attribute Message
cardStyle =
    style "margin-bottom" "20px"


muted : String -> Html Message
muted message =
    Html.p [ style "color" "#8a8a8a", style "font-family" "monospace", style "font-size" "12px" ] [ Html.text message ]


errorLine : String -> Html Message
errorLine message =
    Html.p [ class "agent-page-error", style "color" "#e0a44e" ] [ Html.text message ]
