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
        ]
