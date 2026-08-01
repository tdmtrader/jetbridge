module AgentGraph.Model exposing
    ( Decoration(..)
    , Edge
    , Graph
    , Node
    , NodeKind(..)
    , decorationName
    , findNode
    , isEndpoint
    , isExecution
    , kindName
    )

{-| The semantic workflow graph, shared by the workflow overview and the
individual run page. Prompts, task configs, and broker profiles are absent by
construction: the server never sends them, and these types could not hold them
if it did.

This mirrors `agent/workflow/graph/graph.go` exactly. It carries no field the
server cannot populate — in particular no reusable-node provenance, which
`node_reference.go` expands away before compilation and which the Go contract
therefore deliberately omits.

-}


{-| The eight node kinds `graph.NodeKind` can carry.

They divide into two classes with different identity rules, which
`isExecution` and `isEndpoint` answer:

  - **execution** nodes (load, agent, task, await, publish) actually run, are
    the only kinds `agent_workflow_run_node_occurrences` stores, and carry
    bare workflow-local ids;
  - **endpoint** nodes (input, resource\_source, output) exist to make dataflow
    legible, never execute, and carry kind-qualified ids (`input:x`,
    `output:x`, `source:x`) so a public output port named after an agent
    cannot collide with it.

-}
type NodeKind
    = Input
    | ResourceSource
    | Load
    | Agent
    | Task
    | Await
    | Publish
    | Output


{-| Control machinery that decorates a node instead of becoming one.
-}
type Decoration
    = Retry
    | Timeout
    | Try
    | Ensure
    | OnFailure
    | OnError
    | OnAbort
    | OnSuccess


type alias Node =
    { id : String
    , kind : NodeKind
    , displayName : String
    , typeRef : String

    -- optional means this node's production is not guaranteed: an optional
    -- port, an optional load_snapshot, or an await the server may prove
    -- unnecessary. It is not an error state and must never render as one.
    , optional : Bool
    , decorations : List Decoration
    }


type alias Edge =
    { from : String
    , to : String
    , portName : String
    , typeRef : String

    -- optional means this specific dataflow may not occur in a given run.
    -- An edge that carried nothing is a legitimate run, not a broken one.
    , optional : Bool
    }


type alias Graph =
    { nodes : List Node
    , edges : List Edge
    }


findNode : String -> Graph -> Maybe Node
findNode id graph =
    graph.nodes
        |> List.filter (\node -> node.id == id)
        |> List.head


{-| True for the kinds that actually run and can therefore carry a durable
occurrence.
-}
isExecution : NodeKind -> Bool
isExecution kind =
    case kind of
        Load ->
            True

        Agent ->
            True

        Task ->
            True

        Await ->
            True

        Publish ->
            True

        Input ->
            False

        ResourceSource ->
            False

        Output ->
            False


{-| True for the kinds that exist only to make dataflow legible. Their ids are
kind-qualified and they never carry state.
-}
isEndpoint : NodeKind -> Bool
isEndpoint kind =
    not (isExecution kind)


{-| The wire name of a kind. Kept beside the type so the decoder, the view, and
the tests all agree on one spelling.
-}
kindName : NodeKind -> String
kindName kind =
    case kind of
        Input ->
            "input"

        ResourceSource ->
            "resource_source"

        Load ->
            "load"

        Agent ->
            "agent"

        Task ->
            "task"

        Await ->
            "await"

        Publish ->
            "publish"

        Output ->
            "output"


{-| The wire name of a decoration, which is also what the node badge shows.
-}
decorationName : Decoration -> String
decorationName decoration =
    case decoration of
        Retry ->
            "retry"

        Timeout ->
            "timeout"

        Try ->
            "try"

        Ensure ->
            "ensure"

        OnFailure ->
            "on_failure"

        OnError ->
            "on_error"

        OnAbort ->
            "on_abort"

        OnSuccess ->
            "on_success"
