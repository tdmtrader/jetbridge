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

import Agent.Nav as Nav
import AgentBadge
import Application.Models exposing (Session)
import Concourse.Agent
import Concourse.AgentDispatcher as AgentDispatcher
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
import UserState exposing (UserState(..))
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
        , dispatcher : Maybe AgentDispatcher.Status
        , armedMode : Maybe AgentDispatcher.Mode
        , showNewForm : Bool
        , newTitle : String
        , newBody : String
        , newRepo : String
        , newBranch : String
        , newBudget : String
        , newWorkflow : String
        , newQueue : Bool
        , workflows : List Concourse.Agent.WorkflowSummary
        , creating : Bool
        , createError : Maybe String
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
    , "errored"
    , "draft"
    , "sent_back"
    , "failed"
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
      , dispatcher = Nothing
      , armedMode = Nothing
      , showNewForm = False
      , newTitle = ""
      , newBody = ""
      , newRepo = ""
      , newBranch = ""
      , newBudget = ""
      , newWorkflow = ""
      , newQueue = False
      , workflows = []
      , creating = False
      , createError = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTickets, FetchAgentTicketCosts, FetchAgentDispatcher ]
    )


documentTitle : String
documentTitle =
    "Ticket queue"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentTicketsFetched (Ok fresh) ->
            ( { model
                | tickets =
                    -- Keep the old list when the 5s refetch decoded identical
                    -- data, so reference-equality checks (Html.Lazy) stay
                    -- viable downstream instead of being defeated every poll.
                    if fresh == model.tickets then
                        model.tickets

                    else
                        fresh
                , loaded = True
                , loadError = False
              }
            , effects
            )

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

        AgentDispatcherFetched (Ok status) ->
            ( { model | dispatcher = Just status }, effects )

        AgentDispatcherFetched (Err _) ->
            ( model, effects )

        AgentDispatcherSet (Ok status) ->
            -- The PUT echoes the new effective state, so adopt it directly and
            -- clear any armed pending action.
            ( { model | dispatcher = Just status, armedMode = Nothing }, effects )

        AgentDispatcherSet (Err _) ->
            -- Leave the last-known status in place; disarm so the operator can
            -- retry. A 403 (non-admin) surfaces as the global HTTP error toast.
            ( { model | armedMode = Nothing }, effects )

        AgentWorkflowsFetched (Ok workflows) ->
            ( { model | workflows = workflows }, effects )

        AgentWorkflowsFetched (Err _) ->
            ( model, effects )

        AgentTicketCreated (Ok ticket) ->
            -- Created as a draft. If "queue immediately" was checked, fire the
            -- draft→queued transition, then navigate to the new ticket's detail
            -- page — where the existing two-step Dispatch confirm is the money
            -- gate. We deliberately do NOT auto-dispatch from the queue form.
            let
                queueEffect =
                    if model.newQueue then
                        [ TransitionAgentTicket
                            { id = ticket.id, from = "draft", to = "queued" }
                        ]

                    else
                        []
            in
            ( { model
                | showNewForm = False
                , creating = False
                , createError = Nothing
                , newTitle = ""
                , newBody = ""
                , newRepo = ""
                , newBranch = ""
                , newBudget = ""
                , newWorkflow = ""
                , newQueue = False
              }
            , effects
                ++ queueEffect
                ++ [ NavigateTo (Routes.toString (Routes.AgentTicket { id = ticket.id })) ]
            )

        AgentTicketCreated (Err _) ->
            ( { model | creating = False, createError = Just "Couldn't create the ticket." }, effects )

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

        AgentDispatcherModeClicked token ->
            -- First click arms the target mode; a second click on the same
            -- action confirms (see ConfirmAgentDispatcherMode). Clicking a
            -- different action re-arms to that one.
            ( { model | armedMode = Just (AgentDispatcher.modeFromString token) }, effects )

        ConfirmAgentDispatcherMode token ->
            ( { model | armedMode = Nothing }
            , effects ++ [ SetAgentDispatcher (AgentDispatcher.modeFromString token) ]
            )

        CancelAgentDispatcherMode ->
            ( { model | armedMode = Nothing }, effects )

        ClickNewAgentTicket ->
            -- Open the form and fetch the workflow list lazily (only when a
            -- user actually opens the form, not on every queue-page load).
            ( { model | showNewForm = True, createError = Nothing }
            , effects ++ [ FetchAgentWorkflows ]
            )

        CancelNewAgentTicket ->
            ( { model | showNewForm = False, createError = Nothing }, effects )

        NewAgentTicketTitleChanged v ->
            ( { model | newTitle = v }, effects )

        NewAgentTicketBodyChanged v ->
            ( { model | newBody = v }, effects )

        NewAgentTicketRepoChanged v ->
            ( { model | newRepo = v }, effects )

        NewAgentTicketBranchChanged v ->
            ( { model | newBranch = v }, effects )

        NewAgentTicketBudgetChanged v ->
            ( { model | newBudget = v }, effects )

        NewAgentTicketWorkflowChanged v ->
            ( { model | newWorkflow = v }, effects )

        NewAgentTicketQueueToggled ->
            ( { model | newQueue = not model.newQueue }, effects )

        SubmitNewAgentTicket ->
            let
                title =
                    String.trim model.newTitle

                repo =
                    String.trim model.newRepo
            in
            if title == "" || repo == "" then
                ( { model | createError = Just "Title and repo are required." }, effects )

            else
                ( { model | creating = True, createError = Nothing }
                , effects
                    ++ [ CreateAgentTicket
                            { title = title
                            , body = model.newBody
                            , repo = repo
                            , targetBranch = String.trim model.newBranch
                            , workflowName = model.newWorkflow
                            , workflowVersion = Nothing
                            , budgetUsd = parseBudget model.newBudget
                            }
                       ]
                )

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
                (Nav.view route
                    :: headerRow model
                    :: dispatcherControls session model
                    ++ [ dispatcherBanner model
                       , content model
                       ]
                )
            ]
        ]


