module AgentGraphLayoutTests exposing (all)

import AgentGraph.Decoder as Decoder
import AgentGraph.Layout as Layout
import AgentGraph.Model as Model
import AgentGraphFixtures as Fixtures
import Expect
import Fuzz exposing (Fuzzer)
import Json.Decode
import Set
import Test exposing (Test, describe, fuzz, test)


all : Test
all =
    describe "agent graph layout"
        [ describe "ranks" ranks
        , describe "columns" columns
        , describe "geometry" geometry
        , describe "edges" edges
        , describe "degenerate graphs" degenerate
        , describe "the real serialized seed graphs" realGraphs
        , describe "invariants" invariants
        ]



-- ranks ----------------------------------------------------------------------


ranks : List Test
ranks =
    [ test "a chain's source sits at rank 0" <|
        \_ ->
            rankOf "repository" chain
                |> Expect.equal (Just 0)
    , test "ranks a consumer after its producer" <|
        \_ ->
            Layout.layout chain
                |> .nodes
                |> List.map (\n -> ( n.node.id, n.rank ))
                |> List.sortBy Tuple.first
                |> Expect.equal
                    [ ( "change", 2 ), ( "implement", 1 ), ( "repository", 0 ) ]
    , test "takes the longest path when a node has producers at different depths" <|
        \_ ->
            -- `sink` is reachable in one hop from `root` and in two through
            -- `middle`. Ranking it at 1 would draw an edge backwards.
            rankOf "sink" longestPath
                |> Expect.equal (Just 2)
    , test "pulls a source as far right as its earliest consumer allows" <|
        \_ ->
            -- `late` feeds only a rank-2 consumer, so leaving it at rank 0
            -- would stretch one edge across the whole canvas. This is the
            -- backward pass web/public/graph.mjs calls latestPossibleRank.
            rankOf "late" asLateAsPossible
                |> Expect.equal (Just 1)
    , test "the backward pass never pulls a source past its earliest consumer" <|
        \_ ->
            -- `early` feeds a rank-1 consumer as well as the rank-2 one, so it
            -- must stay at rank 0.
            rankOf "early" asLateAsPossible
                |> Expect.equal (Just 0)
    , test "a sink keeps the rank its producers give it" <|
        \_ ->
            [ rankOf "shallow-sink" mixedSinks, rankOf "deep-sink" mixedSinks ]
                |> Expect.equal [ Just 1, Just 3 ]
    ]



-- columns --------------------------------------------------------------------


columns : List Test
columns =
    [ test "places parallel siblings at the same rank in different columns" <|
        \_ ->
            let
                siblings =
                    Layout.layout parallel
                        |> .nodes
                        |> List.filter (\n -> List.member n.node.id [ "left", "right" ])
            in
            Expect.all
                [ \s -> List.map .rank s |> Expect.equal [ 1, 1 ]
                , \s -> List.map .column s |> Set.fromList |> Set.size |> Expect.equal 2
                ]
                siblings
    , test "columns within a rank start at 0 and are contiguous" <|
        \_ ->
            Layout.layout parallel
                |> .nodes
                |> List.filter (\n -> n.rank == 1)
                |> List.map .column
                |> List.sort
                |> Expect.equal [ 0, 1 ]
    , test "orders a rank by its producers' columns, not by node id" <|
        \_ ->
            -- `zulu` is produced by the top-most rank-0 node and `alpha` by the
            -- bottom-most, so alphabetical column order would cross both edges.
            Layout.layout crossing
                |> .nodes
                |> List.filter (\n -> n.rank == 1)
                |> List.sortBy .column
                |> List.map (.node >> .id)
                |> Expect.equal [ "zulu", "alpha" ]
    , test "breaks a tie inside a rank by node id, so the result cannot drift" <|
        \_ ->
            Layout.layout parallel
                |> .nodes
                |> List.filter (\n -> n.rank == 1)
                |> List.sortBy .column
                |> List.map (.node >> .id)
                |> Expect.equal [ "left", "right" ]
    , test "orders a rank by the average producer column, not the total" <|
        \_ ->
            -- `x` is fed from columns 0 and 3 (average 1.5) and `y` from
            -- column 2 (average 2), so `x` sits above `y`. Summing instead of
            -- averaging would give x=3 and y=2 and flip them, pulling a node
            -- down purely for having more producers.
            Layout.layout weightedBarycentre
                |> .nodes
                |> List.filter (\n -> n.rank == 1)
                |> List.sortBy .column
                |> List.map (.node >> .id)
                |> Expect.equal [ "x", "y" ]
    , test "a node with no producers sorts ahead of the producer-anchored nodes in its rank" <|
        \_ ->
            -- `late` was pulled to rank 1 by the backward pass and has no
            -- producer to be ordered against; `middle` does. The unanchored
            -- node takes the first column so the anchored ones stay lined up
            -- with the branch that feeds them.
            Layout.layout asLateAsPossible
                |> .nodes
                |> List.filter (\n -> n.rank == 1)
                |> List.sortBy .column
                |> List.map (.node >> .id)
                |> Expect.equal [ "late", "middle" ]
    ]



