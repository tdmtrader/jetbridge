module AgentDecoderTests exposing (all)

import Concourse.Agent as Agent
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Concourse.Agent"
        [ test "workflow summary decoder preserves lifecycle fields" <|
            \_ ->
                """{"name":"legacy-dev","description":"the old flow","annotation":"migrate to standard-dev","hidden":true,"latest_version":1,"schema_version":3,"signature_version":1,"content_hash":"def456","live_version":1,"created_at":1751800000}"""
                    |> Json.Decode.decodeString
                        (Json.Decode.map
                            (\workflow -> ( workflow.annotation, workflow.hidden ))
                            Agent.decodeWorkflowSummary
                        )
                    |> Expect.equal (Ok ( "migrate to standard-dev", True ))
        ]
