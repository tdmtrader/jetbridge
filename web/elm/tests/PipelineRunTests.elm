module PipelineRunTests exposing (all)

import Concourse
import Concourse.BuildStatus exposing (BuildStatus(..))
import Concourse.PipelineRun as PipelineRun
import Dict
import Expect
import Json.Decode
import Json.Encode
import Test exposing (Test, describe, test)
import Time


all : Test
all =
    describe "pipeline run decoding"
        [ test "encodes the create body with the typed vars envelope" <|
            \_ ->
                PipelineRun.encodeCreatePipelineRun
                    (Dict.fromList
                        [ ( "count", Concourse.JsonNumber 2 )
                        , ( "enabled", Concourse.JsonRaw <| Json.Encode.bool True )
                        ]
                    )
                    |> Json.Encode.encode 0
                    |> Expect.equal "{\"vars\":{\"count\":2,\"enabled\":true}}"
        , test "decodes running as started and keeps numeric and Boolean params typed" <|
            \_ ->
                """
                {"id":7,"number":3,"status":"running","params":{"count":2,"enabled":true},"created_at":1700000000,"completed_at":null,"reclaimed":false}
                """
                    |> Json.Decode.decodeString PipelineRun.decodePipelineRun
                    |> Result.map
                        (\run ->
                            { status = run.status
                            , params = Json.Encode.encode 0 (Concourse.encodeInstanceVars run.params)
                            , createdAt = run.createdAt
                            , completedAt = run.completedAt
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { status = BuildStatusStarted
                            , params = "{\"count\":2,\"enabled\":true}"
                            , createdAt = Time.millisToPosix 1700000000000
                            , completedAt = Nothing
                            }
                        )
        , test "decodes base, run payload, and ordinary pipeline markers independently" <|
            \_ ->
                [ """
                  {"id":1,"name":"template","instance_vars":{},"paused":false,"archived":false,"public":true,"team_name":"team","groups":[],"last_updated":0,"display":{},"template":true,"params_schema":[{"name":"size","type":"number","required":true,"default":2,"values":[1,2],"description":"worker count"}],"last_run_number":0,"can_create_run":false}
                  """
                , """
                  {"id":2,"name":"run-instance","instance_vars":{"run":3},"paused":false,"archived":false,"public":true,"team_name":"team","groups":[],"last_updated":0,"display":{},"template":false,"run_number":3,"run_template_ref":{"team_name":"team","pipeline_name":"template","instance_vars":{}}}
                  """
                , """
                  {"id":3,"name":"ordinary","instance_vars":{},"paused":false,"archived":false,"public":true,"team_name":"team","groups":[],"last_updated":0,"display":{}}
                  """
                ]
                    |> List.map
                        (Json.Decode.decodeString Concourse.decodePipeline
                            >> Result.map
                                (\pipeline ->
                                    { template = pipeline.template
                                    , runNumber = pipeline.runNumber
                                    , templateRef = pipeline.runTemplateRef |> Maybe.map .pipelineName
                                    , schema =
                                        pipeline.paramsSchema
                                            |> List.map
                                                (\param ->
                                                    { name = param.name
                                                    , type_ = param.type_
                                                    , required = param.required
                                                    , default = param.default |> Maybe.map (Concourse.encodeJsonValue >> Json.Encode.encode 0)
                                                    , valueCount = List.length param.values
                                                    , description = param.description
                                                    }
                                                )
                                    , lastRunNumber = pipeline.lastRunNumber
                                    , canCreateRun = pipeline.canCreateRun
                                    }
                                )
                        )
                    |> Expect.equal
                        [ Ok
                            { template = Just True
                            , runNumber = Nothing
                            , templateRef = Nothing
                            , schema =
                                [ { name = "size"
                                  , type_ = Concourse.NumberParam
                                  , required = True
                                  , default = Just "2"
                                  , valueCount = 2
                                  , description = Just "worker count"
                                  }
                                ]
                            , lastRunNumber = Just 0
                            , canCreateRun = False
                            }
                        , Ok
                            { template = Just False
                            , runNumber = Just 3
                            , templateRef = Just "template"
                            , schema = []
                            , lastRunNumber = Nothing
                            , canCreateRun = False
                            }
                        , Ok
                            { template = Nothing
                            , runNumber = Nothing
                            , templateRef = Nothing
                            , schema = []
                            , lastRunNumber = Nothing
                            , canCreateRun = False
                            }
                        ]
        , test "decodes terminal statuses exactly and leaves instance ref optional" <|
            \_ ->
                [ ( "succeeded", BuildStatusSucceeded )
                , ( "failed", BuildStatusFailed )
                , ( "errored", BuildStatusErrored )
                , ( "aborted", BuildStatusAborted )
                ]
                    |> List.map
                        (\( status, expected ) ->
                            ( "{\"id\":7,\"number\":3,\"status\":\""
                                ++ status
                                ++ "\",\"created_at\":0,\"reclaimed\":true}"
                            )
                                |> Json.Decode.decodeString PipelineRun.decodePipelineRun
                                |> Result.map (\run -> ( run.status, run.instanceRef, run.reclaimed ))
                                |> (==) (Ok ( expected, Nothing, True ))
                        )
                    |> List.all identity
                    |> Expect.equal True
        , test "decodes the actual instance reference when present" <|
            \_ ->
                """
                {"id":7,"number":3,"status":"succeeded","created_at":0,"reclaimed":false,"instance_ref":{"team_name":"team","pipeline_name":"generated","instance_vars":{"count":2}}}
                """
                    |> Json.Decode.decodeString PipelineRun.decodePipelineRun
                    |> Result.map (\run -> run.instanceRef |> Maybe.map .pipelineName)
                    |> Expect.equal (Ok (Just "generated"))
        ]
