module RunContextTests exposing (all)

import Concourse
import Application.Application as Application
import Common
import Concourse.BuildStatus as BuildStatus
import Concourse.PipelineRun as PipelineRun
import Data
import Dict
import Expect
import Http
import Html
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..))
import Pipeline.Pipeline as Pipeline
import Routes
import Views.RunContext as RunContext
import Test exposing (Test, describe, test)
import Time
import UpdateMsg exposing (UpdateMsg(..))
import Test.Html.Query as Query
import Test.Html.Selector exposing (id, text)


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
        , test "keeps a transient durable-header failure visible until retry" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> withoutEffects
                    |> Pipeline.handleCallback (PipelineRunFetched (Err Http.NetworkError))
                    |> Tuple.second
                    |> expectNoEffect (FetchPipelineRun template 42)
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
        , test "keeps live payload authorization inaccessible without a login redirect or retry" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched (Ok liveRun))
                    |> withoutEffects
                    |> Pipeline.handleCallback (PipelineFetched Data.httpUnauthorized)
                    |> Tuple.second
                    |> Expect.all [ expectNoEffect RedirectToLogin, expectNoEffect (FetchPipeline returnedRef) ]
        , test "renders a reclaimed result when the second header wins the reclaim race" <|
            \_ ->
                runPage
                    |> Application.handleCallback (PipelineFetched Data.httpNotFound)
                    |> Tuple.first
                    |> Application.handleCallback (PipelineRunFetched (Ok { liveRun | reclaimed = True }))
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all
                        [ Query.has [ id "run-record", text "This run payload has been reclaimed." ]
                        , Query.hasNot [ text "The run payload is unavailable to this viewer." ]
                        ]
        , test "polls only the durable header before a payload is known" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleDelivery (ClockTicked FiveSeconds (Time.millisToPosix 0))
                    |> Tuple.second
                    |> Expect.all [ expectEffect (FetchPipelineRun template 42), expectNoEffect (FetchPipeline template) ]
        , test "does not poll a reclaimed record" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> Pipeline.handleCallback (PipelineRunFetched (Ok { liveRun | reclaimed = True, instanceRef = Nothing }))
                    |> Pipeline.handleDelivery (ClockTicked FiveSeconds (Time.millisToPosix 0))
                    |> Tuple.second
                    |> expectNoEffect (FetchPipeline template)
        , test "renders text for every durable context state" <|
            \_ ->
                let
                    contexts =
                        [ RunContext.Live liveRun payload
                        , RunContext.Completed { liveRun | status = BuildStatus.BuildStatusSucceeded } payload
                        , RunContext.RecordOnly liveRun
                        , RunContext.Reclaimed { liveRun | reclaimed = True }
                        ]
                in
                Expect.all
                    (List.map (\context _ -> Html.div [] [ RunContext.view Nothing context ] |> Query.fromHtml |> Query.has [ id "run-context", text "Status:" ]) contexts)
                    ()
        , test "renders an actionable retry for transient payload failure" <|
            \_ ->
                RunContext.view (Just "Unable to load this run payload.") (RunContext.Live liveRun payload)
                    |> (\view -> Html.div [] [ view ])
                    |> Query.fromHtml
                    |> Query.has [ text "Unable to load this run payload.", text "Retry" ]
        , test "keeps child authorization as an inaccessible record" <|
            \_ ->
                runPage
                    |> Application.handleCallback (PipelineFetched Data.httpUnauthorized)
                    |> Tuple.first
                    |> Common.queryView
                    |> Expect.all [ Query.has [ text "The run payload is unavailable to this viewer." ], Query.hasNot [ text "Retry" ] ]
        , test "shows retry UI for a transient child fetch" <|
            \_ ->
                runPage
                    |> Application.handleCallback (PipelineFetched (Err Http.NetworkError))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "Unable to load this run payload.", text "Retry" ]
        , test "spells every run status in its durable context" <|
            \_ ->
                Expect.all
                    (List.map
                        (\status _ ->
                            RunContext.view Nothing (RunContext.RecordOnly { liveRun | status = status })
                                |> (\view -> Html.div [] [ view ])
                                |> Query.fromHtml
                                |> Query.has [ text (BuildStatus.show status) ]
                        )
                        [ BuildStatus.BuildStatusPending, BuildStatus.BuildStatusStarted, BuildStatus.BuildStatusSucceeded, BuildStatus.BuildStatusFailed, BuildStatus.BuildStatusErrored, BuildStatus.BuildStatusAborted ]
                    )
                    ()
        , test "retries through the durable header when the retry action is chosen" <|
            \_ ->
                Pipeline.initRun { template = template, number = 42 }
                    |> withoutEffects
                    |> Pipeline.update RetryPipelineRuns
                    |> Tuple.second
                    |> Expect.equal [ FetchPipelineRun template 42 ]
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


withoutEffects : ( a, List Effect ) -> ( a, List Effect )
withoutEffects =
    Tuple.mapSecond (always [])


runPage : Application.Model
runPage =
    Common.initRoute (Routes.PipelineRun { template = template, number = 42 })
        |> Application.handleCallback (PipelineRunFetched (Ok liveRun))
        |> Tuple.first
