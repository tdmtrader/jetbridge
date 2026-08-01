module Concourse.WorkflowRunGraph exposing
    ( Occurrence
    , RunGraph
    , decodeRunGraph
    , nodeStateLookup
    , occurrencesForNode
    )

{-| One run's exact DAG and its own node state.

This mirrors `agent/api/workflowruns.GraphResponse` field for field. Three of
its properties matter here:

**The graph is the run's own revision**, not the promoted one. Opening an old
run shows the shape that actually executed, which is why this is a separate
response from the workflow overview rather than the overview with a filter.

**There is no second graph model.** `AgentGraph.Model` is shared, and
`nodeStateLookup` is the run-scoped half of the same `AgentGraph.View.NodeState`
lookup the overview supplies from aggregate counts. One renderer, two lookups.

**`graph` is always a decodable object.** When derivation fails the server
sends an empty graph plus `graph_unavailable: true`, never `null`, so the list
decoders in `AgentGraph.Decoder` still succeed and only the canvas is missing.

Run, wait, and publication identifiers are strings: they are bigint identities
that exceed the range JavaScript numbers represent exactly, and decoding one as
an `Int` would silently round it.

-}

import AgentGraph.Decoder
import AgentGraph.Model
import AgentGraph.View
import Dict exposing (Dict)
import Json.Decode exposing (Decoder)
import Json.Decode.Extra exposing (andMap)


type alias Occurrence =
    { nodeId : String
    , nodeKind : String
    , retryAttempt : Int
    , attempt : Int
    , status : String
    , planId : String
    , waitId : Maybe String
    , publicationId : Maybe String
    , startedAt : Maybe String
    , completedAt : Maybe String
    , durationSeconds : Int
    , costUsd : Float
    }


type alias RunGraph =
    { workflowRunId : String
    , workflowName : String
    , workflowVersion : Int
    , graph : AgentGraph.Model.Graph
    , graphUnavailable : Bool
    , occurrences : List Occurrence
    }


decodeRunGraph : Decoder RunGraph
decodeRunGraph =
    Json.Decode.succeed RunGraph
        |> andMap (Json.Decode.field "workflow_run_id" Json.Decode.string)
        |> andMap (Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (Json.Decode.field "workflow_version" Json.Decode.int)
        |> andMap (Json.Decode.field "graph" AgentGraph.Decoder.graph)
        |> andMap (optionalField "graph_unavailable" Json.Decode.bool False)
        |> andMap (Json.Decode.field "occurrences" (Json.Decode.list decodeOccurrence))


decodeOccurrence : Decoder Occurrence
decodeOccurrence =
    Json.Decode.succeed Occurrence
        |> andMap (Json.Decode.field "node_id" Json.Decode.string)
        |> andMap (Json.Decode.field "node_kind" Json.Decode.string)
        |> andMap (optionalField "retry_attempt" Json.Decode.int 0)
        |> andMap (optionalField "attempt" Json.Decode.int 0)
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (optionalField "plan_id" Json.Decode.string "")
        |> andMap (Json.Decode.maybe (Json.Decode.field "wait_id" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "publication_id" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "started_at" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" Json.Decode.string))
        |> andMap (optionalField "duration_seconds" Json.Decode.int 0)
        |> andMap (optionalField "cost_usd" Json.Decode.float 0)


optionalField : String -> Decoder a -> a -> Decoder a
optionalField name decoder fallback =
    Json.Decode.oneOf
        [ Json.Decode.field name decoder
        , Json.Decode.field name (Json.Decode.null fallback)
        , Json.Decode.succeed fallback
        ]


occurrencesForNode : RunGraph -> String -> List Occurrence
occurrencesForNode runGraph nodeId =
    runGraph.occurrences
        |> List.filter (\occurrence -> occurrence.nodeId == nodeId)
        |> List.sortBy .attempt


{-| This run's node state, in the shape the shared renderer already takes.

For a single run each occurrence contributes exactly one non-zero count, which
is what makes one renderer serve both surfaces: the overview counts across many
runs, this counts across one. A node retried inside its run legitimately shows
two — a failure and the success that followed it — and collapsing that would
erase the recovery.

`needsAttention` is answered the same way `occurrence.ResolveEffective` answers
it for the canvas: a live occurrence always asks for action, and a terminal
failure asks unless something later in the same node's history resolved it.
Within one run, "something later" is a success at the same node.
-}
nodeStateLookup : RunGraph -> (String -> AgentGraph.View.NodeState)
nodeStateLookup runGraph =
    let
        byId : Dict String AgentGraph.View.NodeState
        byId =
            List.foldl accumulate Dict.empty runGraph.occurrences
    in
    \nodeId -> Dict.get nodeId byId |> Maybe.withDefault AgentGraph.View.emptyState


accumulate :
    Occurrence
    -> Dict String AgentGraph.View.NodeState
    -> Dict String AgentGraph.View.NodeState
accumulate occurrence byId =
    let
        current =
            Dict.get occurrence.nodeId byId |> Maybe.withDefault AgentGraph.View.emptyState

        counted =
            case occurrence.status of
                "running" ->
                    { current | running = current.running + 1 }

                "waiting" ->
                    { current | waiting = current.waiting + 1 }

                "pending" ->
                    { current | pending = current.pending + 1 }

                "succeeded" ->
                    { current | succeeded = current.succeeded + 1 }

                "failed" ->
                    { current | failed = current.failed + 1 }

                "errored" ->
                    { current | errored = current.errored + 1 }

                "aborted" ->
                    { current | aborted = current.aborted + 1 }

                "skipped" ->
                    { current | skipped = current.skipped + 1 }

                _ ->
                    current
    in
    Dict.insert occurrence.nodeId
        { counted
            | needsAttention = attentionOf counted
            , hasWindowActivity = True
        }
        byId


attentionOf : AgentGraph.View.NodeState -> Bool
attentionOf state =
    let
        unresolved =
            state.failed + state.errored + state.aborted
    in
    state.running > 0 || state.waiting > 0 || (unresolved > 0 && state.succeeded == 0)
