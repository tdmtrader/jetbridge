module Concourse.Snapshot exposing
    ( BuildOccurrence
    , Detail
    , Manifest
    , Production
    , ProductionInput
    , ProductionSummary
    , Ref
    , RetentionClaim
    , decodeDetail
    , decodeId
    , decodeManifest
    , decodeOptionalIdField
    , decodeProduction
    , decodeRef
    , decodeRetentionClaim
    )

import Char
import Json.Decode exposing (Decoder, Value)
import Json.Decode.Extra exposing (andMap)


type alias Ref =
    { id : String
    , typeRef : String
    , digest : String
    }


type alias Manifest =
    { id : String
    , typeRef : String
    , digest : String
    , byteSize : Float
    , fileCount : Float
    , representation : String
    , intrinsicMetadata : Maybe Value
    , contentState : String
    , createdAt : String
    }


type alias RetentionClaim =
    { id : String
    , snapshotId : String
    , class : String
    , expiresAt : Maybe String
    , actor : String
    , reason : String
    , createdAt : String
    }


type alias ProductionInput =
    { position : Int
    , portName : String
    , snapshot : Ref
    }


type alias BuildOccurrence =
    { id : Int
    , planId : String
    , attempt : String
    , stepKind : String
    , stepName : String
    , workflowDefinitionId : Maybe Int
    , workflowRunId : Maybe String
    , workflowName : Maybe String
    }


type alias Production =
    { id : String
    , kind : String
    , createdBy : String
    , build : Maybe BuildOccurrence
    , outputPort : String
    , sourceMetadata : Maybe Value
    , createdAt : String
    , inputs : List ProductionInput
    }


type alias ProductionSummary =
    { id : String
    , kind : String
    , createdBy : String
    , build : Maybe BuildOccurrence
    , outputPort : String
    , snapshot : Ref
    , createdAt : String
    }


type alias Detail =
    { manifest : Manifest
    , replicaCount : Int
    , retentionClaims : List RetentionClaim
    , productions : List Production
    , downstream : List ProductionSummary
    }


decodeId : Decoder String
decodeId =
    Json.Decode.string
        |> Json.Decode.andThen
            (\value ->
                if isCanonicalPositiveDecimal value then
                    Json.Decode.succeed value

                else
                    Json.Decode.fail "durable IDs must be quoted canonical positive decimals"
            )


decodeOptionalIdField : String -> Decoder (Maybe String)
decodeOptionalIdField fieldName =
    optionalField fieldName decodeId


isCanonicalPositiveDecimal : String -> Bool
isCanonicalPositiveDecimal value =
    case String.uncons value of
        Nothing ->
            False

        Just ( first, rest ) ->
            first
                /= '0'
                && Char.isDigit first
                && String.all Char.isDigit rest


decodeRef : Decoder Ref
decodeRef =
    Json.Decode.succeed Ref
        |> andMap (Json.Decode.field "id" decodeId)
        |> andMap (Json.Decode.field "type" Json.Decode.string)
        |> andMap (Json.Decode.field "digest" Json.Decode.string)


decodeManifest : Decoder Manifest
decodeManifest =
    Json.Decode.succeed Manifest
        |> andMap (Json.Decode.field "id" decodeId)
        |> andMap (Json.Decode.field "type" Json.Decode.string)
        |> andMap (Json.Decode.field "digest" Json.Decode.string)
        |> andMap (Json.Decode.field "byte_size" Json.Decode.float)
        |> andMap (Json.Decode.field "file_count" Json.Decode.float)
        |> andMap (Json.Decode.field "representation" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "intrinsic_metadata" Json.Decode.value))
        |> andMap (Json.Decode.field "content_state" Json.Decode.string)
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)


decodeRetentionClaim : Decoder RetentionClaim
decodeRetentionClaim =
    Json.Decode.succeed RetentionClaim
        |> andMap (Json.Decode.field "id" decodeId)
        |> andMap (Json.Decode.field "snapshot_id" decodeId)
        |> andMap (Json.Decode.field "class" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "expires_at" Json.Decode.string))
        |> andMap (defaultTo "" (Json.Decode.field "actor" Json.Decode.string))
        |> andMap (Json.Decode.field "reason" Json.Decode.string)
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)


decodeProductionInput : Decoder ProductionInput
decodeProductionInput =
    Json.Decode.succeed ProductionInput
        |> andMap (Json.Decode.field "position" Json.Decode.int)
        |> andMap (Json.Decode.field "port" Json.Decode.string)
        |> andMap (Json.Decode.field "snapshot" decodeRef)


decodeBuildOccurrence : Decoder BuildOccurrence
decodeBuildOccurrence =
    Json.Decode.succeed BuildOccurrence
        |> andMap (Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (Json.Decode.field "plan_id" Json.Decode.string)
        |> andMap (Json.Decode.field "attempt" Json.Decode.string)
        |> andMap (Json.Decode.field "step_kind" Json.Decode.string)
        |> andMap (Json.Decode.field "step_name" Json.Decode.string)
        |> andMap (optionalField "workflow_definition_id" Json.Decode.int)
        |> andMap (optionalField "workflow_run_id" decodeId)
        |> andMap (optionalField "workflow_name" Json.Decode.string)


decodeProduction : Decoder Production
decodeProduction =
    Json.Decode.succeed Production
        |> andMap (Json.Decode.field "id" decodeId)
        |> andMap (Json.Decode.field "kind" Json.Decode.string)
        |> andMap (Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (optionalField "build" decodeBuildOccurrence)
        |> andMap (defaultTo "" (Json.Decode.field "output_port" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "source_metadata" Json.Decode.value))
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)
        |> andMap (defaultTo [] (Json.Decode.field "inputs" (Json.Decode.list decodeProductionInput)))


decodeProductionSummary : Decoder ProductionSummary
decodeProductionSummary =
    Json.Decode.succeed ProductionSummary
        |> andMap (Json.Decode.field "id" decodeId)
        |> andMap (Json.Decode.field "kind" Json.Decode.string)
        |> andMap (Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (optionalField "build" decodeBuildOccurrence)
        |> andMap (defaultTo "" (Json.Decode.field "output_port" Json.Decode.string))
        |> andMap (Json.Decode.field "snapshot" decodeRef)
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)


decodeDetail : Decoder Detail
decodeDetail =
    Json.Decode.succeed Detail
        |> andMap (Json.Decode.field "manifest" decodeManifest)
        |> andMap (Json.Decode.field "replica_count" Json.Decode.int)
        |> andMap (defaultTo [] (Json.Decode.field "retention_claims" (Json.Decode.list decodeRetentionClaim)))
        |> andMap (defaultTo [] (Json.Decode.field "productions" (Json.Decode.list decodeProduction)))
        |> andMap (defaultTo [] (Json.Decode.field "downstream" (Json.Decode.list decodeProductionSummary)))


defaultTo : a -> Decoder a -> Decoder a
defaultTo fallback decoder =
    Json.Decode.maybe decoder
        |> Json.Decode.map (Maybe.withDefault fallback)


optionalField : String -> Decoder a -> Decoder (Maybe a)
optionalField fieldName decoder =
    Json.Decode.keyValuePairs Json.Decode.value
        |> Json.Decode.andThen
            (\fields ->
                if List.any (Tuple.first >> (==) fieldName) fields then
                    Json.Decode.field fieldName (Json.Decode.nullable decoder)

                else
                    Json.Decode.succeed Nothing
            )
