module AgentTicketPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common
import Concourse.AgentTicket as AgentTicket
import Data
import Expect
import Html.Attributes
import Http
import Json.Decode
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message
import Message.Subscription exposing (Delivery(..), Interval(..))
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, id, tag, text)
import Time
import Url


sampleDetailJson : String
sampleDetailJson =
    """
    { "ticket":
        { "id": 12, "title": "ship fly archives", "state": "needs_review"
        , "workflow_name": "develop", "body": "do the thing"
        , "created_at": 200
        , "repo": "tdmtrader/jetbridge", "target_branch": "main", "branch": "agent/ticket-12"
        , "workflow_run_id": "9007199254740993"
        , "work_item_snapshot_id": "9007199254741003"
        , "repository_snapshot_id": "9007199254740995"
        }
    }
    """


queuedDetailJson : String
queuedDetailJson =
    """
    { "ticket": { "id": 9, "title": "queued work", "state": "queued", "workflow_name": "develop", "created_at": 50 } }
    """


runningDetailJson : String
runningDetailJson =
    """
    { "ticket": { "id": 9, "title": "queued work", "state": "running", "workflow_name": "develop", "created_at": 50 } }
    """


namelessDetailJson : String
namelessDetailJson =
    """
    { "ticket": { "id": 9, "title": "queued work", "state": "queued", "created_at": 50 } }
    """


