module Concourse.PipelineRun exposing (PipelineRun, decodePipelineRun, encodeCreatePipelineRun)

import Concourse exposing (InstanceVars, PipelineIdentifier, decodeInstanceVars, decodePipelineIdentifier)
import Concourse.BuildStatus exposing (BuildStatus(..))
import Dict
import Json.Decode
import Json.Decode.Extra exposing (andMap)
import Json.Encode
import Time exposing (Posix)

type alias PipelineRun =
    { id : Int
    , number : Int
    , status : BuildStatus
    , params : InstanceVars
    , createdBy : Maybe String
    , createdAt : Posix
    , completedAt : Maybe Posix
    , reclaimed : Bool
    , instanceRef : Maybe PipelineIdentifier
    }

decodePipelineRun : Json.Decode.Decoder PipelineRun
decodePipelineRun =
    Json.Decode.succeed PipelineRun
        |> andMap (Json.Decode.field "id" Json.Decode.int)
        |> andMap (Json.Decode.field "number" Json.Decode.int)
        |> andMap (Json.Decode.field "status" decodeRunStatus)
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "params" decodeInstanceVars)
        |> andMap (Json.Decode.maybe (Json.Decode.field "created_by" Json.Decode.string))
        |> andMap (Json.Decode.field "created_at" (Json.Decode.map secondsToPosix Json.Decode.int))
        |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" (Json.Decode.map secondsToPosix Json.Decode.int)))
        |> andMap (defaultTo False <| Json.Decode.field "reclaimed" Json.Decode.bool)
        |> andMap (Json.Decode.maybe (Json.Decode.field "instance_ref" decodePipelineIdentifier))

decodeRunStatus : Json.Decode.Decoder BuildStatus
decodeRunStatus =
    Json.Decode.string
        |> Json.Decode.andThen
            (\status ->
                case status of
                    "running" ->
                        Json.Decode.succeed BuildStatusStarted
                    "pending" ->
                        Json.Decode.succeed BuildStatusPending
                    "started" ->
                        Json.Decode.succeed BuildStatusStarted
                    "succeeded" ->
                        Json.Decode.succeed BuildStatusSucceeded
                    "failed" ->
                        Json.Decode.succeed BuildStatusFailed

                    "errored" ->
                        Json.Decode.succeed BuildStatusErrored

                    "aborted" ->
                        Json.Decode.succeed BuildStatusAborted

                    unknown ->
                        Json.Decode.fail <| "unknown pipeline run status: " ++ unknown
            )

secondsToPosix : Int -> Posix
secondsToPosix =
    Time.millisToPosix << (*) 1000

defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.map (Maybe.withDefault default) << Json.Decode.maybe

encodeCreatePipelineRun : InstanceVars -> Json.Encode.Value
encodeCreatePipelineRun vars =
    Json.Encode.object
        [ ( "vars", Concourse.encodeInstanceVars vars )
        ]