-- geometry -------------------------------------------------------------------


geometry : List Test
geometry =
    [ test "x is a function of rank alone" <|
        \_ ->
            Layout.layout parallel
                |> .nodes
                |> List.map (\n -> ( n.rank, n.x ))
                |> Set.fromList
                |> Set.size
                |> Expect.equal 2
    , test "x advances by one node plus one gap per rank" <|
        \_ ->
            Layout.layout chain
                |> .nodes
                |> List.sortBy .rank
                |> List.map .x
                |> Expect.equal
                    [ 0
                    , Layout.nodeWidth + Layout.rankSpacing
                    , 2 * (Layout.nodeWidth + Layout.rankSpacing)
                    ]
    , test "centres a short rank against the tallest one" <|
        \_ ->
            -- `root` is alone at rank 0 while rank 1 holds two nodes, so root
            -- sits half a row down rather than pinned to the top edge.
            Layout.layout parallel
                |> .nodes
                |> List.filter (\n -> n.node.id == "repository")
                |> List.map .y
                |> Expect.equal [ (Layout.nodeHeight + Layout.columnSpacing) / 2 ]
    , test "the reported extent covers every node" <|
        \_ ->
            let
                laid =
                    Layout.layout parallel
            in
            Expect.all
                [ \l -> List.all (\n -> n.x + Layout.nodeWidth <= l.width) l.nodes |> Expect.equal True
                , \l -> List.all (\n -> n.y + Layout.nodeHeight <= l.height) l.nodes |> Expect.equal True
                ]
                laid
    , test "the reported extent is not slack" <|
        \_ ->
            let
                laid =
                    Layout.layout parallel
            in
            Expect.all
                [ \l -> List.map (\n -> n.x + Layout.nodeWidth) l.nodes |> List.maximum |> Expect.equal (Just l.width)
                , \l -> List.map (\n -> n.y + Layout.nodeHeight) l.nodes |> List.maximum |> Expect.equal (Just l.height)
                ]
                laid
    ]



-- edges ----------------------------------------------------------------------