closedDetailJson : String
closedDetailJson =
    """
    { "ticket":
        { "id": 12, "title": "ship fly archives", "state": "closed"
        , "workflow_name": "develop", "body": "do the thing"
        , "created_at": 200
        }
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


runDetailFor workflowName workflowRunId =
    let
        detail =
            AgenticData.runDetail

        summary =
            detail.summary
    in
    { detail
        | summary =
            { summary
                | id = workflowRunId
                , workflowName = workflowName
            }
    }


all : Test
all =
    describe "ticket detail page"
        [ test "decodes a detail fixture" <|
            \_ ->
                withDetail sampleDetailJson (\d -> Expect.equal d.ticket.title "ship fly archives")
        , test "decodes a detail fixture with only a ticket" <|
            \_ ->
                withDetail queuedDetailJson (\d -> Expect.equal d.ticket.state "queued")
        , test "fetches the ticket on load and requests no legacy ticket metrics" <|
            \_ ->
                initDetail
                    |> Tuple.second
                    |> Common.contains (Effects.FetchAgentTicket 12)
        , test "fetches the exact durable workflow run after the ticket loads" <|
            \_ ->
                withDetail sampleDetailJson <|
                    \detail ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                            |> Tuple.second
                            |> Expect.all
                                [ Common.contains
                                    (Effects.FetchAgentWorkflowRun
                                        "develop"
                                        "9007199254740993"
                                    )
                                ]
        , test "links the captured revision, repository, durable run, and typed outputs" <|
            \_ ->
                withDetail sampleDetailJson <|
                    \detail ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentWorkflowRunFetched
                                    "9007199254740993"
                                    (Ok (runDetailFor "develop" "9007199254740993"))
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ id "ticket-durable-evidence" ]
                            |> Expect.all
                                [ Query.has
                                    [ attribute (Html.Attributes.href "/agent/snapshots/9007199254740995")
                                    , attribute (Html.Attributes.href "/agent/snapshots/9007199254741003")
                                    , attribute (Html.Attributes.href "/agent/workflows/develop/runs/9007199254740993")
                                    , containing [ text "workflow run #9007199254740993" ]
                                    , attribute (Html.Attributes.href "/agent/snapshots/9007199254740997")
                                    , containing [ text "change repository-change/v1 #9007199254740997" ]
                                    ]
                                , Query.findAll
                                    [ attribute
                                        (Html.Attributes.href
                                            "/agent/workflows/develop/runs/9007199254740993"
                                        )
                                    ]
                                    >> Query.count (Expect.equal 1)
                                ]
        , test "renders the ticket header and body — no spec/plan tabs" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Expect.all
                                [ Query.has
                                    [ containing [ text "ship fly archives" ]
                                    , containing [ text "do the thing" ]
                                    ]
                                , Query.hasNot [ text "Spec" ]
                                , Query.hasNot [ text "Plan" ]
                                ]
                    )
        , test "shows the created timestamp in the app-wide date format" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        let
                            ticket =
                                d.ticket
                        in
                        renderWith "/agent-tickets/12"
                            (Callback.AgentTicketFetched
                                (Ok { d | ticket = { ticket | createdAt = 1784385000 } })
                            )
                            |> Query.find [ id "ticket-timestamps" ]
                            |> Query.has [ text "created Jul 18, 2026 14:30" ]
                    )
        , test "offers exactly Close and Re-queue for a needs_review ticket" <|
            \_ ->
                -- ONE close action, not a disposition menu: whether the work
                -- merged, was dropped or was analysis-only is the durable run's
                -- outcome, never a ticket state.
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Expect.all
                                [ Query.has
                                    [ containing [ text "Close" ]
                                    , containing [ text "Re-queue" ]
                                    ]
                                , Query.hasNot [ text "Merge with fixes" ]
                                , Query.hasNot [ text "Send back" ]
                                , Query.hasNot [ text "Abandon" ]
                                , Query.hasNot [ text "Conclude" ]
                                ]
                    )
        , test "reads the run outcome from the durable run, not from ticket state" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentWorkflowRunFetched
                                    "9007199254740993"
                                    (Ok (runDetailFor "develop" "9007199254740993"))
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.find [ id "ticket-run-outcome" ]
                            |> Query.has [ text "run outcome" ]
                    )
        , test "shows no run-outcome chip before the durable run is in hand" <|
            \_ ->
                -- inventing an outcome from ticket state is the second truth
                -- this page exists to stop showing
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.hasNot [ id "ticket-run-outcome" ]
                    )
        , test "offers a dispatch button for a queued ticket" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.has [ containing [ text "Dispatch run" ] ]
                    )
        , test "a clean dispatch navigates to the durable run it created" <|
            \_ ->
                -- Dispatch's one product is a workflow run; the old code
                -- decoded the response and discarded it, leaving the user on an
                -- unchanged ticket with no sign of what had been created.
                withDetail queuedDetailJson
                    (\d ->
                        Common.init "/agent-tickets/9"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketDispatched 9
                                    (Ok { workflowRunId = "9007199254740993", warnings = [] })
                                )
                            |> Tuple.second
                            |> Common.contains
                                (Effects.NavigateTo
                                    "/agent/workflows/develop/runs/9007199254740993"
                                )
                    )
        , test "a dispatch with warnings holds the page and surfaces them" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        Common.init "/agent-tickets/9"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketDispatched 9
                                    (Ok
                                        { workflowRunId = "9007199254740993"
                                        , warnings = [ "auto-dispatch is paused" ]
                                        }
                                    )
                                )
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.notContains
                                        (Effects.NavigateTo
                                            "/agent/workflows/develop/runs/9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.find [ id "ticket-dispatch-notice" ]
                                    >> Expect.all
                                        [ Query.has [ text "auto-dispatch is paused" ]
                                        , Query.has
                                            [ attribute
                                                (Html.Attributes.href
                                                    "/agent/workflows/develop/runs/9007199254740993"
                                                )
                                            ]
                                        ]
                                ]
                    )
        , test "dismissing the dispatch warning notice clears it" <|
            \_ ->
                withDetail queuedDetailJson
                    (\d ->
                        Common.init "/agent-tickets/9"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketDispatched 9
                                    (Ok
                                        { workflowRunId = "9007199254740993"
                                        , warnings = [ "auto-dispatch is paused" ]
                                        }
                                    )
                                )
                            |> Tuple.first
                            |> Application.update
                                (Msgs.Update Message.Message.DismissAgentTicketDispatchNotice)
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.hasNot [ id "ticket-dispatch-notice" ]
                    )
        , test "a dispatch with no workflow name to route with names the run instead" <|
            \_ ->
                -- There is no run route to build without a workflow name, so
                -- navigating is impossible — but the run ID must not be lost.
                withDetail namelessDetailJson
                    (\d ->
                        Common.init "/agent-tickets/9"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentTicketDispatched 9
                                    (Ok { workflowRunId = "9007199254740993", warnings = [] })
                                )
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.notContains
                                        (Effects.NavigateTo
                                            "/agent/workflows//runs/9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.find [ id "ticket-dispatch-notice" ]
                                    >> Query.has [ text "workflow run #9007199254740993" ]
                                ]
                    )
        , test "shows an error notice when the ticket fails to load" <|
            \_ ->
                renderWith "/agent-tickets/12" (Callback.AgentTicketFetched Data.httpUnauthorized)
                    |> Query.has [ text "Couldn't load ticket." ]
        , test "linked ticket renders canonical workflow evidence without legacy actions" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentWorkflowRunFetched
                                    "9007199254740993"
                                    (Ok (runDetailFor "develop" "9007199254740993"))
                                )
                            |> Tuple.first
                            |> Common.queryView
                            |> Expect.all
                                [ Query.has [ text "workflow run #9007199254740993" ]
                                , Query.has
                                    [ attribute
                                        (Html.Attributes.href
                                            "/agent/snapshots/9007199254740997"
                                        )
                                    ]
                                , Query.hasNot [ class "agent-ticket-compare-link" ]
                                , Query.hasNot [ class "agent-ticket-digest-compare-link" ]
                                , Query.hasNot [ id "ticket-review-digest" ]
                                ]
                    )
        , test "shows no legacy compare link even when the ticket has a branch" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok d))
                            |> Query.hasNot [ class "agent-ticket-compare-link" ]
                    )
        , test "a ticket without a durable run shows no evidence line" <|
            \_ ->
                withDetail queuedDetailJson
                    (\detail ->
                        renderWith "/agent-tickets/12" (Callback.AgentTicketFetched (Ok detail))
                            |> Query.hasNot [ id "ticket-durable-evidence" ]
                    )
        , test "a same durable pair keeps matching output detail while refreshing it" <|
            \_ ->
                withDetail sampleDetailJson
                    (\detail ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentWorkflowRunFetched
                                    "9007199254740993"
                                    (Ok (runDetailFor "develop" "9007199254740993"))
                                )
                            |> Tuple.first
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.contains
                                        (Effects.FetchAgentWorkflowRun
                                            "develop"
                                            "9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.has
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                ]
                    )
        , test "removing the durable ID clears outputs and rejects a late response" <|
            \_ ->
                withDetail sampleDetailJson
                    (\detail ->
                        let
                            ticket =
                                detail.ticket

                            withoutRun =
                                { detail | ticket = { ticket | workflowRunId = Nothing } }

                            afterRefetch =
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                                    |> Tuple.first
                                    |> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254740993"))
                                        )
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok withoutRun))
                        in
                        afterRefetch
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.notContains
                                        (Effects.FetchAgentWorkflowRun
                                            "develop"
                                            "9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                , Tuple.first
                                    >> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254740993"))
                                        )
                                    >> Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                ]
                    )
        , test "blanking the workflow name clears outputs and rejects the old-name response" <|
            \_ ->
                withDetail sampleDetailJson
                    (\detail ->
                        let
                            ticket =
                                detail.ticket

                            blankName =
                                { detail | ticket = { ticket | workflowName = "  " } }

                            afterRefetch =
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                                    |> Tuple.first
                                    |> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254740993"))
                                        )
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok blankName))
                        in
                        afterRefetch
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.notContains
                                        (Effects.FetchAgentWorkflowRun
                                            "  "
                                            "9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                , Tuple.first
                                    >> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254740993"))
                                        )
                                    >> Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                ]
                    )
        , test "changing the durable run ID clears old outputs and fetches only the new run" <|
            \_ ->
                withDetail sampleDetailJson
                    (\detail ->
                        let
                            ticket =
                                detail.ticket

                            newRun =
                                { detail
                                    | ticket =
                                        { ticket
                                            | workflowRunId = Just "9007199254741005"
                                        }
                                }
                        in
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                            |> Tuple.first
                            |> Application.handleCallback
                                (Callback.AgentWorkflowRunFetched
                                    "9007199254740993"
                                    (Ok (runDetailFor "develop" "9007199254740993"))
                                )
                            |> Tuple.first
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok newRun))
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.contains
                                        (Effects.FetchAgentWorkflowRun
                                            "develop"
                                            "9007199254741005"
                                        )
                                , Tuple.second
                                    >> Common.notContains
                                        (Effects.FetchAgentWorkflowRun
                                            "develop"
                                            "9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                ]
                    )
        , test "changing only the workflow name clears outputs and rejects the old-name response" <|
            \_ ->
                withDetail sampleDetailJson
                    (\detail ->
                        let
                            ticket =
                                detail.ticket

                            renamed =
                                { detail | ticket = { ticket | workflowName = "release" } }

                            afterRefetch =
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                                    |> Tuple.first
                                    |> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254740993"))
                                        )
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok renamed))
                        in
                        afterRefetch
                            |> Expect.all
                                [ Tuple.second
                                    >> Common.contains
                                        (Effects.FetchAgentWorkflowRun
                                            "release"
                                            "9007199254740993"
                                        )
                                , Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                , Tuple.first
                                    >> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254740993"))
                                        )
                                    >> Tuple.first
                                    >> Common.queryView
                                    >> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                                ]
                    )
        , test "rejects workflow-run details whose summary ID or name mismatches the current pair" <|
            \_ ->
                withDetail sampleDetailJson
                    (\detail ->
                        let
                            loaded =
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                                    |> Tuple.first

                            mismatchedId =
                                loaded
                                    |> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "develop" "9007199254741005"))
                                        )
                                    |> Tuple.first
                                    |> Common.queryView

                            mismatchedName =
                                loaded
                                    |> Application.handleCallback
                                        (Callback.AgentWorkflowRunFetched
                                            "9007199254740993"
                                            (Ok (runDetailFor "release" "9007199254740993"))
                                        )
                                    |> Tuple.first
                                    |> Common.queryView
                        in
                        Expect.all
                            [ \_ ->
                                mismatchedId
                                    |> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                            , \_ ->
                                mismatchedName
                                    |> Query.hasNot
                                        [ attribute
                                            (Html.Attributes.href
                                                "/agent/snapshots/9007199254740997"
                                            )
                                        ]
                            ]
                            ()
                    )
        , test "a periodic self-heal refetch does not clobber an open edit form" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.Message.AgentTicketBodyChanged "MY UNSAVED EDIT"))
                            |> Tuple.first
                            -- the 5s refetch lands while the user is still editing:
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.has
                                [ tag "textarea"
                                , attribute (Html.Attributes.value "MY UNSAVED EDIT")
                                ]
                    )
        , test "saving after a mid-edit refetch posts the typed values, not the server's" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.Message.AgentTicketBodyChanged "MY UNSAVED EDIT"))
                            |> Tuple.first
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketSave)
                            |> Tuple.second
                            |> Common.contains
                                (Effects.SaveAgentTicket
                                    { id = 12
                                    , title = "ship fly archives"
                                    , body = "MY UNSAVED EDIT"
                                    }
                                )
                    )
        , test "clicking Edit opens the form seeded with the current ticket values" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                            |> Tuple.first
                            |> Common.queryView
                            |> Expect.all
                                [ Query.has [ tag "input", attribute (Html.Attributes.value "ship fly archives") ]
                                , Query.has [ tag "textarea", attribute (Html.Attributes.value "do the thing") ]
                                , Query.findAll [ tag "input" ] >> Query.count (Expect.equal 1)
                                ]
                    )
        , test "a ticket going terminal mid-edit closes the form and says why" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        withDetail closedDetailJson
                            (\closed ->
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                                    |> Tuple.first
                                    |> Application.update (Msgs.Update Message.Message.ClickAgentTicketEdit)
                                    |> Tuple.first
                                    |> Application.update (Msgs.Update (Message.Message.AgentTicketBodyChanged "MY UNSAVED EDIT"))
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok closed))
                                    |> Tuple.first
                                    |> Common.queryView
                                    |> Expect.all
                                        [ Query.hasNot [ tag "textarea" ]
                                        , Query.has [ text "unsaved changes were discarded" ]
                                        ]
                            )
                    )
        , test "a state change under an armed transition disarms the confirm" <|
            \_ ->
                withDetail queuedDetailJson
                    (\queued ->
                        withDetail runningDetailJson
                            (\running ->
                                Common.init "/agent-tickets/12"
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok queued))
                                    |> Tuple.first
                                    |> Application.update (Msgs.Update (Message.Message.ClickAgentTicketTransition "closed"))
                                    |> Tuple.first
                                    |> Application.handleCallback (Callback.AgentTicketFetched (Ok running))
                                    |> Tuple.first
                                    |> Common.queryView
                                    |> Query.hasNot [ text "Confirm close" ]
                            )
                    )
        , test "a same-state refetch leaves an armed transition armed" <|
            \_ ->
                withDetail queuedDetailJson
                    (\queued ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok queued))
                            |> Tuple.first
                            |> Application.update (Msgs.Update (Message.Message.ClickAgentTicketTransition "closed"))
                            |> Tuple.first
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok queued))
                            |> Tuple.first
                            |> Common.queryView
                            |> Query.has [ text "Confirm close" ]
                    )
        , test "the 5s tick refetches the ticket while it can still change" <|
            \_ ->
                withDetail sampleDetailJson
                    (\d ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok d))
                            |> Tuple.first
                            |> Application.update
                                (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                            |> Tuple.second
                            |> Common.contains (Effects.FetchAgentTicket 12)
                    )
        , test "the 5s tick keeps polling while the ticket hasn't loaded yet" <|
            \_ ->
                Common.init "/agent-tickets/12"
                    |> Application.update
                        (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                    |> Tuple.second
                    |> Common.contains (Effects.FetchAgentTicket 12)
        , test "the 5s tick stops refetching once the ticket is terminal" <|
            \_ ->
                withDetail closedDetailJson
                    (\closed ->
                        Common.init "/agent-tickets/12"
                            |> Application.handleCallback (Callback.AgentTicketFetched (Ok closed))
                            |> Tuple.first
                            |> Application.update
                                (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                            |> Tuple.second
                            |> Common.notContains (Effects.FetchAgentTicket 12)
                    )
        , describe "cross-workflow journal"
            [ test "fetches the ticket's whole run history on load" <|
                \_ ->
                    initDetail
                        |> Tuple.second
                        |> Common.contains (Effects.FetchAgentTicketRuns 12)
            , test "keeps the journal current on the 5s tick" <|
                \_ ->
                    Common.init "/agent-tickets/12"
                        |> Application.update
                            (Msgs.DeliveryReceived (ClockTicked FiveSeconds <| Time.millisToPosix 0))
                        |> Tuple.second
                        |> Common.contains (Effects.FetchAgentTicketRuns 12)
            , test "renders every associated run occurrence in the order given" <|
                \_ ->
                    journalView
                        |> Query.findAll [ class "agent-journal-entry-workflow" ]
                        |> Expect.all
                            [ Query.index 0 >> Query.has [ text "small-fix" ]
                            , Query.index 1 >> Query.has [ text "pr-create" ]
                            , Query.index 2 >> Query.has [ text "small-fix" ]
                            ]
            , test "keeps a repeated execution of one workflow as its own entry" <|
                \_ ->
                    journalView
                        |> Query.findAll [ class "agent-journal-entry" ]
                        |> Query.count (Expect.equal 3)
            , test "elevates a run that still needs a human" <|
                \_ ->
                    journalView
                        |> Query.find [ class "agent-journal-entry--outstanding" ]
                        |> Query.has [ text "failed" ]
            , test "groups a retry with the run it retried" <|
                \_ ->
                    journalView
                        |> Query.find [ class "agent-journal-entry--retry" ]
                        |> Query.has [ text "retry of 9007199254740993" ]
            , test "links each entry to its own run, at its own workflow" <|
                \_ ->
                    journalView
                        |> Query.findAll [ class "agent-journal-entry-link" ]
                        |> Query.index 1
                        |> Query.has
                            [ attribute
                                (Html.Attributes.href "/agent/workflows/pr-create/runs/9007199254740994")
                            ]
            , test "says so plainly when a ticket has driven no runs" <|
                \_ ->
                    withDetail sampleDetailJson
                        (\detail ->
                            Common.init "/agent-tickets/12"
                                |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                                |> Tuple.first
                                |> Common.queryView
                                |> Query.find [ class "agent-journal-empty" ]
                                |> Query.has [ text "No runs yet" ]
                        )
            , test "a failed journal read leaves the last good history on screen" <|
                \_ ->
                    journalModel
                        |> Tuple.first
                        |> Application.handleCallback
                            (Callback.AgentTicketRunsFetched (Err Http.NetworkError))
                        |> Tuple.first
                        |> Common.queryView
                        |> Query.findAll [ class "agent-journal-entry" ]
                        |> Query.count (Expect.equal 3)
            ]
        ]


journalFixture : List AgentTicket.JournalEntry
journalFixture =
    [ { workflowRunId = "9007199254740993"
      , workflowName = "small-fix"
      , workflowVersion = 3
      , status = "failed"
      , originKind = "ticket"
      , retryOfWorkflowRunId = Nothing
      , createdAt = "2026-07-31T09:10:00Z"
      , startedAt = Just "2026-07-31T09:10:00Z"
      , completedAt = Just "2026-07-31T09:12:00Z"
      , outstanding = True
      , errorMessage = ""
      }
    , { workflowRunId = "9007199254740994"
      , workflowName = "pr-create"
      , workflowVersion = 2
      , status = "succeeded"
      , originKind = "resource-source-build"
      , retryOfWorkflowRunId = Nothing
      , createdAt = "2026-07-31T09:20:00Z"
      , startedAt = Just "2026-07-31T09:20:00Z"
      , completedAt = Just "2026-07-31T09:21:00Z"
      , outstanding = False
      , errorMessage = ""
      }
    , { workflowRunId = "9007199254740995"
      , workflowName = "small-fix"
      , workflowVersion = 4
      , status = "succeeded"
      , originKind = "retry"
      , retryOfWorkflowRunId = Just "9007199254740993"
      , createdAt = "2026-07-31T09:30:00Z"
      , startedAt = Just "2026-07-31T09:30:00Z"
      , completedAt = Just "2026-07-31T09:31:00Z"
      , outstanding = False
      , errorMessage = ""
      }
    ]


journalModel : ( Application.Model, List Effects.Effect )
journalModel =
    let
        loaded =
            case Json.Decode.decodeString AgentTicket.decodeDetail sampleDetailJson of
                Ok detail ->
                    Common.init "/agent-tickets/12"
                        |> Application.handleCallback (Callback.AgentTicketFetched (Ok detail))
                        |> Tuple.first

                Err _ ->
                    Common.init "/agent-tickets/12"
    in
    loaded
        |> Application.handleCallback
            (Callback.AgentTicketRunsFetched (Ok journalFixture))


journalView : Query.Single Msgs.TopLevelMessage
journalView =
    journalModel |> Tuple.first |> Common.queryView
