module AgentSnapshot.RepositoryChange exposing (view)

import Concourse.WorkflowRun exposing (ChangedFile, RepositoryChange)
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Message.Message exposing (Message)


view : RepositoryChange -> Html Message
view change =
    Html.section
        [ class "agent-repository-change"
        , style "margin-top" "16px"
        , style "border" "1px solid #3d3c3c"
        ]
        [ Html.div
            [ style "display" "flex"
            , style "gap" "12px"
            , style "padding" "10px 12px"
            , style "background" "#242323"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            ]
            [ Html.strong [] [ Html.text "repository change" ]
            , Html.span [ class "agent-projection-status" ] [ Html.text change.status ]
            , Html.span []
                [ Html.text
                    (String.fromInt change.fileCount
                        ++ " files · +"
                        ++ String.fromInt change.linesAdded
                        ++ " −"
                        ++ String.fromInt change.linesDeleted
                    )
                ]
            ]
        , changeBody change
        ]


changeBody : RepositoryChange -> Html Message
changeBody change =
    case change.status of
        "pending" ->
            note "projection is still being materialized"

        "unavailable" ->
            note "a bounded diff is unavailable for this representation"

        "invalid" ->
            note "the repository-change snapshot did not satisfy its projection contract"

        _ ->
            Html.div []
                [ if List.isEmpty change.files then
                    note "no changed files"

                  else
                    Html.div [ class "agent-change-files" ] (List.map fileRow change.files)
                , if change.unifiedDiff == "" then
                    Html.text ""

                  else
                    Html.pre
                        [ class "agent-unified-diff"
                        , style "overflow-x" "auto"
                        , style "white-space" "pre"
                        , style "font-size" "12px"
                        , style "line-height" "1.45"
                        , style "padding" "12px"
                        , style "margin" "0"
                        , style "border-top" "1px solid #3d3c3c"
                        ]
                        [ Html.text change.unifiedDiff ]
                , if change.truncated then
                    Html.p
                        [ class "agent-diff-truncated"
                        , style "color" "#e0a44e"
                        , style "padding" "0 12px 12px"
                        ]
                        [ Html.text ("diff truncated: " ++ change.truncationReason) ]

                  else
                    Html.text ""
                ]


fileRow : ChangedFile -> Html Message
fileRow file =
    Html.div
        [ class "agent-change-file"
        , style "display" "grid"
        , style "grid-template-columns" "80px minmax(0, 1fr) auto"
        , style "gap" "12px"
        , style "padding" "6px 12px"
        , style "border-top" "1px solid #302f2f"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        ]
        [ Html.span [] [ Html.text file.status ]
        , Html.span []
            [ Html.text
                (if file.previousPath /= "" && file.previousPath /= file.path then
                    file.previousPath ++ " → " ++ file.path

                 else
                    file.path
                )
            ]
        , Html.span []
            [ Html.text
                (if file.binary then
                    "binary"

                 else
                    "+" ++ String.fromInt file.linesAdded ++ " −" ++ String.fromInt file.linesDeleted
                )
            ]
        ]


note : String -> Html Message
note message =
    Html.p
        [ style "color" "#8a8a8a", style "padding" "8px 12px" ]
        [ Html.text message ]
