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
import Concourse.AgentReview
import Concourse.AgentTicket as AgentTicket
import Dict exposing (Dict)
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, href, id, placeholder, style, value)
import Html.Events exposing (on, onClick, onInput)
import Json.Decode
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import Time
import Tooltip
import UserState
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
        , reviewBuildId : Maybe Int
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

        -- Review panel state — structurally satisfies Build.AgentReview.PanelState
        -- so the panel view can be reused unchanged.
        , agentReviews : List Concourse.AgentReview.BuildReview
        , agentReviewLoadError : Bool
        , agentReviewPanelExpanded : Bool
        , expandedFindings : Set String
        , showObservations : Bool
        , agentReviewNotes : Dict String String
        , verdictErrors : Set String
        , expandedDescriptions : Set String
        }


init : { id : Int } -> ( Model, List Effect )
init { id } =
    ( { ticketId = id
      , detail = Nothing
      , runMetrics = []
      , reviewBuildId = Nothing
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
      , agentReviews = []
      , agentReviewLoadError = False
      , agentReviewPanelExpanded = False
      , expandedFindings = Set.empty
      , showObservations = False
      , agentReviewNotes = Dict.empty
      , verdictErrors = Set.empty
      , expandedDescriptions = Set.empty
      , isUserMenuExpanded = False
      }
    , [ FetchAgentTicket id, FetchAgentTicketMetrics id ]
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
        AgentTicketFetched (Ok detail) ->
            ( { model
                | detail = Just detail
                , loaded = True
                , loadError = False
                , editTitle = detail.ticket.title
                , editBody = detail.ticket.body
                , editBudget =
                    detail.ticket.budgetUsd
                        |> Maybe.map String.fromFloat
                        |> Maybe.withDefault ""
              }
            , effects
            )

        AgentTicketFetched (Err _) ->
            ( { model | loaded = True, loadError = True }, effects )

        AgentTicketMetricsFetched _ (Ok metrics) ->
            let
                latestBuild =
                    metrics |> List.map .buildId |> List.maximum
            in
            ( { model | runMetrics = metrics, reviewBuildId = latestBuild }
            , case latestBuild of
                Just b ->
                    effects ++ [ FetchBuildAgentReviews b ]

                Nothing ->
                    effects
            )

        AgentTicketMetricsFetched _ (Err _) ->
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
            , effects ++ [ FetchAgentTicket model.ticketId, FetchAgentTicketMetrics model.ticketId ]
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


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked FiveSeconds _ ->
            -- U11: live-update on the dashboard's 5s cadence so state, spend and
            -- runs stay current (a page was showing "Running" ~20 min after the
            -- ticket errored). Only replaces fetched data.
            ( model, effects ++ [ FetchAgentTicket model.ticketId, FetchAgentTicketMetrics model.ticketId ] )

        _ ->
            ( model, effects )


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        AgentTicketTabClicked tab ->
            ( { model | activeTab = tabFromString tab }, effects )

        ClickAgentTicketEdit ->
            ( { model | editing = True, actionError = Nothing }, effects )

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

        ToggleAgentReviewObservations ->
            ( { model | showObservations = not model.showObservations }, effects )

        ToggleAgentReviewFindingBody findingId ->
            ( { model | expandedDescriptions = toggleSet findingId model.expandedDescriptions }, effects )

        AgentReviewVerdictClicked params ->
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
            ( { model | agentReviewNotes = Dict.insert findingId note model.agentReviewNotes }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


subscriptions : List Subscription
subscriptions =
    [ OnClockTick FiveSeconds ]



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

                    top =
                        [ header model ticket
                        , provenanceLine ticket
                        , provenanceTimestamps session.timeZone ticket
                        , errorNotice ticket
                        , actionErrorBanner model
                        ]

                    rest =
                        [ budgetBar ticket model.runMetrics
                        , editForm model ticket
                        , Html.div [ id "ticket-hitl-slot" ] []
                        , tabsBar model
                        , tabContent model detail
                        , taskList detail.tasks
                        , runHistory model.runMetrics
                        ]
                in
                Html.div []
                    (if ticket.state == "needs_review" then
                        -- U6: keep the evidence (digest + review card) beside the
                        -- decision (the disposition bar) so a reviewer sees both.
                        top
                            ++ [ reviewDigest ticket model.runMetrics
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
                                (class "agent-ticket-compare-link" :: href url :: linkStyle)
                                [ Html.text ("review diff vs " ++ ticket.targetBranch) ]
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
            [ ( "queued", "Retry" ) ]

        "errored" ->
            [ ( "queued", "Retry" ) ]

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

        latestSummary =
            case latestBuild of
                Just b ->
                    metrics
                        |> List.filter (\m -> m.buildId == b)
                        |> List.filterMap
                            (\m ->
                                if m.summary == "" then
                                    Nothing

                                else
                                    Just m.summary
                            )
                        |> List.head
                        |> Maybe.withDefault ""

                Nothing ->
                    ""

        summaryRow =
            if latestSummary == "" then
                Html.text ""

            else
                Html.div
                    [ style "color" "#d0d0d0", style "line-height" "1.4", style "margin-bottom" "6px" ]
                    [ Html.span [ style "color" "#9aa39b" ] [ Html.text "latest run — " ]
                    , Html.text latestSummary
                    ]

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


tabContent : Model -> AgentTicket.Detail -> Html Message
tabContent model detail =
    Html.div [ style "padding" "12px 0" ]
        [ case model.activeTab of
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
-}
prose : String -> Html Message
prose =
    Views.Prose.view


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


{-| Per-run cost, aggregated from the step-level run metrics by build id.
-}
runHistory : List Concourse.Agent.RunMetric -> Html Message
runHistory metrics =
    if List.isEmpty metrics then
        Html.text ""

    else
        let
            builds =
                metrics
                    |> List.map .buildId
                    |> unique
                    |> List.sortBy negate
        in
        Html.div [ style "margin" "12px 0" ]
            (formLabel "runs"
                :: List.map (runRow metrics) builds
            )


runRow : List Concourse.Agent.RunMetric -> Int -> Html Message
runRow metrics buildId =
    let
        forBuild =
            List.filter (\m -> m.buildId == buildId) metrics

        cost =
            forBuild |> List.map .costUsd |> List.sum

        runStatus =
            forBuild |> List.head |> Maybe.map .status |> Maybe.withDefault ""

        buildStatus =
            forBuild |> List.head |> Maybe.map .buildStatus |> Maybe.withDefault ""

        hasResult =
            List.any (\m -> m.summary /= "") forBuild

        summary =
            forBuild
                |> List.filterMap
                    (\m ->
                        if m.summary == "" then
                            Nothing

                        else
                            Just m.summary
                    )
                |> List.head
                |> Maybe.withDefault ""

        -- U2/U3: the build status wins over the step status for display truth.
        statusView =
            case AgentBadge.runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult } of
                Just s ->
                    AgentBadge.view s

                Nothing ->
                    Html.span [ style "color" "#b0b0b0", style "font-size" "12px" ] [ Html.text runStatus ]
    in
    Html.a
        [ class "agent-ticket-run-row"
        , href (Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing }))
        , style "display" "flex"
        , style "align-items" "center"
        , style "gap" "10px"
        , style "padding" "6px 0"
        , style "border-bottom" "1px solid #2a2929"
        , style "color" "inherit"
        , style "text-decoration" "none"
        ]
        [ Html.span [ style "font-family" "monospace", style "color" "#7aa37a", style "min-width" "80px", style "flex-shrink" "0" ]
            [ Html.text ("build " ++ String.fromInt buildId) ]
        , Html.span [ style "flex-shrink" "0" ] [ statusView ]
        , Html.span [ style "color" "#9aa39b", style "flex" "1", style "min-width" "0", style "font-size" "12px", style "overflow" "hidden", style "text-overflow" "ellipsis", style "white-space" "nowrap" ]
            [ Html.text summary ]
        , Html.span [ style "font-family" "monospace", style "color" "#b0b0b0", style "flex-shrink" "0" ] [ Html.text ("$" ++ formatUsd cost) ]
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


unique : List comparable -> List comparable
unique xs =
    xs
        |> List.foldl
            (\x acc ->
                if List.member x acc then
                    acc

                else
                    acc ++ [ x ]
            )
            []


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
    let
        posix =
            Time.millisToPosix (epochSeconds * 1000)

        pad n =
            String.padLeft 2 '0' (String.fromInt n)
    in
    monthAbbr (Time.toMonth zone posix)
        ++ " "
        ++ String.fromInt (Time.toDay zone posix)
        ++ ", "
        ++ String.fromInt (Time.toYear zone posix)
        ++ " "
        ++ pad (Time.toHour zone posix)
        ++ ":"
        ++ pad (Time.toMinute zone posix)


monthAbbr : Time.Month -> String
monthAbbr month =
    case month of
        Time.Jan ->
            "Jan"

        Time.Feb ->
            "Feb"

        Time.Mar ->
            "Mar"

        Time.Apr ->
            "Apr"

        Time.May ->
            "May"

        Time.Jun ->
            "Jun"

        Time.Jul ->
            "Jul"

        Time.Aug ->
            "Aug"

        Time.Sep ->
            "Sep"

        Time.Oct ->
            "Oct"

        Time.Nov ->
            "Nov"

        Time.Dec ->
            "Dec"


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
