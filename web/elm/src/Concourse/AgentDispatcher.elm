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

  - `active` — the loop auto-dispatches queued tickets and runs the
    run-completion reconciler.
  - `paused` — the loop does NOT auto-dispatch, but the reconciler stays alive;
    manual `fly agent tickets dispatch` still works (a separate, ungated path).
  - `off` — the loop neither auto-dispatches nor reconciles (fully dormant).

-}
type Mode
    = Off
    | Paused
    | Active
    | Unknown String


{-| The GET/PUT `/api/v1/agent/dispatcher` payload.

  - `mode` is the EFFECTIVE mode the loop honours right now.
  - `source` is "setting" when it came from the agent\_settings row, or
    "boot-default" when no row exists yet (fell back to the boot flag).
  - `bootDefault` is what the `--agent-dispatcher-enabled` boot flag resolves
    to, shown so the UI can explain the fallback.
  - `updatedAt` / `updatedBy` are null (→ Nothing) until someone sets a mode
    at runtime. `updatedAt` is an RFC3339 string kept verbatim for display.

-}
type alias Status =
    { mode : Mode
    , source : String
    , updatedAt : Maybe String
    , updatedBy : Maybe String
    , bootDefault : Mode
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
        |> andMap (defaultTo "" <| Json.Decode.field "source" Json.Decode.string)
        |> andMap (optionalString "updated_at")
        |> andMap (optionalString "updated_by")
        |> andMap (decodeMode "boot_default")


{-| The PUT body: `{ "mode": "off|paused|active" }`.
-}
encodeMode : Mode -> Json.Encode.Value
encodeMode mode =
    Json.Encode.object [ ( "mode", Json.Encode.string (modeToken mode) ) ]
