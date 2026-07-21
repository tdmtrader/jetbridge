module ViewsProseTests exposing (all)

import Expect
import Html
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (tag, text)
import Views.Prose


{-| Wrap the prose output in a container so it can be queried as a single root.
-}
render : String -> Query.Single msg
render body =
    Html.div [] [ Views.Prose.view body ]
        |> Query.fromHtml


all : Test
all =
    describe "Views.Prose"
        [ test "renders plain text" <|
            \_ ->
                render "hello world"
                    |> Query.has [ text "hello world" ]
        , test "splits blank-line-separated paragraphs into separate p elements" <|
            \_ ->
                render "first para\n\nsecond para"
                    |> Query.findAll [ tag "p" ]
                    |> Query.count (Expect.equal 2)
        , test "renders an inline code span in a code element" <|
            \_ ->
                render "run `fly agent auth` now"
                    |> Query.find [ tag "code" ]
                    |> Query.has [ text "fly agent auth" ]
        , test "renders a bold run in a strong element" <|
            \_ ->
                render "this is **important** text"
                    |> Query.find [ tag "strong" ]
                    |> Query.has [ text "important" ]
        , test "leaves an unbalanced backtick as literal text (no code element)" <|
            \_ ->
                render "a lone ` backtick"
                    |> Query.hasNot [ tag "code" ]
        , test "consumes a fenced-code language tag rather than rendering it as text" <|
            \_ ->
                render "```python\nprint(1)\n```"
                    |> Query.hasNot [ text "```python" ]
        , test "does not render the language token 'python' as its own text node" <|
            \_ ->
                render "```python\nprint(1)\n```"
                    |> Query.findAll [ text "python" ]
                    |> Query.count (Expect.equal 0)
        , test "renders fenced-code body inside a pre element" <|
            \_ ->
                render "intro\n\n```json\n{\"a\":1}\n```"
                    |> Query.find [ tag "pre" ]
                    |> Query.has [ text "{\"a\":1}" ]
        , test "keeps a blank line inside a fence in the same code block" <|
            \_ ->
                render "```\nline1\n\nline2\n```"
                    |> Query.find [ tag "pre" ]
                    |> Query.has [ text "line1\n\nline2" ]
        , test "does not treat fenced-code content as inline code/bold markup" <|
            \_ ->
                -- **not bold** inside a fence must stay literal, not <strong>
                render "```\n**not bold**\n```"
                    |> Query.hasNot [ tag "strong" ]
        , test "inline renders a bold run as a strong element" <|
            \_ ->
                Html.div [] (Views.Prose.inline "ship **it** now")
                    |> Query.fromHtml
                    |> Query.find [ tag "strong" ]
                    |> Query.has [ text "it" ]
        , test "inline renders an inline code span as a code element" <|
            \_ ->
                Html.div [] (Views.Prose.inline "run `fly` please")
                    |> Query.fromHtml
                    |> Query.find [ tag "code" ]
                    |> Query.has [ text "fly" ]
        , test "inline does not wrap plain text in a paragraph" <|
            \_ ->
                Html.div [] (Views.Prose.inline "just text")
                    |> Query.fromHtml
                    |> Query.hasNot [ tag "p" ]
        ]
