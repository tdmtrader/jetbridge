module AgentPage.Chrome exposing (view)

import Application.Models exposing (Session)
import Colors
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style)
import Login.Login as Login
import Message.Message exposing (Message)
import Routes
import SideBar.SideBar as SideBar
import Views.Styles
import Views.TopBar as TopBar


view :
    Session
    -> Login.Model model
    -> Routes.Route
    -> String
    -> String
    -> List (Html Message)
    -> Html Message
view session model route title subtitle children =
    Html.div
        (id "page-including-top-bar" :: Views.Styles.pageIncludingTopBar)
        [ Html.div
            (id "top-bar-app" :: Views.Styles.topBar False)
            [ Html.div
                [ style "display" "flex", style "align-items" "center" ]
                (SideBar.sideBarIcon session
                    :: TopBar.breadcrumbs session route
                )
            , Login.view session.userState model
            ]
        , Html.div
            (id "page-below-top-bar" :: Views.Styles.pageBelowTopBar route)
            [ SideBar.view session Nothing
            , Html.main_
                [ class "agent-page"
                , style "padding" "20px 24px 48px"
                , style "width" "100%"
                , style "box-sizing" "border-box"
                , style "overflow-y" "auto"
                ]
                ([ Html.div
                    [ style "display" "flex"
                    , style "align-items" "baseline"
                    , style "gap" "16px"
                    , style "margin-bottom" "6px"
                    ]
                    [ Html.h1
                        [ style "font-size" "20px"
                        , style "margin" "0"
                        , style "color" Colors.text
                        ]
                        [ Html.text title ]
                    , agentNav
                    ]
                 , Html.p
                    [ style "font-family" "monospace"
                    , style "font-size" "12px"
                    , style "color" "#8a8a8a"
                    , style "margin" "0 0 20px"
                    ]
                    [ Html.text subtitle ]
                 ]
                    ++ children
                )
            ]
        ]


agentNav : Html Message
agentNav =
    Html.nav
        [ class "agent-local-nav"
        , style "display" "flex"
        , style "gap" "12px"
        , style "font-size" "12px"
        ]
        [ navLink (Routes.toString Routes.Agent) "workflows"
        , navLink (Routes.toString Routes.AgentTickets) "tickets"
        , navLink (Routes.toString (Routes.AgentReviews { teamName = "main" })) "reviews"
        , navLink (Routes.toString Routes.AgentExperiments) "experiments"
        ]


navLink : String -> String -> Html Message
navLink url label =
    Html.a
        [ href url
        , style "color" "#7a9ac0"
        , style "text-decoration" "none"
        ]
        [ Html.text label ]
