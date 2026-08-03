module AgentWorkflowFiltersTests exposing (all)

import AgentWorkflow.Filters as Filters
import Expect
import Test exposing (Test, describe, test)
import Url.Builder


all : Test
all =
    describe "workflow page filters"
        [ describe "defaults"
            [ test "defaults to a seven-day operational attention view" <|
                \_ ->
                    Expect.all
                        [ \f -> Expect.equal Filters.SevenDays f.window
                        , \f -> Expect.equal Filters.Operational f.scope
                        , \f -> Expect.equal Filters.Attention f.status
                        , \f -> Expect.equal Nothing f.selectedNode
                        , \f -> Expect.equal Nothing f.selectedNodeStatus
                        , \f -> Expect.equal Nothing f.version
                        , \f -> Expect.equal "" f.search
                        , \f -> Expect.equal "" f.origin
                        ]
                        Filters.default
            , test "omits defaults from the query so a clean page has a clean URL" <|
                \_ ->
                    Filters.toQueryPairs Filters.default
                        |> Expect.equal []
            , test "omits defaults from the builder query too" <|
                \_ ->
                    Filters.toQuery Filters.default
                        |> Expect.equal []
            ]
        , describe "URL round trip"
            [ test "round-trips a fully specified filter through the query string" <|
                \_ ->
                    let
                        filters =
                            { window = Filters.TwentyFourHours
                            , scope = Filters.Experiment
                            , status = Filters.All
                            , search = "ticket-42"
                            , selectedNode = Just "implement"
                            , selectedNodeStatus = Just "failed"
                            , version = Just 3
                            , origin = "manual"
                            }
                    in
                    Filters.fromQuery (Filters.toQueryPairs filters)
                        |> Expect.equal filters
            , test "round-trips every window" <|
                \_ ->
                    [ Filters.TwentyFourHours, Filters.SevenDays, Filters.ThirtyDays ]
                        |> List.map
                            (\window ->
                                Filters.fromQuery
                                    (Filters.toQueryPairs { default | window = window })
                                    |> .window
                            )
                        |> Expect.equal [ Filters.TwentyFourHours, Filters.SevenDays, Filters.ThirtyDays ]
            , test "round-trips every scope" <|
                \_ ->
                    [ Filters.Operational, Filters.Experiment, Filters.AllScopes ]
                        |> List.map
                            (\scope ->
                                Filters.fromQuery
                                    (Filters.toQueryPairs { default | scope = scope })
                                    |> .scope
                            )
                        |> Expect.equal [ Filters.Operational, Filters.Experiment, Filters.AllScopes ]
            , test "round-trips every status lens" <|
                \_ ->
                    [ Filters.Attention, Filters.Active, Filters.All ]
                        |> List.map
                            (\status ->
                                Filters.fromQuery
                                    (Filters.toQueryPairs { default | status = status })
                                    |> .status
                            )
                        |> Expect.equal [ Filters.Attention, Filters.Active, Filters.All ]
            , test "builds the query parameters a link carries" <|
                \_ ->
                    Filters.toQuery { default | window = Filters.ThirtyDays, selectedNode = Just "review" }
                        |> Expect.equal
                            [ Url.Builder.string "window" "30d"
                            , Url.Builder.string "node" "review"
                            ]
            ]
        , describe "unrecognised values degrade instead of failing the page"
            [ test "ignores an unrecognised window" <|
                \_ ->
                    Filters.fromQuery [ ( "window", "90d" ) ]
                        |> .window
                        |> Expect.equal Filters.SevenDays
            , test "ignores an unrecognised scope" <|
                \_ ->
                    Filters.fromQuery [ ( "scope", "everything" ) ]
                        |> .scope
                        |> Expect.equal Filters.Operational
            , test "ignores an unrecognised status lens" <|
                \_ ->
                    Filters.fromQuery [ ( "status", "sideways" ) ]
                        |> .status
                        |> Expect.equal Filters.Attention
            , test "ignores a non-numeric version" <|
                \_ ->
                    Filters.fromQuery [ ( "version", "latest" ) ]
                        |> .version
                        |> Expect.equal Nothing
            , test "drops a node status that has no node to qualify" <|
                \_ ->
                    Filters.fromQuery [ ( "node_status", "failed" ) ]
                        |> .selectedNodeStatus
                        |> Expect.equal Nothing
            , test "keeps a node status that does have a node" <|
                \_ ->
                    Filters.fromQuery [ ( "node", "implement" ), ( "node_status", "failed" ) ]
                        |> .selectedNodeStatus
                        |> Expect.equal (Just "failed")
            , test "drops an unrecognised node status rather than sending it to the server" <|
                \_ ->
                    Filters.fromQuery [ ( "node", "implement" ), ( "node_status", "sideways" ) ]
                        |> .selectedNodeStatus
                        |> Expect.equal Nothing
            , test "drops an endpoint node, whose filter no occurrence can satisfy" <|
                \_ ->
                    -- The server's node filter is an EXISTS over
                    -- agent_workflow_run_node_occurrences, and that projection
                    -- holds only execution nodes with bare ids. Forwarding
                    -- `input:repository` returns an empty list the page then
                    -- captions as "No runs need attention in this window" — a
                    -- server fact about a population it never examined. The
                    -- value is URL-addressable, so a shared link would
                    -- reproduce that for a reader who never made the click.
                    [ "input:repository", "output:change", "source:git" ]
                        |> List.map (\nodeId -> Filters.fromQuery [ ( "node", nodeId ) ] |> .selectedNode)
                        |> Expect.equal [ Nothing, Nothing, Nothing ]
            , test "drops the node status of a dropped endpoint node too" <|
                \_ ->
                    Filters.fromQuery
                        [ ( "node", "input:repository" ), ( "node_status", "failed" ) ]
                        |> .selectedNodeStatus
                        |> Expect.equal Nothing
            , test "keeps an execution node whose name merely starts like a port" <|
                \_ ->
                    -- The prefixes are `input:`, not the word "input". An
                    -- agent legitimately called `inputs` must still filter.
                    Filters.fromQuery [ ( "node", "inputs" ) ]
                        |> .selectedNode
                        |> Expect.equal (Just "inputs")
            ]
        , describe "the overview API query"
            [ test "carries only the window and scope, which is all the endpoint accepts" <|
                \_ ->
                    Filters.overviewQuery
                        { default
                            | window = Filters.ThirtyDays
                            , scope = Filters.Experiment
                            , selectedNode = Just "implement"
                            , search = "x"
                        }
                        |> Expect.equal [ ( "window", "30d" ), ( "scope", "experiment" ) ]
            , test "states the window and scope explicitly even at their defaults" <|
                \_ ->
                    Filters.overviewQuery default
                        |> Expect.equal [ ( "window", "7d" ), ( "scope", "operational" ) ]
            , test "the canvas and the run list agree about the population they describe" <|
                \_ ->
                    -- The overview accepted no scope at all, so the experiments
                    -- toggle narrowed the list while the DAG kept aggregating
                    -- operational runs that were not in it.
                    let
                        experiment =
                            { default | scope = Filters.Experiment }
                    in
                    ( Filters.overviewQuery experiment, Filters.runsQuery experiment )
                        |> Expect.equal
                            ( [ ( "window", "7d" ), ( "scope", "experiment" ) ]
                            , [ ( "window", "7d" ), ( "scope", "experiment" ), ( "lens", "attention" ) ]
                            )
            ]
        , describe "the run list API query"
            [ test "states window, scope, and lens explicitly" <|
                \_ ->
                    Filters.runsQuery default
                        |> Expect.equal
                            [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
            , test "sends the selected node and its status" <|
                \_ ->
                    Filters.runsQuery
                        { default | selectedNode = Just "implement", selectedNodeStatus = Just "failed" }
                        |> Expect.equal
                            [ ( "window", "7d" )
                            , ( "scope", "operational" )
                            , ( "lens", "attention" )
                            , ( "node", "implement" )
                            , ( "node_status", "failed" )
                            ]
            , test "never sends a node status without its node, which the server rejects" <|
                \_ ->
                    Filters.runsQuery { default | selectedNodeStatus = Just "failed" }
                        |> Expect.equal
                            [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
            , test "sends the search term as q" <|
                \_ ->
                    Filters.runsQuery { default | search = "ticket-42" }
                        |> Expect.equal
                            [ ( "window", "7d" )
                            , ( "scope", "operational" )
                            , ( "lens", "attention" )
                            , ( "q", "ticket-42" )
                            ]
            , test "sends origin as origin_kind, the name the API actually accepts" <|
                \_ ->
                    Filters.runsQuery { default | origin = "manual" }
                        |> Expect.equal
                            [ ( "window", "7d" )
                            , ( "scope", "operational" )
                            , ( "lens", "attention" )
                            , ( "origin_kind", "manual" )
                            ]
            , test "never sends version, which the run list API does not accept" <|
                \_ ->
                    Filters.runsQuery { default | version = Just 3 }
                        |> Expect.equal
                            [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "attention" ) ]
            , test "sends the attention lens as lens, not as the server's status vocabulary" <|
                \_ ->
                    Filters.runsQuery { default | status = Filters.Active }
                        |> Expect.equal
                            [ ( "window", "7d" ), ( "scope", "operational" ), ( "lens", "active" ) ]
            , test "the page URL keeps the design's status spelling while the wire uses lens" <|
                \_ ->
                    Filters.toQueryPairs { default | status = Filters.All }
                        |> Expect.equal [ ( "status", "all" ) ]
            ]
        ]


default : Filters.Filters
default =
    Filters.default
