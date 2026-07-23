module WorkflowRunDecoderTests exposing (all)

import Concourse.WorkflowRun as WorkflowRun
import Dict
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


runJson : String
runJson =
    """
    { "workflow_run_id":"9007199254740995"
    , "pipeline_run_id":44
    , "workflow_name":"code-review"
    , "workflow_version":3
    , "schema_version":3
    , "signature_version":1
    , "definition_content_hash":"definition-hash"
    , "function_id":null
    , "status":"succeeded"
    , "execution_status":"succeeded"
    , "origin_kind":"experiment"
    , "origin_reference":"experiment:7:cell:9"
    , "created_by":"alice"
    , "retry_of_workflow_run_id":null
    , "created_at":"2026-07-22T12:00:00Z"
    , "updated_at":"2026-07-22T12:01:00Z"
    , "started_at":"2026-07-22T12:00:05Z"
    , "completed_at":"2026-07-22T12:00:55Z"
    , "parameterized_config_hash":"parameterized-hash"
    , "instance_config_hash":"instance-hash"
    , "actual_plan_hash":"plan-hash"
    , "planned_build_id":47
    , "inputs":[]
    , "outputs":[]
    }
    """


all : Test
all =
    describe "Concourse.WorkflowRun"
        [ test "decodes durable run identity without coercing it to Int" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeDetail runJson
                    |> Result.map (\run -> ( run.summary.id, run.summary.originKind, run.summary.actualPlanHash ))
                    |> Expect.equal (Ok ( "9007199254740995", "experiment", Just "plan-hash" ))
        , test "rejects numeric workflow run IDs" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeDetail
                    (String.replace "\"9007199254740995\"" "9007199254740995" runJson)
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "decodes exact operational counts beyond list page limits" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeOperationalStatusCounts
                    """{"workflow_name":"code-review","counts":{"aborted":0,"admitting":1007,"canceling":0,"errored":2,"failed":3,"running":4,"succeeded":91}}"""
                    |> Result.map
                        (\aggregate ->
                            ( aggregate.workflowName
                            , Dict.get "admitting" aggregate.counts
                            , Dict.get "errored" aggregate.counts
                            )
                        )
                    |> Expect.equal (Ok ( "code-review", Just 1007, Just 2 ))
        , test "decodes exact snapshot refs in input bindings" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeInputBinding
                    """{"port":"repo","snapshot":{"id":"9007199254740997","type":"repository/v1","digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}}"""
                    |> Result.map (\binding -> ( binding.portName, binding.snapshot.id ))
                    |> Expect.equal (Ok ( "repo", "9007199254740997" ))
        , test "decodes an active human wait and attributed resolution" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeWait
                    """{"id":"7","output_name":"answer","question_name":"clarification","question":{"id":"9007199254741001","type":"question/v1","digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"prompt":"Ship this change?","context":"Validation passed.","options":["approve","reject"],"default":"reject","expected_type":"human-answer/v1","deadline":"2026-07-22T13:00:00Z","timeout_policy":"fail","has_default":false,"workflow_port":"answer","status":"waiting","created_at":"2026-07-22T12:00:00Z","updated_at":"2026-07-22T12:00:00Z"}"""
                    |> Result.map
                        (\wait ->
                            { id = wait.id
                            , question = wait.question.id
                            , status = wait.status
                            , expectedType = wait.expectedType
                            , options = wait.options
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { id = "7"
                            , question = "9007199254741001"
                            , status = "waiting"
                            , expectedType = "human-answer/v1"
                            , options = [ "approve", "reject" ]
                            }
                        )
        , test "decodes generic output outcomes with exact snapshot identity" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeOutcome
                    """{"workflow_run_id":"9007199254740995","output_snapshot_id":"9007199254741003","disposition":"accepted","publication_state":"published","publication_id":"9007199254741007","human_modified":true,"modification_snapshot_id":"9007199254741005","intervention_count":2,"labels":["reviewed"],"actor":"alice","revision":1,"audited_at":"2026-07-22T12:05:00Z"}"""
                    |> Result.map
                        (\outcome ->
                            { outputSnapshotId = outcome.outputSnapshotId
                            , publicationId = outcome.publicationId
                            , interventionCount = outcome.interventionCount
                            , modificationSnapshotId = outcome.modificationSnapshotId
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { outputSnapshotId = "9007199254741003"
                            , publicationId = Just "9007199254741007"
                            , interventionCount = 2
                            , modificationSnapshotId = Just "9007199254741005"
                            }
                        )
        , test "rejects a numeric publication ID" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeOutcome
                    """{"workflow_run_id":"1","output_snapshot_id":"2","disposition":"accepted","publication_state":"published","publication_id":3,"human_modified":false,"intervention_count":0,"labels":[],"actor":"alice","revision":1,"audited_at":"2026-07-22T12:05:00Z"}"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "decodes a bounded repository change projection" <|
            \_ ->
                Json.Decode.decodeString WorkflowRun.decodeRepositoryChange
                    """{"snapshot_id":"9007199254741007","status":"ready","repository_id":"org/repo","base_sha":"aaaaaaaa","result_sha":"bbbbbbbb","result_tree_sha":"cccccccc","representation":"patch","files":[{"path":"src/Main.elm","status":"modified","lines_added":3,"lines_deleted":1}],"file_count":1,"lines_added":3,"lines_deleted":1,"unified_diff":"@@ -1 +1 @@","truncated":true,"truncation_reason":"byte limit"}"""
                    |> Result.map (\projection -> ( projection.snapshotId, projection.fileCount, projection.truncated ))
                    |> Expect.equal (Ok ( "9007199254741007", 1, True ))
        ]
