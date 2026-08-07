module AgentGraphDecoderTests exposing (all)

import AgentGraph.Decoder as Decoder
import AgentGraph.Model as Model
import AgentGraphFixtures as Fixtures
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "agent graph decoder"
        [ describe "hand-written payloads" handWritten
        , describe "the real serialized seed graphs" realGraphs
        , describe "strictness" strictness
        , describe "model helpers" modelHelpers
        ]



-- hand-written payloads ------------------------------------------------------


handWritten : List Test
handWritten =
    [ test "decodes nodes, kinds, decorations, and edges" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph payload
                |> Expect.equal
                    (Ok
                        { nodes =
                            [ { id = "input:repository"
                              , kind = Model.Input
                              , displayName = "repository"
                              , typeRef = "repository/v1"
                              , optional = False
                              , decorations = []
                              }
                            , { id = "implement"
                              , kind = Model.Agent
                              , displayName = "implement"
                              , typeRef = ""
                              , optional = False
                              , decorations = [ Model.Retry, Model.Timeout ]
                              }
                            ]
                        , edges =
                            [ { from = "input:repository"
                              , to = "implement"
                              , portName = "repository"
                              , typeRef = "repository/v1"
                              , optional = False
                              }
                            ]
                        }
                    )
    , test "absent type_ref decodes to the empty string" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x"}"""
                |> Result.map .typeRef
                |> Expect.equal (Ok "")
    , test "absent decorations decodes to the empty list" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x"}"""
                |> Result.map .decorations
                |> Expect.equal (Ok [])
    , test "absent optional decodes to False on a node" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x"}"""
                |> Result.map .optional
                |> Expect.equal (Ok False)
    , test "present optional decodes to True on a node" <|
        \_ ->
            decodeNode """{"id":"x","kind":"output","display_name":"x","optional":true}"""
                |> Result.map .optional
                |> Expect.equal (Ok True)
    , test "absent optional decodes to False on an edge" <|
        \_ ->
            decodeEdge """{"from":"a","to":"b","port_name":"p"}"""
                |> Result.map .optional
                |> Expect.equal (Ok False)
    , test "present optional decodes to True on an edge" <|
        \_ ->
            decodeEdge """{"from":"a","to":"b","port_name":"p","optional":true}"""
                |> Result.map .optional
                |> Expect.equal (Ok True)
    , test "decodes every node kind the server can emit" <|
        \_ ->
            [ "input", "resource_source", "load", "agent", "task", "await", "publish", "output" ]
                |> List.map
                    (\raw ->
                        decodeNode
                            ("""{"id":"x","kind":\"""" ++ raw ++ """","display_name":"x"}""")
                            |> Result.map (.kind >> Model.kindName)
                    )
                |> Expect.equal
                    ([ "input", "resource_source", "load", "agent", "task", "await", "publish", "output" ]
                        |> List.map Ok
                    )
    , test "decodes every decoration the server can emit" <|
        \_ ->
            let
                names =
                    [ "retry", "timeout", "try", "ensure", "on_failure", "on_error", "on_abort", "on_success" ]
            in
            decodeNode
                ("""{"id":"x","kind":"agent","display_name":"x","decorations":["""
                    ++ String.join "," (List.map (\n -> "\"" ++ n ++ "\"") names)
                    ++ "]}"
                )
                |> Result.map (.decorations >> List.map Model.decorationName)
                |> Expect.equal (Ok names)
    , test "empty node and edge arrays decode to an empty graph" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph """{"nodes":[],"edges":[]}"""
                |> Expect.equal (Ok { nodes = [], edges = [] })
    ]


payload : String
payload =
    """
    { "nodes":
        [ {"id":"input:repository","kind":"input","display_name":"repository","type_ref":"repository/v1"}
        , {"id":"implement","kind":"agent","display_name":"implement","decorations":["retry","timeout"]}
        ]
    , "edges":
        [ {"from":"input:repository","to":"implement","port_name":"repository","type_ref":"repository/v1"}
        ]
    }
    """



-- the real serialized seed graphs --------------------------------------------


