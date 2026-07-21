module RoutesTests exposing (all)

import Concourse exposing (JsonValue(..))
import Dict
import Expect
import Routes
import Test exposing (Test, describe, test)
import Url


all : Test
all =
    describe "Routes"
        [ test "parses dashboard search query respecting space" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/"
                    , query = Just "search=asdf+sd"
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just
                            (Routes.Dashboard
                                { searchType = Routes.Normal "asdf sd"
                                , dashboardView = Routes.ViewNonArchivedPipelines
                                }
                            )
                        )
        , test "parses dashboard without search" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/"
                    , query = Nothing
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just
                            (Routes.Dashboard
                                { searchType = Routes.Normal ""
                                , dashboardView = Routes.ViewNonArchivedPipelines
                                }
                            )
                        )
        , test "parses dashboard with 'all' view" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/"
                    , query = Just "view=all"
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just
                            (Routes.Dashboard
                                { searchType = Routes.Normal ""
                                , dashboardView = Routes.ViewAllPipelines
                                }
                            )
                        )
        , test "parses dashboard with unknown view defaults to non archived only" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/"
                    , query = Just "view=blah"
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just
                            (Routes.Dashboard
                                { searchType = Routes.Normal ""
                                , dashboardView = Routes.ViewNonArchivedPipelines
                                }
                            )
                        )
        , test "parses dashboard in hd view" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/hd"
                    , query = Nothing
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just
                            (Routes.Dashboard
                                { searchType = Routes.HighDensity
                                , dashboardView = Routes.ViewNonArchivedPipelines
                                }
                            )
                        )
        , test "dashboard hd view ignores search and instance group query params" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/hd"
                    , query = Just "search=abc&team=def&group=ghi"
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just
                            (Routes.Dashboard
                                { searchType = Routes.HighDensity
                                , dashboardView = Routes.ViewNonArchivedPipelines
                                }
                            )
                        )
        , test "fly success has noop parameter" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/fly_success"
                    , query = Just "fly_port=1234&noop=true"
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just <| Routes.FlySuccess True (Just 1234))
        , test "fly noop parameter defaults to False" <|
            \_ ->
                Routes.parsePath
                    { protocol = Url.Http
                    , host = ""
                    , port_ = Nothing
                    , path = "/fly_success"
                    , query = Just "fly_port=1234"
                    , fragment = Nothing
                    }
                    |> Expect.equal
                        (Just <| Routes.FlySuccess False (Just 1234))
        , test "toString serializes 'all' dashboard view" <|
            \_ ->
                ("http://example.com"
                    ++ Routes.toString
                        (Routes.Dashboard
                            { searchType = Routes.Normal "hello world"
                            , dashboardView = Routes.ViewAllPipelines
                            }
                        )
                )
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal
                        (Just <|
                            Routes.Dashboard
                                { searchType = Routes.Normal "hello world"
                                , dashboardView = Routes.ViewAllPipelines
                                }
                        )
        , test "toString doesn't serialize 'non_archived' dashboard view" <|
            \_ ->
                Routes.toString
                    (Routes.Dashboard
                        { searchType = Routes.Normal ""
                        , dashboardView = Routes.ViewNonArchivedPipelines
                        }
                    )
                    |> Expect.equal "/"
        , test "toString on Pipeline doesn't add empty instance vars" <|
            \_ ->
                Routes.toString
                    (Routes.Pipeline
                        { id =
                            { teamName = "team"
                            , pipelineName = "pipeline"
                            , pipelineInstanceVars = Dict.empty
                            }
                        , groups = []
                        }
                    )
                    |> Expect.equal "/teams/team/pipelines/pipeline"
        , test "toString on Pipeline adds instance vars if non-empty" <|
            \_ ->
                Routes.toString
                    (Routes.Pipeline
                        { id =
                            { teamName = "team"
                            , pipelineName = "pipeline"
                            , pipelineInstanceVars =
                                Dict.fromList
                                    [ ( "k", JsonString "s" )
                                    , ( "foo"
                                      , JsonObject
                                            [ ( "bar"
                                              , JsonObject
                                                    [ ( "baz.qux", JsonNumber 1 )
                                                    , ( "special_chars", JsonString "/\"'&." )
                                                    ]
                                              )
                                            ]
                                      )
                                    ]
                            }
                        , groups = []
                        }
                    )
                    |> Expect.equal "/teams/team/pipelines/pipeline?vars.foo.bar.%22baz.qux%22=1&vars.foo.bar.special_chars=%22%2F%5C%22'%26.%22&vars.k=%22s%22"
        , test "Pipeline route can be parsed properly" <|
            \_ ->
                ("http://example.com"
                    ++ Routes.toString
                        (Routes.Pipeline
                            { id =
                                { teamName = "team"
                                , pipelineName = "pipeline"
                                , pipelineInstanceVars =
                                    Dict.fromList
                                        [ ( "k1", JsonNumber 1 )
                                        , ( "k2", JsonString "/\"'&." )
                                        ]
                                }
                            , groups = []
                            }
                        )
                )
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal
                        (Just <|
                            Routes.Pipeline
                                { id =
                                    { teamName = "team"
                                    , pipelineName = "pipeline"
                                    , pipelineInstanceVars =
                                        Dict.fromList
                                            [ ( "k1", JsonNumber 1 )
                                            , ( "k2", JsonString "/\"'&." )
                                            ]
                                    }
                                , groups = []
                                }
                        )
        , test "Pipeline route can be parsed properly given rooted vars" <|
            \_ ->
                "http://example.com/teams/team/pipelines/pipeline?vars=%7B%22foo%22%3A%22bar%22%7D"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal
                        (Just <|
                            Routes.Pipeline
                                { id =
                                    { teamName = "team"
                                    , pipelineName = "pipeline"
                                    , pipelineInstanceVars = Dict.fromList [ ( "foo", JsonString "bar" ) ]
                                    }
                                , groups = []
                                }
                        )
        , test "toString respects noop parameter with a fly port" <|
            \_ ->
                ("http://example.com"
                    ++ Routes.toString (Routes.FlySuccess True (Just 1234))
                )
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.FlySuccess True (Just 1234))
        , test "toString respects noop parameter without a fly port" <|
            \_ ->
                ("http://example.com"
                    ++ Routes.toString (Routes.FlySuccess True Nothing)
                )
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.FlySuccess True Nothing)
        , test "resources" <|
            \_ ->
                "http://example.com/teams/team/pipelines/pipeline/resources/resource?filter=version:sha:123abc"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal
                        (Just <|
                            Routes.Resource
                                { id =
                                    { teamName = "team"
                                    , pipelineName = "pipeline"
                                    , pipelineInstanceVars = Dict.empty
                                    , resourceName = "resource"
                                    }
                                , page = Nothing
                                , version = Just <| Dict.fromList [ ( "version", "sha:123abc" ) ]
                                , groups = []
                                }
                        )
        , test "agent tickets queue" <|
            \_ ->
                ("http://example.com" ++ Routes.toString Routes.AgentTickets)
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just Routes.AgentTickets)
        , test "agent tickets queue path is /agent/tickets" <|
            \_ ->
                Routes.toString Routes.AgentTickets
                    |> Expect.equal "/agent/tickets"
        , test "agent ticket detail roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.AgentTicket { id = 12 }))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentTicket { id = 12 })
        , test "agent ticket detail path is /agent/tickets/12" <|
            \_ ->
                Routes.toString (Routes.AgentTicket { id = 12 })
                    |> Expect.equal "/agent/tickets/12"
        , test "agent runs section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentRuns))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentRuns)
        , test "agent workflows section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentWorkflows))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentWorkflows)
        , test "agent spend section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentSpend))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentSpend)
        , test "agent admin section roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.Agent Routes.AgentAdmin))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentAdmin)
        , test "bare /agent legacy alias parses to runs" <|
            \_ ->
                "http://example.com/agent"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.Agent Routes.AgentRuns)
        , test "agent reviews team-less path roundtrip" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.AgentReviews { teamName = "main" }))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentReviews { teamName = "main" })
        , test "agent reviews path is /agent/reviews" <|
            \_ ->
                Routes.toString (Routes.AgentReviews { teamName = "main" })
                    |> Expect.equal "/agent/reviews"
        , test "legacy /teams/main/agent-reviews still parses" <|
            \_ ->
                "http://example.com/teams/main/agent-reviews"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentReviews { teamName = "main" })
        , test "bare /reviews shortcut parses to agent reviews" <|
            \_ ->
                "http://example.com/reviews"
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentReviews { teamName = "main" })
        , test "AgentRunTranscript round-trips through toString/parsePath" <|
            \_ ->
                ("http://example.com" ++ Routes.toString (Routes.AgentRunTranscript { id = 12, buildId = 4567 }))
                    |> Url.fromString
                    |> Maybe.andThen Routes.parsePath
                    |> Expect.equal (Just <| Routes.AgentRunTranscript { id = 12, buildId = 4567 })
        ]
