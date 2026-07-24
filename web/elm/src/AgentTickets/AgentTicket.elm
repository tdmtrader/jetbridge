module AgentTickets.AgentTicket exposing
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

{-| The agent-ticket DETAIL page (a PR-style view for one ticket).

The ticket is a `work-item/v1` projection shell, never an execution identity:
it renders the ticket header, an editable title/body/budget form, the lifecycle
action buttons (dispatch / transition), the spec & plan tabs, and the task list.
All execution evidence — the agent review, the repository-change diff, run cost
and outcome — belongs to the ticket's durable workflow run, and is reached only
by links out to that run and its promoted output snapshots (see
`durableEvidenceLine`); it is never embedded or recomputed here. The server
state machine stays authoritative: buttons only pick which transition to
_offer_, and a rejected transition (409) surfaces inline and triggers a refetch.

-}

import AgentBadge
import Application.Models exposing (Session)
import Concourse.AgentTicket as AgentTicket
import Concourse.WorkflowRun as WorkflowRun
import DateFormat
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, href, id, placeholder, style, value)
import Html.Events exposing (on, onClick, onInput)
import Html.Lazy
import Json.Decode
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery, Interval(..), Subscription)
import Polling
import Routes
import SideBar.SideBar as SideBar
import Time
import Tooltip
import Views.Prose
import Views.Styles
import Views.TopBar as TopBar


type Tab
    = SpecTab
    | PlanTab


type alias Model =
    Login.Model
        { ticketId : Int
        , detail : Maybe AgentTicket.Detail
        , durableRun : Maybe WorkflowRun.Detail
        , activeTab : Tab
        , loaded : Bool
        , loadError : Bool
        , actionError : Maybe String
        , editing : Bool
        , editTitle : String
        , editBody : String
        , editBudget : String
        , dispatchConfirm : Bool
        , pendingTransition : Maybe String
        }


init : { id : Int } -> ( Model, List Effect )
init { id } =
    ( { ticketId = id
      , detail = Nothing
      , durableRun = Nothing
      , activeTab = SpecTab
      , loaded = False
      , loadError = False
      , actionError = Nothing
      , editing = False
      , editTitle = ""
      , editBody = ""
      , editBudget = ""
      , dispatchConfirm = False
      , pendingTransition = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTicket id ]
    )


