module Views.AgentDiff exposing (view)

{-| Renders a §1.11.1 DiffPage as colored unified-diff hunks. Static (no
interaction) in v1 — one `<div>` per file with a monospace body whose lines are
colored by `Concourse.AgentDiff.classifyLine`. This is the PRIMARY review
surface on the ticket page; the GitHub compare link is demoted to a secondary
affordance in `AgentTicket.provenanceLine`.
-}

import Concourse.AgentDiff as AgentDiff exposing (DiffFile, DiffPage, LineKind(..))
import Html exposing (Html)
import Html.Attributes exposing (class, style)


view : DiffPage -> Html msg
view page =
    Html.div
        [ class "agent-ticket-diff"
        , style "margin" "12px 0"
        , style "border" "1px solid #2b2b2b"
        , style "border-radius" "4px"
        , style "overflow" "hidden"
        ]
        (List.map fileBlock page.files
            ++ (if page.hasMore then
                    [ moreNotice page ]

                else
                    []
               )
        )


fileBlock : DiffFile -> Html msg
fileBlock file =
    Html.div
        [ style "border-top" "1px solid #2b2b2b" ]
        [ Html.div
            [ class "agent-ticket-diff-file-header"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "padding" "6px 10px"
            , style "background" "#1b1b1b"
            , style "color" "#c8d0c8"
            , style "display" "flex"
            , style "justify-content" "space-between"
            ]
            [ Html.span [] [ Html.text file.path ]
            , if file.truncated then
                Html.span [ style "color" "#c0a060" ] [ Html.text "truncated (64 KiB cap)" ]

              else
                Html.text ""
            ]
        , Html.div
            [ style "font-family" "monospace"
            , style "font-size" "12px"
            , style "line-height" "1.45"
            , style "overflow-x" "auto"
            , style "background" "#0f0f0f"
            ]
            (file.patch
                |> String.split "\n"
                |> List.map lineRow
            )
        ]


lineRow : String -> Html msg
lineRow line =
    let
        ( bg, fg ) =
            case AgentDiff.classifyLine line of
                Addition ->
                    ( "#122a12", "#a7d7a7" )

                Deletion ->
                    ( "#2a1212", "#d7a7a7" )

                HunkHeader ->
                    ( "#12203a", "#7a9ac0" )

                Meta ->
                    ( "#0f0f0f", "#7f7f7f" )

                Context ->
                    ( "#0f0f0f", "#b8b8b8" )
    in
    Html.div
        [ style "background" bg
        , style "color" fg
        , style "padding" "0 10px"
        , style "white-space" "pre"
        ]
        [ Html.text
            (if line == "" then
                " "

             else
                line
            )
        ]


moreNotice : DiffPage -> Html msg
moreNotice page =
    Html.div
        [ style "font-family" "monospace"
        , style "font-size" "12px"
        , style "padding" "6px 10px"
        , style "background" "#1b1b1b"
        , style "color" "#9aa39b"
        , style "border-top" "1px solid #2b2b2b"
        ]
        [ Html.text
            (String.fromInt (List.length page.files)
                ++ " of "
                ++ String.fromInt page.totalFiles
                ++ " files shown — open the full diff on GitHub for the rest."
            )
        ]
