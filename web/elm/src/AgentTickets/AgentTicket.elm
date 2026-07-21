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

Renders the ticket header, an editable title/body/budget form, the lifecycle
action buttons (dispatch / transition), the spec & plan tabs, the task list,
per-run cost history, and — reusing `Build.AgentReview` verbatim — the agent
review for the ticket's most recent run build. The server state machine stays
authoritative: buttons only pick which transition to _offer_, and a rejected
transition (409) surfaces inline and triggers a refetch.

-}

import AgentBadge
import Application.Models exposing (Session)
import Build.AgentReview
import Concourse.Agent
import Concourse.AgentDiff
import Concourse.AgentReview
import Concourse.AgentTicket as AgentTicket
import DateFormat
import Dict exposing (Dict)
import Duration
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
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription)
import Polling
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import Time
import Tooltip
import UserState
import Views.AgentDiff
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
        , runMetrics : List Concourse.Agent.RunMetric
        , runMetricsByBuild : Dict Int (List Concourse.Agent.RunMetric)
        , reviewBuildId : Maybe Int
        , activeTab : Tab
        , loaded : Bool
        , loadError : Bool
        , actionError : Maybe String
        , diff : Maybe Concourse.AgentDiff.DiffPage
        , diffLoadError : Bool
        , editing : Bool
        , editTitle : String
        , editBody : String
        , editBudget : String
        , dispatchConfirm : Bool
        , pendingTransition : Maybe String

        -- Live clock for the run rows' relative "N ago" times, advanced on the
        -- same 5s tick that drives the self-heal refetch.
        , now : Maybe Time.Posix

        -- Review panel state — structurally satisfies Build.AgentReview.PanelState
        -- so the panel view can be reused unchanged.
        , agentReviews : List Concourse.AgentReview.BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Maybe Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
        , expandedDescriptions : Set String
        }


init : { id : Int } -> ( Model, List Effect )
init { id } =
    ( { ticketId = id
      , detail = Nothing
      , runMetrics = []
      , runMetricsByBuild = Dict.empty
      , reviewBuildId = Nothing
      , activeTab = SpecTab
      , loaded = False
      , loadError = False
      , actionError = Nothing
      , diff = Nothing
      , diffLoadError = False
      , editing = False
      , editTitle = ""
      , editBody = ""
      , editBudget = ""
      , dispatchConfirm = False
      , pendingTransition = Nothing
      , now = Nothing
      , agentReviews = []
      , agentReviewLoadError = False
      , agentReviewPanelExpanded = False
      , expandedFindings = Set.empty
      , showObservations = Nothing
      , agentReviewNotes = Dict.empty
      , verdictErrors = Set.empty
      , expandedDescriptions = Set.empty
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTicket id, FetchAgentTicketMetrics id, FetchAgentTicketDiff id ]
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
            in
            ( { model
                | detail = Just detail
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
            )

        AgentTicketFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        AgentTicketMetricsFetched _ (Ok fresh) ->
            let
                -- Reference-preserved like the detail refetch above, and the
                -- by-build grouping the run history renders from is computed
                -- once per change instead of on every render.
                ( metrics, metricsByBuild ) =
                    if fresh == model.runMetrics then
                        ( model.runMetrics, model.runMetricsByBuild )

                    else
                        ( fresh, groupMetricsByBuild fresh )

                latestBuild =
                    metrics |> List.map .buildId |> List.maximum
            in
            ( { model | runMetrics = metrics, runMetricsByBuild = metricsByBuild, reviewBuildId = latestBuild }
            , case latestBuild of
                Just b ->
                    effects ++ [ FetchBuildAgentReviews b ]

                Nothing ->
                    effects
            )

        AgentTicketMetricsFetched _ (Err _) ->
            ( model, effects )

        AgentTicketDiffFetched (Ok fresh) ->
            let
                -- Reference-preserve so the lazy views below aren't defeated by
                -- an equal-but-fresh record installed on every 5s self-heal.
                page =
                    case model.diff of
                        Just old ->
                            if old == fresh then
                                old

                            else
                                fresh

                        Nothing ->
                            fresh
            in
            ( { model | diff = Just page, diffLoadError = False }, effects )

        AgentTicketDiffFetched (Err _) ->
            -- No diff yet (404 before harvest pushes base/pushed shas) or a
            -- transient error: keep it off-screen and leave the GitHub compare
            -- link as the fallback. Don't surface a red banner for the common
            -- "diff not ready" case.
            ( { model | diff = Nothing, diffLoadError = True }, effects )

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
            , effects ++ [ FetchAgentTicket model.ticketId, FetchAgentTicketMetrics model.ticketId, FetchAgentTicketDiff model.ticketId ]
            )

        AgentTicketDispatched _ (Err _) ->
            ( { model | actionError = Just "Dispatch failed." }, effects )

        BuildAgentReviewsFetched (Ok reviews) ->
            ( { model | agentReviews = reviews, agentReviewLoadError = False }, effects )

        BuildAgentReviewsFetched (Err _) ->
            ( { model | agentReviewLoadError = True }, effects )

        AgentReviewVerdictSubmitted findingId (Ok ()) ->
            ( { model | verdictErrors = Set.remove findingId model.verdictErrors }
            , effects
                ++ (model.reviewBuildId
                        |> Maybe.map (\b -> [ FetchBuildAgentReviews b ])
                        |> Maybe.withDefault []
                   )
            )

        AgentReviewVerdictSubmitted findingId (Err _) ->
            ( { model | verdictErrors = Set.insert findingId model.verdictErrors }, effects )

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
                    [ FetchAgentTicket model.ticketId
                    , FetchAgentTicketMetrics model.ticketId
                    , FetchAgentTicketDiff model.ticketId
                    ]
      }
    ]


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    -- Compose the polling fetch (Polling.handleDelivery only produces effects,
    -- never touches the model) with a local model write advancing the `now`
    -- clock the run rows' relative times read from — the pattern Polling's doc
    -- calls out for pages that also need a tick-driven model update.
    ( advanceClock delivery model
    , Polling.handleDelivery polls delivery ( model, effects ) |> Tuple.second
    )


