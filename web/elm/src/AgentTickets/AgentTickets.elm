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
import Duration
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, placeholder, style, type_, value)
import Html.Events exposing (onClick, onInput)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription)
import Polling
import Routes
import SideBar.SideBar as SideBar
import Time
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { tickets : List AgentTicket.Ticket
        , costByTicket : Dict String Float
        , loaded : Bool
        , loadError : Bool
        , now : Maybe Time.Posix
        , filter : String
        , sortByWait : Bool
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
      , now = Nothing
      , filter = ""
      , sortByWait = False
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


polls : List (Polling.Poll Model)
polls =
    [ -- U11: live-update the queue on the dashboard's 5s cadence so state
      -- never goes stale.
      { interval = FiveSeconds, fetch = \_ -> [ FetchAgentTickets ] }
    , -- The cost rollup is a whole-window ledger aggregation — far too
      -- heavy to run 12x/minute per open tab for numbers that move at
      -- run granularity. Refresh it on the minute like the /agent page.
      { interval = OneMinute, fetch = \_ -> [ FetchAgentTicketCosts ] }
    ]


handleDelivery : Delivery -> ET Model
handleDelivery delivery =
    advanceNow delivery >> Polling.handleDelivery polls delivery


{-| Advance "now" for the elapsed-time labels on the same beat as the 5s
refetch (a dedicated OneSecond tick used to re-filter and re-sort the whole
queue every second just to move "N ago" labels the refetch redraws anyway).
This is the page's one model write on a clock tick, kept out of `polls` so
polling stays fetch-only.
-}
advanceNow : Delivery -> ET Model
advanceNow delivery ( model, effects ) =
    case delivery of
        ClockTicked FiveSeconds time ->
            ( { model | now = Just time }, effects )

        _ ->
            ( model, effects )


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        AgentTicketsFilterChanged v ->
            ( { model | filter = v }, effects )

        AgentTicketsSortToggled ->
            ( { model | sortByWait = not model.sortByWait }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls


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
            visible =
                List.filter (matchesFilter model.filter) model.tickets

            knownSections =
                List.filterMap (sectionView model visible) sectionOrder

            leftover =
                visible
                    |> List.filter (\t -> not (List.member t.state sectionOrder))

            body =
                if List.isEmpty visible then
                    [ Html.p
                        [ style "color" "#b0b0b0", style "margin-top" "16px" ]
                        [ Html.text "No tickets match the filter." ]
                    ]

                else
                    knownSections ++ leftoverSection model leftover
        in
        Html.div []
            (controlsBar model
                :: body
                ++ unattributedFooter model.costByTicket
            )


{-| U10: client-side title filter — a case-insensitive substring match over the
ticket title, id and author. An empty filter matches everything.
-}
matchesFilter : String -> AgentTicket.Ticket -> Bool
matchesFilter raw t =
    case String.trim (String.toLower raw) of
        "" ->
            True

        needle ->
            let
                haystack =
                    String.toLower
                        (t.title
                            ++ " #"
                            ++ String.fromInt t.id
                            ++ " "
                            ++ t.userName
                        )
            in
            String.contains needle haystack


{-| U10: a lightweight filter/sort bar. The text box narrows the list by title;
the sort toggle flips between newest-first (default) and longest-waiting-first
so a reviewer can surface the tickets that have been queued the longest.
-}
controlsBar : Model -> Html Message
controlsBar model =
    Html.div
        [ id "agent-ticket-controls"
        , style "display" "flex"
        , style "flex-wrap" "wrap"
        , style "align-items" "center"
        , style "gap" "8px"
        , style "margin" "8px 0 4px 0"
        ]
        [ Html.input
            [ class "agent-ticket-filter"
            , type_ "text"
            , placeholder "filter by title…"
            , value model.filter
            , onInput AgentTicketsFilterChanged
            , style "background" "#141313"
            , style "color" "#e0e0e0"
            , style "border" "1px solid #3d3c3c"
            , style "padding" "5px 8px"
            , style "font-size" "13px"
            , style "flex" "1"
            , style "min-width" "160px"
            , style "box-sizing" "border-box"
            ]
            []
        , Html.button
            [ class "agent-ticket-sort"
            , type_ "button"
            , onClick AgentTicketsSortToggled
            , style "background" "#2a2929"
            , style "color" "#d0d0d0"
            , style "border" "1px solid #3d3c3c"
            , style "padding" "5px 12px"
            , style "cursor" "pointer"
            , style "font-size" "13px"
            , style "white-space" "nowrap"
            ]
            [ Html.text
                (if model.sortByWait then
                    "sort: longest waiting"

                 else
                    "sort: newest"
                )
            ]
        ]


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


leftoverSection : Model -> List AgentTicket.Ticket -> List (Html Message)
leftoverSection model tickets =
    if List.isEmpty tickets then
        []

    else
        [ sectionBlock (withCount "other" tickets) (List.map (ticketRow model) (sortTickets model.sortByWait tickets)) ]


sectionView : Model -> List AgentTicket.Ticket -> String -> Maybe (Html Message)
sectionView model tickets state =
    case List.filter (\t -> t.state == state) tickets of
        [] ->
            Nothing

        matching ->
            Just (sectionBlock (withCount (sectionLabel state) matching) (List.map (ticketRow model) (sortTickets model.sortByWait matching)))


{-| U10: append a per-section count, e.g. "needs review (2)" (uppercased by CSS).
-}
withCount : String -> List a -> String
withCount label xs =
    label ++ " (" ++ String.fromInt (List.length xs) ++ ")"


sortTickets : Bool -> List AgentTicket.Ticket -> List AgentTicket.Ticket
sortTickets sortByWait =
    if sortByWait then
        -- Oldest first: the ticket that has waited longest floats to the top.
        List.sortBy .createdAt

    else
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


ticketRow : Model -> AgentTicket.Ticket -> Html Message
ticketRow model t =
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
        , Html.div
            [ class "agent-ticket-main"
            , style "flex" "1"
            , style "min-width" "0"
            , style "display" "flex"
            , style "flex-direction" "column"
            , style "gap" "2px"
            ]
            [ Html.span
                [ class "agent-ticket-title"
                , style "overflow" "hidden"
                , style "text-overflow" "ellipsis"
                , style "white-space" "nowrap"
                ]
                [ Html.text t.title ]
            , metaLine model t
            ]
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
        , if t.workflowName == "" then
            Html.text ""

          else
            Html.span
                [ class "agent-ticket-workflow"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "color" "#7a7a7a"
                ]
                [ Html.text (workflowLabel t) ]
        , Html.span
            [ class "agent-ticket-cost"
            , style "font-family" "monospace"
            , style "color" "#b0b0b0"
            , style "min-width" "60px"
            , style "text-align" "right"
            ]
            [ Html.text (costLabel model.costByTicket t.id) ]
        ]


{-| U10: a compact secondary line under the title carrying the fields already on
the decoded ticket — author, retry count (only when the ticket has been retried),
and how long it has been waiting (needs the live `now` clock from U11).
-}
metaLine : Model -> AgentTicket.Ticket -> Html Message
metaLine model t =
    let
        parts =
            List.filterMap identity
                [ if t.userName == "" then
                    Nothing

                  else
                    Just t.userName
                , if t.attemptCount > 1 then
                    Just ("attempt " ++ String.fromInt t.attemptCount)

                  else
                    Nothing
                , elapsedLabel model.now t.createdAt
                ]
    in
    if List.isEmpty parts then
        Html.text ""

    else
        Html.span
            [ class "agent-ticket-meta"
            , style "font-size" "11px"
            , style "color" "#7a7a7a"
            , style "overflow" "hidden"
            , style "text-overflow" "ellipsis"
            , style "white-space" "nowrap"
            ]
            [ Html.text (String.join " · " parts) ]


{-| Workflow name, with its pinned version appended when the ticket froze one.
-}
workflowLabel : AgentTicket.Ticket -> String
workflowLabel t =
    case t.workflowVersion of
        Just v ->
            t.workflowName ++ " v" ++ String.fromInt v

        Nothing ->
            t.workflowName


{-| Relative "N ago" from the ticket's creation epoch to the live clock. Nothing
when the clock hasn't ticked yet or the timestamp is unknown / in the future.
-}
elapsedLabel : Maybe Time.Posix -> Int -> Maybe String
elapsedLabel maybeNow createdAt =
    case maybeNow of
        Just now ->
            if createdAt > 0 then
                let
                    elapsed =
                        Duration.between (Time.millisToPosix (createdAt * 1000)) now
                in
                if elapsed >= 0 then
                    Just (Duration.format elapsed ++ " ago")

                else
                    Nothing

            else
                Nothing

        Nothing ->
            Nothing


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
