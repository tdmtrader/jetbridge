module AgentPage.Chrome exposing (contentId, view)

{-| The ONE page shell every agent-platform page renders through.

Before this existed each agent page hand-rolled the same top bar, sidebar,
content container, `<h1>` and nav strip, so the pages drifted apart in padding,
scroll behaviour and nav content. `view` takes the two things a page actually
differs by — its title and one-line subtitle — plus its body.

-}

import AgentPage.Nav as Nav
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


{-| The shell's scrolling content element. Pages that jump to their own
sections via the `scrollToId` port need a scrolling parent addressable by id —
this is it, so no page has to opt out of the shell to keep an in-page nav.
-}
contentId : String
contentId =
    "agent-page-content"


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
                , id contentId
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
        , style "flex-wrap" "wrap"
        , style "gap" "12px"
        , style "font-size" "12px"
        ]
        (List.map navLink Nav.items)


navLink : Nav.Item -> Html Message
navLink item =
    Html.a
        [ id ("agent-nav-" ++ item.id)
        , href (Routes.toString item.route)
        , style "color" "#7a9ac0"
        , style "text-decoration" "none"
        ]
        [ Html.text item.label ]