advanceClock : Delivery -> Model -> Model
advanceClock delivery model =
    case delivery of
        ClockTicked _ time ->
            { model | now = Just time }

        _ ->
            model


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

        ToggleAgentReviewPanel ->
            ( { model | agentReviewPanelExpanded = not model.agentReviewPanelExpanded }, effects )

        ToggleAgentReviewFinding findingId ->
            ( { model | expandedFindings = toggleSet findingId model.expandedFindings }, effects )

        ToggleAgentReviewObservations open ->
            ( { model | showObservations = Just open }, effects )

        ToggleAgentReviewFindingBody findingId ->
            ( { model | expandedDescriptions = toggleSet findingId model.expandedDescriptions }, effects )

        AgentReviewVerdictClicked params ->
            -- A blank findingId can't disambiguate one finding from another, so
            -- a verdict keyed on it would misattribute human triage feedback.
            -- The card renders blank-id findings read-only, but guard here too.
            if params.findingId == "" then
                ( model, effects )

            else
                ( model
                , effects
                    ++ [ SubmitAgentReviewVerdict
                            { repo = params.repo
                            , commitSha = params.commitSha
                            , findingId = params.findingId
                            , verdict = params.verdict
                            , notes = Dict.get params.findingId model.agentReviewNotes |> Maybe.withDefault ""
                            , reviewer = params.reviewer
                            }
                       ]
                )

        AgentReviewNoteChanged findingId note ->
            if findingId == "" then
                ( model, effects )

            else
                ( { model | agentReviewNotes = Dict.insert findingId note model.agentReviewNotes }, effects )

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

                    reviewCard =
                        Build.AgentReview.view (reviewerName session) model

                    -- The failing run is the ticket's latest build; the error
                    -- box links there so a reviewer can open the run that
                    -- produced the failure text.
                    latestBuild =
                        model.runMetrics |> List.map .buildId |> List.maximum

                    top =
                        [ header model ticket
                        , provenanceLine ticket
                        , provenanceTimestamps session.timeZone ticket
                        , errorNotice latestBuild ticket
                        , activeAttempt ticket model.runMetrics
                        , actionErrorBanner model
                        , diffSection model
                        ]

                    -- The heavy sub-views render lazily: their arguments are
                    -- reference-stable across the 5s self-heal refetch (see
                    -- handleCallback), so a tick that changed nothing skips
                    -- them entirely.
                    rest =
                        [ Html.Lazy.lazy2 budgetBar ticket model.runMetrics
                        , editForm model ticket
                        , Html.div [ id "ticket-hitl-slot" ] []
                        , tabsBar model
                        , Html.Lazy.lazy2 tabContent model.activeTab detail
                        , Html.Lazy.lazy taskList detail.tasks

                        -- Not lazy: the relative run times read the `now` clock,
                        -- which changes on every 5s tick, so a memo would never
                        -- hit anyway.
                        , runHistory model.ticketId model.now session.timeZone model.runMetricsByBuild
                        ]
                in
                Html.div []
                    (if ticket.state == "needs_review" then
                        -- U6: keep the evidence (digest + review card) beside the
                        -- decision (the disposition bar) so a reviewer sees both.
                        top
                            ++ [ Html.Lazy.lazy2 reviewDigest ticket model.runMetrics
                               , lifecycleBar model ticket
                               , reviewCard
                               ]
                            ++ rest

                     else
                        top
                            ++ [ lifecycleBar model ticket ]
                            ++ rest
                            ++ [ reviewCard ]
                    )


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

            -- The global stylesheet sets `h1 { line-height: 60px }`, which on a
            -- title that wraps to a second line opens a ~60px gap between the
            -- lines. The single-line edit input never hits this because it
            -- can't wrap; override to a normal heading line-height so a wrapped
            -- title reads as one paragraph, matching the edit view.
            , style "line-height" "1.3"
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


