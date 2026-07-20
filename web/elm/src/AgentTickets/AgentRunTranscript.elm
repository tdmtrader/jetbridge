module AgentTickets.AgentRunTranscript exposing
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

{-| The per-attempt TRANSCRIPT page (spectator view, S-2 / Proposal C).

Fetches the run's flight-events NDJSON (L-3 / #43 read API, served as
`application/x-ndjson` at `.../runs/:build_id/transcript`) and the ticket's run
metrics, folds the NDJSON into a collapsible turn timeline, and shows a live
totals bar drawn from the server-derived metrics rows for this build. Keeps
current on the dashboard's 5s cadence via `Polling`; stops once the run's
metrics report a terminal build status (nothing more will be appended).

There is no browser-facing SSE today (the only EventSource in the app is the
Concourse build-events stream, which carries CI log lines, not flight events);
"live" here is the 5s poll re-fetching the growing NDJSON. See the plan's Open
Decisions for the SSE-vs-poll call.
-}

import Application.Models exposing (Session)
import Concourse.Agent
import Concourse.AgentEvent as AE
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, id, style)
import Html.Events exposing (onClick)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import Tooltip
import Views.Prose
import Views.Styles


type alias Model =
    Login.Model
        { ticketId : Int
        , buildId : Int
        , transcript : AE.Transcript
        , runMetrics : List Concourse.Agent.RunMetric
        , loaded : Bool
        , loadError : Bool
        , expanded : Set Int
        , showRaw : Bool
        }


init : { id : Int, buildId : Int } -> ( Model, List Effect )
init { id, buildId } =
    ( { ticketId = id
      , buildId = buildId
      , transcript = { entries = [], skipped = 0 }
      , runMetrics = []
      , loaded = False
      , loadError = False
      , expanded = Set.empty
      , showRaw = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentRunEvents id buildId, FetchAgentTicketMetrics id ]
    )


documentTitle : Model -> String
documentTitle model =
    "Attempt · build #" ++ String.fromInt model.buildId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentRunEventsFetched _ buildId (Ok body) ->
            if buildId /= model.buildId then
                ( model, effects )

            else
                ( { model | transcript = AE.parseTranscript body, loaded = True, loadError = False }, effects )

        AgentRunEventsFetched _ buildId (Err _) ->
            if buildId /= model.buildId then
                ( model, effects )

            else
                ( { model | loaded = True, loadError = True }, effects )

        AgentTicketMetricsFetched _ (Ok fresh) ->
            let
                mine =
                    List.filter (\m -> m.buildId == model.buildId) fresh
            in
            ( { model | runMetrics = mine }, effects )

        AgentTicketMetricsFetched _ (Err _) ->
            ( model, effects )

        _ ->
            ( model, effects )


{-| Poll while the build is not yet terminal; a terminal build appends nothing
more to its NDJSON. Terminal is derived from the metrics rows' build_status
(succeeded / failed / errored / aborted). Keep polling while metrics are still
unknown.
-}
polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch =
            \model ->
                let
                    terminal =
                        not (List.isEmpty model.runMetrics)
                            && List.all (\m -> isTerminalBuild m.buildStatus) model.runMetrics
                in
                if terminal then
                    []

                else
                    [ FetchAgentRunEvents model.ticketId model.buildId, FetchAgentTicketMetrics model.ticketId ]
      }
    ]


isTerminalBuild : String -> Bool
isTerminalBuild s =
    List.member s [ "succeeded", "failed", "errored", "aborted" ]


handleDelivery : Delivery -> ET Model
handleDelivery =
    Polling.handleDelivery polls


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        TranscriptEntryToggled seq ->
            ( { model
                | expanded =
                    if Set.member seq model.expanded then
                        Set.remove seq model.expanded

                    else
                        Set.insert seq model.expanded
              }
            , effects
            )

        TranscriptRawToggled ->
            ( { model | showRaw = not model.showRaw }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> Session -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentRunTranscript { id = model.ticketId, buildId = model.buildId }
    in
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div (id "top-bar-app" :: Views.Styles.topBar False)
            [ Html.div [ style "display" "flex", style "align-items" "center" ]
                [ SideBar.sideBarIcon session
                , Html.text (documentTitle model)
                ]
            , Login.view session.userState model
            ]
        , Html.div (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar route)
            [ SideBar.view session Nothing
            , Html.div [ class "agent-transcript", style "padding" "20px", style "overflow-y" "auto", style "flex-grow" "1" ]
                (totalsBar model :: rawToggle model :: bodyView model)
            ]
        ]


totalsBar : Model -> Html Message
totalsBar model =
    let
        sumF get =
            List.foldl (\m acc -> acc + get m) 0 model.runMetrics

        sumI get =
            List.foldl (\m acc -> acc + get m) 0 model.runMetrics

        cost =
            sumF .costUsd

        turns =
            sumI .turns

        inTok =
            sumI (\m -> m.usage.inputTokens)

        outTok =
            sumI (\m -> m.usage.outputTokens)

        outcome =
            model.runMetrics
                |> List.map .outcome
                |> List.filter (\o -> o /= "")
                |> List.head
                |> Maybe.withDefault ""
    in
    Html.div [ class "transcript-totals", style "display" "flex", style "gap" "24px", style "margin-bottom" "16px", style "font-family" "monospace" ]
        [ Html.span [] [ Html.text ("turns " ++ String.fromInt turns) ]
        , Html.span [] [ Html.text ("tokens " ++ String.fromInt (inTok + outTok)) ]
        , Html.span [] [ Html.text ("$" ++ formatCost cost) ]
        , Html.span [] [ Html.text outcome ]
        ]


