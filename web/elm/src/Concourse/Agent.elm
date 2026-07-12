module Concourse.Agent exposing
    ( CostRollup
    , CostRow
    , CostSummary
    , WorkflowSummary
    , decodeCostRollup
    , decodeWorkflowSummary
    )

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


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


dateFromSeconds : Int -> Time.Posix
dateFromSeconds =
    Time.millisToPosix << (*) 1000


decodeWorkflowSummary : Json.Decode.Decoder WorkflowSummary
decodeWorkflowSummary =
    Json.Decode.succeed WorkflowSummary
        |> andMap (defaultTo "" <| Json.Decode.field "name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "latest_version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "content_hash" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "live_version" Json.Decode.int)
        |> andMap (defaultTo (dateFromSeconds 0) <| Json.Decode.field "created_at" (Json.Decode.map dateFromSeconds Json.Decode.int))


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