{-| Page title with the live "Auto-dispatch: active/paused/off" status pill.
-}
headerRow : Model -> Html Message
headerRow model =
    Html.div
        [ style "display" "flex"
        , style "align-items" "center"
        , style "gap" "12px"
        , style "flex-wrap" "wrap"
        ]
        (Html.h1 [ style "font-size" "18px", style "margin" "0" ] [ Html.text "Ticket queue" ]
            :: (case model.dispatcher of
                    Just status ->
                        [ dispatcherPill status ]

                    Nothing ->
                        []
               )
        )


{-| Reuse the AgentBadge tone classes for the auto-dispatch pill: active is a
green "good", paused an amber "warn", off/unknown a muted "neutral".
-}
dispatcherToneClass : AgentDispatcher.Mode -> String
dispatcherToneClass mode =
    case mode of
        AgentDispatcher.Active ->
            "agent-badge--good"

        AgentDispatcher.Paused ->
            "agent-badge--warn"

        AgentDispatcher.Off ->
            "agent-badge--neutral"

        AgentDispatcher.Unknown _ ->
            "agent-badge--neutral"


dispatcherPill : AgentDispatcher.Status -> Html Message
dispatcherPill status =
    Html.span
        [ class "agent-badge"
        , class (dispatcherToneClass status.mode)
        , id "dispatcher-status-pill"
        , Html.Attributes.title (dispatcherPillTitle status)
        ]
        [ Html.span [ class "agent-badge__dot" ] []
        , Html.text ("Auto-dispatch: " ++ AgentDispatcher.modeLabel status.mode)
        ]


dispatcherPillTitle : AgentDispatcher.Status -> String
dispatcherPillTitle status =
    let
        srcNote =
            if status.source == "boot-default" then
                " (boot default: " ++ AgentDispatcher.modeLabel status.bootDefault ++ ")"

            else
                ""

        byNote =
            case status.updatedBy of
                Just who ->
                    " · set by " ++ who

                Nothing ->
                    ""

        atNote =
            case status.updatedAt of
                Just when ->
                    " · " ++ when

                Nothing ->
                    ""
    in
    "Auto-dispatch is " ++ AgentDispatcher.modeLabel status.mode ++ srcNote ++ byNote ++ atNote


