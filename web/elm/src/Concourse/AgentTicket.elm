module Concourse.AgentTicket exposing
    ( Detail
    , DispatchResult
    , Ticket
    , decodeDetail
    , decodeDispatchResult
    , decodeTicket
    , repoWebUrl
    )

{-| Client-side view of the agent-ticket API (agent/api/tickets/types.go).

The ticket is a queue shell: title, body, where it sits in the queue, and the
durable identifiers that link out to the workflow run carrying every piece of
execution evidence. It has no spec/plan/task content of its own (those tables
went with their deleted write routes) and no budget or run-error mirror.

All decoders are tolerant (mirroring Concourse.AgentReview): missing string
fields default to "" and missing scalars to a sensible zero, so a partial
payload never fails the whole page.

-}

import Concourse.Snapshot as Snapshot
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
    , userName : String
    , branch : String
    , createdAt : Int
    , updatedAt : Int
    , workflowVersion : Maybe Int
    , pipelineRunId : Maybe Int
    , attemptCount : Int
    , completedAt : Maybe Int
    , workflowRunId : Maybe String
    , workItemSnapshotId : Maybe String
    , repositorySnapshotId : Maybe String
    }


type alias Detail =
    { ticket : Ticket
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
        |> andMap (defaultTo "" <| Json.Decode.field "user_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "branch" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "updated_at" Json.Decode.int)
        |> andMap (optionalInt "workflow_version")
        |> andMap (optionalInt "pipeline_run_id")
        |> andMap (defaultTo 0 <| Json.Decode.field "attempt_count" Json.Decode.int)
        |> andMap (optionalInt "completed_at")
        |> andMap (Snapshot.decodeOptionalIdField "workflow_run_id")
        |> andMap (Snapshot.decodeOptionalIdField "work_item_snapshot_id")
        |> andMap (Snapshot.decodeOptionalIdField "repository_snapshot_id")


decodeDetail : Json.Decode.Decoder Detail
decodeDetail =
    Json.Decode.succeed Detail
        |> andMap (Json.Decode.field "ticket" decodeTicket)


decodeDispatchResult : Json.Decode.Decoder DispatchResult
decodeDispatchResult =
    Json.Decode.succeed DispatchResult
        |> andMap (defaultTo 0 <| Json.Decode.field "run_id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "pipeline_name" Json.Decode.string)


{-| Normalize the ticket's `repo` field to a browsable web URL. The field
arrives in three shapes: a full clone URL (`https://host/org/name.git`), an
SSH form (`git@host:org/name.git`), or a bare GitHub slug (`org/name` — what
`fly agent tickets create` records today). Anything else yields Nothing so
callers fall back to plain text instead of a broken link.
-}
repoWebUrl : String -> Maybe String
repoWebUrl repo =
    let
        stripGit url =
            if String.endsWith ".git" url then
                String.dropRight 4 url

            else
                url
    in
    if String.startsWith "http://" repo || String.startsWith "https://" repo then
        Just (stripGit repo)

    else if String.startsWith "git@" repo then
        case String.split ":" (String.dropLeft 4 repo) of
            [ host, path ] ->
                Just (stripGit ("https://" ++ host ++ "/" ++ path))

            _ ->
                Nothing

    else
        case String.split "/" repo of
            [ org, name ] ->
                if org /= "" && name /= "" && not (String.contains " " repo) then
                    Just (stripGit ("https://github.com/" ++ org ++ "/" ++ name))

                else
                    Nothing

            _ ->
                Nothing
