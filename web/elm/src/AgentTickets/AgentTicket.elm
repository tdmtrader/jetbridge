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

{-| The agent-ticket DETAIL page (a queue-slot view for one ticket).

The ticket is a `work-item/v1` projection shell, never an execution identity:
it renders the ticket header, an editable title/body form, the QUEUE actions
(dispatch / queue / close), and the ticket's markdown body — that is the whole
of its own content. All execution evidence — the agent review, the
repository-change diff, run cost, and above all the run's OUTCOME and
DISPOSITION — belongs to the ticket's durable workflow run and is read from the
run data this page already fetches (see `durableEvidenceLine` and
`runOutcomeChip`); it is never mirrored onto the ticket. The server state
machine stays authoritative: buttons only pick which transition to _offer_, and
a rejected transition (409) surfaces inline and triggers a refetch.

Dispatch is the one action that produces something new, and what it produces is
a durable workflow run — so a clean dispatch hands the user straight to that
run's page rather than leaving them on a ticket that looks unchanged (see
`handleCallback`'s `AgentTicketDispatched` and `dispatchNoticeBanner`).

-}

import AgentBadge
import AgentPage.Chrome as Chrome
import AgentTicket.Journal as Journal
import Application.Models exposing (Session)
import Concourse.AgentTicket as AgentTicket
import Concourse.Timestamp as Timestamp
import Concourse.WorkflowRun as WorkflowRun
import DateFormat
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (href, id, style, value)
import Html.Events exposing (onClick, onInput)
import Html.Lazy
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Polling
import Routes
import Time
import Tooltip
import Views.Prose


type alias Model =
    Login.Model
        { ticketId : Int
        , detail : Maybe AgentTicket.Detail
        , durableRun : Maybe WorkflowRun.Detail
        , loaded : Bool
        , loadError : Bool
        , actionError : Maybe String
        , editing : Bool
        , editTitle : String
        , editBody : String
        , dispatchConfirm : Bool
        , dispatchNotice : Maybe DispatchNotice
        , pendingTransition : Maybe String

        {- The ticket's cross-workflow journal: every associated run
           occurrence in order. A ticket that has driven several workflows, or
           the same workflow twice, has a history the single `durableRun` link
           cannot express — that link is the CURRENT dispatch attempt, this is
           the whole story.
        -}
        , journal : List AgentTicket.JournalEntry
        , now : Maybe Time.Posix
        }


{-| What a dispatch that could NOT simply hand the user to its run has to say.

A clean dispatch navigates straight to the durable run it created — that run is
the thing the user asked for, and the ticket page has nothing new to show. When
the server attaches warnings (or the ticket carries no workflow name, so there
is no run route to build), navigation would throw that away, so the page holds
still and renders this instead, with a link on to the run.

-}
type alias DispatchNotice =
    { workflowRunId : String
    , workflowName : String
    , warnings : List String
    }


init : { id : Int } -> ( Model, List Effect )
init { id } =
    ( { ticketId = id
      , detail = Nothing
      , durableRun = Nothing
      , loaded = False
      , loadError = False
      , actionError = Nothing
      , editing = False
      , editTitle = ""
      , editBody = ""
      , dispatchConfirm = False
      , dispatchNotice = Nothing
      , pendingTransition = Nothing
      , journal = []
      , now = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTicket id, FetchAgentTicketRuns id ]
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

        AgentTicketRunsFetched (Ok journal) ->
            -- Keep the installed list when a refetch decoded identical data:
            -- Html.Lazy compares by reference, so replacing an equal list
            -- every 5s would defeat the lazy journal view.
            ( { model
                | journal =
                    if model.journal == journal then
                        model.journal

                    else
                        journal
              }
            , effects
            )

        AgentTicketRunsFetched (Err _) ->
            -- The journal is supporting context, not the page. A failed
            -- journal read leaves the last good history on screen rather than
            -- blanking a ticket that loaded perfectly well.
            ( model, effects )

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

        -- Dispatch's one product is a durable workflow run, and the run page is
        -- where every subsequent truth about it lives. The old code decoded the
        -- response and threw it away (`Ok _`), leaving the user on an unchanged
        -- ticket with no clue what had just been created, so take them there.
        AgentTicketDispatched _ (Ok result) ->
            let
                workflowName =
                    model.detail
                        |> Maybe.map (.ticket >> .workflowName >> String.trim)
                        |> Maybe.withDefault ""

                notice =
                    { workflowRunId = result.workflowRunId
                    , workflowName = workflowName
                    , warnings = result.warnings
                    }
            in
            if List.isEmpty result.warnings && workflowName /= "" then
                ( { model | actionError = Nothing, dispatchNotice = Nothing }
                , effects ++ [ NavigateTo (Routes.toString (dispatchedRunRoute notice)) ]
                )

            else
                -- Warnings (or a ticket with no workflow name to route with)
                -- must not be swallowed by a navigation the user never sees.
                ( { model | actionError = Nothing, dispatchNotice = Just notice }
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
                    [ FetchAgentTicket model.ticketId, FetchAgentTicketRuns model.ticketId ]
      }
    ]


