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
import Routes
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, containing, id, tag, text)
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
                        , Effects.GetCurrentTime
                        ]
        , test "uses the route keyset page instead of replacing it" <|
            \_ ->
                PipelineRuns.init { id = Data.shortPipelineId, page = Just { direction = Pagination.To 25, limit = 20 } }
                    |> Tuple.second
                    |> Expect.equal
                        [ Effects.FetchPipeline Data.shortPipelineId
                        , Effects.FetchPipelineRuns Data.shortPipelineId { direction = Pagination.To 25, limit = 20 }
                        , Effects.GetCurrentTime
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
                    modelAfterSubmit =
                        pageWithTemplate
                            |> Application.update (Update OpenPipelineRunForm)
                            |> Tuple.first
                            |> Application.update (Update (SetPipelineRunParam "environment" "staging"))
                            |> Tuple.first
                            |> Application.update (Update SubmitPipelineRun)
                            |> Tuple.first
                in
                modelAfterSubmit
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
        , test "does not duplicate a create request while submission is pending" <|
            \_ ->
                submitted
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.second
                    |> Expect.equal []
        , test "disables editable controls while submission is pending" <|
            \_ ->
                submitted
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment" ]
                    |> Query.has [ attribute <| Attr.disabled True ]
        , test "holds resubmission through a conflict refresh and preserves the value" <|
            \_ ->
                let
                    afterConflict =
                        submitted
                            |> Application.handleCallback (PipelineRunCreated (Err conflict))
                            |> Tuple.first
                in
                afterConflict
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.second
                    |> Expect.equal []
        , test "treats a refresh-worthy bad request like a conflict" <|
            \_ ->
                submitted
                    |> Application.handleCallback (PipelineRunCreated (Err badRequest))
                    |> Tuple.first
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.second
                    |> Expect.equal []
        , test "connects the first invalid input to a concrete validation error and focuses it" <|
            \_ ->
                let
                    invalid =
                        pageWithTemplate
                            |> Application.update (Update OpenPipelineRunForm)
                            |> Tuple.first
                            |> Application.update (Update SubmitPipelineRun)
                in
                invalid
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment" ]
                    |> Query.has [ attribute <| Attr.attribute "aria-describedby" "run-param-environment-description run-param-environment-error" ]
        , test "focuses the first invalid input" <|
            \_ ->
                pageWithTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.second
                    |> Expect.equal [ Effects.Focus "run-param-environment" ]
        , test "renders the first validation error at the described element" <|
            \_ ->
                pageWithTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment-error" ]
                    |> Query.has [ text "environment is required" ]
        , test "shows a deterministic age for an active run" <|
            \_ ->
                pageWithRuns
                    |> Application.handleCallback (GotCurrentTime (Time.millisToPosix 2000))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "2s ago" ]
        , test "focuses the mounted server error and fetches the template after a conflict" <|
            \_ ->
                submitted
                    |> Application.handleCallback (PipelineRunCreated (Err conflict))
                    |> Tuple.second
                    |> Expect.equal [ Effects.FetchPipeline Data.pipelineId, Effects.Focus "run-form-error" ]
        , test "marks the form busy while its controls are disabled" <|
            \_ ->
                submitted
                    |> Common.queryView
                    |> Query.find [ tag "form" ]
                    |> Query.has [ attribute <| Attr.attribute "aria-busy" "true" ]
        , test "keeps paused template fields editable while holding the submit action" <|
            \_ ->
                pageFor pausedTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment" ]
                    |> Query.hasNot [ attribute <| Attr.disabled True ]
        , test "keeps archived template fields editable while holding the submit action" <|
            \_ ->
                pageFor archivedTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-param-environment" ]
                    |> Query.hasNot [ attribute <| Attr.disabled True ]
        , test "does not render a creation action for a non-writer" <|
            \_ ->
                pageFor nonWriterTemplate
                    |> Common.queryView
                    |> Query.hasNot [ text "Start a run", tag "form" ]
        , test "renders a loading state" <|
            \_ ->
                Common.init "/teams/team/pipelines/pipeline/runs" |> Common.queryView |> Query.has [ text "Loading run history…" ]
        , test "renders retry and private states" <|
            \_ ->
                pageWithTemplate
                    |> Application.handleCallback (PipelineRunsFetched Data.httpInternalServerError)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "Retry" ]
        , test "renders the private-template state" <|
            \_ ->
                Common.init "/teams/team/pipelines/pipeline/runs"
                    |> Application.handleCallback (PipelineFetched Data.httpForbidden)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "You do not have access to this pipeline." ]
        , test "routes a missing template to not found" <|
            \_ ->
                Common.init "/teams/team/pipelines/pipeline/runs"
                    |> Application.handleCallback (PipelineFetched Data.httpNotFound)
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.has [ text "this page was not found" ]
        , test "uses scoped headers and route-driven pager links" <|
            \_ ->
                pagedRuns
                    |> Common.queryView
                    |> Query.find [ tag "th", attribute <| Attr.attribute "scope" "col", containing [ text "Run" ] ]
                    |> Query.has [ text "Run" ]
        , test "keeps the response keyset page in the next-page native link" <|
            \_ ->
                pagedRuns
                    |> Common.queryView
                    |> Query.find [ tag "a", attribute <| Attr.href (Routes.toString <| Routes.PipelineRuns { id = Data.pipelineId, page = Just { direction = Pagination.To 10, limit = 50 } }) ]
                    |> Query.has [ attribute <| Attr.attribute "aria-label" "Next page" ]
        , test "keeps the server error in the permanently mounted live region during a refresh hold" <|
            \_ ->
                submitted
                    |> Application.handleCallback (PipelineRunCreated (Err badRequest))
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-form-error" ]
                    |> Query.has [ attribute <| Attr.attribute "aria-live" "polite", text "template changed" ]
        , test "does not let a racing history response erase a refresh-hold error" <|
            \_ ->
                submitted
                    |> Application.handleCallback (PipelineRunCreated (Err conflict))
                    |> Tuple.first
                    |> Application.handleCallback
                        (PipelineRunsFetched
                            (Ok
                                ( { direction = Pagination.ToMostRecent, limit = 50 }
                                , { content = [], pagination = { previousPage = Nothing, nextPage = Nothing } }
                                )
                            )
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ id "run-form-error" ]
                    |> Query.has [ text "template changed" ]
        , test "holds paused creation but leaves values editable" <|
            \_ ->
                pageFor pausedTemplate
                    |> Application.update (Update OpenPipelineRunForm)
                    |> Tuple.first
                    |> Application.update (Update (SetPipelineRunParam "environment" "staging"))
                    |> Tuple.first
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.second
                    |> Expect.equal [ Effects.Focus "run-form-error" ]
        , test "derives the paused hold after the conflict refresh settles" <|
            \_ ->
                submitted
                    |> Application.handleCallback (PipelineRunCreated (Err conflict))
                    |> Tuple.first
                    |> Application.handleCallback (PipelineFetched (Ok pausedTemplate))
                    |> Tuple.first
                    |> Application.update (Update SubmitPipelineRun)
                    |> Tuple.second
                    |> Expect.equal [ Effects.Focus "run-form-error" ]
        , test "keeps reclaimed numbers out of the anchor tab order" <|
            \_ ->
                pageWithRuns
                    |> Common.queryView
                    |> Query.hasNot [ tag "a", text "#41" ]
        , test "does not render deferred search, filters, jump, prefill, or fly controls" <|
            \_ ->
                pageWithRuns
                    |> Common.queryView
                    |> Query.hasNot [ text "Search", text "Fly", text "Jump to run" ]
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