edges : List Test
edges =
    [ test "attaches an edge to the right side of its producer and the left side of its consumer" <|
        \_ ->
            Layout.layout chain
                |> .edges
                |> List.filter (\e -> e.edge.from == "repository")
                |> List.map (\e -> ( e.source, e.target ))
                |> Expect.equal
                    [ ( { x = Layout.nodeWidth, y = Layout.nodeHeight / 2 }
                      , { x = Layout.nodeWidth + Layout.rankSpacing, y = Layout.nodeHeight / 2 }
                      )
                    ]
    , test "the path is a cubic bezier with horizontal control points" <|
        \_ ->
            Layout.layout chain
                |> .edges
                |> List.filter (\e -> e.edge.from == "repository")
                |> List.map .path
                |> Expect.equal [ "M 200,36 C 250,36 250,36 300,36" ]
    , test "the bezier leaves horizontally and arrives horizontally when the ranks differ in height" <|
        \_ ->
            -- The first control point must share the source's y and the second
            -- the target's. On a level edge both are the same number and the
            -- distinction is invisible, so this asserts on a sloped one.
            Layout.layout parallel
                |> .edges
                |> List.filter (\e -> e.edge.to == "left")
                |> List.map .path
                |> Expect.equal [ "M 200,82 C 250,82 250,36 300,36" ]
    , test "carries the edge itself through, so optionality survives layout" <|
        \_ ->
            Layout.layout optionalEdge
                |> .edges
                |> List.map (.edge >> .optional)
                |> Expect.equal [ True ]
    , test "drops an edge whose producer is not in the graph" <|
        \_ ->
            Layout.layout danglingFrom
                |> .edges
                |> Expect.equal []
    , test "drops an edge whose consumer is not in the graph" <|
        \_ ->
            Layout.layout danglingTo
                |> .edges
                |> Expect.equal []
    , test "an edge from a missing producer does not push its consumer right" <|
        \_ ->
            -- A phantom producer must not rank `b` at 1: the node would drift
            -- a whole column away from an edge nobody can see.
            Layout.layout danglingFrom
                |> .nodes
                |> List.map (\n -> ( n.node.id, n.rank ))
                |> Expect.equal [ ( "b", 0 ) ]
    , test "an edge to a missing consumer does not disturb the ranks that remain" <|
        \_ ->
            Layout.layout danglingTo
                |> .nodes
                |> List.map (\n -> ( n.node.id, n.rank ))
                |> Expect.equal [ ( "a", 0 ) ]
    , test "an edge to a missing consumer does not block a node from compacting right" <|
        \_ ->
            -- `a`'s only real consumer is at rank 2, so `a` belongs at rank 1.
            -- A phantom consumer left in the graph would look like an earlier
            -- one and pin `a` back at rank 0 for no visible reason.
            rankOf "a" danglingBlocksCompaction
                |> Expect.equal (Just 1)
    ]



-- degenerate graphs ----------------------------------------------------------


degenerate : List Test
degenerate =
    [ test "an empty graph lays out to nothing with no extent" <|
        \_ ->
            Layout.layout { nodes = [], edges = [] }
                |> Expect.equal { nodes = [], edges = [], width = 0, height = 0 }
    , test "an isolated node still gets a position" <|
        \_ ->
            Layout.layout { nodes = [ agentNode "alone" ], edges = [] }
                |> .nodes
                |> List.map (\n -> ( n.rank, n.column, ( n.x, n.y ) ))
                |> Expect.equal [ ( 0, 0, ( 0, 0 ) ) ]
    , test "a cycle terminates and still places every node" <|
        \_ ->
            -- A cycle would be a server bug. Hanging the page is not an
            -- acceptable way to report one.
            Layout.layout cycle
                |> .nodes
                |> List.map (.node >> .id)
                |> List.sort
                |> Expect.equal [ "a", "b", "c" ]
    , test "a cycle never drags a node left of where the forward pass put it" <|
        \_ ->
            -- The backward pass compacts producers rightwards. On a cycle
            -- there is no consistent ordering to compact against, so without
            -- the guard against lowering a rank it walks nodes left on every
            -- pass and off the canvas into negative coordinates.
            Layout.layout twoCycle
                |> .nodes
                |> List.filter (\n -> n.x < 0 || n.y < 0)
                |> List.map (.node >> .id)
                |> Expect.equal []
    , test "a self edge terminates and still places the node" <|
        \_ ->
            Layout.layout selfEdge
                |> .nodes
                |> List.length
                |> Expect.equal 1
    ]



