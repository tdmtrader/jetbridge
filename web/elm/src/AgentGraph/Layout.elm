module AgentGraph.Layout exposing
    ( LaidOut
    , LaidOutEdge
    , LaidOutNode
    , Point
    , columnSpacing
    , layout
    , nodeHeight
    , nodeWidth
    , rankSpacing
    )

{-| Pure layered layout for the semantic workflow graph.

`layout` is a total function from a graph to geometry: no dictionary of
positions is threaded in, nothing is measured from the DOM, and nothing is
random. That is what earns the Elm-native layout over driving
`web/public/graph.mjs` through a port — the whole thing is fuzz-testable, and
the graph DOM stays inside Elm where the coupled graph-to-list interaction is
ordinary state rather than a two-way port dance.

The algorithm follows `web/public/graph.mjs`, the original Concourse pipeline
layout, so the shape reads the same way:

1.  **Forward ranking.** A node's rank is one more than the deepest rank among
    its producers (longest path, not shortest), so no edge is ever drawn right
    to left.
2.  **Backward compaction.** Every node is then pulled as far right as its
    earliest consumer allows — `graph.mjs`'s `latestPossibleRank`. Without it a
    workflow input consumed only at the end of the graph stretches one edge
    across the entire canvas.
3.  **Columns.** Within a rank, nodes are ordered by the average column of
    their producers and ties are broken by node id. The barycentre keeps
    parallel branches from crossing; the id tiebreak makes the result depend on
    the graph rather than on the order the server happened to list it in, so
    the canvas does not shuffle between polls.

Both relaxation passes are bounded by the node count. A cycle would be a
server bug, and hanging the page is not an acceptable way to report one.

-}

import AgentGraph.Model as Model
import Dict exposing (Dict)
import Set exposing (Set)


{-| Node box width. Labels longer than this truncate to a tooltip in CSS
rather than growing the box, so ranks stay aligned.
-}
nodeWidth : Float
nodeWidth =
    200


{-| Node box height: three text rows — name, state, decorations.
-}
nodeHeight : Float
nodeHeight =
    72


{-| Horizontal gap between ranks.
-}
rankSpacing : Float
rankSpacing =
    100


{-| Vertical gap between nodes sharing a rank.
-}
columnSpacing : Float
columnSpacing =
    20


type alias Point =
    { x : Float
    , y : Float
    }


type alias LaidOutNode =
    { node : Model.Node
    , rank : Int
    , column : Int
    , x : Float
    , y : Float
    }


{-| An edge with its endpoints resolved. The renderer never has to search the
node list, and an edge whose endpoints are not both present never reaches it.
-}
type alias LaidOutEdge =
    { edge : Model.Edge
    , source : Point
    , target : Point
    , path : String
    }


type alias LaidOut =
    { nodes : List LaidOutNode
    , edges : List LaidOutEdge
    , width : Float
    , height : Float
    }


layout : Model.Graph -> LaidOut
layout graph =
    let
        present : Set String
        present =
            graph.nodes |> List.map .id |> Set.fromList

        -- An edge naming a node the graph does not contain cannot be drawn,
        -- and must not be allowed to influence ranking either: a phantom
        -- producer would push a real node right for no visible reason.
        liveEdges : List Model.Edge
        liveEdges =
            graph.edges
                |> List.filter
                    (\edge -> Set.member edge.from present && Set.member edge.to present)

        rankByID : Dict String Int
        rankByID =
            assignRanks graph.nodes liveEdges

        rankOf : Model.Node -> Int
        rankOf node =
            Dict.get node.id rankByID |> Maybe.withDefault 0

        nodesByRank : Dict Int (List Model.Node)
        nodesByRank =
            graph.nodes
                |> List.foldl
                    (\node acc ->
                        Dict.update (rankOf node)
                            (Maybe.withDefault [] >> (::) node >> Just)
                            acc
                    )
                    Dict.empty

        columnByID : Dict String Int
        columnByID =
            assignColumns nodesByRank (producersByConsumer liveEdges)

        rowPitch : Float
        rowPitch =
            nodeHeight + columnSpacing

        tallestRank : Int
        tallestRank =
            Dict.values nodesByRank
                |> List.map List.length
                |> List.maximum
                |> Maybe.withDefault 0

        -- Short ranks are centred against the tallest one, which is what keeps
        -- a single upstream node visually opposite the branch it feeds.
        rankOffset : Int -> Float
        rankOffset rank =
            let
                count =
                    Dict.get rank nodesByRank |> Maybe.withDefault [] |> List.length
            in
            toFloat (tallestRank - count) / 2 * rowPitch

        laidOutNodes : List LaidOutNode
        laidOutNodes =
            graph.nodes
                |> List.map
                    (\node ->
                        let
                            rank =
                                rankOf node

                            column =
                                Dict.get node.id columnByID |> Maybe.withDefault 0
                        in
                        { node = node
                        , rank = rank
                        , column = column
                        , x = toFloat rank * (nodeWidth + rankSpacing)
                        , y = rankOffset rank + toFloat column * rowPitch
                        }
                    )
                |> List.sortBy (\item -> ( item.rank, item.column ))

        anchors : Dict String Point
        anchors =
            laidOutNodes
                |> List.map (\item -> ( item.node.id, { x = item.x, y = item.y } ))
                |> Dict.fromList

        widestRank : Int
        widestRank =
            laidOutNodes |> List.map .rank |> List.maximum |> Maybe.withDefault -1
    in
    { nodes = laidOutNodes
    , edges = liveEdges |> List.filterMap (layOutEdge anchors) |> List.sortBy edgeOrder
    , width =
        if List.isEmpty laidOutNodes then
            0

        else
            toFloat (widestRank + 1) * (nodeWidth + rankSpacing) - rankSpacing
    , height =
        if List.isEmpty laidOutNodes then
            0

        else
            toFloat tallestRank * rowPitch - columnSpacing
    }


