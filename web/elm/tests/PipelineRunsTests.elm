module PipelineRunsTests exposing (all)

import Application.Application as Application
import Common
import Concourse
import Concourse.BuildStatus as BuildStatus
import Concourse.Pagination as Pagination
import Concourse.PipelineRun
import Data
import Dict
import Expect
import Html.Attributes as Attr
import Http
import Message.Effects as Effects
import Message.Message exposing (Message(..))
import Message.TopLevelMessage exposing (TopLevelMessage(..))
import Message.Callback exposing (Callback(..))
import PipelineRuns.PipelineRuns as PipelineRuns
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, id, tag, text)
import Time


all : Test
all =
    describe "pipeline runs page"
        [ test "fetches the template and newest fifty runs on initialization" <|
            \_ ->
                PipelineRuns.init { id = Data.shortPipelineId, page = Nothing }
                    |> Tuple.second
                    |> Expect.equal
                        [ Effects.FetchPipeline Data.shortPipelineId
                        , Effects.FetchPipelineRuns Data.shortPipelineId { direction = Pagination.ToMostRecent, limit = 50 }
                        ]
        , test "uses the route keyset page instead of replacing it" <|
            \_ ->
                PipelineRuns.init { id = Data.shortPipelineId, page = Just { direction = Pagination.To 25, limit = 20 } }
                    |> Tuple.second
                    |> Expect.equal
                        [ Effects.FetchPipeline Data.shortPipelineId
                        , Effects.FetchPipelineRuns Data.shortPipelineId { direction = Pagination.To 25, limit = 20 }
                        ]
        , test "keeps the form closed until the explicit start action" <|
            \_ ->
                pageWithTemplate
                    |> Common.queryView
                    |> Query.has
                        [ text "Start a run" ]
        , test "does not auto-open the form" <|
            \_ ->
                pageWithTemplate
                    |> Common.queryView
                    |> Query.hasNot [ tag "form" ]
        , test "shows the accessible form while history is still loading" <|
            \_ ->
                pageWithTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ tag "form" ]
                    |> Query.has [ attribute <| Attr.attribute "aria-busy" "false" ]
        , test "describes each parameter input" <|
            \_ ->
                pageWithTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment" ]
                    |> Query.has [ attribute <| Attr.attribute "aria-describedby" "run-param-environment-description" ]
        , test "uses a native pretty link only when the server gives an instance reference" <|
            \_ ->
                pageWithRuns
                    |> Common.queryView
                    |> Query.find [ tag "a", attribute <| Attr.href "/teams/team/pipelines/pipeline/runs/42" ]
                    |> Query.has [ attribute <| Attr.attribute "aria-label" "run #42" ]
        , test "renders a reclaimed number without link semantics" <|
            \_ ->
                pageWithRuns
                    |> Common.queryView
                    |> Query.find [ tag "span", text "#41" ]
                    |> Query.has []
        , test "keeps entered values and refreshes the template after a conflict" <|
            \_ ->
                let
                    submitted =
                        pageWithTemplate
                            |> Application.update (Update OpenPipelineRunForm)
                            |> Tuple.first
                            |> Application.update (Update (SetPipelineRunParam "environment" "staging"))
                            |> Tuple.first
                            |> Application.update (Update SubmitPipelineRun)
                            |> Tuple.first
                in
                submitted
                    |> Application.handleCallback (PipelineRunCreated (Err conflict))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment" ]
                    |> Query.has [ attribute <| Attr.value "staging" ]
        , test "navigates to the durable pretty route after creation" <|
            \_ ->
                pageWithTemplate
                    |> Application.handleCallback (PipelineRunCreated (Ok liveRun))
                    |> Tuple.second
                    |> Expect.equal [ Effects.NavigateTo "/teams/team/pipelines/pipeline/runs/42" ]
        ]


pageWithTemplate : Application.Model
pageWithTemplate =
    Common.init "/teams/team/pipelines/pipeline/runs"
        |> Application.handleCallback (PipelineFetched (Ok template))
        |> Tuple.first


pageWithRuns : Application.Model
pageWithRuns =
    pageWithTemplate
        |> Application.handleCallback
            (PipelineRunsFetched
                (Ok
                    ( { direction = Pagination.ToMostRecent, limit = 50 }
                    , { content = [ liveRun, reclaimedRun ], pagination = { previousPage = Nothing, nextPage = Nothing } }
                    )
                )
            )
        |> Tuple.first


template : Concourse.Pipeline
template =
    let pipeline = Data.pipeline "team" 1 in
    { pipeline
        | template = Just True
        , canCreateRun = True
        , paramsSchema = [ { name = "environment", type_ = Concourse.EnumParam, required = True, default = Nothing, values = [ Concourse.JsonString "staging" ], description = Just "where to deploy" } ]
    }


liveRun : Concourse.PipelineRun.PipelineRun
liveRun =
    { id = 1
    , number = 42
    , status = BuildStatus.BuildStatusStarted
    , params = Dict.empty
    , createdBy = Just "alice"
    , createdAt = Time.millisToPosix 0
    , completedAt = Nothing
    , reclaimed = False
    , instanceRef = Just { teamName = "team", pipelineName = "live", pipelineInstanceVars = Dict.empty }
    }


reclaimedRun : Concourse.PipelineRun.PipelineRun
reclaimedRun =
    { liveRun | number = 41, instanceRef = Nothing, reclaimed = True }


conflict : Http.Error
conflict =
    Http.BadStatus
        { url = "http://example.com"
        , status = { code = 409, message = "template changed" }
        , headers = Dict.empty
        , body = ""
        }