-- the real serialized seed graphs --------------------------------------------


realGraphs : List Test
realGraphs =
    [ test "places every node of every shipped seed graph exactly once" <|
        \_ ->
            seedGraphs
                |> List.map
                    (\( name, graph ) ->
                        ( name
                        , List.length (Layout.layout graph).nodes
                        )
                    )
                |> Expect.equal
                    [ ( "anonymization-audit-v3.json", 5 )
                    , ( "code-review-v3.json", 4 )
                    , ( "log-diagnosis-v3.json", 4 )
                    , ( "measure-review-v3.json", 3 )
                    , ( "merge-delivery-v3.json", 10 )
                    , ( "small-fix-v3.json", 9 )
                    , ( "version-upgrade-v3.json", 9 )
                    ]
    , test "keeps every edge of every shipped seed graph" <|
        \_ ->
            seedGraphs
                |> List.map (\( name, graph ) -> ( name, List.length (Layout.layout graph).edges ))
                |> Expect.equal
                    [ ( "anonymization-audit-v3.json", 4 )
                    , ( "code-review-v3.json", 3 )
                    , ( "log-diagnosis-v3.json", 3 )
                    , ( "measure-review-v3.json", 2 )
                    , ( "merge-delivery-v3.json", 15 )
                    , ( "small-fix-v3.json", 12 )
                    , ( "version-upgrade-v3.json", 13 )
                    ]
    , test "every seed edge runs strictly left to right" <|
        \_ ->
            seedGraphs
                |> List.concatMap (\( name, graph ) -> backwardEdges name graph)
                |> Expect.equal []
    , test "no two nodes of a seed graph share a position" <|
        \_ ->
            seedGraphs
                |> List.filterMap
                    (\( name, graph ) ->
                        let
                            laid =
                                (Layout.layout graph).nodes

                            positions =
                                List.map (\n -> ( n.x, n.y )) laid
                        in
                        if Set.size (Set.fromList positions) == List.length laid then
                            Nothing

                        else
                            Just name
                    )
                |> Expect.equal []
    , test "a seed graph lays out identically however its nodes and edges are ordered" <|
        \_ ->
            seedGraphs
                |> List.filterMap
                    (\( name, graph ) ->
                        if Layout.layout graph == Layout.layout (reverseOrder graph) then
                            Nothing

                        else
                            Just name
                    )
                |> Expect.equal []
    ]


seedGraphs : List ( String, Model.Graph )
seedGraphs =
    Fixtures.all
        |> List.filterMap
            (\( name, json ) ->
                Json.Decode.decodeString Decoder.graph json
                    |> Result.toMaybe
                    |> Maybe.map (Tuple.pair name)
            )



-- invariants -----------------------------------------------------------------


