module Concourse.WorkflowRun exposing
    ( ChangedFile
    , Detail
    , InputBinding
    , OperationalStatusCounts
    , Outcome
    , OutputBinding
    , RepositoryChange
    , Summary
    , Wait
    , decodeDetail
    , decodeInputBinding
    , decodeOperationalStatusCounts
    , decodeOutcome
    , decodeOutputBinding
    , decodeRepositoryChange
    , decodeSummary
    , decodeWait
    , decodeWaitList
    , effectiveState
    )

import Concourse.Snapshot as Snapshot
import Dict exposing (Dict)
import Json.Decode exposing (Decoder)
import Json.Decode.Extra exposing (andMap)


type alias Summary =
    { id : String
    , pipelineRunId : Maybe Int
    , workflowName : String
    , workflowVersion : Int
    , schemaVersion : Int
    , signatureVersion : Int
    , definitionContentHash : String
    , functionId : Maybe String
    , status : String
    , executionStatus : Maybe String
    , originKind : String
    , originReference : String
    , ticketId : Maybe Int
    , ticketReference : String
    , createdBy : String
    , retryOf : Maybe String
    , createdAt : String
    , updatedAt : String
    , startedAt : Maybe String
    , completedAt : Maybe String
    , parameterizedConfigHash : String
    , instanceConfigHash : Maybe String
    , actualPlanHash : Maybe String
    , plannedBuildId : Maybe Int
    }


{-| The one state a run should be rendered as, derived from the two the server
carries.

`status` is the DURABLE outcome; `execution_status` is the Concourse build's.
They are not two views of one fact, and the durable one is not the weaker
copy: `agentWorkflowRunsFactory.Finalize` recomputes the terminal status from
the run's output evidence AFTER the execution ended, then writes `status`
alone — `execution_status` is `COALESCE`-immutable once set and no later write
repairs it. A run whose every step exited zero but whose declared outputs
failed evidence validation, or whose public port contract did not match,
therefore settles permanently as `status = "failed"` with
`execution_status = "succeeded"`. `agent/workflowrun/reconciler.go` produces
that pair on four distinct paths, so it is the normal shape of an
"all green, nothing delivered" run rather than a corner case.

The rule is therefore worst-truth-wins over the pair, not "the fresher column
wins":

  - while the durable status is still non-terminal the execution status really
    is fresher — the build has ended and finalization has not run yet — so it
    is reported;
  - once both are terminal the worse of the two is reported, which stops a
    durably failed run from reading as green AND stops a run that ended
    cleanly around a failed execution from reading as green;
  - an execution status this UI does not understand never overrides anything,
    because a value that is not a state must not become the state.

-}
effectiveState : { a | status : String, executionStatus : Maybe String } -> String
effectiveState run =
    case run.executionStatus of
        Nothing ->
            run.status

        Just execution ->
            if not (isTerminalState execution) then
                run.status

            else if isTerminalState run.status then
                worseState run.status execution

            else
                execution


{-| The four terminal values `db.AgentWorkflowRunStatus` and
`db.AgentWorkflowRunExecutionStatus` share. Anything else is either still in
flight or a value this UI does not understand; both are treated the same way,
which is that they never win.
-}
isTerminalState : String -> Bool
isTerminalState state =
    List.member state [ "succeeded", "failed", "errored", "aborted" ]


{-| Rank the terminal states by how much a reader should be alarmed. This
invents no outcome: both operands are states the server actually wrote, and
the worse of the two is always literally true of the run.
-}
worseState : String -> String -> String
worseState left right =
    if severity right > severity left then
        right

    else
        left


severity : String -> Int
severity state =
    case state of
        "errored" ->
            3

        "failed" ->
            2

        "aborted" ->
            1

        _ ->
            0


type alias InputBinding =
    { portName : String
    , snapshot : Snapshot.Ref
    }


type alias OperationalStatusCounts =
    { workflowName : String
    , counts : Dict String Int
    }


type alias OutputBinding =
    { portName : String
    , snapshot : Snapshot.Manifest
    }


type alias Detail =
    { summary : Summary
    , inputs : List InputBinding
    , outputs : List OutputBinding
    }


