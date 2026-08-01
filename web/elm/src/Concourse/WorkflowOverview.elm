module Concourse.WorkflowOverview exposing
    ( NodeState
    , Overview
    , RevisionBoundary
    , Window
    , Workflow
    , decodeOverview
    , nodeStateLookup
    )

{-| The workflow overview response.

This mirrors `agent/api/workflowoverview.Response` field for field. Two of its
deliberate absences matter here:

**There is no aggregate node status.** A node representing several concurrent
runs has no canonical state, so the server sends active counts, windowed
history counts, and a retry-resolved attention flag, and refuses to collapse
them. Nothing in this module invents one either.

**`graph` is always a decodable object.** When derivation fails the server
sends an empty graph plus `graph_unavailable: true`, never `null`, precisely
so the list decoders in `AgentGraph.Decoder` still succeed and only the canvas
is missing. `graph_unavailable` is therefore a fact to render, not an error to
handle.

Run and revision identifiers are strings: a durable run ID exceeds the range
JavaScript numbers represent exactly, and decoding one as an `Int` would
silently round it.

-}

import AgentGraph.Decoder
import AgentGraph.Model
import AgentGraph.View
import Dict exposing (Dict)
import Json.Decode exposing (Decoder)
import Json.Decode.Extra exposing (andMap)


type alias Overview =
    { workflow : Workflow
    , window : Window
    , graph : AgentGraph.Model.Graph
    , graphUnavailable : Bool
    , nodeState : List NodeState
    , revisionBoundaries : List RevisionBoundary
    , hasHistoricalOnlyNodes : Bool
    }


type alias Workflow =
    { name : String
    , hasPromotedVersion : Bool

    -- The revision that supplied the rendered graph: the promoted one when
    -- there is one, otherwise the latest imported one. With
    -- hasPromotedVersion false this is what "not promoted — showing version N"
    -- names.
    , graphVersion : Int
    , contentHash : String
    }


type alias Window =
    { kind : String
    , from : String
    , to : String

    -- Always true, and stated rather than implied: active runs are
    -- represented regardless of how long ago they began, so changing the
    -- window never changes the meaning of active state.
    , includesActiveBeforeWindow : Bool
    }


type alias NodeState =
    { nodeId : String
    , running : Int
    , waiting : Int
    , pending : Int
    , succeeded : Int
    , failed : Int
    , errored : Int
    , aborted : Int
    , skipped : Int
    , needsAttention : Bool
    , hasWindowActivity : Bool
    }


type alias RevisionBoundary =
    { version : Int
    , promotedAt : Maybe String
    , firstRunId : String
    , firstRunAt : String
    }


decodeOverview : Decoder Overview
decodeOverview =
    Json.Decode.succeed Overview
        |> andMap (Json.Decode.field "workflow" decodeWorkflow)
        |> andMap (Json.Decode.field "window" decodeWindow)
        |> andMap (Json.Decode.field "graph" AgentGraph.Decoder.graph)
        |> andMap (optional "graph_unavailable" Json.Decode.bool False)
        |> andMap (Json.Decode.field "node_state" (Json.Decode.list decodeNodeState))
        |> andMap (Json.Decode.field "revision_boundaries" (Json.Decode.list decodeRevisionBoundary))
        |> andMap (optional "has_historical_only_nodes" Json.Decode.bool False)


decodeWorkflow : Decoder Workflow
decodeWorkflow =
    Json.Decode.succeed Workflow
        |> andMap (Json.Decode.field "name" Json.Decode.string)
        |> andMap (optional "has_promoted_version" Json.Decode.bool False)
        |> andMap (Json.Decode.field "graph_version" Json.Decode.int)
        |> andMap (optional "content_hash" Json.Decode.string "")


decodeWindow : Decoder Window
decodeWindow =
    Json.Decode.succeed Window
        |> andMap (Json.Decode.field "kind" Json.Decode.string)
        |> andMap (Json.Decode.field "from" Json.Decode.string)
        |> andMap (Json.Decode.field "to" Json.Decode.string)
        |> andMap (optional "includes_active_before_window" Json.Decode.bool False)


decodeNodeState : Decoder NodeState
decodeNodeState =
    Json.Decode.succeed NodeState
        |> andMap (Json.Decode.field "node_id" Json.Decode.string)
        |> andMap (counter [ "active", "running" ])
        |> andMap (counter [ "active", "waiting" ])
        |> andMap (counter [ "active", "pending" ])
        |> andMap (counter [ "history", "succeeded" ])
        |> andMap (counter [ "history", "failed" ])
        |> andMap (counter [ "history", "errored" ])
        |> andMap (counter [ "history", "aborted" ])
        |> andMap (counter [ "history", "skipped" ])
        |> andMap (optional "needs_attention" Json.Decode.bool False)
        |> andMap (optional "has_window_activity" Json.Decode.bool False)


{-| A count Go omitted is zero, not an error: `ActiveCounts` and
`HistoryCounts` are plain int structs, and a future `omitempty` on any of them
must not blank the page.
-}
counter : List String -> Decoder Int
counter path =
    Json.Decode.maybe (Json.Decode.at path Json.Decode.int)
        |> Json.Decode.map (Maybe.withDefault 0)


decodeRevisionBoundary : Decoder RevisionBoundary
decodeRevisionBoundary =
    Json.Decode.succeed RevisionBoundary
        |> andMap (Json.Decode.field "version" Json.Decode.int)
        |> andMap (Json.Decode.field "promoted_at" (Json.Decode.nullable Json.Decode.string))
        |> andMap (Json.Decode.field "first_run_id" Json.Decode.string)
        |> andMap (Json.Decode.field "first_run_at" Json.Decode.string)


optional : String -> Decoder a -> a -> Decoder a
optional field decoder fallback =
    Json.Decode.maybe (Json.Decode.field field decoder)
        |> Json.Decode.map (Maybe.withDefault fallback)


{-| The renderer's `NodeState` lookup.

A node the overview says nothing about is not a healthy node: it is a node
with no data, which `AgentGraph.View.emptyState` renders as a dashed border
rather than as a check. That distinction is the whole point of
`has_window_activity`, so the fallback has to preserve it.

-}
nodeStateLookup : Overview -> (String -> AgentGraph.View.NodeState)
nodeStateLookup overview =
    let
        byId : Dict String AgentGraph.View.NodeState
        byId =
            overview.nodeState
                |> List.map
                    (\state ->
                        ( state.nodeId
                        , { running = state.running
                          , waiting = state.waiting
                          , pending = state.pending
                          , succeeded = state.succeeded
                          , failed = state.failed
                          , errored = state.errored
                          , aborted = state.aborted
                          , skipped = state.skipped
                          , needsAttention = state.needsAttention
                          , hasWindowActivity = state.hasWindowActivity
                          }
                        )
                    )
                |> Dict.fromList
    in
    \nodeId -> Dict.get nodeId byId |> Maybe.withDefault AgentGraph.View.emptyState
