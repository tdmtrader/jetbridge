module Agent.Shared exposing
    ( amberColor
    , canMint
    , errorLine
    , expiresIsValid
    , formatPosix
    , formatUsd
    , mintScopeVocabulary
    , mutedColor
    , mutedLine
    , pill
    , rowBorder
    , sectionBlock
    , secondsToPosix
    , staleDataWarning
    , subtleColor
    , tableCell
    , tableHeaderCell
    )

import Colors
import DateFormat
import Html exposing (Html)
import Html.Attributes exposing (class, id, style)
import Message.Message exposing (Message)
import Set exposing (Set)
import Time


{-| The closed scope vocabulary an admin may grant when minting a principal.
Mirrors agent/api/principals `ValidScopes`; keep the two in lockstep.
-}
mintScopeVocabulary : List String
mintScopeVocabulary =
    [ "reviews:write"
    , "tickets:read"
    , "tickets:write"
    , "metrics:write"
    , "costs:write"
    , "questions:answer"
    ]


{-| The mint button is enabled only with a non-empty name, at least one scope
selected, and a valid expiry field — the same required-field rule the API
enforces. A blank expiry is valid (= no expiry); any non-blank value must
parse to a positive integer number of days.
-}
canMint : { name : String, scopes : Set String, expiresDays : String } -> Bool
canMint { name, scopes, expiresDays } =
    (String.trim name /= "")
        && not (Set.isEmpty scopes)
        && expiresIsValid expiresDays


{-| The "expires in N days" field is valid when it is blank (no expiry) or
parses to a positive integer. Blank, zero, negative, and non-numeric input
that would silently mean "never expires" are surfaced as invalid instead.
-}
expiresIsValid : String -> Bool
expiresIsValid raw =
    case String.trim raw of
        "" ->
            True

        trimmed ->
            case String.toInt trimmed of
                Just days ->
                    days > 0

                Nothing ->
                    False



-- COLORS / STYLE HELPERS


mutedColor : String
mutedColor =
    "#b0b0b0"


subtleColor : String
subtleColor =
    "#7a7a7a"


rowBorder : String
rowBorder =
    "1px solid " ++ Colors.background


amberColor : String
amberColor =
    "#e0a44e"


{-| Round a dollar amount to two decimals via integer cents so the display
never leaks floating-point noise (e.g. 0.1 + 0.2).
-}
formatUsd : Float -> String
formatUsd amount =
    let
        cents =
            round (amount * 100)

        sign =
            if cents < 0 then
                "-"

            else
                ""

        absCents =
            abs cents

        dollars =
            absCents // 100

        remainder =
            modBy 100 absCents

        fraction =
            if remainder < 10 then
                "0" ++ String.fromInt remainder

            else
                String.fromInt remainder
    in
    sign ++ String.fromInt dollars ++ "." ++ fraction


sectionBlock : String -> String -> List (Html Message) -> Html Message
sectionBlock anchorId title children =
    Html.div
        [ id anchorId, style "margin-top" "24px" ]
        (Html.h2
            [ style "font-size" "15px"
            , style "margin" "0 0 8px 0"
            , style "color" Colors.text
            ]
            [ Html.text title ]
            :: children
        )


mutedLine : String -> Html Message
mutedLine content =
    Html.p
        [ style "color" mutedColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "margin" "4px 0"
        ]
        [ Html.text content ]


errorLine : String -> Html Message
errorLine content =
    Html.p
        [ class "agent-section-error"
        , style "color" amberColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "margin" "4px 0"
        ]
        [ Html.text content ]


{-| A visible warning shown above a section's content when a poll fails after
data has already loaded. Without it a broken refresh is invisible — the stale
data keeps rendering forever with no hint that it stopped updating.
-}
staleDataWarning : Maybe String -> List (Html Message)
staleDataWarning maybeError =
    case maybeError of
        Just message ->
            [ Html.div [ class "agent-section-stale" ]
                [ errorLine ("refresh failed — showing stale data: " ++ message) ]
            ]

        Nothing ->
            []


pill : String -> { bg : String, fg : String } -> String -> Html Message
pill className { bg, fg } labelText =
    Html.span
        [ class className
        , style "margin-left" "8px"
        , style "padding" "1px 8px"
        , style "border-radius" "3px"
        , style "font-size" "11px"
        , style "font-weight" "700"
        , style "background" bg
        , style "color" fg
        ]
        [ Html.text labelText ]



-- SHARED TABLE + TIME HELPERS


tableHeaderCell : String -> String -> Html Message
tableHeaderCell align content =
    Html.th
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "color" mutedColor
        , style "font-weight" "700"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


tableCell : String -> String -> Html Message
tableCell align content =
    Html.td
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


secondsToPosix : Int -> Time.Posix
secondsToPosix seconds =
    Time.millisToPosix (seconds * 1000)


{-| Humanize an optional timestamp as a compact absolute time in the viewer's
own time zone (from `session.timeZone`), e.g. "Jul 18, 2026 14:30", or "—"
when absent. Showing local time is what an operator expects for "which of
today's runs came first"; the minutes matter on an ops console. The
server-aggregated cost buckets stay labelled as UTC days separately.
-}
formatPosix : Time.Zone -> Maybe Time.Posix -> String
formatPosix zone maybe =
    case maybe of
        Nothing ->
            "—"

        Just posix ->
            DateFormat.format
                [ DateFormat.monthNameAbbreviated
                , DateFormat.text " "
                , DateFormat.dayOfMonthNumber
                , DateFormat.text ", "
                , DateFormat.yearNumber
                , DateFormat.text " "
                , DateFormat.hourMilitaryFixed
                , DateFormat.text ":"
                , DateFormat.minuteFixed
                ]
                zone
                posix