documentTitle : Model -> String
documentTitle model =
    case model.detail of
        Just d ->
            "#" ++ String.fromInt model.ticketId ++ " " ++ d.ticket.title

        Nothing ->
            "Ticket #" ++ String.fromInt model.ticketId


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentTicketFetched (Ok fresh) ->
            -- Only replace fetched data. The edit buffers are deliberately
            -- not written here: they are seeded when the user clicks Edit
            -- (see ClickAgentTicketEdit), so the 5s self-heal refetch cannot
            -- silently revert unsaved typing — a guard this callback once
            -- forgot, clobbering an open edit every few seconds.
            let
                -- Keep the previously installed record when the refetch decoded
                -- identical data: Html.Lazy compares arguments by reference, so
                -- installing an equal-but-fresh record every 5s would defeat
                -- every lazy view below.
                detail =
                    case model.detail of
                        Just old ->
                            if old == fresh then
                                old

                            else
                                fresh

                        Nothing ->
                            fresh

                stateChanged =
                    model.detail
                        |> Maybe.map (\old -> old.ticket.state /= detail.ticket.state)
                        |> Maybe.withDefault False

                -- The edit form is suppressed for terminal states, so if the
                -- ticket goes terminal under an open edit the form would
                -- silently vanish with `editing` stuck True and no Cancel left
                -- to reach — exit the edit explicitly and say why.
                editKilledByTerminal =
                    model.editing && isTerminal detail.ticket.state

                currentDurableKey =
                    model.detail
                        |> Maybe.andThen (.ticket >> durableKey)

                freshDurableKey =
                    durableKey detail.ticket

                durableRun =
                    if currentDurableKey == freshDurableKey then
                        case ( freshDurableKey, model.durableRun ) of
                            ( Just ( workflowName, workflowRunId ), Just run ) ->
                                if
                                    run.summary.workflowName
                                        == workflowName
                                        && run.summary.id
                                        == workflowRunId
                                then
                                    model.durableRun

                                else
                                    Nothing

                            _ ->
                                Nothing

                    else
                        Nothing
            in
            ( { model
                | detail = Just detail
                , durableRun = durableRun
                , loaded = True
                , loadError = False

                -- An armed-but-unconfirmed transition (or dispatch) was a
                -- decision about the PREVIOUS state; if the state changed
                -- underneath it, disarm so the user re-decides against the
                -- fresh state. (Confirm racing ahead of this refetch is safe:
                -- it posts the old state as `from`, which the server's CAS
                -- rejects with a 409.)
                , pendingTransition =
                    if stateChanged then
                        Nothing

                    else
                        model.pendingTransition
                , dispatchConfirm = model.dispatchConfirm && not stateChanged
                , editing = model.editing && not editKilledByTerminal
                , actionError =
                    if editKilledByTerminal then
                        Just
                            ("Ticket moved to \""
                                ++ detail.ticket.state
                                ++ "\" while you were editing — unsaved changes were discarded."
                            )

                    else
                        model.actionError
              }
            , effects
                ++ (case freshDurableKey of
                        Just ( workflowName, workflowRunId ) ->
                            [ FetchAgentWorkflowRun workflowName workflowRunId ]

                        Nothing ->
                            []
                   )
            )

        AgentTicketFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        AgentWorkflowRunFetched workflowRunId (Ok detail) ->
            case model.detail |> Maybe.andThen (.ticket >> durableKey) of
                Just ( expectedWorkflowName, expectedWorkflowRunId ) ->
                    if
                        expectedWorkflowRunId
                            == workflowRunId
                            && detail.summary.id
                            == expectedWorkflowRunId
                            && detail.summary.workflowName
                            == expectedWorkflowName
                    then
                        ( { model | durableRun = Just detail }, effects )

                    else
                        ( model, effects )

                Nothing ->
                    ( model, effects )

        AgentWorkflowRunFetched _ (Err _) ->
            ( model, effects )

        AgentTicketSaved _ (Ok ()) ->
            ( { model | editing = False, actionError = Nothing }
            , effects ++ [ FetchAgentTicket model.ticketId ]
            )

        AgentTicketSaved _ (Err _) ->
            ( { model | actionError = Just "Couldn't save changes." }, effects )

        AgentTicketTransitioned _ (Ok ()) ->
            ( { model | actionError = Nothing }
            , effects ++ [ FetchAgentTicket model.ticketId ]
            )

        AgentTicketTransitioned _ (Err _) ->
            ( { model | actionError = Just "Transition rejected — the ticket state may have changed. Refreshing…" }
            , effects ++ [ FetchAgentTicket model.ticketId ]
            )

        AgentTicketDispatched _ (Ok _) ->
            ( { model | actionError = Nothing }
            , effects ++ [ FetchAgentTicket model.ticketId ]
            )

        AgentTicketDispatched _ (Err _) ->
            ( { model | actionError = Just "Dispatch failed." }, effects )

        _ ->
            ( model, effects )


{-| U11: live-update on the dashboard's 5s cadence so state, spend and runs
stay current (a page was showing "Running" ~20 min after the ticket errored).
Once the ticket is terminal nothing can change server-side (no dispatch,
frozen runs/costs), so stop polling instead of refetching identical bytes
for the life of the tab; keep polling while detail is still unknown.
-}
polls : List (Polling.Poll Model)
polls =
    [ { interval = FiveSeconds
      , fetch =
            \model ->
                let
                    settled =
                        model.detail
                            |> Maybe.map (\d -> isTerminal d.ticket.state)
                            |> Maybe.withDefault False
                in
                if settled then
                    []

                else
                    [ FetchAgentTicket model.ticketId ]
      }
    ]


