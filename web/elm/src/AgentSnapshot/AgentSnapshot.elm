module AgentSnapshot.AgentSnapshot exposing
    ( Model
    , documentTitle
    , handleCallback
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import AgentPage.Chrome as Chrome
import AgentSnapshot.Projection as Projection
import Api.Endpoints as Endpoints
import Application.Models exposing (Session)
import Concourse.AgentReview exposing (BuildReview)
import Concourse.Snapshot as Snapshot
import Concourse.WorkflowRun exposing (RepositoryChange)
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, disabled, href, style, type_)
import Html.Events exposing (onClick)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Subscription)
import Routes
import Set exposing (Set)
import Tooltip
import UserState exposing (UserState(..))


type alias Model =
    Login.Model
        { snapshotId : String
        , teamName : String
        , detail : Maybe Snapshot.Detail
        , repositoryChange : Maybe RepositoryChange
        , loadError : Bool
        , mutationError : Bool
        , agentReviews : List BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Maybe Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
        , expandedDescriptions : Set String
        }


init : { id : String } -> ( Model, List Effect )
init { id } =
    ( { snapshotId = id
      , teamName = "main"
      , detail = Nothing
      , repositoryChange = Nothing
      , loadError = False
      , mutationError = False
      , agentReviews = []
      , agentReviewLoadError = False
      , agentReviewPanelExpanded = True
      , expandedFindings = Set.empty
      , showObservations = Nothing
      , agentReviewNotes = Dict.empty
      , verdictErrors = Set.empty
      , expandedDescriptions = Set.empty
      , isUserMenuExpanded = False
      }
    , [ FetchAgentSnapshot "main" id ]
    )


documentTitle : Model -> String
documentTitle model =
    "Snapshot " ++ model.snapshotId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentSnapshotFetched snapshotId (Ok detail) ->
            if snapshotId /= model.snapshotId then
                ( model, effects )

            else
                ( { model | detail = Just detail, loadError = False }
                , effects ++ projectionEffects model.teamName detail.manifest
                )

        AgentSnapshotFetched snapshotId (Err _) ->
            if snapshotId == model.snapshotId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentSnapshotRepositoryChangeFetched snapshotId (Ok projection) ->
            if snapshotId == model.snapshotId then
                ( { model | repositoryChange = Just projection }, effects )

            else
                ( model, effects )

        AgentSnapshotRepositoryChangeFetched snapshotId (Err _) ->
            if snapshotId == model.snapshotId then
                ( { model | loadError = True }, effects )

            else
                ( model, effects )

        AgentSnapshotReviewFetched snapshotId (Ok review) ->
            if snapshotId == model.snapshotId then
                ( { model | agentReviews = [ review ], agentReviewLoadError = False }, effects )

            else
                ( model, effects )

        AgentSnapshotReviewFetched snapshotId (Err _) ->
            if snapshotId == model.snapshotId then
                ( { model | agentReviewLoadError = True }, effects )

            else
                ( model, effects )

        AgentSnapshotPinChanged snapshotId (Ok ()) ->
            if snapshotId == model.snapshotId then
                ( { model | mutationError = False }
                , effects ++ [ FetchAgentSnapshot model.teamName model.snapshotId ]
                )

            else
                ( model, effects )

        AgentSnapshotPinChanged snapshotId (Err _) ->
            if snapshotId == model.snapshotId then
                ( { model | mutationError = True }, effects )

            else
                ( model, effects )

        AgentReviewVerdictSubmitted findingId (Ok ()) ->
            ( { model | verdictErrors = Set.remove findingId model.verdictErrors }
            , effects ++ [ FetchAgentSnapshotReview model.snapshotId ]
            )

        AgentReviewVerdictSubmitted findingId (Err _) ->
            ( { model | verdictErrors = Set.insert findingId model.verdictErrors }, effects )

        _ ->
            ( model, effects )


projectionEffects : String -> Snapshot.Manifest -> List Effect
projectionEffects teamName manifest =
    case manifest.typeRef of
        "repository-change/v1" ->
            [ FetchAgentSnapshotRepositoryChange teamName manifest.id ]

        "review/v1" ->
            [ FetchAgentSnapshotReview manifest.id ]

        _ ->
            []