{-| Where this ticket's work lives: the repo, and — once a harvest branch
exists — a direct link to the diff a reviewer needs. The compare link is the
primary affordance for `needs_review`; it degrades to plain text when the
repo field can't be resolved to a web URL.
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
                    case AgentTicket.compareUrl ticket of
                        Just url ->
                            [ Html.text (" · branch " ++ ticket.branch ++ " — ")
                            , Html.a
                                (class "agent-ticket-compare-link"
                                    :: href url
                                    :: style "font-size" "11px"
                                    :: linkStyle
                                )
                                [ Html.text "compare on GitHub ↗" ]
                            ]

                        Nothing ->
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


{-| The in-app diff — the PRIMARY review surface (S-4). Renders only when the
server returned a windowed diff with at least one file; otherwise nothing shows
and the demoted GitHub compare link in `provenanceLine` remains the fallback.
-}
diffSection : Model -> Html Message
diffSection model =
    case model.diff of
        Just page ->
            if List.isEmpty page.files then
                Html.text ""

            else
                Html.div [ id "ticket-diff" ]
                    [ Html.div
                        [ style "font-size" "13px"
                        , style "color" "#c8d0c8"
                        , style "margin" "10px 0 2px 0"
                        ]
                        [ Html.text "Diff vs base" ]
                    , Views.AgentDiff.view page
                    ]

        Nothing ->
            Html.text ""


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
                    |> Maybe.map
                        (\c ->
                            -- An errored ticket didn't "complete"; it ended.
                            (if ticket.state == "errored" then
                                "ended "

                             else
                                "completed "
                            )
                                ++ formatTimestamp zone c
                        )
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
errorNotice : Maybe Int -> AgentTicket.Ticket -> Html Message
errorNotice maybeBuildId ticket =
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
            [ errorNoticeHeading maybeBuildId
            , Html.div
                [ style "white-space" "pre-wrap"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "line-height" "1.4"
                ]
                [ Html.text ticket.errorDetail ]
            ]


{-| The "Run error" heading. When the failing build id is known it is a link to
that build's page, so a reviewer can jump from the error text to the run that
produced it; otherwise it degrades to inert bold text.
-}
errorNoticeHeading : Maybe Int -> Html Message
errorNoticeHeading maybeBuildId =
    let
        labelStyles =
            [ style "font-weight" "bold", style "font-size" "12px", style "margin-bottom" "4px" ]
    in
    case maybeBuildId of
        Just buildId ->
            Html.a
                (class "agent-ticket-error-build-link"
                    :: href (buildHref buildId)
                    :: style "color" "#f0a0a0"
                    :: style "text-decoration" "underline"
                    :: labelStyles
                )
                [ Html.text "Run error" ]

        Nothing ->
            Html.div labelStyles [ Html.text "Run error" ]


{-| The one-off build page URL for a build id, shared by the run rows and the
error-notice heading so both point at the same `/builds/<id>` route.
-}
buildHref : Int -> String
buildHref buildId =
    Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing })


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


