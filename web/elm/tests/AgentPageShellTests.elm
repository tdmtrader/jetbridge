module AgentPageShellTests exposing (all)

{-| Every agent-platform page renders through ONE shell (`AgentPage.Chrome`),
and every agent nav reads ONE list of destinations (`AgentPage.Nav`).

Both properties used to be maintained by hand and both had drifted: four pages
hand-rolled their own top bar / sidebar / content container, and the sidebar and
in-page nav kept two separate literal lists with two different labels for the
same route.

-}

import AgentPage.Nav as Nav
import Application.Application as Application
import Common
import Expect
import Html.Attributes
import Message.Callback as Callback
import Message.Message as Message
import Message.TopLevelMessage as Msgs
import Routes
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, id, text)


{-| Put the viewport on a desktop and toggle the sidebar open, so the sidebar's
agent section actually renders.
-}
withOpenSideBar : Application.Model -> Application.Model
withOpenSideBar model =
    model
        |> Application.handleCallback
            (Callback.ScreenResized
                { scene = { width = 0, height = 0 }
                , viewport = { x = 0, y = 0, width = 1200, height = 900 }
                }
            )
        |> Tuple.first
        |> Application.update (Msgs.Update (Message.Click Message.SideBarIcon))
        |> Tuple.first


{-| Every agent route, by the path a user actually arrives on.
-}
agentPagePaths : List ( String, String )
agentPagePaths =
    [ ( "operations console", "/agent" )
    , ( "ticket queue", "/agent-tickets" )
    , ( "ticket detail", "/agent-tickets/12" )
    , ( "review queue", "/teams/main/agent-reviews" )
    , ( "experiment laboratory", "/agent/experiments" )
    , ( "workflow function", "/agent/workflows/develop" )
    , ( "workflow run", "/agent/workflows/develop/runs/9007199254740993" )
    , ( "snapshot", "/agent/snapshots/9007199254740995" )
    ]


all : Test
all =
    describe "agent page shell"
        [ describe "every agent page renders through the shared shell" <|
            List.map
                (\( label, path ) ->
                    test label <|
                        \_ ->
                            Common.init path
                                |> Common.queryView
                                |> Expect.all
                                    [ Query.has [ id "agent-page-content" ]
                                    , Query.has [ class "agent-local-nav" ]
                                    ]
                )
                agentPagePaths
        , describe "the in-page nav is the shared destination list" <|
            List.map
                (\item ->
                    test item.label <|
                        \_ ->
                            Common.init "/agent-tickets"
                                |> Common.queryView
                                |> Query.find [ class "agent-local-nav" ]
                                |> Query.find [ id ("agent-nav-" ++ item.id) ]
                                |> Expect.all
                                    [ Query.has [ text item.label ]
                                    , Query.has
                                        [ attribute
                                            (Html.Attributes.href (Routes.toString item.route))
                                        ]
                                    ]
                )
                Nav.items
        , describe "the sidebar nav is the same shared destination list" <|
            List.map
                (\item ->
                    test item.label <|
                        \_ ->
                            Common.init "/agent-tickets"
                                |> withOpenSideBar
                                |> Common.queryView
                                |> Query.find [ id "side-bar" ]
                                |> Query.find [ id ("sidebar-" ++ item.id) ]
                                |> Expect.all
                                    [ Query.has [ text item.label ]
                                    , Query.has
                                        [ attribute
                                            (Html.Attributes.href (Routes.toString item.route))
                                        ]
                                    ]
                )
                Nav.items
        , test "the shared list still covers all four platform destinations" <|
            \_ ->
                -- A guard against silently dropping a page from BOTH navs at
                -- once now that they share one list.
                Nav.items
                    |> List.map .route
                    |> Expect.equal
                        [ Routes.Agent
                        , Routes.AgentTickets
                        , Routes.AgentReviews { teamName = "main" }
                        , Routes.AgentExperiments
                        ]
        ]
