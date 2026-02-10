module AgentFeedback.VerdictPicker exposing (Model, Msg(..), init, update, view, getSelectedVerdict, getNotes)

import Html exposing (..)
import Html.Attributes exposing (..)
import Html.Events exposing (onClick, onInput)
import AgentFeedback.Types as Types exposing (Verdict(..), allVerdicts, verdictLabel, verdictToString)


-- MODEL


type alias Model =
    { selected : Maybe Verdict
    , suggested : Maybe Verdict
    , suggestedConfidence : Float
    , notes : String
    }


init : Model
init =
    { selected = Nothing
    , suggested = Nothing
    , suggestedConfidence = 0
    , notes = ""
    }


getSelectedVerdict : Model -> Maybe Verdict
getSelectedVerdict model =
    model.selected


getNotes : Model -> String
getNotes model =
    model.notes


-- UPDATE


type Msg
    = SelectVerdict Verdict
    | SetSuggestion Verdict Float
    | UpdateNotes String


update : Msg -> Model -> Model
update msg model =
    case msg of
        SelectVerdict v ->
            { model | selected = Just v }

        SetSuggestion v conf ->
            { model
                | suggested = Just v
                , suggestedConfidence = conf
                , selected =
                    case model.selected of
                        Nothing -> Just v
                        existing -> existing
            }

        UpdateNotes text ->
            { model | notes = text }


-- VIEW


view : Model -> Html Msg
view model =
    div [ class "verdict-picker" ]
        [ div [ class "verdict-label" ] [ text "Verdict:" ]
        , div [ class "verdict-buttons" ]
            (List.map (verdictButton model) allVerdicts)
        , case model.suggested of
            Just v ->
                div [ class "verdict-suggestion" ]
                    [ text ("Suggested: " ++ verdictLabel v
                        ++ " (" ++ String.fromInt (round (model.suggestedConfidence * 100)) ++ "% confidence)")
                    ]
            Nothing ->
                text ""
        , div [ class "verdict-notes" ]
            [ label [ class "notes-label" ] [ text "Notes (optional):" ]
            , textarea
                [ class "notes-input"
                , placeholder "Add context about your verdict..."
                , value model.notes
                , onInput UpdateNotes
                , rows 3
                ]
                []
            ]
        ]


verdictButton : Model -> Verdict -> Html Msg
verdictButton model verdict =
    let
        isSelected =
            model.selected == Just verdict

        isSuggested =
            model.suggested == Just verdict && not isSelected

        classes =
            "verdict-btn"
                ++ (if isSelected then " verdict-btn-selected" else "")
                ++ (if isSuggested then " verdict-btn-suggested" else "")
    in
    button
        [ class classes
        , onClick (SelectVerdict verdict)
        , type_ "button"
        ]
        [ text (verdictLabel verdict) ]
