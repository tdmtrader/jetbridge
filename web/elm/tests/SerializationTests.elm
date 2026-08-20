module SerializationTests exposing (all)

import Concourse exposing (JsonValue(..))
import Concourse.BuildStatus as BuildStatus
import Data
import Dict
import Expect
import Json.Decode
import Json.Encode
import Test exposing (Test, describe, test)
import Time


all : Test
all =
    describe "type serialization/deserialization" <|
        let
            instanceVars =
                Dict.fromList [ ( "k", JsonString "v" ) ]
        in
        [ test "job encoding/decoding are inverses" <|
            \_ ->
                let
                    job =
                        Data.job 1
                            |> Data.withPipelineInstanceVars instanceVars
                in
                job
                    |> Concourse.encodeJob
                    |> Json.Decode.decodeValue Concourse.decodeJob
                    |> Expect.equal (Ok job)
        , test "resource encoding/decoding are inverses" <|
            \_ ->
                let
                    resource =
                        Data.resource (Just "version")
                            |> Data.withPipelineInstanceVars instanceVars
                in
                resource
                    |> Concourse.encodeResource
                    |> Json.Decode.decodeValue Concourse.decodeResource
                    |> Expect.equal (Ok resource)
        , test "build encoding/decoding are inverses" <|
            \_ ->
                let
                    build =
                        Data.jobBuild BuildStatus.BuildStatusPending
                            |> Data.withTeamName "t"
                            |> Data.withDuration
                                { startedAt =
                                    Just <| Time.millisToPosix 1000
                                , finishedAt =
                                    Just <| Time.millisToPosix 2000
                                }
                            |> Data.withJob
                                (Just
                                    (Data.jobId
                                        |> Data.withTeamName "t"
                                        |> Data.withPipelineInstanceVars instanceVars
                                    )
                                )
                in
                build
                    |> Concourse.encodeBuild
                    |> Json.Decode.decodeValue Concourse.decodeBuild
                    |> Expect.equal (Ok build)
        , test "pipeline encoding/decoding are inverses" <|
            \_ ->
                let
                    pipeline =
                        Data.pipeline "team" 1
                            |> Data.withInstanceVars instanceVars
                in
                pipeline
                    |> Concourse.encodePipeline
                    |> Json.Decode.decodeValue Concourse.decodePipeline
                    |> Expect.equal (Ok pipeline)
        , test "pre-feature pipeline caches decode with safe run defaults" <|
            \_ ->
                """
                {"id":1,"name":"pipeline-1","instance_vars":{},"paused":false,"archived":false,"public":true,"team_name":"team","groups":[],"last_updated":0,"display":{"background_image":null,"background_filter":null}}
                """
                    |> Json.Decode.decodeString Concourse.decodePipeline
                    |> Result.map
                        (\pipeline ->
                            { template = pipeline.template
                            , runNumber = pipeline.runNumber
                            , runTemplateRef = pipeline.runTemplateRef
                            , paramsSchemaLength = List.length pipeline.paramsSchema
                            , lastRunNumber = pipeline.lastRunNumber
                            , canCreateRun = pipeline.canCreateRun
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { template = Nothing
                            , runNumber = Nothing
                            , runTemplateRef = Nothing
                            , paramsSchemaLength = 0
                            , lastRunNumber = Nothing
                            , canCreateRun = False
                            }
                        )
        , test "new pipeline cache values round-trip" <|
            \_ ->
                let
                    basePipeline =
                        Data.pipeline "team" 1

                    pipeline =
                        { basePipeline
                            | template = Just True
                            , paramsSchema = []
                            , lastRunNumber = Just 0
                            , canCreateRun = False
                        }
                in
                pipeline
                    |> Concourse.encodePipeline
                    |> Json.Decode.decodeValue Concourse.decodePipeline
                    |> Expect.equal (Ok pipeline)
        , test "decodes the API bool parameter literal" <|
            \_ ->
                """
                {"id":1,"name":"template","instance_vars":{},"paused":false,"archived":false,"public":true,"team_name":"team","groups":[],"last_updated":0,"display":{},"template":true,"params_schema":[{"name":"enabled","type":"bool","required":false,"default":false,"values":[],"description":"toggle"}],"last_run_number":0,"can_create_run":true}
                """
                    |> Json.Decode.decodeString Concourse.decodePipeline
                    |> Result.map (.paramsSchema >> List.map .type_)
                    |> Expect.equal (Ok [ Concourse.BoolParam ])
        , test "encodes BoolParam with the API bool literal" <|
            \_ ->
                let
                    basePipeline =
                        Data.pipeline "team" 1

                    pipeline =
                        { basePipeline
                            | template = Just True
                            , paramsSchema = [ { name = "enabled", type_ = Concourse.BoolParam, required = False, default = Just <| JsonRaw <| Json.Encode.bool False, values = [], description = Nothing } ]
                            , lastRunNumber = Just 0
                            , canCreateRun = True
                        }
                in
                pipeline
                    |> Concourse.encodePipeline
                    |> Json.Decode.decodeValue (Json.Decode.at [ "params_schema" ] (Json.Decode.list (Json.Decode.field "type" Json.Decode.string)))
                    |> Expect.equal (Ok [ "bool" ])
        , test "team encoding/decoding are inverses" <|
            \_ ->
                let
                    team =
                        { id = 1
                        , name = "team"
                        }
                in
                team
                    |> Concourse.encodeTeam
                    |> Json.Decode.decodeValue Concourse.decodeTeam
                    |> Expect.equal (Ok team)
        ]