{-| When auto-dispatch is not active, explain that queued tickets will not run
until it is resumed — and that manual dispatch from a ticket page still works.
-}
dispatcherBanner : Model -> Html Message
dispatcherBanner model =
    case Maybe.map .mode model.dispatcher of
        Just AgentDispatcher.Paused ->
            bannerBox "Auto-dispatch is paused — queued tickets will not run automatically until it is resumed. You can still dispatch any ticket manually from its page."

        Just AgentDispatcher.Off ->
            bannerBox "Auto-dispatch is off — queued tickets will not run automatically, and completed runs are not being reconciled. You can still dispatch any ticket manually from its page."

        _ ->
            Html.text ""


bannerBox : String -> Html Message
bannerBox message =
    Html.div
        [ id "dispatcher-banner"
        , style "margin" "12px 0 0 0"
        , style "padding" "8px 12px"
        , style "border" "1px solid #6a5a1f"
        , style "border-left" "3px solid #d4a72c"
        , style "background" "#2a2410"
        , style "color" "#e8d9a0"
        , style "font-size" "13px"
        ]
        [ Html.text message ]


{-| Admin-only pause/resume/off controls that PUT the dispatcher mode. The
control is rendered only for an admin session; the server independently
enforces the admin tier (a non-admin PUT gets a 403). Uses a two-step
arm/confirm so a mode change is never a single stray click.
-}
dispatcherControls : Session -> Model -> List (Html Message)
dispatcherControls session model =
    case ( isAdmin session, model.dispatcher ) of
        ( True, Just status ) ->
            let
                buttons =
                    [ AgentDispatcher.Active, AgentDispatcher.Paused, AgentDispatcher.Off ]
                        |> List.filter (\m -> m /= status.mode)
                        |> List.map (modeButton model.armedMode)
            in
            [ Html.div
                [ id "dispatcher-controls"
                , style "display" "flex"
                , style "align-items" "center"
                , style "gap" "8px"
                , style "flex-wrap" "wrap"
                , style "margin" "10px 0 0 0"
                ]
                buttons
            ]

        _ ->
            []


modeButton : Maybe AgentDispatcher.Mode -> AgentDispatcher.Mode -> Html Message
modeButton armed target =
    let
        token =
            AgentDispatcher.modeToken target
    in
    if armed == Just target then
        Html.span
            [ style "display" "inline-flex", style "gap" "6px", style "align-items" "center" ]
            [ ctrlButton "primary" (ConfirmAgentDispatcherMode token) ("Confirm: " ++ modeActionLabel target)
            , ctrlButton "secondary" CancelAgentDispatcherMode "Cancel"
            ]

    else
        ctrlButton "secondary" (AgentDispatcherModeClicked token) (modeActionLabel target)


{-| Verb-first label for the control that switches INTO a mode.
-}
modeActionLabel : AgentDispatcher.Mode -> String
modeActionLabel mode =
    case mode of
        AgentDispatcher.Active ->
            "Resume auto-dispatch"

        AgentDispatcher.Paused ->
            "Pause auto-dispatch"

        AgentDispatcher.Off ->
            "Turn off"

        AgentDispatcher.Unknown other ->
            "Set " ++ other


ctrlButton : String -> Message -> String -> Html Message
ctrlButton kind msg label =
    let
        ( bg, fg ) =
            if kind == "primary" then
                ( "#2e4f2e", "#cfe8cf" )

            else
                ( "#2a2929", "#d0d0d0" )
    in
    Html.button
        [ type_ "button"
        , onClick msg
        , style "background" bg
        , style "color" fg
        , style "border" "1px solid #3d3c3c"
        , style "padding" "5px 12px"
        , style "cursor" "pointer"
        , style "font-size" "13px"
        , style "white-space" "nowrap"
        ]
        [ Html.text label ]


