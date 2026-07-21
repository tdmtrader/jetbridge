module Agent.Nav exposing (view)

import Colors
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, href, id, style)
import Message.Message exposing (Message)
import Routes


{-| The persistent agent sub-nav mounted by every /agent/* page. Real links so
back/forward and bookmarking work; the tab matching the current route is marked
`aria-current="page"` and rendered in the active color.
-}
view : Routes.Route -> Html Message
view current =
    Html.div
        [ id "agent-subnav"
        , class "agent-subnav"
        , style "display" "flex"
        , style "flex-wrap" "wrap"
        , style "gap" "4px"
        , style "margin" "0 0 16px 0"
        , style "border-bottom" ("1px solid " ++ Colors.background)
        , style "font-family" "monospace"
        , style "font-size" "13px"
        ]
        (List.map (tab current) tabs)


tabs : List ( String, String, Routes.Route )
tabs =
    [ ( "tickets", "tickets", Routes.AgentTickets )
    , ( "runs", "runs", Routes.Agent Routes.AgentRuns )
    , ( "reviews", "reviews", Routes.AgentReviews { teamName = "main" } )
    , ( "workflows", "workflows", Routes.Agent Routes.AgentWorkflows )
    , ( "spend", "spend", Routes.Agent Routes.AgentSpend )
    , ( "admin", "admin", Routes.Agent Routes.AgentAdmin )
    ]


tab : Routes.Route -> ( String, String, Routes.Route ) -> Html Message
tab current ( slug, label, route ) =
    let
        active =
            isActive current route

        activeAttrs =
            if active then
                [ attribute "aria-current" "page"
                , style "color" Colors.text
                , style "border-bottom" "2px solid #7a9ac0"
                ]

            else
                [ style "color" "#7a9ac0"
                , style "border-bottom" "2px solid transparent"
                ]
    in
    Html.a
        ([ id ("agent-subnav-" ++ slug)
         , href (Routes.toString route)
         , style "padding" "8px 12px"
         , style "text-decoration" "none"
         ]
            ++ activeAttrs
        )
        [ Html.text label ]


{-| A tab is active when the current route is in its section family. The ticket
DETAIL page (`AgentTicket`) and the run transcript page light the "tickets" tab;
the section routes match by AgentSection; reviews matches any team.
-}
isActive : Routes.Route -> Routes.Route -> Bool
isActive current route =
    case ( current, route ) of
        ( Routes.AgentTickets, Routes.AgentTickets ) ->
            True

        ( Routes.AgentTicket _, Routes.AgentTickets ) ->
            True

        ( Routes.AgentRunTranscript _, Routes.AgentTickets ) ->
            True

        ( Routes.AgentReviews _, Routes.AgentReviews _ ) ->
            True

        ( Routes.Agent a, Routes.Agent b ) ->
            a == b

        _ ->
            False
