module Concourse.Agent exposing
    ( CostRollup
    , CostRow
    , CostSummary
    , CredentialStatus
    , Principal
    , PrincipalCreated
    , RunMetric
    , Usage
    , WorkflowConfig
    , WorkflowDefinition
    , WorkflowGate
    , WorkflowStep
    , WorkflowSummary
    , WorkflowVersionStats
    , decodeCostRollup
    , decodeCredentialStatuses
    , decodePrincipalCreated
    , decodePrincipals
    , decodeRunMetric
    , decodeWorkflowDefinition
    , decodeWorkflowStats
    , decodeWorkflowSummary
    , decodeWorkflowVersions
    )

import Dict exposing (Dict)
import Json.Decode
import Json.Decode.Extra exposing (andMap)
import Time


type alias WorkflowSummary =
    { name : String
    , description : String
    , latestVersion : Int
    , contentHash : String
    , liveVersion : Int
    , createdAt : Time.Posix
    }


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


{-| Read-only status of a stored agent credential. The wire type never
carries the secret itself — secrets are set out-of-band via
`fly agent auth`. `expires_at` / `last_verified_at` are epoch-seconds and
omitted (→ absent → Nothing) when unknown; `jira_account_id` is omitted
when empty (→ "").
-}
type alias CredentialStatus =
    { kind : String
    , expiresAt : Maybe Time.Posix
    , lastVerifiedAt : Maybe Time.Posix
    , jiraAccountId : String
    }


{-| One agent\_principals row. `expires_at` / `revoked_at` / `last_used_at`
are omitempty epoch-seconds on the wire, so they decode to Nothing when
absent. The token material is never included in the list/GET shape.
-}
type alias Principal =
    { id : Int
    , name : String
    , description : String
    , tokenPrefix : String
    , scopes : List String
    , teamName : String
    , createdBy : String
    , createdAt : Time.Posix
    , expiresAt : Maybe Time.Posix
    , revokedAt : Maybe Time.Posix
    , lastUsedAt : Maybe Time.Posix
    }


{-| The POST /agent/principals response: a Principal (its fields inlined at
the top level) plus the one-time `token` secret, surfaced exactly once.
-}
type alias PrincipalCreated =
    { principal : Principal
    , token : String
    }


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


optionalInt : String -> Json.Decode.Decoder (Maybe Int)
optionalInt name =
    Json.Decode.maybe (Json.Decode.field name Json.Decode.int)


dateFromSeconds : Int -> Time.Posix
dateFromSeconds =
    Time.millisToPosix << (*) 1000


{-| Decode an omitempty epoch-seconds field into a Maybe Posix: absent (or
otherwise unreadable) → Nothing, present int → Just.
-}
optionalPosix : String -> Json.Decode.Decoder (Maybe Time.Posix)
optionalPosix fieldName =
    Json.Decode.maybe
        (Json.Decode.field fieldName (Json.Decode.map dateFromSeconds Json.Decode.int))



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
    , buildStatus : String
    , outcome : String
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
        |> andMap (defaultTo "" <| Json.Decode.field "build_status" Json.Decode.string)
        -- server-derived U3 fusion of build_status + status; absent ("") on
        -- servers that predate it, so views fall back to the local fusion
        |> andMap (defaultTo "" <| Json.Decode.field "outcome" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "summary" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "model" Json.Decode.string)
        |> andMap (defaultTo (Usage 0 0 0 0) <| Json.Decode.field "usage" decodeUsage)
        |> andMap (defaultTo 0 <| Json.Decode.field "turns" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "wall_time_seconds" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cost_usd" Json.Decode.float)
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "event_counts" (Json.Decode.dict Json.Decode.int))
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)



-- Workflow definitions --------------------------------------------------------


decodeWorkflowSummary : Json.Decode.Decoder WorkflowSummary
decodeWorkflowSummary =
    Json.Decode.succeed WorkflowSummary
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "latest_version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "content_hash" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "live_version" Json.Decode.int)
        |> andMap (defaultTo (dateFromSeconds 0) <| Json.Decode.field "created_at" (Json.Decode.map dateFromSeconds Json.Decode.int))



-- Workflow detail (definition + config + per-version stats) --------------------