handleDelivery : Delivery -> ET Model
handleDelivery =
    Polling.handleDelivery polls


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        AgentTicketTabClicked tab ->
            ( { model | activeTab = tabFromString tab }, effects )

        ClickAgentTicketEdit ->
            -- Entering edit seeds the buffers from the last-fetched ticket,
            -- once. The fetch callback never writes them (see
            -- AgentTicketFetched), so a future ticket field can't
            -- reintroduce the refetch-clobbers-typing bug by forgetting an
            -- `if editing` guard.
            case model.detail of
                Just { ticket } ->
                    ( { model
                        | editing = True
                        , actionError = Nothing
                        , editTitle = ticket.title
                        , editBody = ticket.body
                        , editBudget =
                            ticket.budgetUsd
                                |> Maybe.map String.fromFloat
                                |> Maybe.withDefault ""
                      }
                    , effects
                    )

                Nothing ->
                    -- Nothing fetched yet, nothing to edit (the Edit button
                    -- only renders once the detail is loaded).
                    ( model, effects )

        AgentTicketTitleChanged v ->
            ( { model | editTitle = v }, effects )

        AgentTicketBodyChanged v ->
            ( { model | editBody = v }, effects )

        AgentTicketBudgetChanged v ->
            ( { model | editBudget = v }, effects )

        ClickAgentTicketCancel ->
            ( { model | editing = False }, effects )

        ClickAgentTicketSave ->
            ( model
            , effects
                ++ [ SaveAgentTicket
                        { id = model.ticketId
                        , title = model.editTitle
                        , body = model.editBody
                        , budgetUsd = parseBudget model.editBudget
                        }
                   ]
            )

        ClickAgentTicketTransition to ->
            -- Two-step confirm: first click arms the disposition (naming the
            -- action), a second click commits it. Mirrors the dispatch confirm.
            ( { model | pendingTransition = Just to, actionError = Nothing }, effects )

        ConfirmAgentTicketTransition ->
            ( { model | pendingTransition = Nothing, actionError = Nothing }
            , case ( model.detail, model.pendingTransition ) of
                ( Just d, Just to ) ->
                    effects ++ [ TransitionAgentTicket { id = model.ticketId, from = d.ticket.state, to = to } ]

                _ ->
                    effects
            )

        CancelAgentTicketTransition ->
            ( { model | pendingTransition = Nothing }, effects )

        ClickAgentTicketDispatch ->
            ( { model | dispatchConfirm = True, actionError = Nothing }, effects )

        ConfirmAgentTicketDispatch ->
            ( { model | dispatchConfirm = False }
            , effects ++ [ DispatchAgentTicket model.ticketId ]
            )

        CancelAgentTicketDispatch ->
            ( { model | dispatchConfirm = False }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls



-- VIEW


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentTicket { id = model.ticketId }
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
                [ style "padding" "16px", style "width" "100%", style "max-width" "900px" ]
                [ content session model ]
            ]
        ]


content : Session -> Model -> Html Message
content session model =
    if model.loadError then
        Html.p [ style "color" "#f0a0a0" ] [ Html.text "Couldn't load ticket." ]

    else
        case model.detail of
            Nothing ->
                Html.p [ style "color" "#b0b0b0" ] [ Html.text "Loading…" ]

            Just detail ->
                let
                    ticket =
                        detail.ticket

                    -- The ticket page is a projection shell: it renders ticket
                    -- content and the human queue/dispatch/disposition controls,
                    -- and links out to the durable workflow run for all execution
                    -- evidence (review, repository-change, cost). It never embeds
                    -- that evidence itself.
                    top =
                        [ header model ticket
                        , provenanceLine ticket
                        , durableEvidenceLine model ticket
                        , provenanceTimestamps session.timeZone ticket
                        , errorNotice ticket
                        , actionErrorBanner model
                        ]

                    -- The heavy sub-views render lazily: their arguments are
                    -- reference-stable across the 5s self-heal refetch (see
                    -- handleCallback), so a tick that changed nothing skips
                    -- them entirely.
                    rest =
                        [ editForm model ticket
                        , Html.div [ id "ticket-hitl-slot" ] []
                        , tabsBar model
                        , Html.Lazy.lazy2 tabContent model.activeTab detail
                        , Html.Lazy.lazy taskList detail.tasks
                        ]
                in
                Html.div []
                    (top ++ [ lifecycleBar model ticket ] ++ rest)