budgetBar : AgentTicket.Ticket -> List Concourse.Agent.RunMetric -> Html Message
budgetBar ticket metrics =
    let
        spent =
            metrics |> List.map .costUsd |> List.sum
    in
    case ticket.budgetUsd of
        Just budget ->
            let
                pct =
                    if budget <= 0 then
                        0

                    else
                        min 100 (spent / budget * 100)

                overBudget =
                    spent > budget
            in
            Html.div [ style "margin" "10px 0" ]
                [ Html.div
                    [ style "display" "flex", style "justify-content" "space-between", style "font-size" "12px", style "color" "#9aa39b", style "margin-bottom" "4px" ]
                    [ Html.text "budget"
                    , Html.text ("$" ++ formatUsd spent ++ " / $" ++ formatUsd budget)
                    ]
                , Html.div
                    [ style "height" "6px", style "background" "#3d3c3c", style "width" "100%" ]
                    [ Html.div
                        [ style "height" "6px"
                        , style "width" (String.fromFloat pct ++ "%")
                        , style "background"
                            (if overBudget then
                                "#f0a0a0"

                             else
                                "#7aa37a"
                            )
                        ]
                        []
                    ]
                ]

        Nothing ->
            if spent > 0 then
                Html.div
                    [ style "font-size" "12px", style "color" "#9aa39b", style "margin" "10px 0" ]
                    [ Html.text ("spent $" ++ formatUsd spent ++ " · no budget set") ]

            else
                Html.text ""


{-| U6: a compact evidence digest shown beside the disposition bar for a
`needs_review` ticket — the latest run's one-line summary, a direct diff link,
and spend-vs-budget — so the reviewer decides with the facts in view.
-}
reviewDigest : AgentTicket.Ticket -> List Concourse.Agent.RunMetric -> Html Message
reviewDigest ticket metrics =
    let
        latestBuild =
            metrics |> List.map .buildId |> List.maximum

        -- Rows are created_at ASC, so the LAST non-empty summary is the final
        -- step's verdict (harvest/judge), not the agent's own first words.
        latestSummary =
            case latestBuild of
                Just b ->
                    metrics
                        |> List.filter (\m -> m.buildId == b)
                        |> lastNonEmptySummary

                Nothing ->
                    ""

        summaryRow =
            if latestSummary == "" then
                Html.text ""

            else
                Html.div
                    [ style "color" "#d0d0d0", style "line-height" "1.4", style "margin-bottom" "6px" ]
                    (Html.span [ style "color" "#9aa39b" ] [ Html.text "latest run — " ]
                        :: Views.Prose.inline latestSummary
                    )

        factsRow =
            Html.div
                [ style "display" "flex", style "flex-wrap" "wrap", style "gap" "12px", style "align-items" "center", style "font-size" "12px" ]
                (digestCompareLink ticket
                    ++ [ Html.span [ style "color" "#9aa39b" ] [ Html.text (costBudgetText ticket metrics) ] ]
                )
    in
    Html.div
        [ id "ticket-review-digest"
        , style "border" "1px solid #3d3c3c"
        , style "background" "#1b201b"
        , style "padding" "10px 12px"
        , style "margin" "10px 0"
        ]
        [ summaryRow, factsRow ]


{-| The diff link for the digest. Kept on its own class so the primary compare
affordance in the provenance line stays the single `agent-ticket-compare-link`.
-}
digestCompareLink : AgentTicket.Ticket -> List (Html Message)
digestCompareLink ticket =
    case AgentTicket.compareUrl ticket of
        Just url ->
            [ Html.a
                [ class "agent-ticket-digest-compare-link"
                , href url
                , style "color" "#7aa37a"
                , style "text-decoration" "none"
                ]
                [ Html.text ("review diff vs " ++ ticket.targetBranch) ]
            ]

        Nothing ->
            []


{-| Spend against the ticket's budget, always well-spaced (U19b): "$X spent /
$Y budget", or just "$X spent" when no budget is set.
-}
costBudgetText : AgentTicket.Ticket -> List Concourse.Agent.RunMetric -> String
costBudgetText ticket metrics =
    let
        spent =
            metrics |> List.map .costUsd |> List.sum
    in
    case ticket.budgetUsd of
        Just budget ->
            "$" ++ formatUsd spent ++ " spent / $" ++ formatUsd budget ++ " budget"

        Nothing ->
            "$" ++ formatUsd spent ++ " spent"


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


