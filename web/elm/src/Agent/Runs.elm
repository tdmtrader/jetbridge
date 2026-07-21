module Agent.Runs exposing (view)

import Agent.Shared as Shared
import AgentBadge
import Colors
import Concourse.Agent as Agent
import Duration
import Html exposing (Html)
import Html.Attributes exposing (class, href, style, title)
import Html.Events exposing (onClick)
import Html.Lazy
import Message.Message exposing (Message(..))
import Routes
import Set exposing (Set)
import Time
import Views.Prose


view : Time.Zone -> Maybe Time.Posix -> Set String -> Maybe (List Agent.RunMetric) -> Maybe String -> Html Message
view zone now expandedRuns maybeRuns runsError =
    Shared.sectionBlock "agent-runs" "Recent runs" <|
        case maybeRuns of
            Nothing ->
                case runsError of
                    Just message ->
                        [ Shared.errorLine message ]

                    Nothing ->
                        [ Shared.mutedLine "loading…" ]

            Just [] ->
                Shared.staleDataWarning runsError
                    ++ [ Shared.mutedLine "no agent runs recorded yet" ]

            Just runs ->
                Shared.staleDataWarning runsError
                    ++ [ Shared.mutedLine "showing the newest 100 runs (capped server-side, most recent first)"

                       -- Lazy so mint-form keystrokes (which rebuild the whole
                       -- model, and with it this page) stop re-rendering the
                       -- 100-row table; the arguments are reference-stable
                       -- until the data (or the once-a-minute clock) changes.
                       , Html.Lazy.lazy4 runsTable zone now expandedRuns runs
                       ]


runsTable : Time.Zone -> Maybe Time.Posix -> Set String -> List Agent.RunMetric -> Html Message
runsTable zone now expandedRuns runs =
    Html.table
        [ class "agent-runs-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (runsHeaderRow :: List.map (runRow zone now expandedRuns) runs)


runsHeaderRow : Html Message
runsHeaderRow =
    Html.tr []
        [ Shared.tableHeaderCell "left" "step"
        , Shared.tableHeaderCell "left" "workflow"
        , Shared.tableHeaderCell "left" "status"
        , Shared.tableHeaderCell "right" "cost"
        , Shared.tableHeaderCell "right" "tokens (in+out)"
        , Shared.tableHeaderCell "right" "turns"
        , Shared.tableHeaderCell "left" "ticket"
        , Shared.tableHeaderCell "left" "when"
        ]


{-| Stable identity for a ledger row. (build id, plan id) is the metrics
table's unique key — a build id alone is shared by sibling step rows of the
same build, and a list ordinal changes when the refetch prepends a newer run.
-}
runKey : Agent.RunMetric -> String
runKey r =
    String.fromInt r.buildId ++ ":" ++ r.planId


runRow : Time.Zone -> Maybe Time.Posix -> Set String -> Agent.RunMetric -> Html Message
runRow zone now expandedRuns r =
    Html.tr [ class "agent-run-row" ]
        [ runStepCell expandedRuns r
        , Shared.tableCell "left" (workflowRef r.workflowName r.workflowVersion)
        , runStatusCell r
        , Shared.tableCell "right" ("$" ++ Shared.formatUsd r.costUsd)
        , Shared.tableCell "right" (String.fromInt r.usage.inputTokens ++ "+" ++ String.fromInt r.usage.outputTokens)
        , Shared.tableCell "right" (String.fromInt r.turns)
        , ticketRefCell r
        , runWhenCell zone now r
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
                    , style "color" Shared.mutedColor
                    , style "max-width" "320px"
                    , style "white-space" "nowrap"
                    , style "overflow" "hidden"
                    , style "text-overflow" "ellipsis"
                    , style "cursor" "pointer"
                    , title r.summary
                    ]
                    (Html.text "▸ " :: Views.Prose.inline r.summary)
                ]
    in
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" Shared.rowBorder
        , style "vertical-align" "top"
        ]
        (Html.a
            [ href (buildHref r.buildId)
            , title ("open build #" ++ String.fromInt r.buildId)
            , style "font-weight" "700"
            , style "color" "#7a9ac0"
            , style "text-decoration" "none"
            ]
            [ Html.text r.stepName ]
            :: summaryBlock
        )


{-| Href to a run's build page, `/builds/<id>`, so every ledger row links to the
build it measured. Built through Routes so it stays in step with the router.
-}
buildHref : Int -> String
buildHref buildId =
    Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing })


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
        , style "border-bottom" Shared.rowBorder
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


{-| A linked "#N" back to the originating ticket for a ticket-backed run, or a
plain "CI" when there is no ticket.
-}
ticketRefCell : Agent.RunMetric -> Html Message
ticketRefCell r =
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" Shared.rowBorder
        ]
        [ case r.ticketId of
            Just t ->
                Html.a
                    [ href (Routes.toString (Routes.AgentTicket { id = t }))
                    , title ("Agent ticket #" ++ String.fromInt t)
                    , style "color" "#7a9ac0"
                    , style "text-decoration" "none"
                    ]
                    [ Html.text ("#" ++ String.fromInt t) ]

            Nothing ->
                Html.span
                    [ title "Continuous-integration review run — not tied to an agent ticket"
                    , style "color" Shared.subtleColor
                    , style "cursor" "help"
                    ]
                    [ Html.text "CI" ]
        ]


{-| The "when" column: a relative "N ago" label (matching the ticket queue's
elapsed labels), with the exact local timestamp on hover. Falls back to the
absolute time as the visible label until the clock has first ticked (or when the
timestamp is absent / in the future), so the column is never blank.
-}
runWhenCell : Time.Zone -> Maybe Time.Posix -> Agent.RunMetric -> Html Message
runWhenCell zone now r =
    let
        absolute =
            Shared.formatPosix zone (Just (Shared.secondsToPosix r.createdAt))

        label =
            relativeWhen now r.createdAt
                |> Maybe.withDefault absolute
    in
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" Shared.rowBorder
        , title absolute
        ]
        [ Html.text label ]


{-| Relative "N ago" from a run's created-at epoch (seconds) to the live clock.
Nothing when the clock hasn't ticked yet, the timestamp is unknown, or it is in
the future — the caller then shows the absolute time instead.
-}
relativeWhen : Maybe Time.Posix -> Int -> Maybe String
relativeWhen maybeNow createdAt =
    case maybeNow of
        Just now ->
            if createdAt > 0 then
                let
                    elapsed =
                        Duration.between (Shared.secondsToPosix createdAt) now
                in
                if elapsed >= 0 then
                    Just (Duration.format elapsed ++ " ago")

                else
                    Nothing

            else
                Nothing

        Nothing ->
            Nothing