header : Model -> AgentTicket.Ticket -> Html Message
header model ticket =
    Html.div
        [ style "display" "flex", style "align-items" "flex-start", style "gap" "12px", style "margin-bottom" "4px" ]
        [ Html.span
            [ style "font-family" "monospace", style "color" "#9aa39b", style "flex-shrink" "0", style "padding-top" "3px" ]
            [ Html.text ("#" ++ String.fromInt ticket.id) ]
        , Html.span [ style "flex-shrink" "0", style "padding-top" "1px" ] [ stateBadge ticket.state ]
        , Html.h1
            [ style "font-size" "18px"
            , style "margin" "0"
            , style "flex" "1"
            , style "min-width" "0"
            , style "text-align" "left"
            , style "overflow-wrap" "anywhere"
            , style "word-break" "break-word"
            ]
            [ Html.text ticket.title ]
        , if model.editing || isTerminal ticket.state then
            Html.text ""

          else
            actionButton "secondary" ClickAgentTicketEdit "Edit"
        ]


{-| Where this ticket's work lives: the repository selection, and the branch
name for orientation. The reviewable diff is not computed here — it lives on
the durable workflow run's repository-change output, linked from
`durableEvidenceLine`. The repo degrades to plain text when the field can't be
resolved to a web URL.
-}
provenanceLine : AgentTicket.Ticket -> Html Message
provenanceLine ticket =
    if ticket.repo == "" && ticket.branch == "" then
        Html.text ""

    else
        let
            linkStyle =
                [ style "color" "#7aa37a", style "text-decoration" "none" ]

            repoPart =
                case AgentTicket.repoWebUrl ticket.repo of
                    Just url ->
                        [ Html.a (href url :: linkStyle) [ Html.text ticket.repo ] ]

                    Nothing ->
                        if ticket.repo == "" then
                            []

                        else
                            [ Html.text ticket.repo ]

            branchPart =
                if ticket.branch == "" then
                    []

                else
                    [ Html.text (" · branch " ++ ticket.branch) ]
        in
        Html.div
            [ id "ticket-provenance"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" "#9aa39b"
            , style "margin" "2px 0 8px 0"
            ]
            (repoPart ++ branchPart)


durableEvidenceLine : Model -> AgentTicket.Ticket -> Html Message
durableEvidenceLine model ticket =
    let
        itemLink label maybeId =
            maybeId
                |> Maybe.map
                    (\snapshotId ->
                        Html.a
                            [ href (Routes.toString (Routes.AgentSnapshot { id = snapshotId }))
                            , style "color" "#7a9ac0"
                            ]
                            [ Html.text (label ++ " #" ++ snapshotId) ]
                    )

        runLink =
            case durableKey ticket of
                Just ( workflowName, runId ) ->
                    Just
                        (Html.a
                            [ href
                                (Routes.toString
                                    (Routes.AgentWorkflowRun
                                        { workflowName = workflowName, id = runId }
                                    )
                                )
                            , style "color" "#7a9ac0"
                            ]
                            [ Html.text ("workflow run #" ++ runId) ]
                        )

                Nothing ->
                    Nothing

        outputs =
            case ( durableKey ticket, model.durableRun ) of
                ( Just ( workflowName, workflowRunId ), Just detail ) ->
                    if
                        detail.summary.workflowName
                            == workflowName
                            && detail.summary.id
                            == workflowRunId
                    then
                        detail.outputs

                    else
                        []

                _ ->
                    []

        outputLinks =
            outputs
                |> List.map
                    (\output ->
                        Html.a
                            [ href (Routes.toString (Routes.AgentSnapshot { id = output.snapshot.id }))
                            , style "color" "#7a9ac0"
                            ]
                            [ Html.text (output.portName ++ " " ++ output.snapshot.typeRef ++ " #" ++ output.snapshot.id) ]
                    )

        links =
            List.filterMap identity
                [ itemLink "captured repository" ticket.repositorySnapshotId
                , itemLink "captured ticket revision" ticket.workItemSnapshotId
                , runLink
                ]
                ++ outputLinks
    in
    if List.isEmpty links then
        Html.text ""

    else
        Html.div
            [ id "ticket-durable-evidence"
            , style "display" "flex"
            , style "flex-wrap" "wrap"
            , style "gap" "10px"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "margin" "2px 0 8px"
            ]
            links