{-| A compact live strip for a dispatched-but-unfinished ticket: the latest
build's current status badge plus a link into the build page. Only shown while
the ticket is running — terminal and needs_review tickets have the full run
history and review card instead. Live because the page already refetches
metrics on the 5s poll.
-}
activeAttempt : AgentTicket.Ticket -> List Concourse.Agent.RunMetric -> Html Message
activeAttempt ticket metrics =
    if ticket.state /= "running" then
        Html.text ""

    else
        case metrics |> List.map .buildId |> List.maximum of
            Nothing ->
                Html.div
                    [ class "agent-ticket-active-attempt"
                    , style "border" "1px solid #3d3c3c"
                    , style "background" "#1b201b"
                    , style "padding" "8px 12px"
                    , style "margin" "10px 0"
                    , style "color" "#9aa39b"
                    , style "font-size" "13px"
                    ]
                    [ Html.text "Attempt starting…" ]

            Just buildId ->
                let
                    forBuild =
                        metrics |> List.filter (\m -> m.buildId == buildId)

                    runStatus =
                        case List.filter (\m -> m.status == "parked") forBuild of
                            parked :: _ ->
                                parked.status

                            [] ->
                                forBuild
                                    |> List.reverse
                                    |> List.head
                                    |> Maybe.map .status
                                    |> Maybe.withDefault ""

                    buildStatus =
                        forBuild |> List.head |> Maybe.map .buildStatus |> Maybe.withDefault ""

                    hasResult =
                        lastNonEmptySummary forBuild /= ""

                    statusView =
                        case AgentBadge.runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult } of
                            Just s ->
                                AgentBadge.view s

                            Nothing ->
                                Html.span [ style "color" "#b0b0b0" ] [ Html.text runStatus ]
                in
                Html.a
                    [ class "agent-ticket-active-attempt"
                    , href (Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing }))
                    , style "display" "flex"
                    , style "align-items" "center"
                    , style "gap" "10px"
                    , style "border" "1px solid #3d5f3d"
                    , style "background" "#1b201b"
                    , style "padding" "8px 12px"
                    , style "margin" "10px 0"
                    , style "color" "inherit"
                    , style "text-decoration" "none"
                    ]
                    [ Html.span [ style "color" "#9aa39b", style "font-size" "12px" ] [ Html.text "active attempt" ]
                    , statusView
                    , Html.span
                        [ style "font-family" "monospace", style "color" "#7aa37a", style "font-size" "12px" ]
                        [ Html.text ("build " ++ String.fromInt buildId ++ " →") ]
                    ]


{-| Per-run cost, aggregated from the step-level run metrics by build id.
Renders from the by-build grouping computed once per metrics fetch —
grouping or filtering here would re-scan every metric row for every run row
on every render.
-}
runHistory : Int -> Maybe Time.Posix -> Time.Zone -> Dict Int (List Concourse.Agent.RunMetric) -> Html Message
runHistory ticketId now zone metricsByBuild =
    if Dict.isEmpty metricsByBuild then
        Html.text ""

    else
        Html.div [ style "margin" "12px 0" ]
            (formLabel "runs"
                :: (metricsByBuild
                        |> Dict.toList
                        -- keys ascend, so oldest build is attempt 1; number
                        -- them before reversing so the newest renders first.
                        |> List.indexedMap (\i entry -> ( i + 1, entry ))
                        |> List.reverse
                        |> List.map (runRow ticketId now zone)
                   )
            )


