module AgentExperiment.Scorecard exposing (view)

import Concourse.Experiment as Experiment
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Message.Message exposing (Message)


view : Experiment.Scorecard -> Html Message
view scorecard =
    Html.section [ class "agent-experiment-scorecard", style "margin-top" "20px" ]
        [ Html.h2 [ style "font-size" "15px" ] [ Html.text "Scorecard" ]
        , Html.p [ class "agent-scorecard-policy", style "color" "#8a8a8a", style "font-size" "12px" ]
            [ Html.text
                "Recommendations are server-derived from paired repetitions, ≥80% valid coverage, 95% bootstrap confidence, bounded platform-error rate, and passing negative controls; Jetbridge never auto-promotes."
            ]
        , variantTable scorecard
        , comparisonTable scorecard
        , if List.any (\comparison -> comparison.recommendation == "insufficient_evidence") scorecard.comparisons then
            Html.div [ class "agent-scorecard-insufficient" ]
                [ Html.h3 [ style "font-size" "13px" ] [ Html.text "Insufficient evidence" ]
                , Html.p [ style "color" "#e0a44e" ]
                    [ Html.text "The recommendation is intentionally withheld. Failed conditions and every raw cell remain visible below." ]
                ]

          else
            Html.text ""
        , rawCells scorecard.cells
        ]


variantTable : Experiment.Scorecard -> Html Message
variantTable scorecard =
    Html.table [ class "agent-scorecard-variants", tableStyle ]
        [ Html.thead []
            [ Html.tr []
                (List.map header
                    [ "variant", "coverage", "platform errors", "budget skipped", "negative controls", "cost p05/median/p95", "latency p05/median/p95", "tokens median", "interventions median" ]
                )
            ]
        , Html.tbody [] (List.map (variantRow scorecard.control) scorecard.variants)
        ]


variantRow : String -> Experiment.VariantScore -> Html Message
variantRow control variant =
    Html.tr [ class "agent-scorecard-variant" ]
        [ cell
            (variant.label
                ++ (if variant.label == control then
                        " · control"

                    else
                        ""
                   )
            )
        , cell
            (String.fromFloat (variant.validCoverage * 100)
                ++ "% ("
                ++ String.fromInt variant.validCells
                ++ "/"
                ++ String.fromInt variant.expectedCells
                ++ ")"
            )
        , cell
            (String.fromInt variant.platformErrors
                ++ " · "
                ++ String.fromFloat (variant.platformErrorRate * 100)
                ++ "%"
            )
        , cell (String.fromInt variant.budgetSkipped)
        , cell
            (if variant.negativeControlsPass then
                "pass"

             else
                "fail"
            )
        , distributionCell "$" variant.costUsd
        , distributionCell "" variant.latencySeconds
        , cell (String.fromFloat variant.tokens.median)
        , cell (String.fromFloat variant.humanInterventions.median)
        ]


distributionCell : String -> Experiment.Distribution -> Html Message
distributionCell prefix distribution =
    cell
        (prefix
            ++ String.fromFloat distribution.p05
            ++ " / "
            ++ prefix
            ++ String.fromFloat distribution.median
            ++ " / "
            ++ prefix
            ++ String.fromFloat distribution.p95
        )


comparisonTable : Experiment.Scorecard -> Html Message
comparisonTable scorecard =
    if List.isEmpty scorecard.comparisons then
        Html.p [ style "color" "#8a8a8a" ] [ Html.text "No paired metric comparison is available yet." ]

    else
        Html.table [ class "agent-scorecard-comparisons", tableStyle ]
            [ Html.thead []
                [ Html.tr []
                    (List.map header
                        [ "variant", "metric", "paired", "wins/ties/losses", "mean Δ", "95% interval", "recommendation", "failed conditions" ]
                    )
                ]
            , Html.tbody [] (List.map comparisonRow scorecard.comparisons)
            ]


comparisonRow : Experiment.Comparison -> Html Message
comparisonRow comparison =
    Html.tr [ class "agent-scorecard-comparison" ]
        [ cell comparison.variant
        , cell (comparison.metric ++ " · " ++ comparison.direction)
        , cell (String.fromInt comparison.pairedCount)
        , cell
            (String.fromInt comparison.wins
                ++ "/"
                ++ String.fromInt comparison.ties
                ++ "/"
                ++ String.fromInt comparison.losses
            )
        , cell (String.fromFloat comparison.meanDelta)
        , cell
            (String.fromFloat comparison.confidenceLow
                ++ "…"
                ++ String.fromFloat comparison.confidenceHigh
            )
        , cell
            (case comparison.winner of
                Just winner ->
                    comparison.recommendation ++ " · " ++ winner

                Nothing ->
                    comparison.recommendation
            )
        , cell (String.join "; " comparison.failedConditions)
        ]


rawCells : List Experiment.Cell -> Html Message
rawCells cells =
    Html.div [ class "agent-scorecard-raw-cells", style "margin-top" "18px" ]
        [ Html.h3 [ style "font-size" "13px" ] [ Html.text "Raw cells" ]
        , if List.isEmpty cells then
            Html.p [ style "color" "#8a8a8a" ] [ Html.text "No raw scorecard cells were returned." ]

          else
            Html.table [ tableStyle ]
                [ Html.thead []
                    [ Html.tr []
                        (List.map header
                            [ "cell", "variant", "fixture", "role", "rep", "status", "measurements", "cost", "latency", "tokens", "human" ]
                        )
                    ]
                , Html.tbody [] (List.map rawCellRow cells)
                ]
        ]


rawCellRow : Experiment.Cell -> Html Message
rawCellRow raw =
    Html.tr [ class "agent-scorecard-raw-cell" ]
        [ cell raw.id
        , cell raw.variant
        , cell raw.fixture
        , cell raw.role
        , cell (String.fromInt raw.repetition)
        , cell raw.status
        , cell
            (raw.measurements
                |> List.map
                    (\measurement ->
                        measurement.name
                            ++ "="
                            ++ String.fromFloat measurement.value
                            ++ measurement.unit
                    )
                |> String.join ", "
            )
        , cell ("$" ++ String.fromFloat raw.costUsd)
        , cell (String.fromFloat raw.latencySeconds ++ "s")
        , cell (String.fromInt (raw.inputTokens + raw.outputTokens))
        , cell (String.fromInt raw.humanInterventions)
        ]


tableStyle : Html.Attribute Message
tableStyle =
    style "width" "100%"


header : String -> Html Message
header label =
    Html.th
        [ style "text-align" "left"
        , style "padding" "6px 8px"
        , style "border-bottom" "1px solid #3d3c3c"
        , style "font-size" "11px"
        ]
        [ Html.text label ]


cell : String -> Html Message
cell content =
    Html.td
        [ style "padding" "6px 8px"
        , style "border-bottom" "1px solid #302f2f"
        , style "font-family" "monospace"
        , style "font-size" "11px"
        , style "vertical-align" "top"
        ]
        [ Html.text content ]
