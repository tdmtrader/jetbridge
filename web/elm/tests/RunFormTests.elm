module RunFormTests exposing (all)

import Concourse
import Dict
import Expect
import Json.Encode
import PipelineRuns.RunForm as RunForm
import Test exposing (Test, describe, test)


all : Test
all =
    describe "run form encoding"
        [ test "coerces each declared scalar type before creating vars" <|
            \_ ->
                RunForm.init schemas
                    |> RunForm.set "name" "release"
                    |> RunForm.set "count" "2.5"
                    |> RunForm.set "enabled" "true"
                    |> RunForm.set "environment" "production"
                    |> RunForm.encode schemas
                    |> Result.map (Concourse.encodeInstanceVars >> Json.Encode.encode 0)
                    |> Expect.equal (Ok "{\"count\":2.5,\"enabled\":true,\"environment\":\"production\",\"name\":\"release\"}")
        , test "rejects the first missing required field" <|
            \_ ->
                RunForm.init schemas
                    |> RunForm.encode schemas
                    |> Expect.equal (Err { fieldId = Just "run-param-name", message = "name is required" })
        , test "rejects enum values outside the typed schema" <|
            \_ ->
                RunForm.init schemas
                    |> RunForm.set "name" "release"
                    |> RunForm.set "count" "2"
                    |> RunForm.set "enabled" "false"
                    |> RunForm.set "environment" "development"
                    |> RunForm.encode schemas
                    |> Expect.equal (Err { fieldId = Just "run-param-environment", message = "environment must be one of staging, production" })
        ]


schemas : List Concourse.ParamSchema
schemas =
    [ { name = "name", type_ = Concourse.StringParam, required = True, default = Nothing, values = [], description = Just "release name" }
    , { name = "count", type_ = Concourse.NumberParam, required = True, default = Nothing, values = [], description = Nothing }
    , { name = "enabled", type_ = Concourse.BoolParam, required = True, default = Nothing, values = [], description = Nothing }
    , { name = "environment", type_ = Concourse.EnumParam, required = True, default = Nothing, values = [ Concourse.JsonString "staging", Concourse.JsonString "production" ], description = Nothing }
    ]