realGraphs : List Test
realGraphs =
    [ test "every shipped seed graph decodes" <|
        \_ ->
            Fixtures.all
                |> List.map (\( name, json ) -> ( name, decodeOk json ))
                |> List.filter (\( _, ok ) -> not ok)
                |> Expect.equal []
    , test "decodes the exact node and edge counts the server serialized" <|
        \_ ->
            Fixtures.all
                |> List.map
                    (\( name, json ) ->
                        ( name
                        , Json.Decode.decodeString Decoder.graph json
                            |> Result.map (\g -> ( List.length g.nodes, List.length g.edges ))
                            |> Result.withDefault ( -1, -1 )
                        )
                    )
                |> Expect.equal
                    [ ( "anonymization-audit-v3.json", ( 5, 4 ) )
                    , ( "code-review-v3.json", ( 4, 3 ) )
                    , ( "log-diagnosis-v3.json", ( 4, 3 ) )
                    , ( "measure-review-v3.json", ( 3, 2 ) )
                    , ( "merge-delivery-v3.json", ( 10, 15 ) )
                    , ( "small-fix-v3.json", ( 9, 12 ) )
                    , ( "version-upgrade-v3.json", ( 9, 13 ) )
                    ]
    , test "decodes code-review-v3 structurally, field for field" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph Fixtures.codeReviewV3
                |> Expect.equal
                    (Ok
                        { nodes =
                            [ { id = "input:before", kind = Model.Input, displayName = "before", typeRef = "repository/v1", optional = False, decorations = [] }
                            , { id = "input:after", kind = Model.Input, displayName = "after", typeRef = "repository/v1", optional = False, decorations = [] }
                            , { id = "review", kind = Model.Agent, displayName = "review", typeRef = "", optional = False, decorations = [] }
                            , { id = "output:review", kind = Model.Output, displayName = "review", typeRef = "review/v1", optional = False, decorations = [] }
                            ]
                        , edges =
                            [ { from = "input:after", to = "review", portName = "after", typeRef = "repository/v1", optional = False }
                            , { from = "input:before", to = "review", portName = "before", typeRef = "repository/v1", optional = False }
                            , { from = "review", to = "output:review", portName = "review", typeRef = "review/v1", optional = False }
                            ]
                        }
                    )
    , test "an endpoint node id may repeat an execution node id without colliding" <|
        \_ ->
            -- code-review-v3 declares output port `review` alongside agent
            -- function_id `review`. Kind qualification is what keeps both.
            decodeGraph Fixtures.codeReviewV3
                |> Result.map (.nodes >> List.map .id >> List.filter (String.endsWith "review"))
                |> Expect.equal (Ok [ "review", "output:review" ])
    , test "carries the optional flag through from the real payload" <|
        \_ ->
            decodeGraph Fixtures.anonymizationAuditV3
                |> Result.map (.nodes >> List.filter .optional >> List.map .id)
                |> Expect.equal (Ok [ "output:change" ])
    , test "carries the optional edge flag through from the real payload" <|
        \_ ->
            decodeGraph Fixtures.anonymizationAuditV3
                |> Result.map (.edges >> List.filter .optional >> List.map .portName)
                |> Expect.equal (Ok [ "change" ])
    , test "counts optional nodes and edges across every seed graph" <|
        \_ ->
            Fixtures.all
                |> List.map
                    (\( name, json ) ->
                        ( name
                        , decodeGraph json
                            |> Result.map
                                (\g ->
                                    ( List.length (List.filter .optional g.nodes)
                                    , List.length (List.filter .optional g.edges)
                                    )
                                )
                            |> Result.withDefault ( -1, -1 )
                        )
                    )
                |> Expect.equal
                    [ ( "anonymization-audit-v3.json", ( 1, 1 ) )
                    , ( "code-review-v3.json", ( 0, 0 ) )
                    , ( "log-diagnosis-v3.json", ( 1, 1 ) )
                    , ( "measure-review-v3.json", ( 0, 0 ) )
                    , ( "merge-delivery-v3.json", ( 0, 0 ) )
                    , ( "small-fix-v3.json", ( 0, 0 ) )
                    , ( "version-upgrade-v3.json", ( 0, 0 ) )
                    ]
    , test "decodes the decorations the seeds actually carry" <|
        \_ ->
            Fixtures.all
                |> List.concatMap
                    (\( _, json ) ->
                        decodeGraph json
                            |> Result.map (.nodes >> List.concatMap .decorations)
                            |> Result.withDefault []
                    )
                |> List.map Model.decorationName
                |> Expect.equal [ "timeout", "timeout", "timeout" ]
    , test "every edge endpoint resolves to a node of the same graph" <|
        \_ ->
            Fixtures.all
                |> List.concatMap
                    (\( name, json ) ->
                        decodeGraph json
                            |> Result.map
                                (\g ->
                                    g.edges
                                        |> List.concatMap (\e -> [ e.from, e.to ])
                                        |> List.filter (\id -> Model.findNode id g == Nothing)
                                        |> List.map (\id -> ( name, id ))
                                )
                            |> Result.withDefault [ ( name, "DECODE FAILED" ) ]
                    )
                |> Expect.equal []
    , test "every endpoint node id is kind qualified and every execution node id is bare" <|
        \_ ->
            Fixtures.all
                |> List.concatMap
                    (\( name, json ) ->
                        decodeGraph json
                            |> Result.map
                                (\g ->
                                    g.nodes
                                        |> List.filter
                                            (\n ->
                                                Model.isEndpoint n.kind
                                                    /= String.contains ":" n.id
                                            )
                                        |> List.map (\n -> ( name, n.id ))
                                )
                            |> Result.withDefault [ ( name, "DECODE FAILED" ) ]
                    )
                |> Expect.equal []
    ]