isAdmin : Session -> Bool
isAdmin session =
    case session.userState of
        UserStateLoggedIn user ->
            user.isAdmin

        _ ->
            False


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

            -- One pass over the visible tickets; sectionView used to re-filter
            -- the whole list once per section (13 passes per render, and the
            -- page re-renders on every 5s refetch).
            byState =
                groupByState visible

            knownSections =
                List.filterMap (sectionView model byState) sectionOrder

            leftover =
                byState
                    |> Dict.toList
                    |> List.filter (\( state, _ ) -> not (List.member state sectionOrder))
                    |> List.concatMap Tuple.second

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
            (newTicketControls model
                :: controlsBar model
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
        [ sectionBlock (sectionHeader model.costByTicket "other" tickets) (List.map (ticketRow model) (sortTickets model.sortByWait tickets)) ]


sectionView : Model -> Dict String (List AgentTicket.Ticket) -> String -> Maybe (Html Message)
sectionView model byState state =
    case Dict.get state byState of
        Nothing ->
            Nothing

        Just matching ->
            Just (sectionBlock (sectionHeader model.costByTicket (sectionLabel state) matching) (List.map (ticketRow model) (sortTickets model.sortByWait matching)))


{-| Group the visible tickets by lifecycle state, preserving within-state
order (the section sort is stable, so ties keep their fetched order).
-}
groupByState : List AgentTicket.Ticket -> Dict String (List AgentTicket.Ticket)
groupByState tickets =
    tickets
        |> List.foldl
            (\t ->
                Dict.update t.state
                    (\group -> Just (t :: Maybe.withDefault [] group))
            )
            Dict.empty
        |> Dict.map (\_ -> List.reverse)


{-| U10: append a per-section count, e.g. "needs review (2)" (uppercased by CSS).
-}
withCount : String -> List a -> String
withCount label xs =
    label ++ " (" ++ String.fromInt (List.length xs) ++ ")"


{-| W-8: a section header carrying the count plus a spend rollup summed from the
per-ticket costs the rows already display, e.g. "merged (20) · $43.10". Sections
with no spend data (or zero spend) fall back to just the count, as before.
-}
sectionHeader : Dict String Float -> String -> List AgentTicket.Ticket -> String
sectionHeader costs label tickets =
    let
        spend =
            tickets
                |> List.filterMap (\t -> Dict.get (String.fromInt t.id) costs)
                |> List.sum
    in
    if spend > 0 then
        withCount label tickets ++ " · $" ++ formatUsd spend

    else
        withCount label tickets


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


{-| The "New ticket" open button and, when open, the create form. Kept above
the filter/sort controls so the primary write action is the first thing on
the queue page.
-}
newTicketControls : Model -> Html Message
newTicketControls model =
    if model.showNewForm then
        newTicketForm model

    else
        Html.div [ style "margin" "8px 0 0 0" ]
            [ Html.button
                [ class "agent-new-ticket-open"
                , type_ "button"
                , onClick ClickNewAgentTicket
                , style "background" "#2e4f2e"
                , style "color" "#cfe8cf"
                , style "border" "1px solid #3d3c3c"
                , style "padding" "6px 14px"
                , style "cursor" "pointer"
                , style "font-size" "13px"
                ]
                [ Html.text "New ticket" ]
            ]


newTicketForm : Model -> Html Message
newTicketForm model =
    Html.div
        [ class "agent-new-ticket-form"
        , style "border" "1px solid #3d3c3c"
        , style "background" "#1e1d1d"
        , style "padding" "12px"
        , style "margin" "8px 0"
        ]
        [ newFieldLabel "title"
        , Html.input
            (class "agent-new-ticket-title" :: value model.newTitle :: onInput NewAgentTicketTitleChanged :: newInputStyles)
            []
        , newFieldLabel "repo (owner/name — required)"
        , Html.input
            (class "agent-new-ticket-repo" :: value model.newRepo :: placeholder "tdmtrader/concourse" :: onInput NewAgentTicketRepoChanged :: newInputStyles)
            []
        , newFieldLabel "spec (markdown body)"
        , Html.textarea
            (class "agent-new-ticket-body" :: value model.newBody :: onInput NewAgentTicketBodyChanged :: style "min-height" "120px" :: newInputStyles)
            []
        , newFieldLabel "target branch (optional)"
        , Html.input
            (class "agent-new-ticket-branch" :: value model.newBranch :: placeholder "main" :: onInput NewAgentTicketBranchChanged :: newInputStyles)
            []
        , newFieldLabel "budget USD (optional)"
        , Html.input
            (class "agent-new-ticket-budget" :: value model.newBudget :: placeholder "e.g. 5.00" :: onInput NewAgentTicketBudgetChanged :: newInputStyles)
            []
        , newFieldLabel "workflow"
        , workflowPicker model
        , Html.label
            [ style "display" "flex", style "align-items" "center", style "gap" "6px", style "margin" "10px 0 0", style "color" "#b0b0b0", style "font-size" "13px" ]
            [ Html.input
                [ class "agent-new-ticket-queue"
                , type_ "checkbox"
                , Html.Attributes.checked model.newQueue
                , onClick NewAgentTicketQueueToggled
                ]
                []
            , Html.text "queue immediately after creating"
            ]
        , case model.createError of
            Just err ->
                Html.p [ style "color" "#f0a0a0", style "margin" "8px 0 0" ] [ Html.text err ]

            Nothing ->
                Html.text ""
        , Html.div
            [ style "display" "flex", style "gap" "8px", style "margin-top" "10px" ]
            [ Html.button
                [ class "agent-new-ticket-submit"
                , type_ "button"
                , onClick SubmitNewAgentTicket
                , Html.Attributes.disabled model.creating
                , style "background" "#2e4f2e"
                , style "color" "#cfe8cf"
                , style "border" "1px solid #3d3c3c"
                , style "padding" "5px 12px"
                , style "cursor" "pointer"
                , style "font-size" "13px"
                ]
                [ Html.text
                    (if model.creating then
                        "Creating…"

                     else
                        "Create ticket"
                    )
                ]
            , Html.button
                [ type_ "button"
                , onClick CancelNewAgentTicket
                , style "background" "#2a2929"
                , style "color" "#d0d0d0"
                , style "border" "1px solid #3d3c3c"
                , style "padding" "5px 12px"
                , style "cursor" "pointer"
                , style "font-size" "13px"
                ]
                [ Html.text "Cancel" ]
            ]
        ]


{-| Workflow `<select>` populated from the lazily-fetched workflow list. The
empty option leaves `workflow_name` unset so dispatch resolves the live
version later (the ticket freezes a version at dispatch, not at create).
-}
workflowPicker : Model -> Html Message
workflowPicker model =
    Html.select
        [ class "agent-new-ticket-workflow"
        , Html.Events.onInput NewAgentTicketWorkflowChanged
        , style "width" "100%"
        , style "background" "#141313"
        , style "color" "#e0e0e0"
        , style "border" "1px solid #3d3c3c"
        , style "padding" "6px 8px"
        , style "box-sizing" "border-box"
        ]
        (Html.option
            [ value "", Html.Attributes.selected (model.newWorkflow == "") ]
            [ Html.text "(decide at dispatch)" ]
            :: List.map
                (\w ->
                    Html.option
                        [ value w.name, Html.Attributes.selected (model.newWorkflow == w.name) ]
                        [ Html.text w.name ]
                )
                model.workflows
        )


newFieldLabel : String -> Html Message
newFieldLabel txt =
    Html.div
        [ style "font-size" "11px", style "text-transform" "uppercase", style "letter-spacing" "0.08em", style "color" "#9aa39b", style "margin" "8px 0 4px" ]
        [ Html.text txt ]


newInputStyles : List (Html.Attribute Message)
newInputStyles =
    [ style "width" "100%"
    , style "background" "#141313"
    , style "color" "#e0e0e0"
    , style "border" "1px solid #3d3c3c"
    , style "padding" "6px 8px"
    , style "box-sizing" "border-box"
    ]


parseBudget : String -> Maybe Float
parseBudget raw =
    case String.trim raw of
        "" ->
            Nothing

        trimmed ->
            String.toFloat trimmed


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