pagedRuns : Application.Model
pagedRuns =
    pageWithTemplate
        |> Application.handleCallback
            (PipelineRunsFetched
                (Ok
                    ( { direction = Pagination.ToMostRecent, limit = 50 }
                    , { content = [ liveRun ], pagination = { previousPage = Nothing, nextPage = Just { direction = Pagination.To 10, limit = 50 } } }
                    )
                )
            )
        |> Tuple.first


opened : Application.Model
opened =
    pageWithTemplate
        |> Application.update (Update OpenPipelineRunForm)
        |> Tuple.first
        |> Application.update (Update (SetPipelineRunParam "environment" "staging"))
        |> Tuple.first


submitted : Application.Model
submitted =
    opened
        |> Application.update (Update SubmitPipelineRun)
        |> Tuple.first


template : Concourse.Pipeline
template =
    let pipeline = Data.pipeline "team" 1 in
    { pipeline
        | template = Just True
        , canCreateRun = True
        , paramsSchema = [ { name = "environment", type_ = Concourse.EnumParam, required = True, default = Nothing, values = [ Concourse.JsonString "staging" ], description = Just "where to deploy" } ]
    }


pausedTemplate : Concourse.Pipeline
pausedTemplate =
    { template | paused = True }


archivedTemplate : Concourse.Pipeline
archivedTemplate =
    { template | archived = True }


nonWriterTemplate : Concourse.Pipeline
nonWriterTemplate =
    { template | canCreateRun = False }


pageFor : Concourse.Pipeline -> Application.Model
pageFor pipeline =
    Common.init "/teams/team/pipelines/pipeline/runs"
        |> Application.handleCallback (PipelineFetched (Ok pipeline))
        |> Tuple.first


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


badRequest : Http.Error
badRequest =
    Http.BadStatus
        { url = "http://example.com"
        , status = { code = 400, message = "template changed" }
        , headers = Dict.empty
        , body = ""
        }
