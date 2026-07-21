module Agent.Workflows exposing (view)

import Agent.Shared as Shared
import Colors
import Concourse.Agent as Agent
import Html exposing (Html)
import Html.Attributes exposing (class, href, style)
import Message.Message exposing (Message)
import Routes


view : Maybe (List Agent.WorkflowSummary) -> Maybe String -> Html Message
view maybeWorkflows workflowsError =
    Shared.sectionBlock "agent-workflows" "Workflows" <|
        case maybeWorkflows of
            Nothing ->
                case workflowsError of
                    Just message ->
                        [ Shared.errorLine message ]

                    Nothing ->
                        [ Shared.mutedLine "loading…" ]

            Just [] ->
                Shared.staleDataWarning workflowsError
                    ++ [ Shared.mutedLine "no workflow definitions — import one with: fly agent workflows import" ]

            Just workflows ->
                Shared.staleDataWarning workflowsError
                    ++ [ Html.div [ class "agent-workflows" ] (List.map workflowRow workflows) ]


workflowRow : Agent.WorkflowSummary -> Html Message
workflowRow w =
    Html.div
        [ class "agent-workflow-row"
        , style "display" "flex"
        , style "align-items" "baseline"
        , style "gap" "12px"
        , style "padding" "8px 0"
        , style "border-bottom" Shared.rowBorder
        ]
        [ Html.div [ style "flex" "1", style "min-width" "0" ]
            [ Html.div []
                (Html.a
                    [ style "font-weight" "700"
                    , style "color" Colors.text
                    , href (Routes.toString (Routes.AgentWorkflow { name = w.name }))
                    ]
                    [ Html.text w.name ]
                    :: workflowPills w
                    ++ (if w.hidden then
                            [ Shared.pill "agent-workflow-deprecated"
                                { bg = "#4f2e2e", fg = "#df9f9f" }
                                "deprecated"
                            ]

                        else
                            []
                       )
                )
            , Html.div
                [ style "font-size" "12px", style "color" Shared.mutedColor ]
                [ Html.text w.description ]
            ]
        , Html.div
            [ style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" Shared.subtleColor
            , style "text-align" "right"
            , style "white-space" "nowrap"
            ]
            [ liveVersionLine w
            , Html.div [] [ Html.text ("latest v" ++ String.fromInt w.latestVersion) ]
            , Html.div [] [ Html.text ("#" ++ String.left 12 w.contentHash) ]
            ]
        ]


workflowPills : Agent.WorkflowSummary -> List (Html Message)
workflowPills w =
    let
        livePill =
            if w.liveVersion > 0 then
                [ Shared.pill "agent-workflow-live"
                    { bg = "#2e4f2e", fg = "#9fdf9f" }
                    "live"
                ]

            else
                []

        candidatePill =
            if w.latestVersion > w.liveVersion then
                [ Shared.pill "agent-workflow-candidate"
                    { bg = Colors.background, fg = Shared.mutedColor }
                    ("candidate v" ++ String.fromInt w.latestVersion)
                ]

            else
                []
    in
    livePill ++ candidatePill


liveVersionLine : Agent.WorkflowSummary -> Html Message
liveVersionLine w =
    if w.liveVersion == 0 then
        Html.div [ style "color" Shared.subtleColor ] [ Html.text "no live version" ]

    else
        Html.div [] [ Html.text ("v" ++ String.fromInt w.liveVersion ++ " live") ]