formatCost : Float -> String
formatCost c =
    let
        cents =
            round (c * 100)
    in
    String.fromInt (cents // 100) ++ "." ++ String.padLeft 2 '0' (String.fromInt (modBy 100 (abs cents)))


rawToggle : Model -> Html Message
rawToggle model =
    Html.button
        [ onClick TranscriptRawToggled, style "margin-bottom" "12px" ]
        [ Html.text
            (if model.showRaw then
                "Hide raw JSON"

             else
                "Show raw JSON"
            )
        ]


bodyView : Model -> List (Html Message)
bodyView model =
    if not model.loaded then
        [ Html.div [ class "transcript-loading" ] [ Html.text "Loading transcript…" ] ]

    else if model.loadError then
        [ Html.div [ class "transcript-error" ] [ Html.text "Transcript not available yet (flight-events read API returned an error)." ] ]

    else if List.isEmpty model.transcript.entries then
        [ Html.div [ class "transcript-empty" ] [ Html.text "No events recorded for this attempt." ] ]

    else
        List.map (entryView model) model.transcript.entries
            ++ skippedNote model.transcript.skipped


skippedNote : Int -> List (Html Message)
skippedNote n =
    if n <= 0 then
        []

    else
        [ Html.div [ class "transcript-skipped", style "opacity" "0.6" ]
            [ Html.text (String.fromInt n ++ " event line(s) could not be parsed and were skipped.") ]
        ]


entryView : Model -> AE.TimelineEntry -> Html Message
entryView model entry =
    let
        open =
            Set.member entry.seq model.expanded

        ( label, detail ) =
            summarize entry.body
    in
    Html.div [ class "transcript-entry", style "border-left" "2px solid #555", style "padding" "6px 12px", style "margin" "4px 0" ]
        [ Html.div [ class "transcript-entry-head", onClick (TranscriptEntryToggled entry.seq), style "cursor" "pointer", style "display" "flex", style "gap" "10px" ]
            [ Html.span [ style "opacity" "0.6", style "font-family" "monospace" ] [ Html.text entry.eventType ]
            , Html.span [] [ Html.text label ]
            ]
        , if open then
            Html.div [ class "transcript-entry-detail", style "margin-top" "6px" ]
                [ Views.Prose.view detail ]

          else
            Html.text ""
        , if model.showRaw then
            Html.pre [ class "transcript-entry-raw", style "opacity" "0.6", style "white-space" "pre-wrap" ] [ Html.text entry.raw ]

          else
            Html.text ""
        ]


summarize : AE.EntryBody -> ( String, String )
summarize body =
    case body of
        AE.StepStarted r ->
            ( "step " ++ r.stepName ++ " started", "" )

        AE.StepEnded r ->
            ( "step " ++ r.stepName ++ " ended (" ++ r.status ++ ", $" ++ formatCost r.costUsd ++ ", " ++ String.fromInt r.turns ++ " turns)", r.summary )

        AE.CostRecorded r ->
            ( r.source ++ " cost $" ++ formatCost r.costUsd ++ " (" ++ String.fromInt r.turns ++ " turns)", "" )

        AE.GateStarted r ->
            ( "gate " ++ r.gate ++ " (" ++ r.component ++ ") started", "" )

        AE.GateResulted r ->
            ( "gate " ++ r.gate ++ " (" ++ r.component ++ "): " ++ r.status
                ++ (if r.flaky then
                        " · flaky"

                    else
                        ""
                   )
            , r.summary
            )

        AE.JudgeScored r ->
            ( "judge " ++ formatCost r.total ++ "/" ++ formatCost r.maxTotal ++ " (" ++ r.model ++ ")"
            , String.join "\n" (List.map (\d -> "- " ++ d.name ++ ": " ++ formatCost d.score ++ "/" ++ formatCost d.max ++ " — " ++ d.rationale) r.dimensions)
            )

        AE.Pushed r ->
            ( "pushed " ++ r.branch ++ " @ " ++ String.left 10 r.sha, "" )

        AE.HumanAsked r ->
            ( "asked human: " ++ r.question, String.join "\n" r.options )

        AE.HumanAnswered r ->
            ( "human answered (" ++ r.answeredBy ++ ")", r.answer )

        AE.CheckpointWaited r ->
            ( "checkpoint " ++ r.checkpoint ++ " — waiting", "" )

        AE.CheckpointReleased r ->
            ( "checkpoint released (" ++ r.answeredBy ++ ")", "" )

        AE.Errored r ->
            ( "error", r.message )

        AE.ToolCalled r ->
            ( "tool " ++ r.tool, "```\n" ++ r.input ++ "\n```" )

        AE.ToolResulted r ->
            ( "tool result " ++ r.tool, "```\n" ++ r.output ++ "\n```" )

        AE.ArtifactWritten r ->
            ( "wrote " ++ r.path ++ " (" ++ String.fromInt r.bytes ++ " bytes)", "" )

        AE.Thought r ->
            ( "thinking", r.summary )

        AE.Unknown ->
            ( "(unrecognized event)", "" )
