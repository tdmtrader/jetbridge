module Concourse.AgentTicket exposing
    ( Detail
    , DispatchResult
    , Spec
    , Task
    , Ticket
    , decodeDetail
    , decodeDispatchResult
    , decodeSpec
    , decodeTask
    , decodeTicket
    )

{-| Client-side view of the agent-ticket API (agent/api/tickets/types.go).
Field names/order are a superset of the frozen plan-06 Task-12 aliases: the
enriched fields (workflowVersion, pipelineRunId, attemptCount, errorDetail,
completedAt) trail the frozen prefix so field-accessor code stays stable, and
`Detail` carries the nullable `spec` the server actually sends.

All decoders are tolerant (mirroring Concourse.AgentReview): missing string
fields default to "" and missing scalars to a sensible zero, so a partial
payload never fails the whole page.

-}

import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias Ticket =
    { id : Int
    , title : String
    , body : String
    , state : String
    , origin : String
    , repo : String
    , targetBranch : String
    , workflowName : String
    , budgetUsd : Maybe Float
    , userName : String
    , branch : String
    , createdAt : Int
    , updatedAt : Int
    , workflowVersion : Maybe Int
    , pipelineRunId : Maybe Int
    , attemptCount : Int
    , errorDetail : String
    , completedAt : Maybe Int
    }


type alias Spec =
    { id : Int
    , version : Int
    , title : String
    , body : String
    , acceptanceCriteria : List String
    , submittedBy : String
    , createdAt : Int
    }


type alias Task =
    { ordering : Int
    , title : String
    , detail : String
    , status : String
    }


type alias Detail =
    { ticket : Ticket
    , spec : Maybe Spec
    , tasks : List Task
    }


type alias DispatchResult =
    { runId : Int
    , pipelineName : String
    }


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


optionalInt : String -> Json.Decode.Decoder (Maybe Int)
optionalInt name =
    Json.Decode.maybe (Json.Decode.field name Json.Decode.int)


decodeTicket : Json.Decode.Decoder Ticket
decodeTicket =
    Json.Decode.succeed Ticket
        |> andMap (Json.Decode.field "id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "title" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "body" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "state" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "origin" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "repo" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "target_branch" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "budget_usd" Json.Decode.float))
        |> andMap (defaultTo "" <| Json.Decode.field "user_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "branch" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "updated_at" Json.Decode.int)
        |> andMap (optionalInt "workflow_version")
        |> andMap (optionalInt "pipeline_run_id")
        |> andMap (defaultTo 0 <| Json.Decode.field "attempt_count" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "error_detail" Json.Decode.string)
        |> andMap (optionalInt "completed_at")


decodeSpec : Json.Decode.Decoder Spec
decodeSpec =
    Json.Decode.succeed Spec
        |> andMap (defaultTo 0 <| Json.Decode.field "id" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "title" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "body" Json.Decode.string)
        |> andMap (defaultTo [] <| Json.Decode.field "acceptance_criteria" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo "" <| Json.Decode.field "submitted_by" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)


decodeTask : Json.Decode.Decoder Task
decodeTask =
    Json.Decode.succeed Task
        |> andMap (defaultTo 0 <| Json.Decode.field "ordering" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "title" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "detail" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "status" Json.Decode.string)


decodeDetail : Json.Decode.Decoder Detail
decodeDetail =
    Json.Decode.succeed Detail
        |> andMap (Json.Decode.field "ticket" decodeTicket)
        |> andMap
            (Json.Decode.maybe (Json.Decode.field "spec" (Json.Decode.nullable decodeSpec))
                |> Json.Decode.map (Maybe.withDefault Nothing)
            )
        |> andMap (defaultTo [] <| Json.Decode.field "tasks" (Json.Decode.list decodeTask))


decodeDispatchResult : Json.Decode.Decoder DispatchResult
decodeDispatchResult =
    Json.Decode.succeed DispatchResult
        |> andMap (defaultTo 0 <| Json.Decode.field "run_id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "pipeline_name" Json.Decode.string)
