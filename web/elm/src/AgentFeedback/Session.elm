module AgentFeedback.Session exposing (Model, Msg(..), init, update, view)

import Html exposing (..)
import Html.Attributes exposing (..)
import Html.Events exposing (onClick)
import Http
import AgentFeedback.Types as Types exposing
    ( Finding
    , FeedbackRecord
    , Verdict
    , ReviewRef
    , verdictToString
    , verdictFromString
    , verdictLabel
    )
import AgentFeedback.FindingCard as FindingCard
import AgentFeedback.ChatPanel as ChatPanel
import AgentFeedback.VerdictPicker as VerdictPicker
import AgentFeedback.Api as Api


-- MODEL


type alias Model =
    { reviewRef : ReviewRef
    , findings : List Finding
    , activeFindingIndex : Int
    , reviewed : List String
    , chatPanel : ChatPanel.Model
    , verdictPicker : VerdictPicker.Model
    , submitting : Bool
    , error : Maybe String
    }


init : ReviewRef -> List Finding -> Model
init ref findings =
    let
        initialMessages =
            case List.head findings of
                Just f ->
                    [ { role = "system"
                      , content = "Finding " ++ f.id ++ ": " ++ f.title
                          ++ " (" ++ f.severity ++ " " ++ f.category ++ ")"
                          ++ " in " ++ f.file ++ ":" ++ String.fromInt f.line
                      }
                    ]
                Nothing ->
                    []
    in
    { reviewRef = ref
    , findings = findings
    , activeFindingIndex = 0
    , reviewed = []
    , chatPanel = ChatPanel.init initialMessages
    , verdictPicker = VerdictPicker.init
    , submitting = False
    , error = Nothing
    }


-- UPDATE


type Msg
    = SelectFinding Int
    | ChatMsg ChatPanel.Msg
    | VerdictMsg VerdictPicker.Msg
    | SubmitVerdict
    | SubmitResult (Result Http.Error String)
    | ClassifyResult (Result Http.Error Api.ClassifyResult)
    | RequestClassify


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        SelectFinding idx ->
            let
                finding = List.drop idx model.findings |> List.head
                initialMessages =
                    case finding of
                        Just f ->
                            [ { role = "system"
                              , content = "Finding " ++ f.id ++ ": " ++ f.title
                                  ++ " (" ++ f.severity ++ " " ++ f.category ++ ")"
                                  ++ " in " ++ f.file ++ ":" ++ String.fromInt f.line
                              }
                            ]
                        Nothing -> []
            in
            ( { model
                | activeFindingIndex = idx
                , chatPanel = ChatPanel.init initialMessages
                , verdictPicker = VerdictPicker.init
                , error = Nothing
              }
            , Cmd.none
            )

        ChatMsg chatMsg ->
            let
                updatedChat = ChatPanel.update chatMsg model.chatPanel
                shouldClassify =
                    case chatMsg of
                        ChatPanel.SendMessage -> True
                        _ -> False
                cmd =
                    if shouldClassify then
                        case List.reverse (ChatPanel.getConversation updatedChat) |> List.head of
                            Just lastMsg ->
                                if lastMsg.role == "human" then
                                    Api.classifyVerdict lastMsg.content (ClassifyResult)
                                else
                                    Cmd.none
                            Nothing -> Cmd.none
                    else
                        Cmd.none
            in
            ( { model | chatPanel = updatedChat }, cmd )

        VerdictMsg verdictMsg ->
            ( { model | verdictPicker = VerdictPicker.update verdictMsg model.verdictPicker }
            , Cmd.none
            )

        RequestClassify ->
            case List.reverse (ChatPanel.getConversation model.chatPanel) |> List.head of
                Just lastMsg ->
                    ( model, Api.classifyVerdict lastMsg.content ClassifyResult )
                Nothing ->
                    ( model, Cmd.none )

        ClassifyResult (Ok result) ->
            case verdictFromString result.verdict of
                Just v ->
                    let
                        updatedPicker =
                            VerdictPicker.update
                                (VerdictPicker.SetSuggestion v result.confidence)
                                model.verdictPicker
                        classifyMsg =
                            "I'd classify this as: " ++ verdictLabel v
                                ++ " (" ++ String.fromInt (round (result.confidence * 100))
                                ++ "% confidence). Agree?"
                        updatedChat =
                            ChatPanel.update (ChatPanel.AddSystemMessage classifyMsg) model.chatPanel
                    in
                    ( { model | verdictPicker = updatedPicker, chatPanel = updatedChat }, Cmd.none )

                Nothing ->
                    ( model, Cmd.none )

        ClassifyResult (Err _) ->
            ( model, Cmd.none )

        SubmitVerdict ->
            case ( VerdictPicker.getSelectedVerdict model.verdictPicker, activeFinding model ) of
                ( Just verdict, Just finding ) ->
                    let
                        record : FeedbackRecord
                        record =
                            { reviewRef = model.reviewRef
                            , findingId = finding.id
                            , findingType = finding.findingType
                            , verdict = verdict
                            , confidence = 1.0
                            , notes = VerdictPicker.getNotes model.verdictPicker
                            , conversation = ChatPanel.getConversation model.chatPanel
                            , reviewer = ""
                            , source = "interactive"
                            }
                    in
                    ( { model | submitting = True }, Api.submitFeedback record SubmitResult )

                _ ->
                    ( { model | error = Just "Select a verdict first" }, Cmd.none )

        SubmitResult (Ok _) ->
            let
                findingId =
                    activeFinding model
                        |> Maybe.map .id
                        |> Maybe.withDefault ""
                nextIndex =
                    Basics.min (model.activeFindingIndex + 1) (List.length model.findings - 1)
            in
            ( { model
                | submitting = False
                , reviewed = findingId :: model.reviewed
                , error = Nothing
              }
            , Cmd.none
            )
                |> (\( m, c ) ->
                        if model.activeFindingIndex < List.length model.findings - 1 then
                            update (SelectFinding nextIndex) m
                                |> Tuple.mapSecond (\cmd -> Cmd.batch [ c, cmd ])
                        else
                            ( m, c )
                   )

        SubmitResult (Err _) ->
            ( { model | submitting = False, error = Just "Failed to submit feedback" }, Cmd.none )