invariants : List Test
invariants =
    [ fuzz dagFuzzer "places every node exactly once" <|
        \graph ->
            (Layout.layout graph).nodes
                |> List.map (.node >> .id)
                |> List.sort
                |> Expect.equal (List.map .id graph.nodes |> List.sort)
    , fuzz dagFuzzer "never puts two nodes in one rank-and-column slot" <|
        \graph ->
            let
                slots =
                    (Layout.layout graph).nodes |> List.map (\n -> ( n.rank, n.column ))
            in
            Set.size (Set.fromList slots)
                |> Expect.equal (List.length slots)
    , fuzz dagFuzzer "never draws two nodes at one pixel position" <|
        \graph ->
            let
                positions =
                    (Layout.layout graph).nodes |> List.map (\n -> ( n.x, n.y ))
            in
            Set.size (Set.fromList positions)
                |> Expect.equal (List.length positions)
    , fuzz dagFuzzer "ranks every consumer strictly after its producer" <|
        \graph ->
            backwardEdges "fuzzed" graph
                |> Expect.equal []
    , fuzz dagFuzzer "is deterministic" <|
        \graph ->
            Layout.layout graph
                |> Expect.equal (Layout.layout graph)
    , fuzz (Fuzz.pair dagFuzzer (Fuzz.intRange 0 6)) "does not depend on the order of the node and edge lists" <|
        \( graph, rotation ) ->
            Layout.layout graph
                |> Expect.equal (Layout.layout (rotate rotation graph))
    , fuzz dagFuzzer "gives every node a non-negative position inside the reported extent" <|
        \graph ->
            let
                laid =
                    Layout.layout graph
            in
            laid.nodes
                |> List.filter
                    (\n ->
                        (n.x < 0)
                            || (n.y < 0)
                            || (n.x + Layout.nodeWidth > laid.width)
                            || (n.y + Layout.nodeHeight > laid.height)
                    )
                |> List.map (.node >> .id)
                |> Expect.equal []
    , fuzz dagFuzzer "keeps every edge whose endpoints are both present" <|
        \graph ->
            List.length (Layout.layout graph).edges
                |> Expect.equal (List.length graph.edges)
    ]


{-| Every edge that would be drawn right to left, which is a ranking bug.
-}
backwardEdges : String -> Model.Graph -> List ( String, String )
backwardEdges label graph =
    let
        laid =
            Layout.layout graph

        rankById id =
            laid.nodes
                |> List.filter (\n -> n.node.id == id)
                |> List.head
                |> Maybe.map .rank
    in
    graph.edges
        |> List.filterMap
            (\edge ->
                case ( rankById edge.from, rankById edge.to ) of
                    ( Just from, Just to ) ->
                        if from < to then
                            Nothing

                        else
                            Just ( label, edge.from ++ " -> " ++ edge.to )

                    _ ->
                        Nothing
            )



-- fuzzers --------------------------------------------------------------------


{-| A random directed acyclic graph. Edges only ever run from a lower index to
a higher one, which makes acyclicity structural rather than something the
fuzzer has to be lucky about.
-}
dagFuzzer : Fuzzer Model.Graph
dagFuzzer =
    Fuzz.intRange 1 9
        |> Fuzz.andThen
            (\size ->
                let
                    candidates =
                        List.range 0 (size - 1)
                            |> List.concatMap
                                (\from ->
                                    List.range (from + 1) (size - 1)
                                        |> List.map (Tuple.pair from)
                                )
                in
                Fuzz.listOfLength (List.length candidates) Fuzz.bool
                    |> Fuzz.map
                        (\keep ->
                            { nodes = List.range 0 (size - 1) |> List.map (\i -> agentNode ("n" ++ String.fromInt i))
                            , edges =
                                List.map2 Tuple.pair candidates keep
                                    |> List.filter Tuple.second
                                    |> List.map
                                        (\( ( from, to ), _ ) ->
                                            { from = "n" ++ String.fromInt from
                                            , to = "n" ++ String.fromInt to
                                            , portName = "p"
                                            , typeRef = ""
                                            , optional = False
                                            }
                                        )
                            }
                        )
            )


rotate : Int -> Model.Graph -> Model.Graph
rotate n graph =
    { nodes = rotateList n graph.nodes
    , edges = rotateList n graph.edges
    }


rotateList : Int -> List a -> List a
rotateList n list =
    List.drop n list ++ List.take n list


reverseOrder : Model.Graph -> Model.Graph
reverseOrder graph =
    { nodes = List.reverse graph.nodes, edges = List.reverse graph.edges }



-- fixtures -------------------------------------------------------------------


rankOf : String -> Model.Graph -> Maybe Int
rankOf id graph =
    Layout.layout graph
        |> .nodes
        |> List.filter (\n -> n.node.id == id)
        |> List.head
        |> Maybe.map .rank


agentNode : String -> Model.Node
agentNode id =
    { id = id
    , kind = Model.Agent
    , displayName = id
    , typeRef = ""
    , optional = False
    , decorations = []
    }


