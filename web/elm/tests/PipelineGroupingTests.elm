module PipelineGroupingTests exposing (all)

import Concourse
import Data
import Dict
import Expect
import Test exposing (Test, describe, test)


all : Test
all =
    describe "pipeline chrome grouping"
        [ test "recognizes only explicit payload presenter fields as run payloads" <|
            \_ ->
                payload
                    |> Concourse.isRunPayload
                    |> Expect.equal True
        , test "recognizes a payload marker even when its rename reference is absent" <|
            \_ ->
                { payload | runTemplateRef = Nothing }
                    |> Concourse.isRunPayload
                    |> Expect.equal True
        , test "keeps ordinary instances with a run variable in normal grouping" <|
            \_ ->
                (Data.pipeline "team" 2 |> Data.withInstanceVars (Dict.fromList [ ( "run", Concourse.JsonNumber 42 ) ]))
                    |> Concourse.isRunPayload
                    |> Expect.equal False
        ]


payload : Concourse.Pipeline
payload =
    Data.pipeline "team" 1
        |> (\pipeline ->
                { pipeline
                    | template = Just False
                    , runNumber = Just 42
                    , runTemplateRef = Just { teamName = "team", pipelineName = "template", pipelineInstanceVars = Dict.empty }
                }
           )
