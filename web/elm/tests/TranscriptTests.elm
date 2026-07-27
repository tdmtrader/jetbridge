module TranscriptTests exposing (all)

import Concourse.Transcript as Transcript
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "agent transcript"
        [ describe "index decoder"
            [ test "decodes one transcript ref" <|
                \_ ->
                    """
                    {"plan_id":"p1","function_id":"implement","step_name":"implement",
                     "build_id":42,"workflow_run_id":"9007199254740993",
                     "byte_len":2048,"truncated":true}
                    """
                        |> Json.Decode.decodeString Transcript.decodeRef
                        |> Expect.equal
                            (Ok
                                { planId = "p1"
                                , functionId = "implement"
                                , stepName = "implement"
                                , buildId = 42
                                , byteLen = 2048
                                , truncated = True
                                }
                            )
            , test "tolerates a ref with only the fields the view needs" <|
                \_ ->
                    """{"plan_id":"p1"}"""
                        |> Json.Decode.decodeString Transcript.decodeRef
                        |> Expect.equal
                            (Ok
                                { planId = "p1"
                                , functionId = ""
                                , stepName = ""
                                , buildId = 0
                                , byteLen = 0
                                , truncated = False
                                }
                            )
            ]
        , describe "ndjson parsing"
            [ test "reads a turn as role and tool rows, in stream order" <|
                \_ ->
                    conversation
                        |> Transcript.parse
                        |> List.map (\entry -> ( entry.kind, entry.label ))
                        |> Expect.equal
                            [ ( Transcript.System, "system · init" )
                            , ( Transcript.Assistant, "assistant" )
                            , ( Transcript.ToolCall, "tool · Bash" )
                            , ( Transcript.ToolResult, "tool result" )
                            , ( Transcript.Outcome, "result · success" )
                            ]
            , test "indexes entries across lines so a turn's blocks stay addressable" <|
                \_ ->
                    conversation
                        |> Transcript.parse
                        |> List.map .index
                        |> Expect.equal [ 0, 1, 2, 3, 4 ]
            , test "shows prose inline and puts tool bodies behind a toggle" <|
                \_ ->
                    conversation
                        |> Transcript.parse
                        |> List.map .collapsible
                        |> Expect.equal [ True, False, True, True, False ]
            , test "renders the assistant's own words, not the envelope" <|
                \_ ->
                    conversation
                        |> Transcript.parse
                        |> List.filter (\entry -> entry.kind == Transcript.Assistant)
                        |> List.map .body
                        |> Expect.equal [ "let me look at the repo" ]
            , test "pretty-prints a tool call's input" <|
                \_ ->
                    conversation
                        |> Transcript.parse
                        |> List.filter (\entry -> entry.kind == Transcript.ToolCall)
                        |> List.map .body
                        |> Expect.equal [ "{\n  \"command\": \"git status\"\n}" ]
            , test "reads a tool result whose content is a block list" <|
                \_ ->
                    """{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}]}}"""
                        |> Transcript.parse
                        |> List.map .body
                        |> Expect.equal [ "line one\nline two" ]
            , test "flags an errored tool result" <|
                \_ ->
                    """{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"boom"}]}}"""
                        |> Transcript.parse
                        |> List.map .label
                        |> Expect.equal [ "tool result · error" ]
            , test "surfaces the server-side head truncation marker" <|
                \_ ->
                    """{"type":"transcript_truncated","dropped_bytes":12,"note":"head-truncated to last 512KiB"}"""
                        |> Transcript.parse
                        |> List.map (\entry -> ( entry.kind, entry.body ))
                        |> Expect.equal
                            [ ( Transcript.Truncation
                              , "12 bytes dropped — head-truncated to last 512KiB"
                              )
                            ]
            , test "keeps an unparseable line verbatim instead of losing the transcript" <|
                \_ ->
                    ("""{"type":"assistant","message":{"content":[{"type":"text","text":"before"}]}}
"""
                        ++ "{\"type\":\"assis\n"
                        ++ """{"type":"assistant","message":{"content":[{"type":"text","text":"after"}]}}"""
                    )
                        |> Transcript.parse
                        |> List.map (\entry -> ( entry.kind, entry.body ))
                        |> Expect.equal
                            [ ( Transcript.Assistant, "before" )
                            , ( Transcript.Unparsed, "{\"type\":\"assis" )
                            , ( Transcript.Assistant, "after" )
                            ]
            , test "keeps a line shape it does not model, re-encoded" <|
                \_ ->
                    """{"type":"stream_event","event":"whatever"}"""
                        |> Transcript.parse
                        |> List.map (\entry -> ( entry.kind, entry.label ))
                        |> Expect.equal [ ( Transcript.Unparsed, "stream_event" ) ]
            , test "drops blank lines" <|
                \_ ->
                    "\n\n   \n"
                        |> Transcript.parse
                        |> Expect.equal []
            ]
        ]


{-| A trimmed but shape-faithful `claude --output-format stream-json` capture:
init, an assistant turn that speaks and calls a tool, the tool result, and the
final result envelope.
-}
conversation : String
conversation =
    String.join "\n"
        [ """{"type":"system","subtype":"init","model":"claude","tools":["Bash"]}"""
        , """{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"let me look at the repo"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}]}}"""
        , """{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"nothing to commit"}]}}"""
        , """{"type":"result","subtype":"success","result":"done","total_cost_usd":0.4,"num_turns":2}"""
        ]
