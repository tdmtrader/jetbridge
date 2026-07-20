module Concourse.AgentEvent exposing
    ( EntryBody(..)
    , TimelineEntry
    , Transcript
    , parseTranscript
    )

{-| Pure decoding of a run's transcript NDJSON into a typed timeline.

The endpoint `.../runs/:build_id/transcript` serves the runner's raw claude-CLI
`--output-format stream-json --verbose` stdout (flight/transcript.ndjson): one
JSON object per line, in the claude-code 2.x stream-json shape — NOT the
flight-events `{"ts","event","data"}` envelope. Each line is one of:

  - `{"type":"system","subtype":"init",...}` — session start.
  - `{"type":"assistant","message":{"content":[ ... ]}}` — assistant turn; each
    content item is `text` (prose), `thinking` (reasoning), or `tool_use`
    (a tool call with `name` + `input`). ONE line can carry SEVERAL items, so a
    single line expands into several timeline entries.
  - `{"type":"user","message":{"content":[ {"type":"tool_result",...} ]}}` —
    tool output; each `tool_result`'s `content` is a string or a list of
    `{"type":"text","text":...}` blocks.
  - `{"type":"result","subtype":...,"num_turns":N,"total_cost_usd":F,...}` —
    terminal summary.
  - `{"type":"transcript_truncated","dropped_bytes":N,"note":...}` — marker the
    runner prepends when it head-truncates the stream to its last 512 KiB.

Lines that are blank are dropped. Lines that are not valid JSON, or whose
top-level `type` is unrecognized, are skipped and counted (`Transcript.skipped`)
rather than aborting the whole transcript — the stream is appended live, so a
half-written trailing line during a poll is normal, and a future CLI line type
degrades to a skip rather than a decode crash.
-}

import Json.Decode as D
import Json.Encode as Encode


type alias Transcript =
    { entries : List TimelineEntry
    , skipped : Int
    }


type alias TimelineEntry =
    { seq : Int
    , ts : String
    , eventType : String
    , raw : String
    , body : EntryBody
    }


type EntryBody
    = SystemInit { subtype : String }
    | Said { text : String }
    | Thought { summary : String }
    | ToolCalled { tool : String, input : String }
    | ToolResulted { output : String, isError : Bool }
    | RunResult { subtype : String, numTurns : Int, costUsd : Float, isError : Bool, result : String }
    | Truncated { droppedBytes : Int, note : String }


parseTranscript : String -> Transcript
parseTranscript raw =
    let
        lines =
            String.split "\n" raw
                |> List.map String.trim
                |> List.filter (\l -> l /= "")

        step line ( accEntries, skipped, seq ) =
            case D.decodeString lineDecoder line of
                Ok tagged ->
                    let
                        ( entries, nextSeq ) =
                            List.foldl
                                (\( eventType, body ) ( acc, s ) ->
                                    ( TimelineEntry s "" eventType line body :: acc, s + 1 )
                                )
                                ( accEntries, seq )
                                tagged
                    in
                    ( entries, skipped, nextSeq )

                Err _ ->
                    ( accEntries, skipped + 1, seq )

        ( revEntries, totalSkipped, _ ) =
            List.foldl step ( [], 0, 0 ) lines
    in
    { entries = List.reverse revEntries, skipped = totalSkipped }


{-| Decode one NDJSON line into zero-or-more `(eventType, body)` pairs, dispatched
on the top-level `type`. An `assistant`/`user` line fans out to one pair per
content item; every other recognized line yields exactly one. An unrecognized
`type` (or a missing one) fails the decoder, so `parseTranscript` counts the line
as skipped.
-}
lineDecoder : D.Decoder (List ( String, EntryBody ))
lineDecoder =
    D.field "type" D.string
        |> D.andThen
            (\t ->
                case t of
                    "system" ->
                        D.map List.singleton systemDecoder

                    "assistant" ->
                        assistantDecoder

                    "user" ->
                        userDecoder

                    "result" ->
                        D.map List.singleton resultDecoder

                    "transcript_truncated" ->
                        D.map List.singleton truncatedDecoder

                    other ->
                        D.fail ("unrecognized transcript line type: " ++ other)
            )


systemDecoder : D.Decoder ( String, EntryBody )
systemDecoder =
    D.map (\sub -> ( "system", SystemInit { subtype = sub } )) (strField "subtype")


resultDecoder : D.Decoder ( String, EntryBody )
resultDecoder =
    D.map5
        (\sub turns cost isErr res ->
            ( "result", RunResult { subtype = sub, numTurns = turns, costUsd = cost, isError = isErr, result = res } )
        )
        (strField "subtype")
        (intField "num_turns")
        costField
        (boolField "is_error")
        resultTextField


truncatedDecoder : D.Decoder ( String, EntryBody )
truncatedDecoder =
    D.map2
        (\dropped note -> ( "transcript_truncated", Truncated { droppedBytes = dropped, note = note } ))
        (intField "dropped_bytes")
        (strField "note")