{-| The one-second clock is not a poll: it only moves "now" so the journal's
relative times and in-flight durations keep counting.
-}
handleDelivery : Delivery -> ET Model
handleDelivery delivery (( model, effects ) as state) =
    case delivery of
        ClockTicked OneSecond time ->
            ( { model | now = Just time }, effects )

        _ ->
            Polling.handleDelivery polls delivery state


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
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

        ClickAgentTicketCancel ->
            ( { model | editing = False }, effects )

        ClickAgentTicketSave ->
            ( model
            , effects
                ++ [ SaveAgentTicket
                        { id = model.ticketId
                        , title = model.editTitle
                        , body = model.editBody
                        }
                   ]
            )

        ClickAgentTicketTransition to ->
            -- Two-step confirm: first click arms the action (naming it), a
            -- second click commits it. Mirrors the dispatch confirm.
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
            ( { model | dispatchConfirm = False, dispatchNotice = Nothing }
            , effects ++ [ DispatchAgentTicket model.ticketId ]
            )

        CancelAgentTicketDispatch ->
            ( { model | dispatchConfirm = False }, effects )

        DismissAgentTicketDispatchNotice ->
            ( { model | dispatchNotice = Nothing }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    OnClockTick OneSecond :: Polling.subscriptions polls



-- VIEW


view : Session -> Model -> Html Message
view session model =
    Chrome.view session
        model
        (Routes.AgentTicket { id = model.ticketId })
        ("Ticket #" ++ String.fromInt model.ticketId)
        "a queue slot — every execution truth belongs to its durable run"
        [ Html.div [ style "max-width" "900px" ] [ content session model ] ]


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
                    -- content and the human queue/dispatch/close controls, and
                    -- reads the durable workflow run for every piece of execution
                    -- evidence (outcome, review, repository-change, cost). It
                    -- never mirrors that evidence onto the ticket itself.
                    top =
                        [ header model ticket
                        , provenanceLine ticket
                        , runOutcomeLine model ticket
                        , durableEvidenceLine model ticket
                        , provenanceTimestamps session.timeZone ticket
                        , actionErrorBanner model
                        , dispatchNoticeBanner model
                        ]

                    -- The body renders lazily: its argument is reference-stable
                    -- across the 5s self-heal refetch (see handleCallback), so a
                    -- tick that changed nothing skips the tokenization entirely.
                    rest =
                        [ editForm model ticket
                        , Html.div [ id "ticket-hitl-slot" ] []
                        , journalSection model
                        , ticketBody ticket
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

        -- h2, not h1: the shell already owns the page's single h1
        -- ("Ticket #N"); the ticket's own title is the level below it.
        , Html.h2
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


{-| Where this ticket's work lives: the repository the human selected when they
filed it. The branch the agent actually delivered on is NOT here — that is the
durable workflow run's repository-change output, linked from
`durableEvidenceLine`. The repo degrades to plain text when the field can't be
resolved to a web URL.
-}
provenanceLine : AgentTicket.Ticket -> Html Message
provenanceLine ticket =
    if ticket.repo == "" then
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
                        [ Html.text ticket.repo ]
        in
        Html.div
            [ id "ticket-provenance"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" "#9aa39b"
            , style "margin" "2px 0 8px 0"
            ]
            repoPart


{-| The run's OUTCOME, read from the durable run — never from the ticket.

The ticket state answers "where is this in the queue"; it deliberately cannot
say whether the work merged, was dropped, or failed. That truth is the workflow
run's, so this line renders the run's own status/execution status straight from
the run detail the page already fetched, and links to the run for the full
disposition record. Nothing renders until the run detail is in hand: a chip
invented from ticket state is exactly the second truth this page is here to
stop showing.

-}
runOutcomeLine : Model -> AgentTicket.Ticket -> Html Message
runOutcomeLine model ticket =
    case ( durableKey ticket, model.durableRun ) of
        ( Just ( workflowName, workflowRunId ), Just detail ) ->
            if
                detail.summary.workflowName
                    == workflowName
                    && detail.summary.id
                    == workflowRunId
            then
                Html.div
                    [ id "ticket-run-outcome"
                    , style "display" "flex"
                    , style "flex-wrap" "wrap"
                    , style "align-items" "center"
                    , style "gap" "8px"
                    , style "margin" "2px 0 8px"
                    , style "font-size" "12px"
                    ]
                    [ Html.span [ style "color" "#9aa39b" ] [ Html.text "run outcome" ]
                    , runStatusChip detail.summary.status
                    , Html.span
                        [ style "font-family" "monospace", style "color" "#9aa39b" ]
                        [ Html.text (Maybe.withDefault "not started" detail.summary.executionStatus) ]
                    ]

            else
                Html.text ""

        _ ->
            Html.text ""


runStatusChip : String -> Html Message
runStatusChip status =
    case AgentBadge.fromApiToken status of
        Just badge ->
            AgentBadge.view badge

        Nothing ->
            Html.span
                [ style "font-family" "monospace", style "color" "#d0d0d0" ]
                [ Html.text status ]


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


dispatchedRunRoute : DispatchNotice -> Routes.Route
dispatchedRunRoute notice =
    Routes.AgentWorkflowRun
        { workflowName = notice.workflowName, id = notice.workflowRunId }


{-| The held-back dispatch result: what the server warned about, and the way on
to the run it nevertheless created. Dismissing it only clears the notice — the
run link stays reachable from `durableEvidenceLine` once the ticket refetch
lands, so dismissing can never strand the user.
-}
dispatchNoticeBanner : Model -> Html Message
dispatchNoticeBanner model =
    case model.dispatchNotice of
        Nothing ->
            Html.text ""

        Just notice ->
            let
                runLabel =
                    "workflow run #" ++ notice.workflowRunId

                runElement =
                    if notice.workflowName == "" then
                        -- No workflow name means no run route exists to build;
                        -- name the run anyway so the ID is not lost.
                        Html.span [ style "font-family" "monospace" ] [ Html.text runLabel ]

                    else
                        Html.a
                            [ id "ticket-dispatch-notice-run-link"
                            , href (Routes.toString (dispatchedRunRoute notice))
                            , style "color" "#7a9ac0"
                            ]
                            [ Html.text ("Open " ++ runLabel) ]
            in
            Html.div
                [ id "ticket-dispatch-notice"
                , style "border" "1px solid #6b5a2a"
                , style "background" "#2a2618"
                , style "color" "#e0cf9a"
                , style "padding" "8px 10px"
                , style "margin" "8px 0"
                ]
                [ Html.div [ style "margin-bottom" "6px" ]
                    [ Html.text "Dispatched with warnings." ]
                , Html.ul
                    [ id "ticket-dispatch-warnings", style "margin" "0 0 8px", style "padding-left" "18px" ]
                    (List.map (\w -> Html.li [] [ Html.text w ]) notice.warnings)
                , Html.div
                    [ style "display" "flex", style "align-items" "center", style "gap" "12px" ]
                    [ runElement
                    , actionButton "secondary" DismissAgentTicketDispatchNotice "Dismiss"
                    ]
                ]


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


{-| `closed` is the one terminal state: it has no outgoing human transition
(see the doc on `transitionTargets`), so editing it is meaningless — the Edit
affordance and the edit form are both suppressed.
-}
isTerminal : String -> Bool
isTerminal state =
    state == "closed"


{-| The transitions a human may drive from a given state, as (target, label).
Mirrors the server's `validTransitions` map (agent/api/tickets/types.go) — only
legal, human-initiated edges are offered. `running`'s edges (→queued /
→needs\_review) are system-driven, so it offers nothing, and `closed` is
terminal. The server stays authoritative and rejects anything stale with a 409.

There is exactly ONE close action, not a menu of dispositions: whether the work
was merged, dropped or was analysis-only is the durable run's outcome, read
back from the run (see `runOutcomeLine`) rather than re-asserted here.

-}
transitionTargets : String -> List ( String, String )
transitionTargets state =
    case state of
        "needs_review" ->
            [ ( "queued", "Re-queue" ), ( "closed", "Close" ) ]

        "draft" ->
            [ ( "queued", "Queue" ), ( "closed", "Close" ) ]

        "queued" ->
            [ ( "draft", "Unqueue" ), ( "closed", "Close" ) ]

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


{-| The ticket's own content is its markdown body — nothing else. The spec and
plan tabs went with the agent-authored spec/task tables, whose only writers (the
sidecar submit routes) are gone; keeping empty tabs would have advertised a
surface that can never be filled.

Lazy on the body string, so the full-text tokenization re-runs only when the
text itself changes, not on every 5s-refetch render.

-}
ticketBody : AgentTicket.Ticket -> Html Message
ticketBody ticket =
    Html.div [ id "ticket-body", style "padding" "12px 0" ]
        [ if String.trim ticket.body == "" then
            Html.p [ style "color" "#9aa39b" ] [ Html.text "No description." ]

          else
            Html.Lazy.lazy Views.Prose.view ticket.body
        ]


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


{-| The ticket's cross-workflow journal.

The journal renders every associated run occurrence in the order the server
returned them — one query, ordered by durable occurrence time. This page adds
no ordering, no grouping by workflow, and no edges: a ticket that ran
`small-fix`, then `pr-create`, then `small-fix` again reads as three entries.

`outstandingAction` is derived from the run's own status rather than from a
node-level wait, which the journal deliberately does not read; the words match
what the run list says about the same run, so the two surfaces cannot
contradict each other.

-}
journalSection : Model -> Html Message
journalSection model =
    Html.div [ style "margin" "16px 0" ]
        [ Html.h3
            [ style "font-size" "13px"
            , style "color" "#9aa39b"
            , style "text-transform" "uppercase"
            , style "letter-spacing" "0.5px"
            , style "margin" "0 0 6px 0"
            ]
            [ Html.text "Runs for this ticket" ]
        , Html.Lazy.lazy2 viewJournal (Maybe.withDefault (Time.millisToPosix 0) model.now) model.journal
        ]


viewJournal : Time.Posix -> List AgentTicket.JournalEntry -> Html Message
viewJournal now journal =
    Journal.view
        { now = now
        , emptyMessage = "No runs yet"
        }
        (List.map journalEntry journal)


journalEntry : AgentTicket.JournalEntry -> Journal.Entry
journalEntry entry =
    { id = entry.workflowRunId
    , url =
        Routes.toString
            (Routes.AgentWorkflowRun
                { workflowName = entry.workflowName, id = entry.workflowRunId }
            )
    , workflowName = entry.workflowName
    , workflowVersion = entry.workflowVersion
    , status = entry.status
    , createdAt = Timestamp.fromIso8601 entry.createdAt
    , startedAt = entry.startedAt |> Maybe.andThen Timestamp.fromIso8601
    , completedAt = entry.completedAt |> Maybe.andThen Timestamp.fromIso8601
    , retryOf = entry.retryOfWorkflowRunId
    , outcome = entry.errorMessage
    , outstandingAction = outstandingAction entry
    }


outstandingAction : AgentTicket.JournalEntry -> String
outstandingAction entry =
    if not entry.outstanding then
        ""

    else
        case entry.status of
            "admitting" ->
                "still admitting"

            "running" ->
                "still running"

            "canceling" ->
                "canceling"

            "failed" ->
                "failed — needs attention"

            "errored" ->
                "errored — needs attention"

            "aborted" ->
                "aborted — needs attention"

            _ ->
                "needs attention"
