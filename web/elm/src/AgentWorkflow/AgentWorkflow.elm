module AgentWorkflow.AgentWorkflow exposing
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
import Application.Models exposing (Session)
import Char
import Concourse.Agent as Agent
import Concourse.Experiment as Experiment
import Concourse.WorkflowRun as WorkflowRun
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (checked, class, disabled, href, name, placeholder, style, type_, value)
import Html.Events exposing (onClick, onInput)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import Time
import Tooltip


type alias Model =
    Login.Model
        { workflowName : String
        , versions : Maybe (List Agent.WorkflowVersion)
        , runs : Maybe (List WorkflowRun.Summary)
        , experiments : Maybe (List Experiment.Experiment)
        , metrics : List Agent.RunMetric
        , selectedVersion : String
        , inputValues : Dict String String
        , runFilter : String
        , starting : Bool
        , startError : Bool
        , loadError : Bool
        , promotionError : Bool
        }


init : { name : String } -> ( Model, List Effect )
init { name } =
    ( { workflowName = name
      , versions = Nothing
      , runs = Nothing
      , experiments = Nothing
      , metrics = []
      , selectedVersion = ""
      , inputValues = Dict.empty
      , runFilter = "operational"
      , starting = False
      , startError = False
      , loadError = False
      , promotionError = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentWorkflowVersions name
      , FetchAgentWorkflowRuns name
      , FetchAgentExperiments
      , FetchAgentRunMetrics
      ]
    )


documentTitle : Model -> String
documentTitle model =
    model.workflowName ++ " workflow"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentWorkflowVersionsFetched workflowName (Ok versions) ->
            if workflowName /= model.workflowName then
                ( model, effects )

            else
                let
                    chosen =
                        versions
                            |> List.filter .live
                            |> List.head
                            |> Maybe.withDefault (latestVersion versions)

                    selected =
                        if model.selectedVersion == "" then
                            String.fromInt chosen.version

                        else
                            model.selectedVersion
                in
                ( { model
                    | versions = Just versions
                    , selectedVersion = selected
                    , inputValues = seedInputs chosen.signature model.inputValues
                    , loadError = False
                    , promotionError = False
                  }
                , effects
                )

        AgentWorkflowVersionsFetched workflowName (Err _) ->
            if workflowName == model.workflowName then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentWorkflowRunsFetched workflowName (Ok runs) ->
            if workflowName == model.workflowName then
                ( { model | runs = Just runs, loadError = False }, effects )

            else
                ( model, effects )

        AgentWorkflowRunsFetched workflowName (Err _) ->
            if workflowName == model.workflowName then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentExperimentsFetched (Ok experiments) ->
            ( { model | experiments = Just experiments }, effects )

        AgentRunMetricsFetched (Ok metrics) ->
            ( { model | metrics = metrics }, effects )

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

        AgentWorkflowRunFilterChanged filter ->
            ( { model | runFilter = filter }, effects )

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


latestVersion : List Agent.WorkflowVersion -> Agent.WorkflowVersion
latestVersion versions =
    versions
        |> List.sortBy (negate << .version)
        |> List.head
        |> Maybe.withDefault
            { id = 0
            , name = ""
            , version = 0
            , schemaVersion = 0
            , signatureVersion = 0
            , contentHash = ""
            , live = False
            , description = ""
            , createdBy = ""
            , createdAt = Time.millisToPosix 0
            , signature = Agent.WorkflowSignature [] []
            }


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


polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch =
            \model ->
                [ FetchAgentWorkflowVersions model.workflowName
                , FetchAgentWorkflowRuns model.workflowName
                , FetchAgentExperiments
                , FetchAgentRunMetrics
                ]
      }
    ]


hasActiveRuns : Model -> Bool
hasActiveRuns model =
    case model.runs of
        Nothing ->
            True

        Just runs ->
            List.any
                (\run -> List.member run.status [ "admitting", "running", "canceling" ])
                runs


handleDelivery : Delivery -> ET Model
handleDelivery delivery (( model, _ ) as state) =
    if hasActiveRuns model then
        Polling.handleDelivery polls delivery state

    else
        state


subscriptions : Model -> List Subscription
subscriptions model =
    if hasActiveRuns model then
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
            Routes.AgentWorkflow { name = model.workflowName }
    in
    Chrome.view session
        model
        route
        model.workflowName
        "versioned workflow function"
        [ if model.loadError then
            errorLine "Some workflow data could not be loaded; available durable data is still shown."

          else
            Html.text ""
        , signatureAndStart model
        , versionTimeline model
        , experimentLinks model
        , runsSection model
        ]