type alias WorkflowStep =
    { agent : String
    , checkpoint : String
    , prompt : String
    , model : String
    , maxTurns : Int
    , inputs : List String
    , outputs : List String
    , budgetSliceUsd : Float
    }


type alias WorkflowGate =
    { gate : String
    , scope : String
    , focus : String
    }


type alias WorkflowConfig =
    { schemaVersion : Int
    , name : String
    , description : String
    , specDelivery : String
    , defaultModel : String
    , defaultMaxTurns : Int
    , budgetTicketUsd : Float
    , budgetJudgeUsd : Float
    , prompts : Dict String String
    , steps : List WorkflowStep
    , gates : List WorkflowGate
    , onGateFailure : String
    }


type alias WorkflowDefinition =
    { id : Int
    , name : String
    , version : Int
    , contentHash : String
    , live : Bool
    , description : String
    , annotation : String
    , hidden : Bool
    , createdBy : String
    , createdAt : Time.Posix
    , rawYaml : String
    , config : WorkflowConfig
    }


type alias WorkflowVersionStats =
    { version : Maybe Int
    , runs : Int
    , tickets : Int
    , succeededRuns : Int
    , successRate : Float
    , avgCostUsd : Float
    , avgTurns : Float
    , totalCostUsd : Float
    }


decodeWorkflowStep : Json.Decode.Decoder WorkflowStep
decodeWorkflowStep =
    Json.Decode.succeed WorkflowStep
        |> andMap (defaultTo "" <| Json.Decode.field "agent" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "checkpoint" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "prompt" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "model" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "max_turns" Json.Decode.int)
        |> andMap (defaultTo [] <| Json.Decode.field "inputs" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo [] <| Json.Decode.field "outputs" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo 0 <| Json.Decode.field "budget_slice_usd" Json.Decode.float)


decodeWorkflowGate : Json.Decode.Decoder WorkflowGate
decodeWorkflowGate =
    Json.Decode.succeed WorkflowGate
        |> andMap (defaultTo "" <| Json.Decode.field "gate" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "scope" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "focus" Json.Decode.string)


decodeWorkflowConfig : Json.Decode.Decoder WorkflowConfig
decodeWorkflowConfig =
    Json.Decode.succeed WorkflowConfig
        |> andMap (defaultTo 1 <| Json.Decode.field "schema_version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "spec_delivery" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.at [ "defaults", "model" ] Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.at [ "defaults", "max_turns" ] Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.at [ "budget", "ticket_usd" ] Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.at [ "budget", "judge_usd" ] Json.Decode.float)
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "prompts" (Json.Decode.dict Json.Decode.string))
        |> andMap (defaultTo [] <| Json.Decode.field "steps" (Json.Decode.list decodeWorkflowStep))
        |> andMap (defaultTo [] <| Json.Decode.at [ "gate_policy", "gates" ] (Json.Decode.list decodeWorkflowGate))
        |> andMap (defaultTo "" <| Json.Decode.at [ "gate_policy", "on_gate_failure" ] Json.Decode.string)


emptyWorkflowConfig : WorkflowConfig
emptyWorkflowConfig =
    { schemaVersion = 1
    , name = ""
    , description = ""
    , specDelivery = ""
    , defaultModel = ""
    , defaultMaxTurns = 0
    , budgetTicketUsd = 0
    , budgetJudgeUsd = 0
    , prompts = Dict.empty
    , steps = []
    , gates = []
    , onGateFailure = ""
    }


