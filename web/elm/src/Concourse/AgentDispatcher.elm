module Concourse.AgentDispatcher exposing
    ( Mode(..)
    , Status
    , decodeStatus
    , encodeMode
    , modeFromString
    , modeLabel
    , modeToken
    )

import Json.Decode
import Json.Decode.Extra exposing (andMap)
import Json.Encode


{-| The three dispatcher control modes plus an escape hatch for a token this
build does not recognise. Decoding never crashes on an unknown string — the
UI treats `Unknown` as a neutral, informational state so a newer server that
grows the vocabulary degrades gracefully.

  - `active` — the loop auto-dispatches queued tickets.
  - `paused` — the loop does NOT auto-dispatch. This is the fail-safe a
    settings-read fault resolves to.
  - `off` — the loop does NOT auto-dispatch. This is an operator's explicit
    disable.

`paused` and `off` are behaviourally identical to the loop; they differ only
in PROVENANCE (a read fault vs. a deliberate choice), which is why the UI
still renders them as distinct labels. No mode stops a finished workflow run
from terminalizing its ticket — that is the always-on workflow-run
reconciler, not the dispatcher — and manual `fly agent tickets dispatch`
remains a separate, ungated path in every mode.

-}
type Mode
    = Off
    | Paused
    | Active
    | Unknown String


{-| The GET/PUT `/api/v1/agent/dispatcher` payload.

  - `mode` is the EFFECTIVE mode the loop honours right now — the single truth
    about whether queued tickets auto-dispatch.
  - `updatedAt` / `updatedBy` are null (→ Nothing) until someone sets a mode
    at runtime. `updatedAt` is an RFC3339 string kept verbatim for display.

The old `source` / `boot_default` pair is gone. The stored setting is now the
only input to the effective mode, so there is no boot-flag fallback left to
explain, and rendering "(boot default: off)" next to an active dispatcher
described a mechanism that no longer decides anything.

-}
type alias Status =
    { mode : Mode
    , updatedAt : Maybe String
    , updatedBy : Maybe String
    }


{-| Parse a wire token into a Mode, tolerating anything unexpected. Empty and
unknown strings become `Unknown` rather than failing the whole decode.
-}
modeFromString : String -> Mode
modeFromString token =
    case token of
        "off" ->
            Off

        "paused" ->
            Paused

        "active" ->
            Active

        other ->
            Unknown other


{-| The wire token for a mode. `Unknown` round-trips its raw string.
-}
modeToken : Mode -> String
modeToken mode =
    case mode of
        Off ->
            "off"

        Paused ->
            "paused"

        Active ->
            "active"

        Unknown other ->
            other


{-| A short human label for the pill / banner copy.
-}
modeLabel : Mode -> String
modeLabel mode =
    case mode of
        Off ->
            "off"

        Paused ->
            "paused"

        Active ->
            "active"

        Unknown other ->
            if other == "" then
                "unknown"

            else
                other


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


{-| Decode an optional string field: absent OR JSON null OR an unreadable
value → Nothing.
-}
optionalString : String -> Json.Decode.Decoder (Maybe String)
optionalString fieldName =
    Json.Decode.maybe (Json.Decode.field fieldName Json.Decode.string)
        |> Json.Decode.map (Maybe.andThen keepNonEmpty)


keepNonEmpty : String -> Maybe String
keepNonEmpty s =
    if s == "" then
        Nothing

    else
        Just s


decodeMode : String -> Json.Decode.Decoder Mode
decodeMode fieldName =
    defaultTo "" (Json.Decode.field fieldName Json.Decode.string)
        |> Json.Decode.map modeFromString


decodeStatus : Json.Decode.Decoder Status
decodeStatus =
    Json.Decode.succeed Status
        |> andMap (decodeMode "mode")
        |> andMap (optionalString "updated_at")
        |> andMap (optionalString "updated_by")


{-| The PUT body: `{ "mode": "off|paused|active" }`.
-}
encodeMode : Mode -> Json.Encode.Value
encodeMode mode =
    Json.Encode.object [ ( "mode", Json.Encode.string (modeToken mode) ) ]