runRow : Int -> Maybe Time.Posix -> Time.Zone -> ( Int, ( Int, List Concourse.Agent.RunMetric ) ) -> Html Message
runRow ticketId now zone ( attempt, ( buildId, forBuild ) ) =
    let
        cost =
            forBuild |> List.map .costUsd |> List.sum

        -- Rows arrive created_at ASC, one per step (agent, then harvest…), so
        -- the run's effective status is the LATEST step's — except a parked
        -- row anywhere wins, or a mid-build HITL park on a later step would
        -- hide behind an earlier step's "ok" and render as merely Running.
        runStatus =
            case List.filter (\m -> m.status == "parked") forBuild of
                parked :: _ ->
                    parked.status

                [] ->
                    forBuild
                        |> List.reverse
                        |> List.head
                        |> Maybe.map .status
                        |> Maybe.withDefault ""

        -- The joined build status is identical on every row of the build.
        buildStatus =
            forBuild |> List.head |> Maybe.map .buildStatus |> Maybe.withDefault ""

        -- The delivered result is the LAST step's non-empty summary (the
        -- harvest/judge verdict), not the first step's self-report.
        summary =
            lastNonEmptySummary forBuild

        hasResult =
            summary /= ""

        -- The run started at its first step (rows are created_at ASC).
        startedAt =
            forBuild |> List.head |> Maybe.map .createdAt |> Maybe.withDefault 0

        -- W-7: an empty summary means the runner delivered nothing readable;
        -- say so rather than leaving the result cell blank.
        summaryText =
            if summary == "" then
                "no result reported by runner"

            else
                summary

        -- U2/U3: the build status wins over the step status for display truth.
        -- The per-ROW server `outcome` field is deliberately not used here:
        -- this view collapses N step rows into ONE build-level verdict
        -- (parked-anywhere, last step's status, last delivered summary), so
        -- the last row's own fusion could lie — e.g. "no output" when an
        -- earlier step delivered. The precedence rule itself is still shared:
        -- runOutcome mirrors the server's agent/schema DeriveOutcome.
        statusView =
            case AgentBadge.runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult } of
                Just s ->
                    AgentBadge.view s

                Nothing ->
                    Html.span [ style "color" "#b0b0b0", style "font-size" "12px" ] [ Html.text runStatus ]
    in
    -- W-2: the row reads "attempt N · <relative time> · <outcome chip> · $cost".
    -- The build id is the LINK TARGET (the href), not a visible label.
    Html.a
        [ class "agent-ticket-run-row"
        , href (Routes.toString (Routes.AgentRunTranscript { id = ticketId, buildId = buildId }))
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "10px"
        , style "padding" "6px 0"
        , style "border-bottom" "1px solid #2a2929"
        , style "color" "inherit"
        , style "text-decoration" "none"
        ]
        [ Html.span [ style "font-family" "monospace", style "color" "#7aa37a", style "min-width" "72px", style "flex-shrink" "0" ]
            [ Html.text ("attempt " ++ String.fromInt attempt) ]
        , relativeRunTime now zone startedAt
        , Html.span [ style "flex-shrink" "0" ] [ statusView ]
        , Html.span [ style "color" "#9aa39b", style "flex" "1", style "min-width" "0", style "font-size" "12px", style "overflow" "hidden", style "text-overflow" "ellipsis", style "white-space" "nowrap" ]
            (Views.Prose.inline summaryText)
        , Html.span [ style "font-family" "monospace", style "color" "#b0b0b0", style "flex-shrink" "0" ] [ Html.text ("$" ++ formatUsd cost) ]
        ]


{-| A run row's start time as a relative "N ago" label (absolute time on hover),
reusing the shared Duration helper the ticket-queue rows use. Renders nothing
until the clock has ticked or when the timestamp is unknown / in the future.
-}
relativeRunTime : Maybe Time.Posix -> Time.Zone -> Int -> Html Message
relativeRunTime now zone epochSeconds =
    case now of
        Just t ->
            if epochSeconds > 0 then
                let
                    elapsed =
                        Duration.between (Time.millisToPosix (epochSeconds * 1000)) t
                in
                if elapsed >= 0 then
                    Html.span
                        [ style "color" "#9aa39b"
                        , style "font-size" "12px"
                        , style "flex-shrink" "0"
                        , Html.Attributes.title (formatTimestamp zone epochSeconds)
                        ]
                        [ Html.text (Duration.format elapsed ++ " ago") ]

                else
                    Html.text ""

            else
                Html.text ""

        Nothing ->
            Html.text ""


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


toggleSet : comparable -> Set comparable -> Set comparable
toggleSet x set =
    if Set.member x set then
        Set.remove x set

    else
        Set.insert x set


{-| Group the step-level metric rows by build id, preserving each build's
created_at-ASC row order (the status/summary logic depends on it).
-}
groupMetricsByBuild : List Concourse.Agent.RunMetric -> Dict Int (List Concourse.Agent.RunMetric)
groupMetricsByBuild metrics =
    metrics
        |> List.foldl
            (\m ->
                Dict.update m.buildId
                    (\rows -> Just (m :: Maybe.withDefault [] rows))
            )
            Dict.empty
        |> Dict.map (\_ -> List.reverse)


{-| The last non-empty summary of a build's metric rows (rows arrive
created\_at ASC): the final step's verdict, not the first step's self-report.
-}
lastNonEmptySummary : List Concourse.Agent.RunMetric -> String
lastNonEmptySummary rows =
    rows
        |> List.filterMap
            (\m ->
                if m.summary == "" then
                    Nothing

                else
                    Just m.summary
            )
        |> List.reverse
        |> List.head
        |> Maybe.withDefault ""


reviewerName : Session -> String
reviewerName session =
    case session.userState of
        UserState.UserStateLoggedIn user ->
            user.userName

        _ ->
            "anonymous"


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