{-| Created / updated / completed provenance. The epoch-seconds fields ride on
every ticket but were rendered nowhere; surface them as a small absolute-time
line (UTC-offset by the viewer's zone). Updated is suppressed when it matches
created, and completed only shows once the ticket has finished.
-}
provenanceTimestamps : Time.Zone -> AgentTicket.Ticket -> Html Message
provenanceTimestamps zone ticket =
    let
        parts =
            List.filterMap identity
                [ if ticket.createdAt > 0 then
                    Just ("created " ++ formatTimestamp zone ticket.createdAt)

                  else
                    Nothing
                , if ticket.updatedAt > 0 && ticket.updatedAt /= ticket.createdAt then
                    Just ("updated " ++ formatTimestamp zone ticket.updatedAt)

                  else
                    Nothing
                , ticket.completedAt
                    |> Maybe.map (\c -> "completed " ++ formatTimestamp zone c)
                ]
    in
    if List.isEmpty parts then
        Html.text ""

    else
        Html.div
            [ id "ticket-timestamps"
            , style "font-size" "11px"
            , style "color" "#7a7a7a"
            , style "margin" "0 0 8px 0"
            ]
            [ Html.text (String.join " · " parts) ]


{-| U4: when a run errored the server records the failure text on the ticket
(`error_detail`), but it was never shown. Surface it prominently, right above
the Retry action, so the reviewer knows what to fix before re-queueing.
-}
errorNotice : AgentTicket.Ticket -> Html Message
errorNotice ticket =
    if ticket.errorDetail == "" then
        Html.text ""

    else
        Html.div
            [ id "ticket-error-detail"
            , style "border" "1px solid #7a3a3a"
            , style "background" "#2a1c1c"
            , style "color" "#f0a0a0"
            , style "padding" "10px 12px"
            , style "margin" "10px 0"
            ]
            [ Html.div
                [ style "font-weight" "bold", style "font-size" "12px", style "margin-bottom" "4px" ]
                [ Html.text "Run error" ]
            , Html.div
                [ style "white-space" "pre-wrap"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "line-height" "1.4"
                ]
                [ Html.text ticket.errorDetail ]
            ]


stateBadge : String -> Html Message
stateBadge state =
    case AgentBadge.fromApiToken state of
        Just status ->
            AgentBadge.view status

        Nothing ->
            Html.span [ style "color" "#b0b0b0" ] [ Html.text state ]


actionErrorBanner : Model -> Html Message
actionErrorBanner model =
    case model.actionError of
        Just err ->
            Html.p
                [ style "color" "#f0a0a0", style "background" "#2a1c1c", style "padding" "6px 10px", style "margin" "8px 0" ]
                [ Html.text err ]

        Nothing ->
            Html.text ""


{-| Human lifecycle actions. The buttons offered depend on the current state,
but the server is the authority: an illegal/stale transition returns 409 and is
surfaced via `actionError`, then the ticket is refetched.
-}
lifecycleBar : Model -> AgentTicket.Ticket -> Html Message
lifecycleBar model ticket =
    let
        transitions =
            transitionTargets ticket.state

        dispatchControls =
            if canDispatch ticket.state then
                if model.dispatchConfirm then
                    [ Html.span [ style "color" "#b0b0b0" ] [ Html.text "Dispatch a run now?" ]
                    , actionButton "primary" ConfirmAgentTicketDispatch "Confirm dispatch"
                    , actionButton "secondary" CancelAgentTicketDispatch "Cancel"
                    ]

                else
                    [ actionButton "primary" ClickAgentTicketDispatch "Dispatch run" ]

            else
                []

        transitionButtons =
            case model.pendingTransition of
                Just to ->
                    let
                        pendingLabel =
                            transitions
                                |> List.filter (\( t, _ ) -> t == to)
                                |> List.head
                                |> Maybe.map Tuple.second
                                |> Maybe.withDefault to
                    in
                    [ Html.span [ style "color" "#b0b0b0" ] [ Html.text (pendingLabel ++ " this ticket?") ]
                    , actionButton "primary" ConfirmAgentTicketTransition ("Confirm " ++ String.toLower pendingLabel)
                    , actionButton "secondary" CancelAgentTicketTransition "Cancel"
                    ]

                Nothing ->
                    transitions
                        |> List.map (\( to, label ) -> actionButton "secondary" (ClickAgentTicketTransition to) label)
    in
    if List.isEmpty dispatchControls && List.isEmpty transitionButtons then
        Html.text ""

    else
        Html.div
            [ style "display" "flex", style "flex-wrap" "wrap", style "align-items" "center", style "gap" "8px", style "margin" "10px 0" ]
            (dispatchControls ++ transitionButtons)


canDispatch : String -> Bool
canDispatch state =
    state == "queued"


{-| U21: terminal states have no outgoing human transition (see the doc on
`transitionTargets`), so editing them is meaningless — the Edit affordance and
the edit form are both suppressed. `sent_back` is deliberately excluded: it is
re-queueable, and the author is expected to revise before re-queueing.
-}
isTerminal : String -> Bool
isTerminal state =
    List.member state
        [ "merged", "merged_with_fixes", "concluded", "abandoned" ]


{-| The transitions a human may drive from a given state, as (target, label).
Mirrors the server's `validTransitions` map (agent/api/tickets/types.go) — only
legal, human-initiated edges are offered. `running`'s edges (→queued/needs\_review/
failed/errored) are all system-driven, so it offers nothing. Terminal states
(merged, merged\_with\_fixes, abandoned, concluded) also offer nothing. The server
stays authoritative and rejects anything stale with a 409.
-}
transitionTargets : String -> List ( String, String )
transitionTargets state =
    case state of
        "needs_review" ->
            [ ( "merged", "Merge" )
            , ( "merged_with_fixes", "Merge with fixes" )
            , ( "sent_back", "Send back" )
            , ( "concluded", "Conclude" )
            , ( "abandoned", "Abandon" )
            ]

        "draft" ->
            [ ( "queued", "Queue" ), ( "abandoned", "Abandon" ) ]

        "queued" ->
            [ ( "draft", "Unqueue" ), ( "abandoned", "Abandon" ) ]

        "sent_back" ->
            [ ( "queued", "Re-queue" ) ]

        "failed" ->
            [ ( "queued", "Retry" ), ( "abandoned", "Abandon" ) ]

        "errored" ->
            [ ( "queued", "Retry" ), ( "abandoned", "Abandon" ) ]

        _ ->
            []