activeFinding : Model -> Maybe Finding
activeFinding model =
    List.drop model.activeFindingIndex model.findings |> List.head


-- VIEW


view : Model -> Html Msg
view model =
    div [ class "feedback-session" ]
        [ sidebar model
        , mainPanel model
        ]


sidebar : Model -> Html Msg
sidebar model =
    div [ class "feedback-sidebar" ]
        [ h2 [ class "sidebar-title" ] [ text "Findings" ]
        , div [ class "sidebar-progress" ]
            [ text (String.fromInt (List.length model.reviewed)
                ++ " / "
                ++ String.fromInt (List.length model.findings)
                ++ " reviewed")
            ]
        , div [ class "finding-list" ]
            (List.indexedMap (findingListItem model) model.findings)
        ]


findingListItem : Model -> Int -> Finding -> Html Msg
findingListItem model idx finding =
    let
        isActive = idx == model.activeFindingIndex
        isReviewed = List.member finding.id model.reviewed
        statusClass =
            if isReviewed then "reviewed"
            else if isActive then "active"
            else "pending"
    in
    div
        [ class ("finding-list-item finding-list-item-" ++ statusClass)
        , onClick (SelectFinding idx)
        ]
        [ span [ class ("severity-dot severity-" ++ finding.severity) ] []
        , span [ class "finding-list-title" ] [ text finding.title ]
        , if isReviewed then
            span [ class "finding-list-check" ] [ text "done" ]
          else
            text ""
        ]


mainPanel : Model -> Html Msg
mainPanel model =
    case activeFinding model of
        Just finding ->
            div [ class "feedback-main" ]
                [ FindingCard.view finding
                , Html.map ChatMsg (ChatPanel.view model.chatPanel)
                , Html.map VerdictMsg (VerdictPicker.view model.verdictPicker)
                , div [ class "submit-bar" ]
                    [ case model.error of
                        Just err ->
                            div [ class "submit-error" ] [ text err ]
                        Nothing ->
                            text ""
                    , button
                        [ class "submit-btn"
                        , onClick SubmitVerdict
                        , disabled model.submitting
                        ]
                        [ text (if model.submitting then "Submitting..." else "Submit Verdict") ]
                    ]
                ]

        Nothing ->
            div [ class "feedback-main feedback-empty" ]
                [ text "No findings to review." ]
