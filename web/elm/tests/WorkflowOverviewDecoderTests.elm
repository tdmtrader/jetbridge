module WorkflowOverviewDecoderTests exposing (all)

import AgentGraph.View as GraphView
import Concourse.WorkflowOverview as Overview
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "workflow overview decoding"
        [ test "decodes the exact payload the Go handler marshals" <|
            \_ ->
                decode goldenResponse
                    |> Result.map
                        (\overview ->
                            { name = overview.workflow.name
                            , hasPromoted = overview.workflow.hasPromotedVersion
                            , graphVersion = overview.workflow.graphVersion
                            , contentHash = overview.workflow.contentHash
                            , windowKind = overview.window.kind
                            , includesActive = overview.window.includesActiveBeforeWindow
                            , nodeIds = List.map .id overview.graph.nodes
                            , unavailable = overview.graphUnavailable
                            , historicalOnly = overview.hasHistoricalOnlyNodes
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { name = "review-api"
                            , hasPromoted = True
                            , graphVersion = 3
                            , contentHash = "abcdef0123456789"
                            , windowKind = "7d"
                            , includesActive = True
                            , nodeIds = [ "implement" ]
                            , unavailable = False
                            , historicalOnly = True
                            }
                        )
        , test "keeps active and windowed history counts apart" <|
            \_ ->
                decode goldenResponse
                    |> Result.map .nodeState
                    |> Expect.equal
                        (Ok
                            [ { nodeId = "implement"
                              , running = 1
                              , waiting = 2
                              , pending = 3
                              , succeeded = 4
                              , failed = 5
                              , errored = 6
                              , aborted = 7
                              , skipped = 8
                              , needsAttention = True
                              , hasWindowActivity = True
                              }
                            ]
                        )
        , test "reads a durable run ID as a string, because it exceeds exact number range" <|
            \_ ->
                decode goldenResponse
                    |> Result.map (.revisionBoundaries >> List.map .firstRunId)
                    |> Expect.equal (Ok [ "9007199254740995", "9007199254740994" ])
        , test "reads a never-promoted revision boundary as an absent time, not a failure" <|
            \_ ->
                decode goldenResponse
                    |> Result.map (.revisionBoundaries >> List.map .promotedAt)
                    |> Expect.equal
                        (Ok [ Just "2026-07-30T09:00:00Z", Nothing ])
        , test "decodes the degraded shape the server sends when derivation fails" <|
            \_ ->
                decode unavailableResponse
                    |> Result.map
                        (\overview ->
                            ( overview.graphUnavailable, overview.graph )
                        )
                    |> Expect.equal (Ok ( True, { nodes = [], edges = [] } ))
        , test "a node the overview says nothing about is no-data, not healthy" <|
            \_ ->
                decode goldenResponse
                    |> Result.map (\overview -> Overview.nodeStateLookup overview "never-heard-of-it")
                    |> Expect.equal (Ok GraphView.emptyState)
        , test "a node the overview does describe reaches the renderer intact" <|
            \_ ->
                decode goldenResponse
                    |> Result.map (\overview -> Overview.nodeStateLookup overview "implement")
                    |> Expect.equal
                        (Ok
                            { running = 1
                            , waiting = 2
                            , pending = 3
                            , succeeded = 4
                            , failed = 5
                            , errored = 6
                            , aborted = 7
                            , skipped = 8
                            , needsAttention = True
                            , hasWindowActivity = True
                            }
                        )
        , test "rejects a null graph rather than rendering a silently empty canvas" <|
            \_ ->
                decode nullGraphResponse
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        ]


decode : String -> Result Json.Decode.Error Overview.Overview
decode raw =
    Json.Decode.decodeString Overview.decodeOverview raw


{-| Verbatim output of `json.Marshal` over a populated
`workflowoverview.Response`, so a field rename on either side breaks this
test rather than the page.
-}
goldenResponse : String
goldenResponse =
    """
{
  "workflow": {
    "name": "review-api",
    "has_promoted_version": true,
    "graph_version": 3,
    "content_hash": "abcdef0123456789"
  },
  "window": {
    "kind": "7d",
    "from": "2026-07-24T12:00:00Z",
    "to": "2026-07-31T12:00:00Z",
    "includes_active_before_window": true
  },
  "graph": {
    "nodes": [
      {
        "id": "implement",
        "kind": "agent",
        "display_name": "implement",
        "type_ref": "review/v1"
      }
    ],
    "edges": []
  },
  "graph_unavailable": false,
  "node_state": [
    {
      "node_id": "implement",
      "active": { "running": 1, "waiting": 2, "pending": 3 },
      "history": {
        "succeeded": 4, "failed": 5, "errored": 6, "aborted": 7, "skipped": 8
      },
      "needs_attention": true,
      "has_window_activity": true
    }
  ],
  "revision_boundaries": [
    {
      "version": 3,
      "promoted_at": "2026-07-30T09:00:00Z",
      "first_run_id": "9007199254740995",
      "first_run_at": "2026-07-30T10:00:00Z"
    },
    {
      "version": 2,
      "promoted_at": null,
      "first_run_id": "9007199254740994",
      "first_run_at": "2026-07-29T10:00:00Z"
    }
  ],
  "has_historical_only_nodes": true
}
"""


unavailableResponse : String
unavailableResponse =
    """
{
  "workflow": {
    "name": "review-api",
    "has_promoted_version": true,
    "graph_version": 3,
    "content_hash": "abcdef0123456789"
  },
  "window": {
    "kind": "7d",
    "from": "2026-07-24T12:00:00Z",
    "to": "2026-07-31T12:00:00Z",
    "includes_active_before_window": true
  },
  "graph": { "nodes": [], "edges": [] },
  "graph_unavailable": true,
  "node_state": [],
  "revision_boundaries": [],
  "has_historical_only_nodes": false
}
"""


nullGraphResponse : String
nullGraphResponse =
    """
{
  "workflow": { "name": "review-api", "graph_version": 3 },
  "window": { "kind": "7d", "from": "a", "to": "b" },
  "graph": null,
  "node_state": [],
  "revision_boundaries": []
}
"""