link : String -> String -> Model.Edge
link from to =
    { from = from, to = to, portName = "p", typeRef = "", optional = False }


chain : Model.Graph
chain =
    { nodes =
        [ { id = "repository", kind = Model.Input, displayName = "repository", typeRef = "repository/v1", optional = False, decorations = [] }
        , agentNode "implement"
        , { id = "change", kind = Model.Output, displayName = "change", typeRef = "repository-change/v1", optional = False, decorations = [] }
        ]
    , edges = [ link "repository" "implement", link "implement" "change" ]
    }


{-| `right` is listed before `left` on purpose: a tiebreak that fell back to
list order instead of node id would still look correct if the fixture happened
to be alphabetical.
-}
parallel : Model.Graph
parallel =
    { nodes =
        [ { id = "repository", kind = Model.Input, displayName = "repository", typeRef = "", optional = False, decorations = [] }
        , agentNode "right"
        , agentNode "left"
        ]
    , edges = [ link "repository" "right", link "repository" "left" ]
    }


longestPath : Model.Graph
longestPath =
    { nodes = [ agentNode "root", agentNode "middle", agentNode "sink" ]
    , edges = [ link "root" "middle", link "middle" "sink", link "root" "sink" ]
    }


asLateAsPossible : Model.Graph
asLateAsPossible =
    { nodes = [ agentNode "early", agentNode "late", agentNode "middle", agentNode "consumer" ]
    , edges =
        [ link "early" "middle"
        , link "middle" "consumer"
        , link "early" "consumer"
        , link "late" "consumer"
        ]
    }


mixedSinks : Model.Graph
mixedSinks =
    { nodes =
        [ agentNode "root"
        , agentNode "shallow-sink"
        , agentNode "one"
        , agentNode "two"
        , agentNode "deep-sink"
        ]
    , edges =
        [ link "root" "shallow-sink"
        , link "root" "one"
        , link "one" "two"
        , link "two" "deep-sink"
        ]
    }


crossing : Model.Graph
crossing =
    { nodes = [ agentNode "aaa", agentNode "bbb", agentNode "zulu", agentNode "alpha" ]
    , edges = [ link "aaa" "zulu", link "bbb" "alpha" ]
    }


optionalEdge : Model.Graph
optionalEdge =
    { nodes = [ agentNode "a", agentNode "b" ]
    , edges = [ { from = "a", to = "b", portName = "p", typeRef = "", optional = True } ]
    }


danglingFrom : Model.Graph
danglingFrom =
    { nodes = [ agentNode "b" ], edges = [ link "gone" "b" ] }


danglingTo : Model.Graph
danglingTo =
    { nodes = [ agentNode "a" ], edges = [ link "a" "gone" ] }


danglingBlocksCompaction : Model.Graph
danglingBlocksCompaction =
    { nodes = List.map agentNode [ "a", "m1", "m2", "target" ]
    , edges =
        [ link "a" "gone"
        , link "a" "target"
        , link "m1" "m2"
        , link "m2" "target"
        ]
    }


cycle : Model.Graph
cycle =
    { nodes = [ agentNode "a", agentNode "b", agentNode "c" ]
    , edges = [ link "a" "b", link "b" "c", link "c" "a" ]
    }


twoCycle : Model.Graph
twoCycle =
    { nodes = [ agentNode "a", agentNode "b" ]
    , edges = [ link "a" "b", link "b" "a" ]
    }


selfEdge : Model.Graph
selfEdge =
    { nodes = [ agentNode "a" ], edges = [ link "a" "a" ] }


weightedBarycentre : Model.Graph
weightedBarycentre =
    { nodes = List.map agentNode [ "a0", "a1", "a2", "a3", "x", "y" ]
    , edges = [ link "a0" "x", link "a3" "x", link "a2" "y" ]
    }
