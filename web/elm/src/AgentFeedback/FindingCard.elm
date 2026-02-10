module AgentFeedback.FindingCard exposing (view)

import Html exposing (..)
import Html.Attributes exposing (..)
import AgentFeedback.Types exposing (Finding)


view : Finding -> Html msg
view finding =
    div [ class "finding-card" ]
        [ div [ class "finding-header" ]
            [ severityBadge finding.severity
            , categoryTag finding.category
            , span [ class "finding-id" ] [ text finding.id ]
            ]
        , h3 [ class "finding-title" ] [ text finding.title ]
        , div [ class "finding-location" ]
            [ text (finding.file ++ ":" ++ String.fromInt finding.line) ]
        , if finding.description /= "" then
            div [ class "finding-description" ] [ text finding.description ]
          else
            text ""
        , if finding.testCode /= "" then
            div [ class "finding-test-code" ]
                [ div [ class "code-label" ] [ text "Proving Test:" ]
                , pre [ class "code-block" ] [ code [] [ text finding.testCode ] ]
                ]
          else
            text ""
        ]


severityBadge : String -> Html msg
severityBadge severity =
    span
        [ class ("severity-badge severity-" ++ severity) ]
        [ text (String.toUpper severity) ]


categoryTag : String -> Html msg
categoryTag category =
    span [ class "category-tag" ] [ text category ]