{-| An assistant turn: `message.content` is a list whose items are `text`,
`thinking`, or `tool_use` blocks. Unknown block types are dropped (not counted).
A line with no decodable content yields an empty list rather than a skip.
-}
assistantDecoder : D.Decoder (List ( String, EntryBody ))
assistantDecoder =
    D.oneOf
        [ D.at [ "message", "content" ] (D.list assistantContentDecoder)
            |> D.map (List.filterMap identity)
        , D.succeed []
        ]


assistantContentDecoder : D.Decoder (Maybe ( String, EntryBody ))
assistantContentDecoder =
    D.field "type" D.string
        |> D.andThen
            (\t ->
                case t of
                    "text" ->
                        D.map (\s -> Just ( "text", Said { text = s } )) (strField "text")

                    "thinking" ->
                        D.map (\s -> Just ( "thinking", Thought { summary = s } )) (strField "thinking")

                    "tool_use" ->
                        D.map Just toolUseDecoder

                    _ ->
                        D.succeed Nothing
            )


toolUseDecoder : D.Decoder ( String, EntryBody )
toolUseDecoder =
    D.map2
        (\name inputVal -> ( "tool_use", ToolCalled { tool = name, input = summarizeInput inputVal } ))
        (strField "name")
        (D.oneOf [ D.field "input" D.value, D.succeed Encode.null ])


{-| A short, human-readable summary of a tool_use `input` object: the first
present of the common single-value fields (Bash's `command`, Read/Edit/Write's
`file_path`, Grep's `pattern`, …); otherwise the compact JSON of the whole input
so nothing is silently lost for less common tools.
-}
summarizeInput : D.Value -> String
summarizeInput v =
    let
        pick field =
            case D.decodeValue (D.field field D.string) v of
                Ok s ->
                    if String.trim s /= "" then
                        Just s

                    else
                        Nothing

                Err _ ->
                    Nothing
    in
    case List.filterMap pick [ "command", "file_path", "path", "pattern", "query", "url", "prompt", "description" ] of
        first :: _ ->
            first

        [] ->
            case Encode.encode 0 v of
                "null" ->
                    ""

                encoded ->
                    encoded


{-| A user turn carries tool_result blocks (the model's tool feedback loop).
Non-tool_result items are dropped. Each tool_result's `content` is either a raw
string or a list of `{"type":"text","text":...}` blocks.
-}
userDecoder : D.Decoder (List ( String, EntryBody ))
userDecoder =
    D.oneOf
        [ D.at [ "message", "content" ] (D.list userContentDecoder)
            |> D.map (List.filterMap identity)
        , D.succeed []
        ]


userContentDecoder : D.Decoder (Maybe ( String, EntryBody ))
userContentDecoder =
    D.field "type" D.string
        |> D.andThen
            (\t ->
                case t of
                    "tool_result" ->
                        D.map Just toolResultDecoder

                    _ ->
                        D.succeed Nothing
            )


toolResultDecoder : D.Decoder ( String, EntryBody )
toolResultDecoder =
    D.map2
        (\output isErr -> ( "tool_result", ToolResulted { output = output, isError = isErr } ))
        (D.oneOf
            [ D.field "content" D.string
            , D.field "content" (D.list textBlockDecoder |> D.map (String.join "\n" << List.filter (\s -> s /= "")))
            , D.succeed ""
            ]
        )
        (boolField "is_error")


textBlockDecoder : D.Decoder String
textBlockDecoder =
    D.oneOf [ D.field "text" D.string, D.succeed "" ]


{-| The terminal cost, preferring `total_cost_usd` (newer CLIs) over `cost_usd`,
mirroring `schema.CLIEnvelope.ResolvedCostUSD` on the runner side.
-}
costField : D.Decoder Float
costField =
    D.map2
        (\total base ->
            if total > 0 then
                total

            else
                base
        )
        (floatField "total_cost_usd")
        (floatField "cost_usd")


{-| The result line's `result` field is the agent's final text, which the CLI
double-encodes as a JSON string (e.g. `"result":"\"done\""`). Unquote it once
when it looks JSON-quoted, matching the runner's `summaryFromResult`.
-}
resultTextField : D.Decoder String
resultTextField =
    D.oneOf [ D.field "result" D.string, D.succeed "" ]
        |> D.map unquoteOnce


unquoteOnce : String -> String
unquoteOnce s =
    if String.startsWith "\"" s then
        case D.decodeString D.string s of
            Ok inner ->
                inner

            Err _ ->
                s

    else
        s


strField : String -> D.Decoder String
strField name =
    D.oneOf [ D.field name D.string, D.succeed "" ]


intField : String -> D.Decoder Int
intField name =
    D.oneOf [ D.field name D.int, D.succeed 0 ]


floatField : String -> D.Decoder Float
floatField name =
    D.oneOf [ D.field name D.float, D.succeed 0 ]


boolField : String -> D.Decoder Bool
boolField name =
    D.oneOf [ D.field name D.bool, D.succeed False ]