signatureAndStart : Model -> Html Message
signatureAndStart model =
    case selectedVersion model.selectedVersion model of
        Nothing ->
            loading "loading workflow signature…"

        Just version ->
            Html.section [ class "agent-workflow-signature-detail", cardStyle ]
                [ heading "Function signature"
                , Html.p [ style "font-family" "monospace", style "color" "#8a8a8a" ]
                    [ Html.text
                        ("schema v"
                            ++ String.fromInt version.schemaVersion
                            ++ " · signature v"
                            ++ String.fromInt version.signatureVersion
                            ++ " · #"
                            ++ String.left 16 version.contentHash
                        )
                    ]
                , Html.div [ style "display" "grid", style "grid-template-columns" "1fr 1fr", style "gap" "20px" ]
                    [ portList "inputs" version.signature.inputs
                    , portList "outputs" version.signature.outputs
                    ]
                , startForm model version
                ]


portList : String -> List Agent.WorkflowPort -> Html Message
portList label ports =
    Html.div [ class ("agent-workflow-" ++ label) ]
        [ Html.h3 [ style "font-size" "13px" ] [ Html.text label ]
        , if List.isEmpty ports then
            loading "none"

          else
            Html.ul []
                (List.map
                    (\signaturePort ->
                        Html.li [ style "font-family" "monospace" ]
                            [ Html.text
                                (signaturePort.name
                                    ++ " : "
                                    ++ signaturePort.typeRef
                                    ++ (if signaturePort.optional then
                                            " (optional)"

                                        else
                                            ""
                                       )
                                )
                            ]
                    )
                    ports
                )
        ]


startForm : Model -> Agent.WorkflowVersion -> Html Message
startForm model version =
    Html.div
        [ class "agent-workflow-start-form"
        , style "border-top" "1px solid #3d3c3c"
        , style "margin-top" "16px"
        , style "padding-top" "12px"
        ]
        ([ Html.h3 [ style "font-size" "13px" ] [ Html.text "Start a durable run" ]
         , Html.label []
            [ Html.text "definition version "
            , Html.select
                [ onInput AgentWorkflowVersionSelected, value model.selectedVersion ]
                (model.versions
                    |> Maybe.withDefault []
                    |> List.sortBy (negate << .version)
                    |> List.map
                        (\candidate ->
                            Html.option [ value (String.fromInt candidate.version) ]
                                [ Html.text
                                    ("v"
                                        ++ String.fromInt candidate.version
                                        ++ (if candidate.live then
                                                " · live"

                                            else
                                                ""
                                           )
                                    )
                                ]
                        )
                )
            ]
         ]
            ++ List.map (inputBinding model) version.signature.inputs
            ++ [ Html.button
                    [ type_ "button"
                    , class "agent-workflow-start"
                    , onClick AgentWorkflowStartClicked
                    , disabled (model.starting || not (canStart version.signature model.inputValues))
                    , style "margin-top" "10px"
                    , style "padding" "7px 12px"
                    ]
                    [ Html.text
                        (if model.starting then
                            "starting…"

                         else
                            "start run"
                        )
                    ]
               , if model.startError then
                    errorLine "The run was not admitted. Check the immutable input bindings and try again."

                 else
                    Html.text ""
               ]
        )


inputBinding : Model -> Agent.WorkflowPort -> Html Message
inputBinding model signaturePort =
    Html.label
        [ class "agent-workflow-input"
        , style "display" "grid"
        , style "grid-template-columns" "180px minmax(240px, 1fr)"
        , style "gap" "10px"
        , style "margin-top" "8px"
        ]
        [ Html.span [] [ Html.text (signaturePort.name ++ " · " ++ signaturePort.typeRef) ]
        , Html.input
            [ name signaturePort.name
            , value (Dict.get signaturePort.name model.inputValues |> Maybe.withDefault "")
            , placeholder "snapshot ID"
            , onInput (AgentWorkflowInputChanged signaturePort.name)
            ]
            []
        ]


versionTimeline : Model -> Html Message
versionTimeline model =
    Html.section [ class "agent-workflow-versions", cardStyle ]
        [ heading "Definition versions"
        , case model.versions of
            Nothing ->
                loading "loading versions…"

            Just versions ->
                Html.div []
                    (versions
                        |> List.sortBy (negate << .version)
                        |> List.map versionRow
                    )
        , if model.promotionError then
            errorLine "Version promotion failed."

          else
            Html.text ""
        ]


