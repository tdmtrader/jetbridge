module AgentTicketTests exposing (all)

import Concourse.AgentTicket as AT
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


detailJson : String
detailJson =
    """
    { "ticket":
        { "id": 12
        , "title": "ship fly archives"
        , "body": "make all four platform archives"
        , "state": "needs_review"
        , "origin": "fly"
        , "repo": "concourse/concourse"
        , "target_branch": "main"
        , "workflow_name": "develop"
        , "workflow_version": 1
        , "budget_usd": 5.0
        , "user_name": "admin"
        , "pipeline_run_id": 10
        , "branch": "agent/ticket-12"
        , "attempt_count": 2
        , "created_at": 1784000000
        , "updated_at": 1784000100
        }
    , "spec": null
    , "tasks":
        [ { "ordering": 1, "title": "patch build-image", "status": "done" }
        , { "ordering": 2, "title": "verify matrix", "detail": "CI", "status": "in_progress" }
        ]
    }
    """


all : Test
all =
    describe "Concourse.AgentTicket"
        [ test "decodeDetail tolerates a null spec and reads ticket + tasks" <|
            \_ ->
                Json.Decode.decodeString AT.decodeDetail detailJson
                    |> Result.map
                        (\d ->
                            { id = d.ticket.id
                            , state = d.ticket.state
                            , hasSpec = d.spec /= Nothing
                            , tasks = List.length d.tasks
                            }
                        )
                    |> Expect.equal
                        (Ok { id = 12, state = "needs_review", hasSpec = False, tasks = 2 })
        , test "decodeTicket keeps enriched fields (attemptCount, pipelineRunId)" <|
            \_ ->
                Json.Decode.decodeString AT.decodeTicket
                    """{ "id": 3, "title": "t", "state": "running", "attempt_count": 2, "pipeline_run_id": 8 }"""
                    |> Result.map (\t -> ( t.id, t.attemptCount, t.pipelineRunId ))
                    |> Expect.equal (Ok ( 3, 2, Just 8 ))
        , test "decodeDispatchResult reads run_id + pipeline_name" <|
            \_ ->
                Json.Decode.decodeString AT.decodeDispatchResult
                    """{ "run_id": 42, "pipeline_name": "agent-ticket-12" }"""
                    |> Expect.equal (Ok { runId = 42, pipelineName = "agent-ticket-12" })
        , test "repoWebUrl resolves a bare GitHub slug" <|
            \_ ->
                AT.repoWebUrl "tdmtrader/jetbridge"
                    |> Expect.equal (Just "https://github.com/tdmtrader/jetbridge")
        , test "repoWebUrl strips .git from a full clone URL" <|
            \_ ->
                AT.repoWebUrl "https://github.com/tdmtrader/jetbridge.git"
                    |> Expect.equal (Just "https://github.com/tdmtrader/jetbridge")
        , test "repoWebUrl converts an SSH remote" <|
            \_ ->
                AT.repoWebUrl "git@github.com:tdmtrader/jetbridge.git"
                    |> Expect.equal (Just "https://github.com/tdmtrader/jetbridge")
        , test "repoWebUrl refuses a bare name it cannot resolve" <|
            \_ ->
                AT.repoWebUrl "jetbridge"
                    |> Expect.equal Nothing
        , test "compareUrl builds the target...branch diff link" <|
            \_ ->
                Json.Decode.decodeString AT.decodeDetail detailJson
                    |> Result.toMaybe
                    |> Maybe.andThen (\d -> AT.compareUrl d.ticket)
                    |> Expect.equal
                        (Just "https://github.com/concourse/concourse/compare/main...agent/ticket-12")
        , test "compareUrl is Nothing without a harvest branch" <|
            \_ ->
                Json.Decode.decodeString AT.decodeTicket
                    """{ "id": 2, "repo": "tdmtrader/jetbridge", "target_branch": "main", "state": "queued" }"""
                    |> Result.toMaybe
                    |> Maybe.andThen AT.compareUrl
                    |> Expect.equal Nothing
        ]