-- strictness -----------------------------------------------------------------


strictness : List Test
strictness =
    [ test "fails on an unknown node kind rather than silently dropping the node" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph unknownKindPayload
                |> Expect.err
    , test "names the unknown kind in the failure message itself" <|
        \_ ->
            -- errorToString echoes the offending JSON value, so asserting only
            -- on "prompt" would pass even for a message that dropped it.
            Json.Decode.decodeString Decoder.graph unknownKindPayload
                |> Result.mapError
                    (Json.Decode.errorToString >> String.contains "unknown node kind: prompt")
                |> Expect.equal (Err True)
    , test "names the unknown decoration in the failure message itself" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x","decorations":["across"]}"""
                |> Result.mapError
                    (Json.Decode.errorToString >> String.contains "unknown decoration: across")
                |> Expect.equal (Err True)
    , test "fails on an unknown decoration rather than silently dropping it" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x","decorations":["retry","across"]}"""
                |> Expect.err
    , test "fails when nodes is missing, because the contract always sends it" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph """{"edges":[]}"""
                |> Expect.err
    , test "fails when edges is missing, because the contract always sends it" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph """{"nodes":[]}"""
                |> Expect.err
    , test "fails on null nodes" <|
        \_ ->
            Json.Decode.decodeString Decoder.graph """{"nodes":null,"edges":[]}"""
                |> Expect.err
    , test "fails when a node has no id" <|
        \_ ->
            decodeNode """{"kind":"agent","display_name":"x"}"""
                |> Expect.err
    , test "fails when a node has no kind" <|
        \_ ->
            decodeNode """{"id":"x","display_name":"x"}"""
                |> Expect.err
    , test "fails when a node has no display_name" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent"}"""
                |> Expect.err
    , test "fails when an edge has no port_name" <|
        \_ ->
            decodeEdge """{"from":"a","to":"b"}"""
                |> Expect.err
    , test "fails when an edge has no from" <|
        \_ ->
            decodeEdge """{"to":"b","port_name":"p"}"""
                |> Expect.err
    , test "fails when an edge has no to" <|
        \_ ->
            decodeEdge """{"from":"a","port_name":"p"}"""
                |> Expect.err
    , test "fails on a present but ill-typed optional rather than defaulting it to False" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x","optional":"yes"}"""
                |> Expect.err
    , test "fails on a present but ill-typed type_ref rather than defaulting it to empty" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x","type_ref":7}"""
                |> Expect.err
    , test "fails on a present but ill-typed decorations rather than defaulting it to empty" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x","decorations":"retry"}"""
                |> Expect.err
    , test "tolerates an explicit null in an omitempty field" <|
        \_ ->
            decodeNode """{"id":"x","kind":"agent","display_name":"x","type_ref":null,"optional":null,"decorations":null}"""
                |> Expect.equal
                    (Ok
                        { id = "x"
                        , kind = Model.Agent
                        , displayName = "x"
                        , typeRef = ""
                        , optional = False
                        , decorations = []
                        }
                    )
    ]