versionRow : Agent.WorkflowVersion -> Html Message
versionRow version =
    Html.div
        [ class "agent-workflow-version"
        , style "display" "grid"
        , style "grid-template-columns" "90px 1fr auto"
        , style "gap" "12px"
        , style "padding" "8px 0"
        , style "border-top" "1px solid #302f2f"
        ]
        [ Html.strong []
            [ Html.text
                ("v"
                    ++ String.fromInt version.version
                    ++ (if version.live then
                            " · live"

                        else
                            ""
                       )
                )
            ]
        , Html.span [ style "font-family" "monospace", style "font-size" "12px" ]
            [ Html.text
                ("schema "
                    ++ String.fromInt version.schemaVersion
                    ++ "/signature "
                    ++ String.fromInt version.signatureVersion
                    ++ " · #"
                    ++ String.left 16 version.contentHash
                    ++ " · by "
                    ++ version.createdBy
                )
            ]
        , if version.live then
            Html.text ""

          else
            Html.button
                [ type_ "button", onClick (AgentWorkflowPromoteClicked version.version) ]
                [ Html.text "promote live" ]
        ]


experimentLinks : Model -> Html Message
experimentLinks model =
    let
        experiments =
            model.experiments
                |> Maybe.withDefault []
                |> List.filter
                    (\experiment ->
                        List.any
                            (\variant -> variant.target.workflowName == model.workflowName)
                            experiment.definition.variants
                    )
    in
    Html.section [ class "agent-workflow-experiments", cardStyle ]
        [ heading "Experiments"
        , if List.isEmpty experiments then
            loading "no experiments target this workflow"

          else
            Html.ul []
                (List.map
                    (\experiment ->
                        Html.li []
                            [ Html.a
                                [ href (Routes.toString (Routes.AgentExperiment { id = experiment.id })) ]
                                [ Html.text experiment.definition.name ]
                            , Html.text (" · " ++ experiment.definition.state)
                            ]
                    )
                    experiments
                )
        ]


runsSection : Model -> Html Message
runsSection model =
    let
        allRuns =
            Maybe.withDefault [] model.runs

        visible =
            case model.runFilter of
                "experiment" ->
                    List.filter (\run -> run.originKind == "experiment") allRuns

                "all" ->
                    allRuns

                _ ->
                    List.filter (\run -> run.originKind /= "experiment") allRuns

        cost =
            model.metrics
                |> List.filter (\metric -> metric.workflowName == model.workflowName)
                |> List.map .costUsd
                |> List.sum
    in
    Html.section [ class "agent-workflow-runs", cardStyle ]
        [ heading "Durable runs"
        , Html.div [ style "display" "flex", style "gap" "12px", style "align-items" "center" ]
            [ filterChoice model "operational" "operational"
            , filterChoice model "experiment" "experiment"
            , filterChoice model "all" "all"
            , Html.span [ style "margin-left" "auto", style "font-family" "monospace" ]
                [ Html.text ("observed cost $" ++ String.fromFloat cost) ]
            ]
        , if model.runs == Nothing then
            loading "loading runs…"

          else if List.isEmpty visible then
            loading "no runs match this filter"

          else
            Html.div [] (List.map runRow visible)
        ]


filterChoice : Model -> String -> String -> Html Message
filterChoice model token label =
    Html.label []
        [ Html.input
            [ type_ "radio"
            , name "workflow-run-filter"
            , checked (model.runFilter == token)
            , onClick (AgentWorkflowRunFilterChanged token)
            ]
            []
        , Html.text label
        ]


runRow : WorkflowRun.Summary -> Html Message
runRow run =
    Html.div
        [ class "agent-workflow-run-row"
        , style "display" "grid"
        , style "grid-template-columns" "minmax(180px, 1fr) 130px 120px 1fr"
        , style "gap" "12px"
        , style "padding" "8px 0"
        , style "border-top" "1px solid #302f2f"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        ]
        [ Html.a
            [ href
                (Routes.toString
                    (Routes.AgentWorkflowRun
                        { workflowName = run.workflowName, id = run.id }
                    )
                )
            ]
            [ Html.text ("run #" ++ run.id) ]
        , Html.span [] [ Html.text run.status ]
        , Html.span [] [ Html.text run.originKind ]
        , Html.span []
            [ Html.text
                ("v"
                    ++ String.fromInt run.workflowVersion
                    ++ " · #"
                    ++ String.left 12 run.definitionContentHash
                )
            ]
        ]


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