editForm : Model -> AgentTicket.Ticket -> Html Message
editForm model ticket =
    if not model.editing || isTerminal ticket.state then
        Html.text ""

    else
        Html.div
            [ style "border" "1px solid #3d3c3c", style "padding" "12px", style "margin" "10px 0", style "background" "#1e1d1d" ]
            [ formLabel "title"
            , Html.input
                (value model.editTitle :: onInput AgentTicketTitleChanged :: inputStyles)
                []
            , formLabel "body"
            , Html.textarea
                (value model.editBody :: onInput AgentTicketBodyChanged :: style "min-height" "120px" :: inputStyles)
                []
            , formLabel "budget (USD)"
            , Html.input
                (value model.editBudget :: placeholder "e.g. 5.00" :: onInput AgentTicketBudgetChanged :: inputStyles)
                []
            , Html.div
                [ style "display" "flex", style "gap" "8px", style "margin-top" "10px" ]
                [ actionButton "primary" ClickAgentTicketSave "Save"
                , actionButton "secondary" ClickAgentTicketCancel "Cancel"
                ]
            ]


formLabel : String -> Html Message
formLabel txt =
    Html.div
        [ style "font-size" "11px", style "text-transform" "uppercase", style "letter-spacing" "0.08em", style "color" "#9aa39b", style "margin" "8px 0 4px" ]
        [ Html.text txt ]


