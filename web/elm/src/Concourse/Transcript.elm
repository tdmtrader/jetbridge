module Concourse.Transcript exposing
    ( Entry
    , Kind(..)
    , Ref
    , decodeRef
    , kindClass
    , kindLabel
    , parse
    )

{-| The agent tool-call transcript: the raw `claude --output-format stream-json`
stdout the runner captured for one agent step, persisted verbatim and served as
ndjson.

Parsing is deliberately total. The transcript is a RECORDING of a third-party
CLI's stream, head-truncated to its last 512KiB server-side — so its first line
is routinely a JSON fragment, and a CLI upgrade may add line shapes this decoder
has never seen. Every line therefore yields at least one entry: an unrecognised
JSON shape falls back to its re-encoded body, and a line that is not JSON at all
falls back to the raw text. A transcript never fails to render.

-}

import Json.Decode
import Json.Decode.Extra exposing (andMap)
import Json.Encode



-- Index --------------------------------------------------------------------


{-| One entry of the run's transcript index (GET .../transcripts): which plans
of the run captured a transcript, and how big each one is. The ndjson body is
fetched per plan, on demand.
-}
type alias Ref =
    { planId : String
    , functionId : String
    , stepName : String
    , buildId : Int
    , byteLen : Int
    , truncated : Bool
    }


decodeRef : Json.Decode.Decoder Ref
decodeRef =
    Json.Decode.succeed Ref
        |> andMap (defaultTo "" <| Json.Decode.field "plan_id" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "function_id" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "step_name" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "byte_len" Json.Decode.int)
        |> andMap (defaultTo False <| Json.Decode.field "truncated" Json.Decode.bool)



-- Conversation --------------------------------------------------------------


type Kind
    = System
    | Assistant
    | User
    | Thinking
    | ToolCall
    | ToolResult
    | Outcome
    | Truncation
    | Unparsed


{-| One readable row of the conversation. `index` is assigned across the whole
transcript (a line may expand to several entries) and is what the view keys its
expand/collapse state on. `collapsible` marks the bulky bodies — tool inputs
and tool results — which start collapsed.
-}
type alias Entry =
    { index : Int
    , kind : Kind
    , label : String
    , body : String
    , collapsible : Bool
    }


kindClass : Kind -> String
kindClass kind =
    case kind of
        System ->
            "system"

        Assistant ->
            "assistant"

        User ->
            "user"

        Thinking ->
            "thinking"

        ToolCall ->
            "tool-call"

        ToolResult ->
            "tool-result"

        Outcome ->
            "outcome"

        Truncation ->
            "truncation"

        Unparsed ->
            "unparsed"


kindLabel : Kind -> String
kindLabel kind =
    case kind of
        System ->
            "system"

        Assistant ->
            "assistant"

        User ->
            "user"

        Thinking ->
            "thinking"

        ToolCall ->
            "tool"

        ToolResult ->
            "tool result"

        Outcome ->
            "result"

        Truncation ->
            "truncated"

        Unparsed ->
            "unparsed line"


{-| Parse a whole ndjson transcript into a readable conversation. Blank lines
are dropped; every other line contributes at least one entry.
-}
parse : String -> List Entry
parse ndjson =
    ndjson
        |> String.lines
        |> List.map String.trim
        |> List.filter (not << String.isEmpty)
        |> List.concatMap parseLine
        |> List.indexedMap (\index row -> { row | index = index })


parseLine : String -> List Entry
parseLine line =
    case Json.Decode.decodeString lineDecoder line of
        Ok entries ->
            entries

        Err _ ->
            [ entry Unparsed (kindLabel Unparsed) line False ]


entry : Kind -> String -> String -> Bool -> Entry
entry kind label body collapsible =
    { index = 0, kind = kind, label = label, body = body, collapsible = collapsible }


