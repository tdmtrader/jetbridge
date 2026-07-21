module Agent.Spend exposing (view)

import Agent.Shared as Shared
import Colors
import Concourse.Agent as Agent
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Message.Message exposing (Message)


view :
    { costRollup : Maybe Agent.CostRollup
    , costError : Maybe String
    , unattributedUsd : Maybe Float
    }
    -> Html Message
view { costRollup, costError, unattributedUsd } =
    Shared.sectionBlock "agent-costs" "Costs" <|
        case costRollup of
            Nothing ->
                case costError of
                    Just message ->
                        [ Shared.errorLine message ]

                    Nothing ->
                        [ Shared.mutedLine "loading…" ]

            Just rollup ->
                Shared.staleDataWarning costError
                    ++ [ costSummaryLine rollup.summary
                       , dailyCapGauge rollup.summary
                       , costTable rollup.rows
                       , unattributedLine unattributedUsd
                       ]


costSummaryLine : Agent.CostSummary -> Html Message
costSummaryLine summary =
    let
        spent =
            "today (UTC day): $" ++ Shared.formatUsd summary.dailySpentUsd ++ " spent"

        cap =
            if summary.dailyCapUsd > 0 then
                " / $"
                    ++ Shared.formatUsd summary.dailyCapUsd
                    ++ " cap ($"
                    ++ Shared.formatUsd summary.dailyRemainingUsd
                    ++ " left)"

            else
                ""

        exhausted =
            if summary.dailyExhausted then
                [ Html.span
                    [ class "agent-budget-exhausted"
                    , style "margin-left" "8px"
                    , style "color" Shared.amberColor
                    , style "font-weight" "700"
                    ]
                    [ Html.text "budget exhausted" ]
                ]

            else
                []
    in
    Html.div
        [ style "margin" "0 0 12px 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.span [] [ Html.text (spent ++ cap) ] :: exhausted)


{-| A slim gauge of today's spend against the daily cap, mirroring the ticket
budget bar. Rendered only when a cap is configured; turns amber once the cap is
exhausted. Complements — does not replace — the exact-text summary line above.
-}
dailyCapGauge : Agent.CostSummary -> Html Message
dailyCapGauge summary =
    if summary.dailyCapUsd <= 0 then
        -- An uncapped ledger deserves an explicit statement, not silence:
        -- otherwise "no gauge" is indistinguishable from "under the cap".
        Html.div
            [ class "agent-daily-cap-none"
            , style "margin" "0 0 12px 0"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" Shared.subtleColor
            ]
            [ Html.text "No daily cap set. Caps are set at deploy time today." ]

    else
        let
            pct =
                min 100 (summary.dailySpentUsd / summary.dailyCapUsd * 100)
        in
        Html.div
            [ class "agent-daily-cap-gauge"
            , style "max-width" "320px"
            , style "margin" "0 0 12px 0"
            , style "height" "6px"
            , style "background" "#3d3c3c"
            ]
            [ Html.div
                [ style "height" "6px"
                , style "width" (String.fromFloat pct ++ "%")
                , style "background"
                    (if summary.dailyExhausted then
                        Shared.amberColor

                     else
                        "#7aa37a"
                    )
                ]
                []
            ]


{-| Spend the by-ticket rollup reports under no ticket at all (the CI review
runs, harvest pushes, platform housekeeping — the rollup's empty-string key).
It is a large share of real spend and would otherwise appear nowhere.
-}
unattributedLine : Maybe Float -> Html Message
unattributedLine maybeUsd =
    case maybeUsd of
        Just usd ->
            if usd > 0 then
                Html.div
                    [ class "agent-unattributed-cost"
                    , style "margin" "8px 0 0 0"
                    , style "font-family" "monospace"
                    , style "font-size" "12px"
                    , style "color" Shared.mutedColor
                    ]
                    [ Html.text ("unattributed (no ticket, all time): $" ++ Shared.formatUsd usd) ]

            else
                Html.text ""

        Nothing ->
            Html.text ""


costTable : List Agent.CostRow -> Html Message
costTable rows =
    if List.isEmpty rows then
        Shared.mutedLine "no cost records yet"

    else
        Html.table
            [ class "agent-costs-table"
            , style "border-collapse" "collapse"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" Colors.text
            ]
            (costHeaderRow :: List.map costRow rows)


costHeaderRow : Html Message
costHeaderRow =
    Html.tr []
        [ Shared.tableHeaderCell "left" "day (UTC)"
        , Shared.tableHeaderCell "right" "entries"
        , Shared.tableHeaderCell "right" "tokens (in+out)"
        , Shared.tableHeaderCell "right" "turns"
        , Shared.tableHeaderCell "right" "cost"
        ]


costRow : Agent.CostRow -> Html Message
costRow r =
    Html.tr [ class "agent-cost-row" ]
        [ Shared.tableCell "left" r.key
        , Shared.tableCell "right" (String.fromInt r.entries)
        , Shared.tableCell "right" (String.fromInt r.inputTokens ++ "+" ++ String.fromInt r.outputTokens)
        , Shared.tableCell "right" (String.fromInt r.turns)
        , Shared.tableCell "right" ("$" ++ Shared.formatUsd r.costUsd)
        ]