inputStyles : List (Html.Attribute Message)
inputStyles =
    [ style "width" "100%"
    , style "background" "#141313"
    , style "color" "#e0e0e0"
    , style "border" "1px solid #3d3c3c"
    , style "padding" "6px 8px"
    , style "box-sizing" "border-box"
    ]


tabsBar : Model -> Html Message
tabsBar model =
    Html.div
        [ attribute "role" "tablist"
        , style "display" "flex"
        , style "gap" "0"
        , style "border-bottom" "1px solid #3d3c3c"
        , style "margin" "16px 0 0"
        ]
        [ tabButton model SpecTab "spec" "Spec"
        , tabButton model PlanTab "plan" "Plan"
        ]


tabButton : Model -> Tab -> String -> String -> Html Message
tabButton model tab token label =
    let
        active =
            model.activeTab == tab
    in
    Html.div
        [ class "agent-ticket-tab"
        , attribute "role" "tab"
        , attribute "aria-selected"
            (if active then
                "true"

             else
                "false"
            )
        , attribute "tabindex" "0"
        , style "padding" "6px 14px"
        , style "cursor" "pointer"
        , style "border-bottom"
            (if active then
                "2px solid #7aa37a"

             else
                "2px solid transparent"
            )
        , style "color"
            (if active then
                "#e0e0e0"

             else
                "#9aa39b"
            )
        , onClick (AgentTicketTabClicked token)
        , onActivationKey (AgentTicketTabClicked token)
        ]
        [ Html.text label ]


{-| Enter / Space activates a `role="tab"` (or any keyboard-operable) control,
firing the same message a click would.
-}
onActivationKey : Message -> Html.Attribute Message
onActivationKey msg =
    on "keydown"
        (Html.Events.keyCode
            |> Json.Decode.andThen
                (\code ->
                    if code == 13 || code == 32 then
                        Json.Decode.succeed msg

                    else
                        Json.Decode.fail "not an activation key"
                )
        )


{-| Takes the active tab rather than the whole model so the caller can wrap it
in Html.Lazy: both arguments are reference-stable when nothing changed, where
the model record is rebuilt by every update.
-}
tabContent : Tab -> AgentTicket.Detail -> Html Message
tabContent activeTab detail =
    Html.div [ style "padding" "12px 0" ]
        [ case activeTab of
            SpecTab ->
                specView detail

            PlanTab ->
                planView detail
        ]


specView : AgentTicket.Detail -> Html Message
specView detail =
    case detail.spec of
        Just spec ->
            Html.div []
                [ prose spec.body
                , if List.isEmpty spec.acceptanceCriteria then
                    Html.text ""

                  else
                    Html.div []
                        [ formLabel "acceptance criteria"
                        , Html.ul [ style "margin" "4px 0", style "padding-left" "20px" ]
                            (List.map (\c -> Html.li [ style "color" "#b0b0b0" ] [ Html.text c ]) spec.acceptanceCriteria)
                        ]
                ]

        Nothing ->
            -- U18b: with no formal spec, promote the ticket body as the spec
            -- content instead of stacking an empty-state notice above it. The
            -- notice only shows when there is genuinely nothing to read.
            if String.trim detail.ticket.body == "" then
                Html.p [ style "color" "#9aa39b" ] [ Html.text "No spec submitted yet." ]

            else
                prose detail.ticket.body


