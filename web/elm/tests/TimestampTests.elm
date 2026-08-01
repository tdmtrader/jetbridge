module TimestampTests exposing (all)

import Concourse.Timestamp as Timestamp
import Expect
import Test exposing (Test, describe, test)
import Time


all : Test
all =
    describe "RFC 3339 timestamps"
        [ test "reads the epoch itself" <|
            \_ ->
                millis "1970-01-01T00:00:00Z"
                    |> Expect.equal (Just 0)
        , test "reads a whole second" <|
            \_ ->
                millis "2026-07-31T12:34:56Z"
                    |> Expect.equal (Just 1785501296000)
        , test "reads the nanosecond form Go actually marshals" <|
            \_ ->
                millis "2026-07-31T12:34:56.123456789Z"
                    |> Expect.equal (Just 1785501296123)
        , test "pads a short fraction rather than misreading it as milliseconds" <|
            \_ ->
                millis "2026-07-31T12:34:56.1Z"
                    |> Expect.equal (Just 1785501296100)
        , test "applies a positive zone offset" <|
            \_ ->
                millis "2026-07-31T14:34:56+02:00"
                    |> Expect.equal (Just 1785501296000)
        , test "applies a negative zone offset" <|
            \_ ->
                millis "2026-07-31T07:34:56-05:00"
                    |> Expect.equal (Just 1785501296000)
        , test "gets a leap day right" <|
            \_ ->
                millis "2024-02-29T00:00:00Z"
                    |> Expect.equal (Just 1709164800000)
        , test "gets the 1900-style non-leap century rule right" <|
            \_ ->
                millis "2100-03-01T00:00:00Z"
                    |> Expect.equal (Just 4107542400000)
        , test "accepts a space separator" <|
            \_ ->
                millis "2026-07-31 12:34:56Z"
                    |> Expect.equal (Just 1785501296000)
        , test "rejects a timestamp with no zone, which would silently shift the row" <|
            \_ ->
                millis "2026-07-31T12:34:56"
                    |> Expect.equal Nothing
        , test "rejects a date with no time" <|
            \_ ->
                millis "2026-07-31"
                    |> Expect.equal Nothing
        , test "rejects an impossible month" <|
            \_ ->
                millis "2026-13-01T00:00:00Z"
                    |> Expect.equal Nothing
        , test "rejects an impossible hour" <|
            \_ ->
                millis "2026-07-31T24:00:00Z"
                    |> Expect.equal Nothing
        , test "rejects a non-numeric fraction" <|
            \_ ->
                millis "2026-07-31T12:34:56.abcZ"
                    |> Expect.equal Nothing
        , test "rejects an out-of-range offset" <|
            \_ ->
                millis "2026-07-31T12:34:56+99:00"
                    |> Expect.equal Nothing
        , test "rejects empty input" <|
            \_ ->
                millis ""
                    |> Expect.equal Nothing
        ]


millis : String -> Maybe Int
millis raw =
    Timestamp.fromIso8601 raw |> Maybe.map Time.posixToMillis
