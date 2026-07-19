module Views.Prose exposing (view)

{-| Render author-written text as light prose without pulling in a markdown
dependency. Paragraphs split on blank lines, hard line breaks inside a
paragraph are preserved, and inline `code` spans and \*\*bold\*\* runs are
styled. Everything is emitted as Elm text nodes and styled spans, so there is
no raw-HTML injection.
-}

import Html exposing (Html)
import Html.Attributes exposing (style)


view : String -> Html msg
view body =
    Html.div
        [ style "color" "#d0d0d0", style "line-height" "1.5" ]
        (body
            |> splitParagraphs
            |> List.map paragraph
        )


{-| Split on one or more blank lines. Normalises CRLF first and drops empty
paragraphs so a trailing blank line does not render an empty block.
-}
splitParagraphs : String -> List String
splitParagraphs body =
    body
        |> String.replace "\u{000D}\n" "\n"
        |> String.split "\n\n"
        |> List.map String.trim
        |> List.filter (\p -> p /= "")


{-| One paragraph. `pre-wrap` preserves the author's single line breaks; the
inline pass styles code/bold within it.
-}
paragraph : String -> Html msg
paragraph text =
    Html.p
        [ style "white-space" "pre-wrap", style "margin" "0 0 10px 0" ]
        (inlines text)


type Segment
    = Code String
    | Plain String


{-| Split a paragraph into inline HTML: `code` spans first, then \*\*bold\*\*
inside the remaining plain text.
-}
inlines : String -> List (Html msg)
inlines text =
    text
        |> splitCode
        |> List.concatMap segmentToHtml


{-| Split on backticks into alternating plain/code segments. Only treats the
runs as code when the backticks are balanced (an odd number of parts); an
unbalanced backtick is left as literal plain text.
-}
splitCode : String -> List Segment
splitCode text =
    let
        parts =
            String.split "`" text
    in
    if modBy 2 (List.length parts) == 1 then
        parts
            |> List.indexedMap
                (\i part ->
                    if modBy 2 i == 1 then
                        Code part

                    else
                        Plain part
                )

    else
        [ Plain text ]


segmentToHtml : Segment -> List (Html msg)
segmentToHtml segment =
    case segment of
        Code c ->
            [ Html.code
                [ style "font-family" "monospace"
                , style "background" "#2a2929"
                , style "padding" "0 3px"
                , style "border-radius" "2px"
                ]
                [ Html.text c ]
            ]

        Plain p ->
            bolds p


{-| Style \*\*bold\*\* runs within a plain-text segment. Only when the double
asterisks are balanced; otherwise the text is left literal.
-}
bolds : String -> List (Html msg)
bolds text =
    let
        parts =
            String.split "**" text
    in
    if modBy 2 (List.length parts) == 1 then
        parts
            |> List.indexedMap
                (\i part ->
                    if modBy 2 i == 1 then
                        Html.strong
                            [ style "font-weight" "700"
                            , style "color" "#e8e8e8"
                            ]
                            [ Html.text part ]

                    else
                        Html.text part
                )

    else
        [ Html.text text ]
