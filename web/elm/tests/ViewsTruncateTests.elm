module ViewsTruncateTests exposing (all)

import Expect
import Test exposing (Test, describe, test)
import Views.Truncate


all : Test
all =
    describe "Views.Truncate.middle"
        [ test "leaves a string shorter than the budget untouched" <|
            \_ ->
                Views.Truncate.middle 10 "short"
                    |> Expect.equal "short"
        , test "leaves a string exactly at the budget untouched" <|
            \_ ->
                Views.Truncate.middle 5 "12345"
                    |> Expect.equal "12345"
        , test "middle-truncates an over-long string to the budget length" <|
            \_ ->
                Views.Truncate.middle 10 "abcdefghijklmnop"
                    |> String.length
                    |> Expect.equal 10
        , test "keeps the head and the distinguishing tail, eliding the middle" <|
            \_ ->
                -- The whole point: a shared prefix must not eat the suffix that
                -- tells two chips apart (e.g. the trailing "(T9 only)").
                Views.Truncate.middle 28 "#42 recorder + evidence (T9 only)"
                    |> Expect.all
                        [ String.startsWith "#42" >> Expect.equal True
                        , String.endsWith "(T9 only)" >> Expect.equal True
                        , String.contains "…" >> Expect.equal True
                        ]
        , test "uses a single ellipsis character, not three dots" <|
            \_ ->
                Views.Truncate.middle 8 "abcdefghijklmnop"
                    |> String.contains "..."
                    |> Expect.equal False
        , test "does not expand a string below a tiny budget" <|
            \_ ->
                Views.Truncate.middle 1 "abcdef"
                    |> String.length
                    |> (\len -> len <= 6)
                    |> Expect.equal True
        ]
