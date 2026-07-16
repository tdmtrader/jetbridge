module Concourse.Agent exposing
    ( CostRollup
    , CostRow
    , CostSummary
    , Credential
    , Principal
    , RunMetric
    , Usage
    , Workflow
    , decodeCostRollup
    , decodeCredential
    , decodePrincipal
    , decodeRunMetric
    , decodeWorkflow
    , principalActive
    )

import Dict exposing (Dict)
import Json.Decode
import Json.Decode.Extra exposing (andMap)
import Time


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


optionalInt : String -> Json.Decode.Decoder (Maybe Int)
optionalInt name =
    Json.Decode.maybe (Json.Decode.field name Json.Decode.int)



-- Agent run metrics (a single agent-step execution) ---------------------------


type alias Usage =
    { inputTokens : Int
    , outputTokens : Int
    , cacheReadInputTokens : Int
    , cacheCreationInputTokens : Int
    }


type alias RunMetric =
    { ticketId : Maybe Int
    , pipelineRunId : Maybe Int
    , buildId : Int
    , planId : String
    , stepName : String
    , workflowName : String
    , workflowVersion : Maybe Int
    , status : String
    , summary : String
    , model : String
    , usage : Usage
    , turns : Int
    , wallTimeSeconds : Int
    , costUsd : Float
    , eventCounts : Dict String Int
    , createdAt : Int
    }


decodeUsage : Json.Decode.Decoder Usage
decodeUsage =
    Json.Decode.succeed Usage
        |> andMap (defaultTo 0 <| Json.Decode.field "input_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "output_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cache_read_input_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cache_creation_input_tokens" Json.Decode.int)


decodeRunMetric : Json.Decode.Decoder RunMetric
decodeRunMetric =
    Json.Decode.succeed RunMetric
        |> andMap (optionalInt "ticket_id")
        |> andMap (optionalInt "pipeline_run_id")
        |> andMap (Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "plan_id" Json.Decode.string)
        |> andMap (Json.Decode.field "step_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (optionalInt "workflow_version")
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "summary" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "model" Json.Decode.string)
        |> andMap (defaultTo (Usage 0 0 0 0) <| Json.Decode.field "usage" decodeUsage)
        |> andMap (defaultTo 0 <| Json.Decode.field "turns" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "wall_time_seconds" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cost_usd" Json.Decode.float)
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "event_counts" (Json.Decode.dict Json.Decode.int))
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)



-- Cost rollup (GET /api/v1/agent/costs) ---------------------------------------


type alias CostSummary =
    { dailyCapUsd : Float
    , dailySpentUsd : Float
    , dailyRemainingUsd : Float
    , dailyExhausted : Bool
    }


type alias CostRow =
    { key : String
    , entries : Int
    , inputTokens : Int
    , outputTokens : Int
    , turns : Int
    , costUsd : Float
    }


type alias CostRollup =
    { groupBy : String
    , summary : CostSummary
    , rows : List CostRow
    }


decodeCostSummary : Json.Decode.Decoder CostSummary
decodeCostSummary =
    Json.Decode.succeed CostSummary
        |> andMap (defaultTo 0 <| Json.Decode.field "daily_cap_usd" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "daily_spent_usd" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "daily_remaining_usd" Json.Decode.float)
        |> andMap (defaultTo False <| Json.Decode.field "daily_exhausted" Json.Decode.bool)


decodeCostRow : Json.Decode.Decoder CostRow
decodeCostRow =
    Json.Decode.succeed CostRow
        |> andMap (Json.Decode.field "key" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "entries" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "input_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "output_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "turns" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cost_usd" Json.Decode.float)


decodeCostRollup : Json.Decode.Decoder CostRollup
decodeCostRollup =
    Json.Decode.succeed CostRollup
        |> andMap (defaultTo "day" <| Json.Decode.field "group_by" Json.Decode.string)
        |> andMap (Json.Decode.field "summary" decodeCostSummary)
        |> andMap (defaultTo [] <| Json.Decode.field "rows" (Json.Decode.list decodeCostRow))



-- Workflow definitions (GET /api/v1/agent/workflows) --------------------------


type alias Workflow =
    { name : String
    , description : String
    , latestVersion : Int
    , contentHash : String
    , liveVersion : Maybe Int
    , createdAt : Int
    }


decodeWorkflow : Json.Decode.Decoder Workflow
decodeWorkflow =
    Json.Decode.succeed Workflow
        |> andMap (Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "latest_version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "content_hash" Json.Decode.string)
        |> andMap (optionalInt "live_version")
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)



-- Principals (GET /api/v1/agent/principals) -----------------------------------


type alias Principal =
    { id : Int
    , name : String
    , description : String
    , tokenPrefix : String
    , scopes : List String
    , teamName : String
    , createdBy : String
    , createdAt : Int
    , expiresAt : Maybe Int
    , revokedAt : Maybe Int
    , lastUsedAt : Maybe Int
    }


decodePrincipal : Json.Decode.Decoder Principal
decodePrincipal =
    Json.Decode.succeed Principal
        |> andMap (Json.Decode.field "id" Json.Decode.int)
        |> andMap (Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "token_prefix" Json.Decode.string)
        |> andMap (defaultTo [] <| Json.Decode.field "scopes" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo "" <| Json.Decode.field "team_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (optionalInt "expires_at")
        |> andMap (optionalInt "revoked_at")
        |> andMap (optionalInt "last_used_at")


{-| A principal is active when it has not been revoked and is not past expiry.
-}
principalActive : Time.Posix -> Principal -> Bool
principalActive now p =
    let
        nowSecs =
            Time.posixToMillis now // 1000
    in
    case p.revokedAt of
        Just _ ->
            False

        Nothing ->
            case p.expiresAt of
                Just e ->
                    e > nowSecs

                Nothing ->
                    True



-- User credential status (GET /api/v1/agent/user-credentials) -----------------


type alias Credential =
    { userId : Int
    , userName : String
    , kind : String
    , expiresAt : Maybe Int
    , lastVerifiedAt : Maybe Int
    }


decodeCredential : Json.Decode.Decoder Credential
decodeCredential =
    Json.Decode.succeed Credential
        |> andMap (defaultTo 0 <| Json.Decode.field "user_id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "user_name" Json.Decode.string)
        |> andMap (Json.Decode.field "kind" Json.Decode.string)
        |> andMap (optionalInt "expires_at")
        |> andMap (optionalInt "last_verified_at")