lineDecoder : Json.Decode.Decoder (List Entry)
lineDecoder =
    Json.Decode.field "type" Json.Decode.string
        |> Json.Decode.andThen
            (\lineType ->
                case lineType of
                    "system" ->
                        Json.Decode.map2
                            (\subtype raw ->
                                [ entry System (labelWith "system" subtype) raw True ]
                            )
                            (defaultTo "" <| Json.Decode.field "subtype" Json.Decode.string)
                            encodedValue

                    "assistant" ->
                        messageDecoder Assistant "assistant"

                    "user" ->
                        messageDecoder User "user"

                    "result" ->
                        Json.Decode.map3
                            (\subtype text raw ->
                                [ entry Outcome
                                    (labelWith "result" subtype)
                                    (if String.isEmpty text then
                                        raw

                                     else
                                        text
                                    )
                                    False
                                ]
                            )
                            (defaultTo "" <| Json.Decode.field "subtype" Json.Decode.string)
                            (defaultTo "" <| Json.Decode.field "result" Json.Decode.string)
                            encodedValue

                    "transcript_truncated" ->
                        Json.Decode.map2
                            (\dropped note ->
                                [ entry Truncation
                                    (kindLabel Truncation)
                                    (String.fromInt dropped ++ " bytes dropped — " ++ note)
                                    False
                                ]
                            )
                            (defaultTo 0 <| Json.Decode.field "dropped_bytes" Json.Decode.int)
                            (defaultTo "head-truncated" <| Json.Decode.field "note" Json.Decode.string)

                    other ->
                        Json.Decode.map
                            (\raw -> [ entry Unparsed other raw True ])
                            encodedValue
            )


{-| An assistant/user line wraps an Anthropic message whose `content` is either
a plain string or a list of blocks. Each block becomes its own entry so a turn
that thinks, speaks and calls three tools reads as five rows, not one blob.
-}
messageDecoder : Kind -> String -> Json.Decode.Decoder (List Entry)
messageDecoder kind label =
    Json.Decode.oneOf
        [ Json.Decode.at [ "message", "content" ] (Json.Decode.list (blockDecoder kind label))
        , Json.Decode.at [ "message", "content" ] Json.Decode.string
            |> Json.Decode.map (\text -> [ entry kind label text False ])
        , Json.Decode.map (\raw -> [ entry kind label raw True ]) encodedValue
        ]


blockDecoder : Kind -> String -> Json.Decode.Decoder Entry
blockDecoder kind label =
    Json.Decode.field "type" Json.Decode.string
        |> Json.Decode.andThen
            (\blockType ->
                case blockType of
                    "text" ->
                        Json.Decode.map
                            (\text -> entry kind label text False)
                            (defaultTo "" <| Json.Decode.field "text" Json.Decode.string)

                    "thinking" ->
                        Json.Decode.map
                            (\text -> entry Thinking (kindLabel Thinking) text True)
                            (defaultTo "" <| Json.Decode.field "thinking" Json.Decode.string)

                    "tool_use" ->
                        Json.Decode.map2
                            (\name input -> entry ToolCall (labelWith (kindLabel ToolCall) name) input True)
                            (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
                            (defaultTo "" <| Json.Decode.field "input" encodedValue)

                    "tool_result" ->
                        Json.Decode.map2
                            (\isError content ->
                                entry ToolResult
                                    (if isError then
                                        kindLabel ToolResult ++ " · error"

                                     else
                                        kindLabel ToolResult
                                    )
                                    content
                                    True
                            )
                            (defaultTo False <| Json.Decode.field "is_error" Json.Decode.bool)
                            (defaultTo "" <| Json.Decode.field "content" toolResultContentDecoder)

                    other ->
                        Json.Decode.map
                            (\raw -> entry Unparsed other raw True)
                            encodedValue
            )


{-| A tool result's content is a string in the simple case and a list of
content blocks when the tool returned structured output.
-}
toolResultContentDecoder : Json.Decode.Decoder String
toolResultContentDecoder =
    Json.Decode.oneOf
        [ Json.Decode.string
        , Json.Decode.list
            (Json.Decode.oneOf
                [ Json.Decode.field "text" Json.Decode.string
                , encodedValue
                ]
            )
            |> Json.Decode.map (String.join "\n")
        , encodedValue
        ]


{-| The value re-encoded as indented JSON: the total fallback for any shape
this decoder does not model.
-}
encodedValue : Json.Decode.Decoder String
encodedValue =
    Json.Decode.map (Json.Encode.encode 2) Json.Decode.value


labelWith : String -> String -> String
labelWith label qualifier =
    if String.isEmpty qualifier then
        label

    else
        label ++ " · " ++ qualifier


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)
