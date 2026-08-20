module RunContextTests exposing (all)

import Concourse
import Concourse.BuildStatus as BuildStatus
import Concourse.PipelineRun as PipelineRun
import Data
import Dict
import Expect
import Http
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Pipeline.Pipeline as Pipeline
import Test exposing (Test, describe, test)
import Time
import UpdateMsg exposing (UpdateMsg(..))


all : Test
all =
    describe "numbered run context"
        [ test "fetches the durable header before its payload" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Tuple.second
                    |> Expect.equal [ FetchPipelineRun template 42 ]
        , test "fetches the exact returned payload reference" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched (Ok liveRun))
                    |> Tuple.second
                    |> Expect.equal [ FetchPipelineRun template 42, FetchPipeline returnedRef ]
        , test "marks a missing durable header as not found" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched Data.httpNotFound)
                    |> Tuple.first
                    |> Pipeline.getUpdateMessage
                    |> Expect.equal NotFound
        , test "sends header authorization failures through login without treating the run as reclaimed" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched Data.httpUnauthorized)
                    |> Tuple.second
                    |> expectEffect RedirectToLogin
        , test "retries transient durable-header failures" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched (Err Http.NetworkError))
                    |> Tuple.second
                    |> expectEffect (FetchPipelineRun template 42)
        , test "refetches the header once when a live payload disappears" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched (Ok liveRun))
                    |> Pipeline.handleCallback (PipelineFetched Data.httpNotFound)
                    |> Tuple.second
                    |> expectEffect (FetchPipelineRun template 42)
        , test "retries transient payload failures against the returned reference" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched (Ok liveRun))
                    |> Pipeline.handleCallback (PipelineFetched (Err Http.NetworkError))
                    |> Tuple.second
                    |> expectEffect (FetchPipeline returnedRef)
        , test "canonicalizes payload URLs only from explicit run fields" <|
            \_ ->
                Pipeline.init pipelineFlags
                    |> Pipeline.handleCallback (PipelineFetched (Ok payload))
                    |> Tuple.second
                    |> expectEffect (ModifyUrl "/teams/team/pipelines/renamed-template/runs/42")
        , test "keeps ordinary run-shaped instances on their stock URL" <|
            \_ ->
                Pipeline.init pipelineFlags
                    |> Pipeline.handleCallback (PipelineFetched (Ok ordinaryRunShaped))
                    |> Tuple.second
                    |> expectNoEffect (ModifyUrl "/teams/team/pipelines/renamed-template/runs/42")
        ]


template : Concourse.PipelineIdentifier
template =
    { teamName = "team", pipelineName = "template", pipelineInstanceVars = Dict.empty }


returnedRef : Concourse.PipelineIdentifier
returnedRef =
    { teamName = "team", pipelineName = "renamed-instance", pipelineInstanceVars = Dict.fromList [ ( "run", Concourse.JsonNumber 42 ) ] }


liveRun : PipelineRun.PipelineRun
liveRun =
    { id = 1
    , number = 42
    , status = BuildStatus.BuildStatusStarted
    , params = Dict.empty
    , createdBy = Nothing
    , createdAt = Time.millisToPosix 0
    , completedAt = Nothing
    , reclaimed = False
    , instanceRef = Just returnedRef
    }


pipelineFlags : Pipeline.Flags
pipelineFlags =
    { pipelineLocator = returnedRef, turbulenceImgSrc = "", selectedGroups = [] }


payload : Concourse.Pipeline
payload =
    Data.pipeline "team" 2
        |> Data.withName "payload"
        |> Data.withInstanceVars returnedRef.pipelineInstanceVars
        |> (\pipeline ->
                { pipeline
                    | template = Just False
                    , runNumber = Just 42
                    , runTemplateRef = Just { template | pipelineName = "renamed-template" }
                }
           )


ordinaryRunShaped : Concourse.Pipeline
ordinaryRunShaped =
    Data.pipeline "team" 3
        |> Data.withInstanceVars returnedRef.pipelineInstanceVars


expectEffect : Effect -> List Effect -> Expect.Expectation
expectEffect effect effects =
    if List.member effect effects then
        Expect.pass
    else
        Expect.fail ("Expected effect: " ++ Debug.toString effect)


expectNoEffect : Effect -> List Effect -> Expect.Expectation
expectNoEffect effect effects =
    if List.member effect effects then
        Expect.fail ("Unexpected effect: " ++ Debug.toString effect)
    else
        Expect.pass
