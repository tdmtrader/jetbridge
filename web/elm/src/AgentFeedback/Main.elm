module AgentFeedback.Main exposing (main)

import Browser
import Html exposing (..)
import Html.Attributes exposing (..)
import Html.Events exposing (onClick)
import Http
import Json.Decode as Decode
import AgentFeedback.Types as Types exposing (Finding, ReviewRef, findingDecoder)
import AgentFeedback.Session as Session
import AgentFeedback.Summary as Summary


-- MAIN


main : Program Flags Model Msg
main =
    Browser.element
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        }


type alias Flags =
    { commit : String
    , repo : String
    }


-- MODEL


type Page
    = LoadingPage
    | SessionPage Session.Model
    | SummaryPage Summary.Model
    | ErrorPage String


type alias Model =
    { page : Page
    , commit : String
    , repo : String
    }


init : Flags -> ( Model, Cmd Msg )
init flags =
    ( { page = LoadingPage
      , commit = flags.commit
      , repo = flags.repo
      }
    , fetchFindings flags.commit
    )


-- UPDATE


type Msg
    = GotFindings (Result Http.Error (List Finding))
    | SessionMsg Session.Msg
    | SummaryMsg Summary.Msg
    | ShowSummary
    | ShowSession


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        GotFindings (Ok findings) ->
            let
                ref : ReviewRef
                ref =
                    { repo = model.repo
                    , commit = model.commit
                    , reviewTimestamp = ""
                    }
            in
            ( { model | page = SessionPage (Session.init ref findings) }
            , Cmd.none
            )

        GotFindings (Err _) ->
            ( { model | page = ErrorPage "Failed to load findings" }
            , Cmd.none
            )

        SessionMsg sessionMsg ->
            case model.page of
                SessionPage sessionModel ->
                    let
                        ( newSession, cmd ) = Session.update sessionMsg sessionModel
                    in
                    ( { model | page = SessionPage newSession }
                    , Cmd.map SessionMsg cmd
                    )

                _ ->
                    ( model, Cmd.none )

        SummaryMsg summaryMsg ->
            case model.page of
                SummaryPage summaryModel ->
                    let
                        ( newSummary, cmd ) = Summary.update summaryMsg summaryModel
                    in
                    ( { model | page = SummaryPage newSummary }
                    , Cmd.map SummaryMsg cmd
                    )

                _ ->
                    ( model, Cmd.none )

        ShowSummary ->
            let
                ( summaryModel, cmd ) = Summary.init
            in
            ( { model | page = SummaryPage summaryModel }
            , Cmd.map SummaryMsg cmd
            )

        ShowSession ->
            ( model, fetchFindings model.commit )


fetchFindings : String -> Cmd Msg
fetchFindings commit =
    Http.send GotFindings
        (Http.get
            ("/api/v1/agent/reviews/" ++ commit ++ "/findings")
            (Decode.list findingDecoder)
        )


-- VIEW


view : Model -> Html Msg
view model =
    div [ class "agent-feedback-app" ]
        [ navbar model
        , div [ class "feedback-content" ]
            [ case model.page of
                LoadingPage ->
                    div [ class "loading" ] [ text "Loading findings..." ]

                SessionPage sessionModel ->
                    Html.map SessionMsg (Session.view sessionModel)

                SummaryPage summaryModel ->
                    Html.map SummaryMsg (Summary.view summaryModel)

                ErrorPage err ->
                    div [ class "error-page" ]
                        [ h2 [] [ text "Error" ]
                        , p [] [ text err ]
                        ]
            ]
        ]


navbar : Model -> Html Msg
navbar model =
    div [ class "feedback-navbar" ]
        [ h1 [ class "navbar-title" ] [ text "Agent Feedback" ]
        , div [ class "navbar-tabs" ]
            [ button
                [ class (navClass model SessionPage_)
                , onClick ShowSession
                ]
                [ text "Review" ]
            , button
                [ class (navClass model SummaryPage_)
                , onClick ShowSummary
                ]
                [ text "Summary" ]
            ]
        , div [ class "navbar-info" ]
            [ text ("Commit: " ++ String.left 8 model.commit) ]
        ]


type PageType
    = SessionPage_
    | SummaryPage_


navClass : Model -> PageType -> String
navClass model pageType =
    let
        isActive =
            case ( model.page, pageType ) of
                ( SessionPage _, SessionPage_ ) -> True
                ( SummaryPage _, SummaryPage_ ) -> True
                _ -> False
    in
    "nav-tab" ++ (if isActive then " nav-tab-active" else "")