planView : AgentTicket.Detail -> Html Message
planView detail =
    if List.isEmpty detail.tasks then
        Html.p [ style "color" "#9aa39b" ] [ Html.text "No plan yet." ]

    else
        prose detail.ticket.body


{-| Render the ticket/spec body as light prose (paragraphs, inline `code` and
**bold**) via the shared Views.Prose renderer — no markdown dependency.
Lazy on the body string, so the full-text tokenization re-runs only when the
text itself changes, not on every 5s-refetch render.
-}
prose : String -> Html Message
prose =
    Html.Lazy.lazy Views.Prose.view


taskList : List AgentTicket.Task -> Html Message
taskList tasks =
    if List.isEmpty tasks then
        Html.text ""

    else
        Html.div [ style "margin" "12px 0" ]
            (formLabel "tasks"
                :: List.map taskRow (List.sortBy .ordering tasks)
            )


taskRow : AgentTicket.Task -> Html Message
taskRow task =
    Html.div
        [ style "display" "flex", style "align-items" "baseline", style "gap" "10px", style "padding" "4px 0", style "border-bottom" "1px solid #2a2929" ]
        [ Html.span [ style "font-family" "monospace", style "color" "#7a7a7a", style "min-width" "24px" ]
            [ Html.text (String.fromInt task.ordering) ]
        , Html.span [ style "color" (taskStatusColor task.status), style "min-width" "80px", style "font-size" "12px" ]
            [ Html.text task.status ]
        , Html.span [ style "flex" "1", style "color" "#d0d0d0" ] [ Html.text task.title ]
        ]


taskStatusColor : String -> String
taskStatusColor status =
    case status of
        "done" ->
            "#7aa37a"

        "in_progress" ->
            "#d0c07a"

        "failed" ->
            "#f0a0a0"

        _ ->
            "#9aa39b"


actionButton : String -> Message -> String -> Html Message
actionButton kind msg label =
    let
        ( bg, fg ) =
            if kind == "primary" then
                ( "#2e4f2e", "#cfe8cf" )

            else
                ( "#2a2929", "#d0d0d0" )
    in
    Html.button
        [ Html.Attributes.type_ "button"
        , onClick msg
        , style "background" bg
        , style "color" fg
        , style "border" "1px solid #3d3c3c"
        , style "padding" "5px 12px"
        , style "cursor" "pointer"
        , style "font-size" "13px"
        ]
        [ Html.text label ]



-- HELPERS


durableKey : AgentTicket.Ticket -> Maybe ( String, String )
durableKey ticket =
    case ticket.workflowRunId of
        Just workflowRunId ->
            if String.trim ticket.workflowName == "" then
                Nothing

            else
                Just ( ticket.workflowName, workflowRunId )

        Nothing ->
            Nothing


tabFromString : String -> Tab
tabFromString tab =
    case tab of
        "plan" ->
            PlanTab

        _ ->
            SpecTab


parseBudget : String -> Maybe Float
parseBudget raw =
    case String.trim raw of
        "" ->
            Nothing

        trimmed ->
            String.toFloat trimmed


{-| Format an epoch-seconds timestamp as a compact absolute time in the
viewer's zone, e.g. "Jul 18, 2026 14:30". Absolute (not relative) because the
page has a zone but no live "now" clock to diff against.
-}
formatTimestamp : Time.Zone -> Int -> String
formatTimestamp zone epochSeconds =
    DateFormat.format
        [ DateFormat.monthNameAbbreviated
        , DateFormat.text " "
        , DateFormat.dayOfMonthNumber
        , DateFormat.text ", "
        , DateFormat.yearNumber
        , DateFormat.text " "
        , DateFormat.hourMilitaryFixed
        , DateFormat.text ":"
        , DateFormat.minuteFixed
        ]
        zone
        (Time.millisToPosix (epochSeconds * 1000))