{-| Render order is derived from the graph, never from the order the server
listed the edges in, so repainting after a poll cannot reshuffle the DOM.
-}
edgeOrder : LaidOutEdge -> ( String, String, String )
edgeOrder item =
    ( item.edge.from, item.edge.to, item.edge.portName )


layOutEdge : Dict String Point -> Model.Edge -> Maybe LaidOutEdge
layOutEdge anchors edge =
    case ( Dict.get edge.from anchors, Dict.get edge.to anchors ) of
        ( Just from, Just to ) ->
            let
                source =
                    { x = from.x + nodeWidth, y = from.y + nodeHeight / 2 }

                target =
                    { x = to.x, y = to.y + nodeHeight / 2 }
            in
            Just
                { edge = edge
                , source = source
                , target = target
                , path = bezier source target
                }

        _ ->
            Nothing


{-| A cubic bezier with horizontal control points at the midpoint, matching
`Edge.prototype.path` in `web/public/graph.mjs`: edges leave a node
horizontally and arrive horizontally, so the eye follows dataflow rather than
geometry.
-}
bezier : Point -> Point -> String
bezier source target =
    let
        controlX =
            source.x + (target.x - source.x) / 2

        point x y =
            String.fromFloat x ++ "," ++ String.fromFloat y
    in
    String.join " "
        [ "M " ++ point source.x source.y
        , "C " ++ point controlX source.y
        , point controlX target.y
        , point target.x target.y
        ]


{-| Longest-path ranking followed by as-late-as-possible compaction.

Each relaxation runs once per node, which is enough to propagate depth through
any acyclic graph and which terminates on a cyclic one instead of looping.

-}
assignRanks : List Model.Node -> List Model.Edge -> Dict String Int
assignRanks nodes liveEdges =
    let
        passes =
            List.range 1 (List.length nodes)

        initial =
            nodes |> List.map (\node -> ( node.id, 0 )) |> Dict.fromList

        consumers =
            consumersByProducer liveEdges

        forward =
            passes |> List.foldl (\_ acc -> pushConsumersRight liveEdges acc) initial
    in
    passes |> List.foldl (\_ acc -> pullProducersRight consumers acc) forward


{-| One forward pass: a consumer sits at least one rank beyond every producer.
-}
pushConsumersRight : List Model.Edge -> Dict String Int -> Dict String Int
pushConsumersRight liveEdges rankByID =
    liveEdges
        |> List.foldl
            (\edge acc ->
                let
                    from =
                        Dict.get edge.from acc |> Maybe.withDefault 0

                    to =
                        Dict.get edge.to acc |> Maybe.withDefault 0
                in
                if to < from + 1 then
                    Dict.insert edge.to (from + 1) acc

                else
                    acc
            )
            rankByID


{-| One backward pass: a producer sits immediately before its earliest
consumer.

The `max` against the node's current rank is a safety net for a cyclic graph,
where the forward pass leaves no consistent ordering to compact against. On an
acyclic graph it can never bind, because the forward pass already guarantees
every consumer is at least one rank ahead.

-}
pullProducersRight : Dict String (List String) -> Dict String Int -> Dict String Int
pullProducersRight consumers rankByID =
    Dict.foldl
        (\producer consumed acc ->
            case consumed |> List.filterMap (\id -> Dict.get id acc) |> List.minimum of
                Nothing ->
                    acc

                Just earliest ->
                    let
                        current =
                            Dict.get producer acc |> Maybe.withDefault 0
                    in
                    Dict.insert producer (max current (earliest - 1)) acc
        )
        rankByID
        consumers


{-| Columns are assigned one rank at a time, left to right, so a node is
ordered against producers that already have columns.
-}
assignColumns : Dict Int (List Model.Node) -> Dict String (List String) -> Dict String Int
assignColumns nodesByRank producers =
    Dict.keys nodesByRank
        |> List.foldl
            (\rank result ->
                Dict.get rank nodesByRank
                    |> Maybe.withDefault []
                    |> List.sortBy (\node -> ( barycentre producers result node, node.id ))
                    |> List.indexedMap Tuple.pair
                    |> List.foldl
                        (\( column, node ) acc -> Dict.insert node.id column acc)
                        result
            )
            Dict.empty


{-| The average column of the producers already placed. A node with none —
every rank-0 node, and anything left unplaceable by a cycle — sorts ahead of
the rest and falls back to the id tiebreak.
-}
barycentre : Dict String (List String) -> Dict String Int -> Model.Node -> Float
barycentre producers placed node =
    let
        columns =
            Dict.get node.id producers
                |> Maybe.withDefault []
                |> List.filterMap (\id -> Dict.get id placed)
    in
    if List.isEmpty columns then
        -1

    else
        toFloat (List.sum columns) / toFloat (List.length columns)


consumersByProducer : List Model.Edge -> Dict String (List String)
consumersByProducer =
    groupBy .from .to


producersByConsumer : List Model.Edge -> Dict String (List String)
producersByConsumer =
    groupBy .to .from


groupBy : (Model.Edge -> String) -> (Model.Edge -> String) -> List Model.Edge -> Dict String (List String)
groupBy key value =
    List.foldl
        (\edge acc ->
            Dict.update (key edge)
                (Maybe.withDefault [] >> (::) (value edge) >> Just)
                acc
        )
        Dict.empty
