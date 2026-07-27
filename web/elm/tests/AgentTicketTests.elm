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
        , "user_name": "admin"
        , "branch": "agent/ticket-12"
        , "attempt_count": 2
        , "created_at": 1784000000
        , "updated_at": 1784000100
        }
    }
    """


all : Test
all =
    describe "Concourse.AgentTicket"
        [ test "decodeDetail reads the ticket — its body is the whole of its content" <|
            \_ ->
                Json.Decode.decodeString AT.decodeDetail detailJson
                    |> Result.map
                        (\d ->
                            { id = d.ticket.id
                            , state = d.ticket.state
                            , body = d.ticket.body
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { id = 12
                            , state = "needs_review"
                            , body = "make all four platform archives"
                            }
                        )
        , test "ignores retired spec/tasks/budget keys from a stale server" <|
            \_ ->
                Json.Decode.decodeString AT.decodeDetail
                    """
                    { "ticket": { "id": 12, "state": "closed", "budget_usd": 5.0 }
                    , "spec": { "version": 2, "title": "gone" }
                    , "tasks": [ { "ordering": 1, "title": "gone", "status": "done" } ]
                    }
                    """
                    |> Result.map (\d -> ( d.ticket.id, d.ticket.state ))
                    |> Expect.equal (Ok ( 12, "closed" ))
        , test "decodeTicket keeps the attempt count" <|
            \_ ->
                Json.Decode.decodeString AT.decodeTicket
                    """{ "id": 3, "title": "t", "state": "running", "attempt_count": 2 }"""
                    |> Result.map (\t -> ( t.id, t.attemptCount ))
                    |> Expect.equal (Ok ( 3, 2 ))
        , test "decodeTicket ignores a pipeline_run_id the server still sends" <|
            \_ ->
                -- The pipeline run is an execution detail of the durable
                -- workflow run and is rendered there; the ticket has nowhere
                -- honest to show it, so it is not decoded — and its presence
                -- must not fail the ticket.
                Json.Decode.decodeString AT.decodeTicket
                    """{ "id": 3, "title": "t", "state": "running", "pipeline_run_id": 8 }"""
                    |> Result.map .id
                    |> Expect.equal (Ok 3)
        , test "decodeDispatchResult reads the quoted workflow run ID and warnings" <|
            \_ ->
                Json.Decode.decodeString AT.decodeDispatchResult
                    """{ "workflow_run_id": "9007199254740993", "pipeline_run_id": 42
                       , "warnings": [ "auto-dispatch is paused" ] }"""
                    |> Expect.equal
                        (Ok
                            { workflowRunId = "9007199254740993"
                            , warnings = [ "auto-dispatch is paused" ]
                            }
                        )
        , test "decodeDispatchResult defaults absent warnings to none" <|
            \_ ->
                -- pipeline_run_id is optional diagnostic: its absence is not an
                -- error, and neither is an absent warnings list.
                Json.Decode.decodeString AT.decodeDispatchResult
                    """{ "workflow_run_id": "42" }"""
                    |> Expect.equal (Ok { workflowRunId = "42", warnings = [] })
        , test "decodeDispatchResult rejects a numeric workflow run ID" <|
            \_ ->
                -- Durable IDs exceed 2^53; a JSON number would round silently
                -- and send the user to someone else's run.
                Json.Decode.decodeString AT.decodeDispatchResult
                    """{ "workflow_run_id": 9007199254740993 }"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "decodeDispatchResult fails without a workflow run ID" <|
            \_ ->
                -- The run is the only thing the caller can act on, so a
                -- response without one is a failed dispatch, not a blank field.
                Json.Decode.decodeString AT.decodeDispatchResult
                    """{ "pipeline_run_id": 42, "warnings": [] }"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "keeps quoted ticket durable IDs above 2^53 exact" <|
            \_ ->
                Json.Decode.decodeString AT.decodeTicket
                    """
                    { "id": 12
                    , "workflow_run_id": "9007199254740993"
                    , "repository_snapshot_id": "9007199254740995"
                    , "work_item_snapshot_id": "9007199254741003"
                    }
                    """
                    |> Result.map
                        (\ticket ->
                            ( ticket.workflowRunId
                            , ticket.repositorySnapshotId
                            , ticket.workItemSnapshotId
                            )
                        )
                    |> Expect.equal
                        (Ok
                            ( Just "9007199254740993"
                            , Just "9007199254740995"
                            , Just "9007199254741003"
                            )
                        )
        , describe "durable ticket ID wire format"
            ([ ( "workflow_run_id", "workflow run" )
             , ( "repository_snapshot_id", "repository snapshot" )
             , ( "work_item_snapshot_id", "work-item snapshot" )
             ]
                |> List.map
                    (\( fieldName, label ) ->
                        test ("rejects a numeric " ++ label ++ " ID") <|
                            \_ ->
                                Json.Decode.decodeString AT.decodeTicket
                                    ("{ \"id\": 12, \"" ++ fieldName ++ "\": 9007199254740993 }")
                                    |> Result.toMaybe
                                    |> Expect.equal Nothing
                    )
            )
        , test "decodes absent and null durable ticket IDs as Nothing" <|
            \_ ->
                Json.Decode.decodeString AT.decodeTicket
                    """
                    { "id": 12
                    , "workflow_run_id": null
                    , "repository_snapshot_id": null
                    }
                    """
                    |> Result.map
                        (\ticket ->
                            ( ticket.workflowRunId
                            , ticket.repositorySnapshotId
                            , ticket.workItemSnapshotId
                            )
                        )
                    |> Expect.equal (Ok ( Nothing, Nothing, Nothing ))
        , test "rejects a noncanonical quoted durable ticket ID" <|
            \_ ->
                Json.Decode.decodeString AT.decodeTicket
                    """{ "id": 12, "workflow_run_id": "01" }"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
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
        ]