update : Message -> ET Model
update message ( model, effects ) =
    case message of
        AgentSnapshotPinClicked ->
            ( { model | mutationError = False }
            , effects ++ [ PinAgentSnapshot model.teamName model.snapshotId ]
            )

        AgentSnapshotUnpinClicked ->
            ( { model | mutationError = False }
            , effects ++ [ UnpinAgentSnapshot model.teamName model.snapshotId ]
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
                        { reviewSnapshotId = Just model.snapshotId
                        , repo = params.repo
                        , commitSha = params.commitSha
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


subscriptions : List Subscription
subscriptions =
    []


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentSnapshot { id = model.snapshotId }
    in
    Chrome.view session
        model
        route
        ("Snapshot " ++ model.snapshotId)
        "immutable typed value and lineage"
        [ content session model ]


content : Session -> Model -> Html Message
content session model =
    case model.detail of
        Nothing ->
            if model.loadError then
                errorLine "Snapshot could not be loaded."

            else
                loading "loading snapshot manifest and lineage…"

        Just detail ->
            Html.div []
                [ manifestCard model detail
                , retentionCard detail
                , lineageCard detail
                , Projection.view
                    (reviewer session)
                    detail.manifest.typeRef
                    model.repositoryChange
                    model
                ]


manifestCard : Model -> Snapshot.Detail -> Html Message
manifestCard model detail =
    let
        manifest =
            detail.manifest

        contentUrl =
            Endpoints.toString [] (Endpoints.AgentSnapshotContent model.teamName model.snapshotId)
    in
    Html.section
        [ class "agent-snapshot-manifest", cardStyle ]
        [ heading "Manifest"
        , definitionRows
            [ ( "type", manifest.typeRef )
            , ( "digest", manifest.digest )
            , ( "representation", manifest.representation )
            , ( "content", manifest.contentState )
            , ( "size", String.fromFloat manifest.byteSize ++ " bytes" )
            , ( "files", String.fromFloat manifest.fileCount )
            , ( "created", manifest.createdAt )
            , ( "replicas", String.fromInt detail.replicaCount )
            ]
        , Html.div [ style "display" "flex", style "gap" "8px", style "margin-top" "12px" ]
            [ if manifest.contentState == "available" then
                Html.a [ href contentUrl, class "agent-snapshot-download", buttonStyle ] [ Html.text "download tar" ]

              else
                Html.button [ disabled True, type_ "button", buttonStyle ] [ Html.text "content expired" ]
            , Html.button [ onClick AgentSnapshotPinClicked, type_ "button", buttonStyle ] [ Html.text "pin" ]
            , Html.button [ onClick AgentSnapshotUnpinClicked, type_ "button", buttonStyle ] [ Html.text "unpin my claim" ]
            ]
        , if model.mutationError then
            errorLine "Retention claim update failed."

          else
            Html.text ""
        ]


retentionCard : Snapshot.Detail -> Html Message
retentionCard detail =
    Html.section [ class "agent-snapshot-retention", cardStyle ]
        [ heading "Retention"
        , if List.isEmpty detail.retentionClaims then
            loading "no active retention claims"

          else
            Html.ul []
                (List.map
                    (\claim ->
                        Html.li []
                            [ Html.text
                                (claim.class
                                    ++ " · "
                                    ++ claim.reason
                                    ++ (case claim.expiresAt of
                                            Just expiry ->
                                                " · expires " ++ expiry

                                            Nothing ->
                                                " · durable"
                                       )
                                )
                            ]
                    )
                    detail.retentionClaims
                )
        ]


lineageCard : Snapshot.Detail -> Html Message
lineageCard detail =
    Html.section [ class "agent-snapshot-lineage", cardStyle ]
        [ heading "Lineage"
        , Html.h3 [] [ Html.text "Produced by" ]
        , if List.isEmpty detail.productions then
            loading "no producer invocation recorded"

          else
            Html.div [] (List.map productionView detail.productions)
        , Html.h3 [] [ Html.text "Downstream consumers" ]
        , if List.isEmpty detail.downstream then
            loading "no downstream consumers recorded"

          else
            Html.ul []
                (List.map
                    (\production ->
                        Html.li []
                            [ Html.text
                                (production.kind
                                    ++ " · "
                                    ++ production.outputPort
                                    ++ " → "
                                )
                            , snapshotLink production.snapshot
                            ]
                    )
                    detail.downstream
                )
        ]


productionView : Snapshot.Production -> Html Message
productionView production =
    Html.div [ class "agent-snapshot-production", style "margin-bottom" "10px" ]
        [ Html.div []
            [ Html.strong [] [ Html.text production.kind ]
            , Html.text (" · " ++ production.createdBy ++ " · output " ++ production.outputPort)
            ]
        , producerNavigation production.build
        , if List.isEmpty production.inputs then
            Html.text ""

          else
            Html.ul []
                (List.map
                    (\input ->
                        Html.li []
                            [ Html.text (input.portName ++ " ← ")
                            , snapshotLink input.snapshot
                            ]
                    )
                    production.inputs
                )
        ]


producerNavigation : Maybe Snapshot.BuildOccurrence -> Html Message
producerNavigation maybeBuild =
    case maybeBuild of
        Nothing ->
            Html.text ""

        Just build ->
            Html.div [ class "agent-snapshot-producer-navigation", style "display" "flex", style "gap" "12px" ]
                [ Html.a
                    [ class "agent-snapshot-producer-build-link"
                    , href
                        (Routes.toString
                            (Routes.OneOffBuild
                                { id = build.id, highlight = Routes.HighlightNothing }
                            )
                        )
                    ]
                    [ Html.text ("build " ++ String.fromInt build.id) ]
                , case ( build.workflowName, build.workflowRunId ) of
                    ( Just workflowName, Just workflowRunId ) ->
                        Html.a
                            [ class "agent-snapshot-producer-workflow-run-link"
                            , href
                                (Routes.toString
                                    (Routes.AgentWorkflowRun
                                        { workflowName = workflowName, id = workflowRunId }
                                    )
                                )
                            ]
                            [ Html.text ("workflow run " ++ workflowRunId) ]

                    _ ->
                        Html.text ""
                ]


snapshotLink : { snapshot | id : String, typeRef : String } -> Html Message
snapshotLink snapshot =
    Html.a
        [ href (Routes.toString (Routes.AgentSnapshot { id = snapshot.id }))
        , class "agent-snapshot-link"
        , style "color" "#7a9ac0"
        ]
        [ Html.text (snapshot.typeRef ++ " #" ++ snapshot.id) ]


reviewer : Session -> String
reviewer session =
    case session.userState of
        UserStateLoggedIn user ->
            Login.userDisplayName user

        _ ->
            ""


definitionRows : List ( String, String ) -> Html Message
definitionRows rows =
    Html.dl [ style "display" "grid", style "grid-template-columns" "130px 1fr", style "gap" "6px 12px" ]
        (List.concatMap
            (\( label, value ) ->
                [ Html.dt [ style "color" "#8a8a8a" ] [ Html.text label ]
                , Html.dd [ style "margin" "0", style "font-family" "monospace" ] [ Html.text value ]
                ]
            )
            rows
        )


heading : String -> Html Message
heading label =
    Html.h2 [ style "font-size" "15px", style "margin" "0 0 12px" ] [ Html.text label ]


cardStyle : Html.Attribute Message
cardStyle =
    style "border" "1px solid #3d3c3c"


buttonStyle : Html.Attribute Message
buttonStyle =
    style "padding" "6px 10px"


loading : String -> Html Message
loading message =
    Html.p [ style "color" "#8a8a8a", style "font-family" "monospace", style "font-size" "12px" ] [ Html.text message ]


errorLine : String -> Html Message
errorLine message =
    Html.p [ class "agent-page-error", style "color" "#e0a44e" ] [ Html.text message ]