type alias Wait =
    { id : String
    , outputName : String
    , questionName : String
    , question : Snapshot.Ref
    , prompt : String
    , context : String
    , options : List String
    , default : String
    , expectedType : String
    , deadline : String
    , timeoutPolicy : String
    , hasDefault : Bool
    , workflowPort : String
    , status : String
    , answer : Maybe Snapshot.Ref
    , resolvedBy : String
    , resolvedByDisplayName : String
    , resolutionSource : String
    , createdAt : String
    , updatedAt : String
    , resolvedAt : Maybe String
    }


type alias Outcome =
    { workflowRunId : String
    , outputSnapshotId : String
    , disposition : String
    , publicationState : String
    , publicationId : Maybe String
    , humanModified : Bool
    , modificationSnapshotId : Maybe String
    , interventionCount : Int
    , labels : List String
    , actor : String
    , revision : Int
    , auditedAt : String
    }


type alias ChangedFile =
    { path : String
    , previousPath : String
    , status : String
    , linesAdded : Int
    , linesDeleted : Int
    , binary : Bool
    , patch : String
    , truncated : Bool
    }


type alias RepositoryChange =
    { snapshotId : String
    , status : String
    , repositoryId : String
    , baseSha : String
    , resultSha : String
    , resultTreeSha : String
    , representation : String
    , files : List ChangedFile
    , fileCount : Int
    , linesAdded : Int
    , linesDeleted : Int
    , unifiedDiff : String
    , truncated : Bool
    , truncationReason : String
    }


decodeSummary : Decoder Summary
decodeSummary =
    Json.Decode.succeed Summary
        |> andMap (Json.Decode.field "workflow_run_id" Snapshot.decodeId)
        |> andMap (Json.Decode.maybe (Json.Decode.field "pipeline_run_id" Json.Decode.int))
        |> andMap (Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (Json.Decode.field "workflow_version" Json.Decode.int)
        |> andMap (Json.Decode.field "schema_version" Json.Decode.int)
        |> andMap (Json.Decode.field "signature_version" Json.Decode.int)
        |> andMap (Json.Decode.field "definition_content_hash" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "function_id" Json.Decode.string))
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "execution_status" Json.Decode.string))
        |> andMap (Json.Decode.field "origin_kind" Json.Decode.string)
        |> andMap (defaultTo "" (Json.Decode.field "origin_reference" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "ticket_id" Json.Decode.int))
        |> andMap (defaultTo "" (Json.Decode.field "ticket_reference" Json.Decode.string))
        |> andMap (Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (Snapshot.decodeOptionalIdField "retry_of_workflow_run_id")
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)
        |> andMap (Json.Decode.field "updated_at" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "started_at" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" Json.Decode.string))
        |> andMap (Json.Decode.field "parameterized_config_hash" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "instance_config_hash" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "actual_plan_hash" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "planned_build_id" Json.Decode.int))


decodeInputBinding : Decoder InputBinding
decodeInputBinding =
    Json.Decode.succeed InputBinding
        |> andMap (Json.Decode.field "port" Json.Decode.string)
        |> andMap (Json.Decode.field "snapshot" Snapshot.decodeRef)


decodeOperationalStatusCounts : Decoder OperationalStatusCounts
decodeOperationalStatusCounts =
    Json.Decode.map2 OperationalStatusCounts
        (Json.Decode.field "workflow_name" Json.Decode.string)
        (Json.Decode.field "counts" (Json.Decode.dict Json.Decode.int))


decodeOutputBinding : Decoder OutputBinding
decodeOutputBinding =
    Json.Decode.succeed OutputBinding
        |> andMap (Json.Decode.field "port" Json.Decode.string)
        |> andMap (Json.Decode.field "snapshot" Snapshot.decodeManifest)


decodeDetail : Decoder Detail
decodeDetail =
    Json.Decode.map3
        (\summary inputs outputs ->
            { summary = summary, inputs = inputs, outputs = outputs }
        )
        decodeSummary
        (defaultTo [] (Json.Decode.field "inputs" (Json.Decode.list decodeInputBinding)))
        (defaultTo [] (Json.Decode.field "outputs" (Json.Decode.list decodeOutputBinding)))


