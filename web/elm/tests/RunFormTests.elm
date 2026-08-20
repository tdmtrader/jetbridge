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
        , test "keeps a required enum without a default empty and invalid" <|
            \_ ->
                RunForm.init [ requiredEnum ]
                    |> Expect.all
                        [ RunForm.value "environment" >> Expect.equal ""
                        , RunForm.encode [ requiredEnum ]
                            >> Expect.equal (Err { fieldId = Just "run-param-environment", message = "environment is required" })
                        ]
        , test "encodes a required empty string default" <|
            \_ ->
                RunForm.init [ requiredEmptyString ]
                    |> encoded [ requiredEmptyString ]
                    |> Expect.equal (Ok "{\"name\":\"\"}")
        , test "encodes an optional explicit empty string while omitting an absent one" <|
            \_ ->
                ( RunForm.init [ optionalString ]
                    |> encoded [ optionalString ]
                , RunForm.init [ optionalString ]
                    |> RunForm.set "note" ""
                    |> encoded [ optionalString ]
                )
                    |> Expect.equal ( Ok "{}", Ok "{\"note\":\"\"}" )
        , test "encodes whitespace-only strings verbatim" <|
            \_ ->
                RunForm.init [ requiredString ]
                    |> RunForm.set "message" " \t "
                    |> encoded [ requiredString ]
                    |> Expect.equal (Ok "{\"message\":\" \\t \"}")
        , test "keeps blank optional non-string fields omitted" <|
            \_ ->
                RunForm.init optionalScalars
                    |> RunForm.set "count" ""
                    |> RunForm.set "enabled" ""
                    |> RunForm.set "environment" ""
                    |> encoded optionalScalars
                    |> Expect.equal (Ok "{}")
        ]


schemas : List Concourse.ParamSchema
schemas =
    [ { name = "name", type_ = Concourse.StringParam, required = True, default = Nothing, values = [], description = Just "release name" }
    , { name = "count", type_ = Concourse.NumberParam, required = True, default = Nothing, values = [], description = Nothing }
    , { name = "enabled", type_ = Concourse.BoolParam, required = True, default = Nothing, values = [], description = Nothing }
    , { name = "environment", type_ = Concourse.EnumParam, required = True, default = Nothing, values = [ Concourse.JsonString "staging", Concourse.JsonString "production" ], description = Nothing }
    ]


requiredEnum : Concourse.ParamSchema
requiredEnum =
    { name = "environment", type_ = Concourse.EnumParam, required = True, default = Nothing, values = [ Concourse.JsonString "staging", Concourse.JsonString "production" ], description = Nothing }


requiredEmptyString : Concourse.ParamSchema
requiredEmptyString =
    { name = "name", type_ = Concourse.StringParam, required = True, default = Just <| Concourse.JsonString "", values = [], description = Nothing }


optionalString : Concourse.ParamSchema
optionalString =
    { name = "note", type_ = Concourse.StringParam, required = False, default = Nothing, values = [], description = Nothing }


requiredString : Concourse.ParamSchema
requiredString =
    { name = "message", type_ = Concourse.StringParam, required = True, default = Nothing, values = [], description = Nothing }


optionalScalars : List Concourse.ParamSchema
optionalScalars =
    [ { name = "count", type_ = Concourse.NumberParam, required = False, default = Nothing, values = [], description = Nothing }
    , { name = "enabled", type_ = Concourse.BoolParam, required = False, default = Nothing, values = [], description = Nothing }
    , { name = "environment", type_ = Concourse.EnumParam, required = False, default = Nothing, values = [ Concourse.JsonString "staging" ], description = Nothing }
    ]


encoded : List Concourse.ParamSchema -> RunForm.Model -> Result RunForm.ValidationError String
encoded paramSchemas form =
    form
        |> RunForm.encode paramSchemas
        |> Result.map (Concourse.encodeInstanceVars >> Json.Encode.encode 0)
