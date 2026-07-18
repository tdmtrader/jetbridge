module AgentTickets.AgentTickets exposing
    ( Model
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import AgentBadge
import Application.Models exposing (Session)
import Concourse.AgentTicket as AgentTicket
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Routes
import SideBar.SideBar as SideBar
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { tickets : List AgentTicket.Ticket
        , costByTicket : Dict String Float
        , loaded : Bool
        , loadError : Bool
        }


{-| Lifecycle states in the order they surface in the queue. `needs_review`
is pinned first because it is the human work queue; terminal states sink to
the bottom. States the server reports but not listed here still render under
their raw token via a trailing catch-all section.
-}
sectionOrder : List String
sectionOrder =
    [ "needs_review"
    , "awaiting_human"
    , "running"
    , "queued"
    , "draft"
    , "sent_back"
    , "failed"
    , "errored"
    , "merged"
    , "merged_with_fixes"
    , "concluded"
    , "abandoned"
    ]


init : ( Model, List Effect )
init =
    ( { tickets = []
      , costByTicket = Dict.empty
      , loaded = False
      , loadError = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTickets, FetchAgentTicketCosts ]
    )


documentTitle : String
documentTitle =
    "Ticket queue"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentTicketsFetched (Ok tickets) ->
            ( { model | tickets = tickets, loaded = True, loadError = False }, effects )

        AgentTicketsFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        AgentCostRollupFetched (Ok rollup) ->
            ( { model
                | costByTicket =
                    rollup.rows
                        |> List.map (\row -> ( row.key, row.costUsd ))
                        |> Dict.fromList
              }
            , effects
            )

        AgentCostRollupFetched (Err _) ->
            ( model, effects )

        _ ->
            ( model, effects )


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked OneMinute _ ->
            -- Self-healing refresh; only replaces fetched data.
            ( model, effects ++ [ FetchAgentTickets, FetchAgentTicketCosts ] )

        _ ->
            ( model, effects )


update : Message -> ET Model
update _ ( model, effects ) =
    ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    [ OnClockTick OneMinute ]


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentTickets
    in
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
            , Html.div
                [ style "padding" "16px", style "width" "100%" ]
                [ Html.h1 [ style "font-size" "18px" ] [ Html.text "Ticket queue" ]
                , content model
                ]
            ]
        ]


content : Model -> Html Message
content model =
    if model.loadError then
        Html.p [ style "color" "#f0a0a0" ] [ Html.text "Couldn't load tickets." ]

    else if model.loaded && List.isEmpty model.tickets then
        Html.p [ style "color" "#b0b0b0" ] [ Html.text "No tickets yet." ]

    else
        let
            knownSections =
                List.filterMap (sectionView model.costByTicket model.tickets) sectionOrder

            leftover =
                model.tickets
                    |> List.filter (\t -> not (List.member t.state sectionOrder))
        in
        Html.div []
            (knownSections
                ++ leftoverSection model.costByTicket leftover
                ++ unattributedFooter model.costByTicket
            )


{-| Spend the cost rollup reports outside any ticket (per-push CI reviews,
platform runs — the rollup's empty-string key). Without this line the queue
page silently under-reports what the platform actually costs.
-}
unattributedFooter : Dict String Float -> List (Html Message)
unattributedFooter costs =
    case Dict.get "" costs of
        Just cost ->
            if cost > 0 then
                [ Html.div
                    [ id "unattributed-cost"
                    , style "display" "flex"
                    , style "gap" "12px"
                    , style "margin-top" "24px"
                    , style "padding" "8px 12px"
                    , style "border-top" "1px solid #3d3c3c"
                    , style "font-family" "monospace"
                    , style "font-size" "12px"
                    , style "color" "#9aa39b"
                    ]
                    [ Html.span [ style "flex" "1" ]
                        [ Html.text "unattributed (no ticket: CI reviews, platform runs, all time)" ]
                    , Html.span [ style "color" "#b0b0b0" ]
                        [ Html.text ("$" ++ formatUsd cost) ]
                    ]
                ]

            else
                []

        Nothing ->
            []


leftoverSection : Dict String Float -> List AgentTicket.Ticket -> List (Html Message)
leftoverSection costs tickets =
    if List.isEmpty tickets then
        []

    else
        [ sectionBlock "other" (List.map (ticketRow costs) (sortByRecent tickets)) ]


sectionView : Dict String Float -> List AgentTicket.Ticket -> String -> Maybe (Html Message)
sectionView costs tickets state =
    case List.filter (\t -> t.state == state) tickets of
        [] ->
            Nothing

        matching ->
            Just (sectionBlock (sectionLabel state) (List.map (ticketRow costs) (sortByRecent matching)))


sortByRecent : List AgentTicket.Ticket -> List AgentTicket.Ticket
sortByRecent =
    List.sortBy (\t -> negate t.createdAt)


sectionLabel : String -> String
sectionLabel state =
    case AgentBadge.fromApiToken state of
        Just status ->
            AgentBadge.label status

        Nothing ->
            state


sectionBlock : String -> List (Html Message) -> Html Message
sectionBlock title rows =
    Html.div
        [ style "margin-top" "20px" ]
        (Html.h2
            [ style "font-size" "13px"
            , style "text-transform" "uppercase"
            , style "letter-spacing" "0.08em"
            , style "color" "#9aa39b"
            , style "margin" "0 0 6px 0"
            ]
            [ Html.text title ]
            :: rows
        )


ticketRow : Dict String Float -> AgentTicket.Ticket -> Html Message
ticketRow costs t =
    Html.a
        [ class "agent-ticket-row"
        , href (Routes.toString (Routes.AgentTicket { id = t.id }))
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "12px"
        , style "padding" "8px 12px"
        , style "border-bottom" "1px solid #3d3c3c"
        , style "color" "inherit"
        , style "text-decoration" "none"
        ]
        [ Html.span
            [ style "font-family" "monospace"
            , style "color" "#9aa39b"
            , style "min-width" "40px"
            ]
            [ Html.text ("#" ++ String.fromInt t.id) ]
        , case AgentBadge.fromApiToken t.state of
            Just status ->
                AgentBadge.view status

            Nothing ->
                Html.span [ style "color" "#b0b0b0" ] [ Html.text t.state ]
        , Html.span
            [ style "flex" "1"
            , style "overflow" "hidden"
            , style "text-overflow" "ellipsis"
            , style "white-space" "nowrap"
            ]
            [ Html.text t.title ]
        , if t.branch == "" then
            Html.text ""

          else
            Html.span
                [ class "agent-ticket-branch"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "color" "#7aa37a"
                ]
                [ Html.text t.branch ]
        , Html.span
            [ style "font-family" "monospace", style "font-size" "12px", style "color" "#7a7a7a" ]
            [ Html.text t.workflowName ]
        , Html.span
            [ style "font-family" "monospace", style "color" "#b0b0b0", style "min-width" "60px", style "text-align" "right" ]
            [ Html.text (costLabel costs t.id) ]
        ]


costLabel : Dict String Float -> Int -> String
costLabel costs ticketId =
    case Dict.get (String.fromInt ticketId) costs of
        Just cost ->
            "$" ++ formatUsd cost

        Nothing ->
            "—"


formatUsd : Float -> String
formatUsd amount =
    let
        cents =
            round (amount * 100)

        absCents =
            abs cents

        dollars =
            absCents // 100

        remainder =
            modBy 100 absCents

        fraction =
            if remainder < 10 then
                "0" ++ String.fromInt remainder

            else
                String.fromInt remainder
    in
    String.fromInt dollars ++ "." ++ fraction