unknownKindPayload : String
unknownKindPayload =
    """{"nodes":[{"id":"x","kind":"prompt","display_name":"x"}],"edges":[]}"""



-- model helpers --------------------------------------------------------------


modelHelpers : List Test
modelHelpers =
    [ test "findNode returns the matching node" <|
        \_ ->
            decodeGraph Fixtures.codeReviewV3
                |> Result.map (Model.findNode "review" >> Maybe.map .kind)
                |> Expect.equal (Ok (Just Model.Agent))
    , test "findNode distinguishes an endpoint id from the bare execution id" <|
        \_ ->
            decodeGraph Fixtures.codeReviewV3
                |> Result.map (Model.findNode "output:review" >> Maybe.map .kind)
                |> Expect.equal (Ok (Just Model.Output))
    , test "findNode matches the whole id, not a substring of a longer one" <|
        \_ ->
            -- The kind-qualified endpoint id `output:review` contains the bare
            -- execution id `review`. A substring match would return whichever
            -- came first in the list, which is a silently wrong node.
            { nodes =
                [ { id = "output:review", kind = Model.Output, displayName = "review", typeRef = "review/v1", optional = False, decorations = [] }
                , { id = "review", kind = Model.Agent, displayName = "review", typeRef = "", optional = False, decorations = [] }
                ]
            , edges = []
            }
                |> Model.findNode "review"
                |> Maybe.map .kind
                |> Expect.equal (Just Model.Agent)
    , test "findNode returns Nothing for an absent id" <|
        \_ ->
            decodeGraph Fixtures.codeReviewV3
                |> Result.map (Model.findNode "nope")
                |> Expect.equal (Ok Nothing)
    , test "execution kinds are exactly agent, task, await, publish, and load" <|
        \_ ->
            [ Model.Input, Model.ResourceSource, Model.Load, Model.Agent, Model.Task, Model.Await, Model.Publish, Model.Output ]
                |> List.filter Model.isExecution
                |> List.map Model.kindName
                |> Expect.equal [ "load", "agent", "task", "await", "publish" ]
    , test "endpoint kinds are exactly input, resource_source, and output" <|
        \_ ->
            [ Model.Input, Model.ResourceSource, Model.Load, Model.Agent, Model.Task, Model.Await, Model.Publish, Model.Output ]
                |> List.filter Model.isEndpoint
                |> List.map Model.kindName
                |> Expect.equal [ "input", "resource_source", "output" ]
    , test "kindName round-trips through the decoder for every kind" <|
        \_ ->
            [ Model.Input, Model.ResourceSource, Model.Load, Model.Agent, Model.Task, Model.Await, Model.Publish, Model.Output ]
                |> List.map
                    (\kind ->
                        decodeNode
                            ("""{"id":"x","kind":\"""" ++ Model.kindName kind ++ """","display_name":"x"}""")
                            |> Result.map .kind
                    )
                |> Expect.equal
                    ([ Model.Input, Model.ResourceSource, Model.Load, Model.Agent, Model.Task, Model.Await, Model.Publish, Model.Output ]
                        |> List.map Ok
                    )
    ]



-- helpers --------------------------------------------------------------------


decodeGraph : String -> Result Json.Decode.Error Model.Graph
decodeGraph =
    Json.Decode.decodeString Decoder.graph


decodeOk : String -> Bool
decodeOk json =
    case decodeGraph json of
        Ok _ ->
            True

        Err _ ->
            False


decodeNode : String -> Result Json.Decode.Error Model.Node
decodeNode =
    Json.Decode.decodeString Decoder.node


decodeEdge : String -> Result Json.Decode.Error Model.Edge
decodeEdge =
    Json.Decode.decodeString Decoder.edge