decodeWorkflowDefinition : Json.Decode.Decoder WorkflowDefinition
decodeWorkflowDefinition =
    Json.Decode.succeed WorkflowDefinition
        |> andMap (defaultTo 0 <| Json.Decode.field "id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "content_hash" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "live" Json.Decode.bool)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "annotation" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "hidden" Json.Decode.bool)
        |> andMap (defaultTo "" <| Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (defaultTo (dateFromSeconds 0) <| Json.Decode.field "created_at" (Json.Decode.map dateFromSeconds Json.Decode.int))
        |> andMap (defaultTo "" <| Json.Decode.field "raw_yaml" Json.Decode.string)
        |> andMap (defaultTo emptyWorkflowConfig <| Json.Decode.field "config" decodeWorkflowConfig)


decodeWorkflowVersions : Json.Decode.Decoder (List WorkflowDefinition)
decodeWorkflowVersions =
    Json.Decode.nullable (Json.Decode.list decodeWorkflowDefinition)
        |> Json.Decode.map (Maybe.withDefault [])


decodeWorkflowVersionStats : Json.Decode.Decoder WorkflowVersionStats
decodeWorkflowVersionStats =
    Json.Decode.succeed WorkflowVersionStats
        |> andMap (optionalInt "version")
        |> andMap (defaultTo 0 <| Json.Decode.field "runs" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "tickets" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "succeeded_runs" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "success_rate" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "avg_cost_usd" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "avg_turns" Json.Decode.float)
        |> andMap (defaultTo 0 <| Json.Decode.field "total_cost_usd" Json.Decode.float)


decodeWorkflowStats : Json.Decode.Decoder (List WorkflowVersionStats)
decodeWorkflowStats =
    Json.Decode.nullable (Json.Decode.list decodeWorkflowVersionStats)
        |> Json.Decode.map (Maybe.withDefault [])


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
        |> andMap (defaultTo "" <| Json.Decode.field "key" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "entries" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "input_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "output_tokens" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "turns" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "cost_usd" Json.Decode.float)


emptyCostSummary : CostSummary
emptyCostSummary =
    { dailyCapUsd = 0
    , dailySpentUsd = 0
    , dailyRemainingUsd = 0
    , dailyExhausted = False
    }


decodeCostRollup : Json.Decode.Decoder CostRollup
decodeCostRollup =
    Json.Decode.succeed CostRollup
        |> andMap (defaultTo "day" <| Json.Decode.field "group_by" Json.Decode.string)
        |> andMap (defaultTo emptyCostSummary <| Json.Decode.field "summary" decodeCostSummary)
        |> andMap
            (Json.Decode.map (Maybe.withDefault [])
                (Json.Decode.field "rows" (Json.Decode.nullable (Json.Decode.list decodeCostRow)))
                |> defaultTo []
            )


decodeCredentialStatus : Json.Decode.Decoder CredentialStatus
decodeCredentialStatus =
    Json.Decode.succeed CredentialStatus
        |> andMap (defaultTo "" <| Json.Decode.field "kind" Json.Decode.string)
        |> andMap (optionalPosix "expires_at")
        |> andMap (optionalPosix "last_verified_at")
        |> andMap (defaultTo "" <| Json.Decode.field "jira_account_id" Json.Decode.string)


{-| Decode the credential-status list. Tolerates a null top-level array
(handler may encode nil) by defaulting to [].
-}
decodeCredentialStatuses : Json.Decode.Decoder (List CredentialStatus)
decodeCredentialStatuses =
    Json.Decode.nullable (Json.Decode.list decodeCredentialStatus)
        |> Json.Decode.map (Maybe.withDefault [])


decodePrincipal : Json.Decode.Decoder Principal
decodePrincipal =
    Json.Decode.succeed Principal
        |> andMap (defaultTo 0 <| Json.Decode.field "id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "token_prefix" Json.Decode.string)
        |> andMap (defaultTo [] <| Json.Decode.field "scopes" (Json.Decode.list Json.Decode.string))
        |> andMap (defaultTo "" <| Json.Decode.field "team_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (defaultTo (dateFromSeconds 0) <| Json.Decode.field "created_at" (Json.Decode.map dateFromSeconds Json.Decode.int))
        |> andMap (optionalPosix "expires_at")
        |> andMap (optionalPosix "revoked_at")
        |> andMap (optionalPosix "last_used_at")


{-| Decode the principals list. Tolerates a null top-level array (handler
encodes nil for the empty case) by defaulting to [].
-}
decodePrincipals : Json.Decode.Decoder (List Principal)
decodePrincipals =
    Json.Decode.nullable (Json.Decode.list decodePrincipal)
        |> Json.Decode.map (Maybe.withDefault [])


{-| The POST response inlines the Principal fields and adds `token`, so
decode the Principal from the same object plus the one-time token.
-}
decodePrincipalCreated : Json.Decode.Decoder PrincipalCreated
decodePrincipalCreated =
    Json.Decode.map2 PrincipalCreated
        decodePrincipal
        (defaultTo "" <| Json.Decode.field "token" Json.Decode.string)