decodeWait : Decoder Wait
decodeWait =
    Json.Decode.succeed Wait
        |> andMap (Json.Decode.field "id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "output_name" Json.Decode.string)
        |> andMap (Json.Decode.field "question_name" Json.Decode.string)
        |> andMap (Json.Decode.field "question" Snapshot.decodeRef)
        |> andMap (defaultTo "" (Json.Decode.field "prompt" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "context" Json.Decode.string))
        |> andMap (defaultTo [] (Json.Decode.field "options" (Json.Decode.list Json.Decode.string)))
        |> andMap (defaultTo "" (Json.Decode.field "default" Json.Decode.string))
        |> andMap (Json.Decode.field "expected_type" Json.Decode.string)
        |> andMap (Json.Decode.field "deadline" Json.Decode.string)
        |> andMap (Json.Decode.field "timeout_policy" Json.Decode.string)
        |> andMap (defaultTo False (Json.Decode.field "has_default" Json.Decode.bool))
        |> andMap (defaultTo "" (Json.Decode.field "workflow_port" Json.Decode.string))
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "answer" Snapshot.decodeRef))
        |> andMap (defaultTo "" (Json.Decode.field "resolved_by" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "resolved_by_display_name" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "resolution_source" Json.Decode.string))
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)
        |> andMap (Json.Decode.field "updated_at" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "resolved_at" Json.Decode.string))


decodeWaitList : Decoder (List Wait)
decodeWaitList =
    Json.Decode.field "waits" (Json.Decode.list decodeWait)


decodeOutcome : Decoder Outcome
decodeOutcome =
    Json.Decode.succeed Outcome
        |> andMap (Json.Decode.field "workflow_run_id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "output_snapshot_id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "disposition" Json.Decode.string)
        |> andMap (Json.Decode.field "publication_state" Json.Decode.string)
        |> andMap (Snapshot.decodeOptionalIdField "publication_id")
        |> andMap (defaultTo False (Json.Decode.field "human_modified" Json.Decode.bool))
        |> andMap (Snapshot.decodeOptionalIdField "modification_snapshot_id")
        |> andMap (defaultTo 0 (Json.Decode.field "intervention_count" Json.Decode.int))
        |> andMap (defaultTo [] (Json.Decode.field "labels" (Json.Decode.list Json.Decode.string)))
        |> andMap (defaultTo "" (Json.Decode.field "actor" Json.Decode.string))
        |> andMap (defaultTo 0 (Json.Decode.field "revision" Json.Decode.int))
        |> andMap (Json.Decode.field "audited_at" Json.Decode.string)


decodeChangedFile : Decoder ChangedFile
decodeChangedFile =
    Json.Decode.succeed ChangedFile
        |> andMap (Json.Decode.field "path" Json.Decode.string)
        |> andMap (defaultTo "" (Json.Decode.field "previous_path" Json.Decode.string))
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (defaultTo 0 (Json.Decode.field "lines_added" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "lines_deleted" Json.Decode.int))
        |> andMap (defaultTo False (Json.Decode.field "binary" Json.Decode.bool))
        |> andMap (defaultTo "" (Json.Decode.field "patch" Json.Decode.string))
        |> andMap (defaultTo False (Json.Decode.field "truncated" Json.Decode.bool))


decodeRepositoryChange : Decoder RepositoryChange
decodeRepositoryChange =
    Json.Decode.succeed RepositoryChange
        |> andMap (Json.Decode.field "snapshot_id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (defaultTo "" (Json.Decode.field "repository_id" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "base_sha" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "result_sha" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "result_tree_sha" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "representation" Json.Decode.string))
        |> andMap (defaultTo [] (Json.Decode.field "files" (Json.Decode.list decodeChangedFile)))
        |> andMap (defaultTo 0 (Json.Decode.field "file_count" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "lines_added" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "lines_deleted" Json.Decode.int))
        |> andMap (defaultTo "" (Json.Decode.field "unified_diff" Json.Decode.string))
        |> andMap (defaultTo False (Json.Decode.field "truncated" Json.Decode.bool))
        |> andMap (defaultTo "" (Json.Decode.field "truncation_reason" Json.Decode.string))


defaultTo : a -> Decoder a -> Decoder a
defaultTo fallback decoder =
    Json.Decode.maybe decoder
        |> Json.Decode.map (Maybe.withDefault fallback)
