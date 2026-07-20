module AgentEventTests exposing (all)

import Concourse.AgentEvent as AE
import Expect
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Concourse.AgentEvent.parseTranscript"
        [ test "decodes step.start / cost.record / step.end into ordered entries" <|
            \_ ->
                let
                    ndjson =
                        String.join "\n"
                            [ """{"ts":"2026-07-19T00:00:01Z","event":"step.start","data":{"step_name":"implement","build_id":42,"plan_id":"p1"}}"""
                            , """{"ts":"2026-07-19T00:00:02Z","event":"cost.record","data":{"source":"agent_step","model":"claude","input_tokens":100,"output_tokens":20,"turns":3,"cost_usd":0.05}}"""
                            , """{"ts":"2026-07-19T00:00:03Z","event":"step.end","data":{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":9,"cost_usd":0.05,"turns":3}}"""
                            ]

                    t =
                        AE.parseTranscript ndjson
                in
                Expect.equal ( List.length t.entries, t.skipped ) ( 3, 0 )
        , test "skips blank and unparseable lines, counting the garbled ones" <|
            \_ ->
                let
                    ndjson =
                        String.join "\n"
                            [ ""
                            , "not json at all"
                            , """{"ts":"2026-07-19T00:00:01Z","event":"push.done","data":{"branch":"agent/ticket-12","sha":"abc123","manifest_artifact":"m"}}"""
                            , "   "
                            ]

                    t =
                        AE.parseTranscript ndjson
                in
                Expect.equal ( List.length t.entries, t.skipped ) ( 1, 1 )
        , test "an unknown event type is kept as an Unknown entry, not dropped" <|
            \_ ->
                let
                    t =
                        AE.parseTranscript """{"ts":"2026-07-19T00:00:01Z","event":"tool.call","data":{"tool":"Bash"}}"""
                in
                case List.map .body t.entries of
                    [ AE.ToolCalled call ] ->
                        Expect.equal call.tool "Bash"

                    _ ->
                        Expect.fail "expected a single ToolCalled entry"
        ]
