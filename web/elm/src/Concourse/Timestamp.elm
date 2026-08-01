module Concourse.Timestamp exposing (fromIso8601)

{-| Parsing for the RFC 3339 timestamps the agent API emits.

Go marshals every `time.Time` as RFC 3339 with nanoseconds, so
`started_at`, `completed_at`, and friends arrive as
`2026-07-31T12:34:56.123456789Z` rather than as an epoch number. The Elm
application has no date-parsing dependency — `ryan-haskell/date-format`
formats, it does not parse — and the run list needs real arithmetic
(duration, relative time), not a string to print.

This is deliberately narrow: it reads exactly the grammar the server emits
and answers `Nothing` for everything else. A row with an unreadable timestamp
renders without a time rather than failing the page, so widening this to
guess at other formats would buy nothing.

-}

import Time


{-| `YYYY-MM-DD` `T` `HH:MM:SS` with an optional fractional part and either
`Z` or a `±HH:MM` offset. A space separator is accepted because Postgres and
several logs spell it that way.
-}
fromIso8601 : String -> Maybe Time.Posix
fromIso8601 raw =
    case splitDateAndTime raw of
        Just ( datePart, timePart ) ->
            Maybe.map2
                (\days ( secondsOfDay, millis, offsetMinutes ) ->
                    Time.millisToPosix
                        (((days * 86400 + secondsOfDay - offsetMinutes * 60) * 1000) + millis)
                )
                (parseDate datePart)
                (parseTime timePart)

        Nothing ->
            Nothing


splitDateAndTime : String -> Maybe ( String, String )
splitDateAndTime raw =
    case String.split "T" raw of
        [ datePart, timePart ] ->
            Just ( datePart, timePart )

        _ ->
            case String.split " " raw of
                [ datePart, timePart ] ->
                    Just ( datePart, timePart )

                _ ->
                    Nothing


{-| Days since the Unix epoch, by Hinnant's civil-from-days inverse. The
proleptic Gregorian calendar is what RFC 3339 specifies, and the formula gets
leap centuries right without a table.
-}
parseDate : String -> Maybe Int
parseDate datePart =
    case String.split "-" datePart |> List.map String.toInt of
        [ Just year, Just month, Just day ] ->
            if year < 1 || month < 1 || month > 12 || day < 1 || day > 31 then
                Nothing

            else
                Just (daysFromCivil year month day)

        _ ->
            Nothing


daysFromCivil : Int -> Int -> Int -> Int
daysFromCivil year month day =
    let
        shiftedYear =
            if month <= 2 then
                year - 1

            else
                year

        era =
            shiftedYear // 400

        yearOfEra =
            shiftedYear - era * 400

        shiftedMonth =
            if month > 2 then
                month - 3

            else
                month + 9

        dayOfYear =
            (153 * shiftedMonth + 2) // 5 + day - 1

        dayOfEra =
            yearOfEra * 365 + yearOfEra // 4 - yearOfEra // 100 + dayOfYear
    in
    era * 146097 + dayOfEra - 719468


{-| Seconds into the day, milliseconds of the fractional part, and the zone
offset in minutes.
-}
parseTime : String -> Maybe ( Int, Int, Int )
parseTime timePart =
    let
        ( withoutZone, offsetMinutes ) =
            splitZone timePart
    in
    case offsetMinutes of
        Nothing ->
            Nothing

        Just minutes ->
            let
                ( clock, fraction ) =
                    case String.split "." withoutZone of
                        [ head, tail ] ->
                            ( head, fractionMillis tail )

                        _ ->
                            ( withoutZone, Just 0 )
            in
            case ( String.split ":" clock |> List.map String.toInt, fraction ) of
                ( [ Just hours, Just mins, Just seconds ], Just millis ) ->
                    if hours < 0 || hours > 23 || mins < 0 || mins > 59 || seconds < 0 || seconds > 60 then
                        Nothing

                    else
                        Just ( hours * 3600 + mins * 60 + seconds, millis, minutes )

                _ ->
                    Nothing


{-| Nanosecond precision is truncated to milliseconds, which is all
`Time.Posix` carries.
-}
fractionMillis : String -> Maybe Int
fractionMillis digits =
    if digits == "" || not (String.all Char.isDigit digits) then
        Nothing

    else
        String.left 3 (digits ++ "000")
            |> String.toInt


splitZone : String -> ( String, Maybe Int )
splitZone timePart =
    if String.endsWith "Z" timePart || String.endsWith "z" timePart then
        ( String.dropRight 1 timePart, Just 0 )

    else
        case findOffset timePart of
            Just ( body, sign, offset ) ->
                ( body, parseOffset offset |> Maybe.map ((*) sign) )

            Nothing ->
                -- RFC 3339 requires an offset. A timestamp without one is
                -- ambiguous, and guessing UTC would silently shift rows.
                ( timePart, Nothing )


findOffset : String -> Maybe ( String, Int, String )
findOffset timePart =
    let
        at index sign =
            Just
                ( String.left index timePart
                , sign
                , String.dropLeft (index + 1) timePart
                )
    in
    case ( lastIndexOf "+" timePart, lastIndexOf "-" timePart ) of
        ( Just index, _ ) ->
            at index 1

        ( Nothing, Just index ) ->
            if index == 0 then
                Nothing

            else
                at index -1

        ( Nothing, Nothing ) ->
            Nothing


parseOffset : String -> Maybe Int
parseOffset offset =
    case String.split ":" offset |> List.map String.toInt of
        [ Just hours, Just minutes ] ->
            if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 then
                Nothing

            else
                Just (hours * 60 + minutes)

        _ ->
            Nothing


lastIndexOf : String -> String -> Maybe Int
lastIndexOf needle haystack =
    String.indexes needle haystack |> List.reverse |> List.head
