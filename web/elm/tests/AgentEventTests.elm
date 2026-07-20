module AgentEventTests exposing (all)

import Concourse.AgentEvent as AE
import Expect
import Test exposing (Test, describe, test)


{-| A realistic slice of the claude-CLI `--output-format stream-json --verbose`
NDJSON that the transcript endpoint serves: session init, an assistant turn
carrying prose + a tool_use, the matching user tool_result, and the terminal
result summary.
-}
streamJson : String
streamJson =
    String.join "\n"
        [ """{"type":"system","subtype":"init","session_id":"s1","tools":["Bash","Read"]}"""
        , """{"type":"assistant","message":{"content":[{"type":"text","text":"Committing the change."},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"git commit -m wip"}}]}}"""
        , """{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":"[main abc123] wip"}]}}"""
        , """{"type":"result","subtype":"success","result":"\\"done\\"","model":"m9","total_cost_usd":0.75,"num_turns":4,"is_error":false,"usage":{"input_tokens":100,"output_tokens":50}}"""
        ]


all : Test
all =
    describe "Concourse.AgentEvent.parseTranscript"
        [ test "fans a full stream-json transcript into ordered timeline entries" <|
            \_ ->
                let
                    t =
                        AE.parseTranscript streamJson
                in
                Expect.equal
                    ( List.map .body t.entries, t.skipped )
                    ( [ AE.SystemInit { subtype = "init" }
                      , AE.Said { text = "Committing the change." }
                      , AE.ToolCalled { tool = "Bash", input = "git commit -m wip" }
                      , AE.ToolResulted { output = "[main abc123] wip", isError = False }
                      , AE.RunResult { subtype = "success", numTurns = 4, costUsd = 0.75, isError = False, result = "done" }
                      ]
                    , 0
                    )
        , test "a tool_use line yields a tool-call entry naming the tool with a summary of its input" <|
            \_ ->
                let
                    line =
                        """{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_9","name":"Read","input":{"file_path":"/etc/hosts"}}]}}"""
                in
                case List.map .body (AE.parseTranscript line).entries of
                    [ AE.ToolCalled call ] ->
                        Expect.equal ( call.tool, call.input ) ( "Read", "/etc/hosts" )

                    _ ->
                        Expect.fail "expected a single ToolCalled entry"
        , test "a result line yields a terminal summary with turns and cost, unquoting the doubly-encoded result" <|
            \_ ->
                let
                    line =
                        """{"type":"result","subtype":"error_max_turns","result":"\\"ran out\\"","num_turns":12,"total_cost_usd":1.5,"is_error":true}"""
                in
                case List.map .body (AE.parseTranscript line).entries of
                    [ AE.RunResult r ] ->
                        Expect.equal r
                            { subtype = "error_max_turns", numTurns = 12, costUsd = 1.5, isError = True, result = "ran out" }

                    _ ->
                        Expect.fail "expected a single RunResult entry"
        , test "one assistant line with several content items fans out to several entries with distinct seqs" <|
            \_ ->
                let
                    line =
                        """{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"ok"},{"type":"tool_use","name":"Grep","input":{"pattern":"todo"}}]}}"""

                    t =
                        AE.parseTranscript line
                in
                Expect.equal
                    ( List.map .body t.entries, List.map .seq t.entries )
                    ( [ AE.Thought { summary = "plan" }
                      , AE.Said { text = "ok" }
                      , AE.ToolCalled { tool = "Grep", input = "todo" }
                      ]
                    , [ 0, 1, 2 ]
                    )
        , test "tool_result content given as a list of text blocks is joined into one output" <|
            \_ ->
                let
                    line =
                        """{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_2","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}],"is_error":true}]}}"""
                in
                case List.map .body (AE.parseTranscript line).entries of
                    [ AE.ToolResulted r ] ->
                        Expect.equal r { output = "line one\nline two", isError = True }

                    _ ->
                        Expect.fail "expected a single ToolResulted entry"
        , test "the head-truncation marker decodes to a Truncated entry (not a skip)" <|
            \_ ->
                let
                    line =
                        """{"type":"transcript_truncated","dropped_bytes":2048,"note":"head-truncated to last 512KiB"}"""
                in
                case List.map .body (AE.parseTranscript line).entries of
                    [ AE.Truncated r ] ->
                        Expect.equal r { droppedBytes = 2048, note = "head-truncated to last 512KiB" }

                    _ ->
                        Expect.fail "expected a single Truncated entry"
        , test "skips blank lines, non-JSON, and the old ts/event envelope; counts the unparseable ones" <|
            \_ ->
                let
                    ndjson =
                        String.join "\n"
                            [ ""
                            , "not json at all"
                            , """{"ts":"2026-07-19T00:00:01Z","event":"push.done","data":{"branch":"agent/ticket-12"}}"""
                            , """{"type":"who_knows","payload":42}"""
                            , "   "
                            , """{"type":"system","subtype":"init"}"""
                            ]

                    t =
                        AE.parseTranscript ndjson
                in
                -- 1 recognized entry (system init); 3 skips: non-JSON, the old
                -- envelope (no top-level "type"), and the unknown "who_knows" type.
                Expect.equal ( List.length t.entries, t.skipped ) ( 1, 3 )
        ]
