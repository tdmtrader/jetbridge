module AgentTicketPageTests exposing (all)

import Application.Application as Application
import Common
import Concourse.AgentTicket as AgentTicket
import Data
import Dict
import Expect
import Html.Attributes
import Json.Decode
import Message.Callback as Callback
import Message.Effects as Effects
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, tag, text)
import Url


sampleDetailJson : String
sampleDetailJson =
    """
    { "ticket":
        { "id": 12, "title": "ship fly archives", "state": "needs_review"
        , "workflow_name": "develop", "body": "do the thing", "budget_usd": 5.0
        , "created_at": 200
        , "repo": "tdmtrader/jetbridge", "target_branch": "main", "branch": "agent/ticket-12"
        }
    , "spec": { "title": "spec title", "body": "spec body", "acceptance_criteria": ["crit a"] }
    , "tasks": [ { "ordering": 1, "title": "first task", "status": "done" } ]
    }
    """


queuedDetailJson : String
queuedDetailJson =
    """
    { "ticket": { "id": 9, "title": "queued work", "state": "queued", "workflow_name": "develop", "created_at": 50 }
    , "spec": null
    , "tasks": []
    }
    """


withDetail : String -> (AgentTicket.Detail -> Expect.Expectation) -> Expect.Expectation
withDetail json f =
    case Json.Decode.decodeString AgentTicket.decodeDetail json of
        Ok d ->
            f d

        Err e ->
            Expect.fail (Json.Decode.errorToString e)


initDetail : ( Application.Model, List Effects.Effect )
initDetail =
    Application.init Data.flags
        { protocol = Url.Http
        , host = ""
        , port_ = Nothing
        , path = "/agent-tickets/12"
        , query = Nothing
        , fragment = Nothing
        }


renderWith path callback =
    Common.init path
        |> Application.handleCallback callback
        |> Tuple.first
        |> Common.queryView


all : Test
all =
    describe "ticket detail page"
        [ test "decodes a detail fixture with a present spec" <|
            \_ ->
                withDetail sampleDetailJson (\d -> Expect.equal d.ticket.title "ship fly archives")
        , test "decodes a detail fixture with a null spec" <|
            \_ ->
                withDetail queuedDetailJson (\d -> Expect.equal d.spec Nothing)
        , test "fetches the ticket and its run metrics on load" <|
            \_ ->
                initDetail
                    |> Tuple.second
                    |> Expect.all
                        [ Common.contains (Effects.FetchAgentTicket 12)
                        , Common.contains (Effects.FetchAgentTicketMetrics 12)
                        ]
        , test "renders the ticket header, tabs and spec body" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has
                                [ containing [ text "ship fly archives" ]
                                , containing [ text "Spec" ]
                                , containing [ text "Plan" ]
                                , containing [ text "spec body" ]
                                ]
                    )
        , test "offers merge / send-back transitions for a needs_review ticket" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has
                                [ containing [ text "Merge" ]
                                , containing [ text "Send back" ]
                                ]
                    )
        , test "offers a dispatch button for a queued ticket" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has [ containing [ text "Dispatch run" ] ]
                    )
        , test "shows an error notice when the ticket fails to load" <|
            \_ ->
                renderWith "/agent-tickets/12" (Callback.AgentTicketFetched Data.httpUnauthorized)
                    |> Query.has [ text "Couldn't load ticket." ]
        , test "links the harvest-branch diff for a needs_review ticket" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.find [ class "agent-ticket-compare-link" ]
                            |> Query.has
                                [ attribute
                                    (Html.Attributes.href
                                        "https://github.com/tdmtrader/jetbridge/compare/main...agent/ticket-12"
                                    )
                                ]
                    )
        , test "omits the compare link when there is no harvest branch" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.hasNot [ class "agent-ticket-compare-link" ]
                    )
        , test "run history rows link to their build" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketMetricsFetched 12
                                    (Ok
                                        [ { ticketId = Just 12
                                          , pipelineRunId = Just 2
                                          , buildId = 561978
                                          , planId = "plan-xyz"
                                          , stepName = "implement"
                                          , workflowName = "develop"
                                          , workflowVersion = Just 1
                                          , status = "ok"
                                          , buildStatus = "succeeded"
                                          , summary = ""
                                          , model = ""
                                          , usage =
                                                { inputTokens = 19
                                                , outputTokens = 2480
                                                , cacheReadInputTokens = 0
                                                , cacheCreationInputTokens = 0
                                                }
                                          , turns = 25
                                          , wallTimeSeconds = 77
                                          , costUsd = 0.21
                                          , eventCounts = Dict.empty
                                          , createdAt = 100
                                          }
                                        ]
                                    )
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ class "agent-ticket-run-row" ]
                            |> Query.has
                                [ tag "a"
                                , attribute (Html.Attributes.href "/builds/561978")
                                ]
                    )
        ]
