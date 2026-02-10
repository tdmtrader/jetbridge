module AgentFeedback.Summary exposing (Model, Msg(..), init, update, view)

import Html exposing (..)
import Html.Attributes exposing (..)
import Http
import AgentFeedback.Types as Types exposing (VerdictSummary)
import AgentFeedback.Api as Api


-- MODEL


type alias Model =
    { summary : Maybe VerdictSummary
    , loading : Bool
    , error : Maybe String
    }


init : ( Model, Cmd Msg )
init =
    ( { summary = Nothing
      , loading = True
      , error = Nothing
      }
    , Api.fetchSummary GotSummary
    )


-- UPDATE


type Msg
    = GotSummary (Result Http.Error VerdictSummary)
    | Refresh


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        GotSummary (Ok summary) ->
            ( { model | summary = Just summary, loading = False, error = Nothing }
            , Cmd.none
            )

        GotSummary (Err _) ->
            ( { model | loading = False, error = Just "Failed to load summary" }
            , Cmd.none
            )

        Refresh ->
            ( { model | loading = True }, Api.fetchSummary GotSummary )


-- VIEW


view : Model -> Html Msg
view model =
    div [ class "feedback-summary" ]
        [ h2 [] [ text "Feedback Summary" ]
        , if model.loading then
            div [ class "loading" ] [ text "Loading..." ]
          else
            case model.error of
                Just err ->
                    div [ class "error" ] [ text err ]

                Nothing ->
                    case model.summary of
                        Just summary ->
                            summaryView summary

                        Nothing ->
                            div [] [ text "No data available" ]
        ]


summaryView : VerdictSummary -> Html Msg
summaryView summary =
    div [ class "summary-content" ]
        [ div [ class "summary-stats" ]
            [ statCard "Total Reviewed" (String.fromInt summary.total)
            , statCard "Accuracy Rate" (percentStr summary.accuracyRate)
            , statCard "False Positive Rate" (percentStr summary.fpRate)
            ]
        , div [ class "summary-breakdown" ]
            [ h3 [] [ text "By Verdict" ]
            , div [ class "breakdown-table" ]
                (List.map verdictRow summary.byVerdict)
            ]
        ]


statCard : String -> String -> Html msg
statCard label val =
    div [ class "stat-card" ]
        [ div [ class "stat-value" ] [ text val ]
        , div [ class "stat-label" ] [ text label ]
        ]


verdictRow : ( String, Int ) -> Html msg
verdictRow ( verdict, count ) =
    div [ class "verdict-row" ]
        [ span [ class "verdict-name" ] [ text verdict ]
        , span [ class "verdict-count" ] [ text (String.fromInt count) ]
        ]


percentStr : Float -> String
percentStr f =
    String.fromInt (round (f * 100)) ++ "%"
