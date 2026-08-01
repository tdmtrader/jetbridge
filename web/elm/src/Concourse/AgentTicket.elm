module Concourse.AgentTicket exposing
    ( Detail
    , DispatchResult
    , JournalEntry
    , Ticket
    , decodeDetail
    , decodeDispatchResult
    , decodeJournal
    , decodeTicket
    , repoWebUrl
    )

{-| Client-side view of the agent-ticket API (agent/api/tickets/types.go).

The ticket is a queue shell: title, body, where it sits in the queue, and the
durable identifiers that link out to the workflow run carrying every piece of
execution evidence. It has no spec/plan/task content of its own (those tables
went with their deleted write routes) and no budget or run-error mirror. It also
does not carry the ticket's `pipeline_run_id`: the server may still send it, but
a pipeline run is an execution detail of the durable workflow run, rendered
there, so decoding it here only produced a field nothing could render.

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
    , createdAt : Int
    , updatedAt : Int
    , workflowVersion : Maybe Int
    , attemptCount : Int
    , completedAt : Maybe Int
    , workflowRunId : Maybe String
    , workItemSnapshotId : Maybe String
    , repositorySnapshotId : Maybe String
    }


type alias Detail =
    { ticket : Ticket
    }


{-| The response to `POST /api/v1/agent/tickets/:id/dispatch`.

The dispatch response carries ONE identity: the durable workflow run it
created, as a quoted canonical decimal (durable IDs exceed 2^53, so a JSON
number would silently round). The old `run_id` + `pipeline_name` pair is gone —
a pipeline name is an execution detail of one attempt, never the thing the user
was sent to look at.

`pipeline_run_id` may ride along as a server-side diagnostic; it is
deliberately NOT decoded here. The run page renders that diagnostic from the
run's own summary (the durable record), so decoding a second copy off a
transient response would only create a field with nowhere honest to render.

-}
type alias DispatchResult =
    { workflowRunId : String
    , warnings : List String
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
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "updated_at" Json.Decode.int)
        |> andMap (optionalInt "workflow_version")
        |> andMap (defaultTo 0 <| Json.Decode.field "attempt_count" Json.Decode.int)
        |> andMap (optionalInt "completed_at")
        |> andMap (Snapshot.decodeOptionalIdField "workflow_run_id")
        |> andMap (Snapshot.decodeOptionalIdField "work_item_snapshot_id")
        |> andMap (Snapshot.decodeOptionalIdField "repository_snapshot_id")


decodeDetail : Json.Decode.Decoder Detail
decodeDetail =
    Json.Decode.succeed Detail
        |> andMap (Json.Decode.field "ticket" decodeTicket)


{-| Unlike the tolerant ticket decoders, `workflow_run_id` is REQUIRED: it is
the only thing the caller can act on, so a response without it is a failed
dispatch, not a dispatch with a blank field. `warnings` defaults to empty.
-}
decodeDispatchResult : Json.Decode.Decoder DispatchResult
decodeDispatchResult =
    Json.Decode.succeed DispatchResult
        |> andMap (Json.Decode.field "workflow_run_id" Snapshot.decodeId)
        |> andMap
            (defaultTo [] <|
                Json.Decode.field "warnings" (Json.Decode.list Json.Decode.string)
            )


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


{-| One entry of the ticket's cross-workflow journal
(`GET /api/v1/agent/tickets/:id/runs`).

Identities are STRINGS: durable run IDs exceed 2^53, so a JSON number would
silently round. Timestamps stay in their wire form and are parsed where they
are rendered, matching every other run surface.

-}
type alias JournalEntry =
    { workflowRunId : String
    , workflowName : String
    , workflowVersion : Int
    , status : String
    , originKind : String
    , retryOfWorkflowRunId : Maybe String
    , createdAt : String
    , startedAt : Maybe String
    , completedAt : Maybe String
    , outstanding : Bool
    , errorMessage : String
    }


decodeJournal : Json.Decode.Decoder (List JournalEntry)
decodeJournal =
    defaultTo [] <| Json.Decode.field "runs" (Json.Decode.list decodeJournalEntry)


decodeJournalEntry : Json.Decode.Decoder JournalEntry
decodeJournalEntry =
    Json.Decode.succeed JournalEntry
        |> andMap (Json.Decode.field "workflow_run_id" Snapshot.decodeId)
        |> andMap (defaultTo "" <| Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "workflow_version" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "status" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "origin_kind" Json.Decode.string)
        |> andMap (Snapshot.decodeOptionalIdField "retry_of_workflow_run_id")
        |> andMap (defaultTo "" <| Json.Decode.field "created_at" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "started_at" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" Json.Decode.string))
        |> andMap (defaultTo False <| Json.Decode.field "outstanding" Json.Decode.bool)
        |> andMap (defaultTo "" <| Json.Decode.field "error_message" Json.Decode.string)
