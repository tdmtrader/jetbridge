module Agent.Agent exposing
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
import Colors
import Concourse.Agent as Agent
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (checked, class, disabled, href, id, placeholder, style, title, type_, value)
import Html.Events exposing (onClick, onInput)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.ScrollDirection as ScrollDirection
import Message.Subscription
    exposing
        ( Delivery(..)
        , Interval(..)
        , Subscription(..)
        )
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import Time
import Tooltip
import Views.Prose
import Views.Styles
import Views.TopBar as TopBar


{-| The closed scope vocabulary an admin may grant when minting a principal.
Mirrors agent/api/principals `ValidScopes`; keep the two in lockstep.
-}
mintScopeVocabulary : List String
mintScopeVocabulary =
    [ "reviews:write"
    , "tickets:read"
    , "tickets:write"
    , "metrics:write"
    , "costs:write"
    , "questions:answer"
    ]


type alias Model =
    Login.Model
        { runs : Maybe (List Agent.RunMetric)
        , runsError : Maybe String
        , workflows : Maybe (List Agent.WorkflowSummary)
        , costRollup : Maybe Agent.CostRollup
        , workflowsError : Maybe String
        , costError : Maybe String
        , credentials : Maybe (List Agent.CredentialStatus)
        , credentialsError : Maybe String
        , platformCredentials : Maybe (List Agent.CredentialStatus)
        , unattributedUsd : Maybe Float
        , principals : Maybe (List Agent.Principal)
        , principalsError : Maybe String
        , mintName : String
        , mintDescription : String
        , mintScopes : Set String
        , mintExpiresDays : String
        , mintedToken : Maybe String
        , mintError : Maybe String
        , minting : Bool
        , revokeError : Maybe String
        , showEphemeralPrincipals : Bool
        , expandedRuns : Set String
        }


init : ( Model, List Effect )
init =
    ( { runs = Nothing
      , runsError = Nothing
      , workflows = Nothing
      , costRollup = Nothing
      , workflowsError = Nothing
      , costError = Nothing
      , credentials = Nothing
      , credentialsError = Nothing
      , platformCredentials = Nothing
      , unattributedUsd = Nothing
      , principals = Nothing
      , principalsError = Nothing
      , mintName = ""
      , mintDescription = ""
      , mintScopes = Set.empty
      , mintExpiresDays = ""
      , mintedToken = Nothing
      , mintError = Nothing
      , minting = False
      , revokeError = Nothing
      , showEphemeralPrincipals = False
      , expandedRuns = Set.empty
      , isUserMenuExpanded = False
      }
    , [ FetchAgentRunMetrics
      , FetchAgentWorkflows
      , FetchAgentCostRollup
      , FetchAgentTicketCosts
      , FetchAgentCredentials
      , FetchAgentPlatformCredentials
      , FetchAgentPrincipals
      ]
    )


documentTitle : String
documentTitle =
    "Agent"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentRunMetricsFetched (Ok runs) ->
            ( { model | runs = Just runs, runsError = Nothing }, effects )

        AgentRunMetricsFetched (Err err) ->
            ( { model | runsError = Just (errorMessage "runs" err) }, effects )

        AgentWorkflowsFetched (Ok workflows) ->
            ( { model | workflows = Just workflows, workflowsError = Nothing }, effects )

        AgentWorkflowsFetched (Err err) ->
            ( { model | workflowsError = Just (errorMessage "workflows" err) }, effects )

        AgentCostRollupFetched (Ok costRollup) ->
            -- The console fires both the by-day rollup (the Costs table) and
            -- the by-ticket rollup (for the unattributed bucket); they share
            -- one callback and are told apart by the response's group_by.
            if costRollup.groupBy == "ticket" then
                ( { model
                    | unattributedUsd =
                        costRollup.rows
                            |> List.filter (\row -> row.key == "")
                            |> List.map .costUsd
                            |> List.sum
                            |> Just
                  }
                , effects
                )

            else
                ( { model | costRollup = Just costRollup, costError = Nothing }, effects )

        AgentCostRollupFetched (Err err) ->
            ( { model | costError = Just (errorMessage "costs" err) }, effects )

        AgentCredentialsFetched (Ok credentials) ->
            ( { model | credentials = Just credentials, credentialsError = Nothing }, effects )

        AgentCredentialsFetched (Err err) ->
            ( { model | credentialsError = Just (errorMessage "credentials" err) }, effects )

        AgentPlatformCredentialsFetched (Ok credentials) ->
            ( { model | platformCredentials = Just credentials }, effects )

        AgentPlatformCredentialsFetched (Err _) ->
            -- Non-admins get a 403 here; the platform row is simply omitted.
            ( { model | platformCredentials = Nothing }, effects )

        AgentPrincipalsFetched (Ok principals) ->
            ( { model | principals = Just principals, principalsError = Nothing }, effects )

        AgentPrincipalsFetched (Err err) ->
            ( { model | principalsError = Just (errorMessage "principals" err) }, effects )

        AgentPrincipalCreated (Ok created) ->
            -- Surface the one-time token, clear the form, and refetch so the
            -- new principal appears in the table. The refetch does not touch
            -- mintedToken, so the token box survives the next 5s tick.
            -- Clearing `minting` re-enables the form for the next mint.
            ( { model
                | mintedToken = Just created.token
                , mintName = ""
                , mintDescription = ""
                , mintScopes = Set.empty
                , mintExpiresDays = ""
                , mintError = Nothing
                , minting = False
              }
            , effects ++ [ FetchAgentPrincipals ]
            )

        AgentPrincipalCreated (Err err) ->
            -- Re-enable the form so the admin can retry after a failed mint.
            ( { model | mintError = Just (mutationError "mint" err), minting = False }
            , effects
            )

        AgentPrincipalRevoked (Ok ()) ->
            ( { model | revokeError = Nothing }, effects ++ [ FetchAgentPrincipals ] )

        AgentPrincipalRevoked (Err err) ->
            -- Revoke failures surface next to the principals table (via
            -- revokeError), not inside the mint form.
            ( { model | revokeError = Just (mutationError "revoke" err) }, effects )

        _ ->
            ( model, effects )


{-| Turn an Http.Error into a short human message. A 403 on these admin-only
endpoints is the common case (non-admin users), so it gets a specific note;
everything else falls back to a generic "couldn't load …".
-}
errorMessage : String -> Http.Error -> String
errorMessage what err =
    case err of
        Http.BadStatus { status } ->
            if status.code == 403 then
                "not authorized — the agent " ++ what ++ " API is admin-only"

            else
                "couldn't load " ++ what

        _ ->
            "couldn't load " ++ what


{-| Short message for a failed principal mutation (mint/revoke). A 403 is the
admin-only case; anything else is a generic couldn't-verb message.
-}
mutationError : String -> Http.Error -> String
mutationError verb err =
    case err of
        Http.BadStatus { status } ->
            if status.code == 403 then
                "not authorized — principals are admin-only"

            else
                "couldn't " ++ verb ++ " principal"

        _ ->
            "couldn't " ++ verb ++ " principal"


{-| The mint button is enabled only with a non-empty name, at least one scope
selected, and a valid expiry field — the same required-field rule the API
enforces. A blank expiry is valid (= no expiry); any non-blank value must
parse to a positive integer number of days.
-}
canMint : Model -> Bool
canMint model =
    (String.trim model.mintName /= "")
        && not (Set.isEmpty model.mintScopes)
        && expiresIsValid model.mintExpiresDays


{-| The "expires in N days" field is valid when it is blank (no expiry) or
parses to a positive integer. Blank, zero, negative, and non-numeric input
that would silently mean "never expires" are surfaced as invalid instead.
-}
expiresIsValid : String -> Bool
expiresIsValid raw =
    case String.trim raw of
        "" ->
            True

        trimmed ->
            case String.toInt trimmed of
                Just days ->
                    days > 0

                Nothing ->
                    False


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        AgentMintNameChanged name ->
            ( { model | mintName = name }, effects )

        AgentMintDescriptionChanged description ->
            ( { model | mintDescription = description }, effects )

        AgentMintScopeToggled scope ->
            let
                scopes =
                    if Set.member scope model.mintScopes then
                        Set.remove scope model.mintScopes

                    else
                        Set.insert scope model.mintScopes
            in
            ( { model | mintScopes = scopes }, effects )

        AgentMintExpiresChanged days ->
            ( { model | mintExpiresDays = days }, effects )

        AgentMintSubmitted ->
            -- Guard on `minting` as well so a fast double-click can't dispatch
            -- two CreateAgentPrincipal effects (which would orphan the first
            -- one's one-time token). `minting` is cleared in the
            -- AgentPrincipalCreated Ok/Err handlers.
            if canMint model && not model.minting then
                ( { model | minting = True }
                , effects
                    ++ [ CreateAgentPrincipal
                            { name = String.trim model.mintName
                            , description = String.trim model.mintDescription
                            , scopes = Set.toList model.mintScopes
                            , expiresInDays =
                                model.mintExpiresDays
                                    |> String.trim
                                    |> String.toInt
                            }
                       ]
                )

            else
                ( model, effects )

        AgentMintedTokenDismissed ->
            ( { model | mintedToken = Nothing }, effects )

        AgentPrincipalRevokeClicked principalId ->
            -- Clear any stale revoke error before the new attempt.
            ( { model | revokeError = Nothing }
            , effects ++ [ RevokeAgentPrincipal principalId ]
            )

        AgentPrincipalsShowEphemeralToggled ->
            ( { model | showEphemeralPrincipals = not model.showEphemeralPrincipals }, effects )

        AgentSectionNavClicked anchorId ->
            ( model, effects ++ [ Scroll (ScrollDirection.ToId anchorId) agentContentId ] )

        AgentRunExpandToggled rowKey ->
            -- Toggle a single ledger row between its one-line summary and the
            -- full run summary. Keyed by build id + plan id (see runKey): a
            -- build carries one metric row per step (agent + harvest), so the
            -- build id alone would toggle sibling rows together, and an
            -- ordinal would jump when the 5s refetch prepends a newer run.
            let
                expanded =
                    if Set.member rowKey model.expandedRuns then
                        Set.remove rowKey model.expandedRuns

                    else
                        Set.insert rowKey model.expandedRuns
            in
            ( { model | expandedRuns = expanded }, effects )

        _ ->
            ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked OneMinute _ ->
            -- Self-healing refresh. These fetches only replace the fetched
            -- data (and clear their own errors); they never touch the mint
            -- form or the one-time token box, so a tick can't wipe them.
            -- One minute is plenty: this is near-static admin data (the cost
            -- rollup alone is a 30-day ledger aggregation), and mutations
            -- (mint/revoke/promote) already refetch explicitly.
            ( model
            , effects
                ++ [ FetchAgentRunMetrics
                   , FetchAgentWorkflows
                   , FetchAgentCostRollup
                   , FetchAgentTicketCosts
                   , FetchAgentCredentials
                   , FetchAgentPlatformCredentials
                   , FetchAgentPrincipals
                   ]
            )

        _ ->
            ( model, effects )


subscriptions : List Subscription
subscriptions =
    [ OnClockTick OneMinute ]



-- COLORS / STYLE HELPERS


mutedColor : String
mutedColor =
    "#b0b0b0"


subtleColor : String
subtleColor =
    "#7a7a7a"


rowBorder : String
rowBorder =
    "1px solid " ++ Colors.background


amberColor : String
amberColor =
    "#e0a44e"


{-| Round a dollar amount to two decimals via integer cents so the display
never leaks floating-point noise (e.g. 0.1 + 0.2).
-}
formatUsd : Float -> String
formatUsd amount =
    let
        cents =
            round (amount * 100)

        sign =
            if cents < 0 then
                "-"

            else
                ""

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
    sign ++ String.fromInt dollars ++ "." ++ fraction


sectionBlock : String -> String -> List (Html Message) -> Html Message
sectionBlock anchorId title children =
    Html.div
        [ id anchorId, style "margin-top" "24px" ]
        (Html.h2
            [ style "font-size" "15px"
            , style "margin" "0 0 8px 0"
            , style "color" Colors.text
            ]
            [ Html.text title ]
            :: children
        )


mutedLine : String -> Html Message
mutedLine content =
    Html.p
        [ style "color" mutedColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "margin" "4px 0"
        ]
        [ Html.text content ]


errorLine : String -> Html Message
errorLine content =
    Html.p
        [ class "agent-section-error"
        , style "color" amberColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "margin" "4px 0"
        ]
        [ Html.text content ]


{-| A visible warning shown above a section's content when a poll fails after
data has already loaded. Without it a broken refresh is invisible — the stale
data keeps rendering forever with no hint that it stopped updating.
-}
staleDataWarning : Maybe String -> List (Html Message)
staleDataWarning maybeError =
    case maybeError of
        Just message ->
            [ Html.div [ class "agent-section-stale" ]
                [ errorLine ("refresh failed — showing stale data: " ++ message) ]
            ]

        Nothing ->
            []


pill : String -> { bg : String, fg : String } -> String -> Html Message
pill className { bg, fg } labelText =
    Html.span
        [ class className
        , style "margin-left" "8px"
        , style "padding" "1px 8px"
        , style "border-radius" "3px"
        , style "font-size" "11px"
        , style "font-weight" "700"
        , style "background" bg
        , style "color" fg
        ]
        [ Html.text labelText ]



-- VIEW


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.Agent
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
                -- The console's own scroll container (like the build page's
                -- body): the section nav jumps by setting its scrollTop via
                -- the scrollToId port, which needs a scrolling parent by id.
                [ id agentContentId
                , style "padding" "16px"
                , style "width" "100%"
                , style "box-sizing" "border-box"
                , style "overflow-y" "auto"
                ]
                [ Html.h1
                    [ style "font-size" "18px"
                    , style "margin" "0"
                    , style "color" Colors.text
                    ]
                    [ Html.text "Agent" ]
                , Html.p
                    [ style "color" mutedColor
                    , style "font-family" "monospace"
                    , style "font-size" "12px"
                    , style "margin" "4px 0 0 0"
                    ]
                    [ Html.text "workflows and spend" ]
                , sectionNav
                , runsSection session.timeZone model
                , workflowsSection model
                , costsSection model
                , credentialsSection session.timeZone model
                , principalsSection session.timeZone model
                ]
            ]
        ]



-- SECTION NAV


agentContentId : String
agentContentId =
    "agent-content"


{-| A slim in-page nav strip so the long single-column console can be jumped
around without scroll-hunting. Each entry scrolls to a section's `id` via the
`scrollToId` port — a plain `#fragment` href is dead here: `Browser.application`
intercepts every internal link click and re-navigates through `Routes`, which
carries no fragment for this page, so the browser never performs the jump. -}
sectionNav : Html Message
sectionNav =
    Html.div
        [ class "agent-section-nav"
        , style "display" "flex"
        , style "flex-wrap" "wrap"
        , style "gap" "12px"
        , style "margin" "12px 0 0 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        ]
        (List.map navLink
            [ ( "agent-runs", "runs" )
            , ( "agent-workflows", "workflows" )
            , ( "agent-costs", "costs" )
            , ( "agent-credentials", "credentials" )
            , ( "agent-principals", "principals" )
            ]
        )


navLink : ( String, String ) -> Html Message
navLink ( anchorId, label ) =
    Html.button
        [ onClick (AgentSectionNavClicked anchorId)
        , type_ "button"
        , style "background" "transparent"
        , style "border" "none"
        , style "padding" "0"
        , style "font" "inherit"
        , style "color" "#7a9ac0"
        , style "cursor" "pointer"
        ]
        [ Html.text label ]



-- RUNS SECTION


runsSection : Time.Zone -> Model -> Html Message
runsSection zone model =
    sectionBlock "agent-runs" "Recent runs" <|
        case model.runs of
            Nothing ->
                case model.runsError of
                    Just message ->
                        [ errorLine message ]

                    Nothing ->
                        [ mutedLine "loading…" ]

            Just [] ->
                staleDataWarning model.runsError
                    ++ [ mutedLine "no agent runs recorded yet" ]

            Just runs ->
                staleDataWarning model.runsError
                    ++ [ mutedLine "showing the newest 100 runs (capped server-side, most recent first)"
                       , runsTable zone model.expandedRuns runs
                       ]


runsTable : Time.Zone -> Set String -> List Agent.RunMetric -> Html Message
runsTable zone expandedRuns runs =
    Html.table
        [ class "agent-runs-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (runsHeaderRow :: List.map (runRow zone expandedRuns) runs)


runsHeaderRow : Html Message
runsHeaderRow =
    Html.tr []
        [ tableHeaderCell "left" "step"
        , tableHeaderCell "left" "workflow"
        , tableHeaderCell "left" "status"
        , tableHeaderCell "right" "cost"
        , tableHeaderCell "right" "tokens (in+out)"
        , tableHeaderCell "right" "turns"
        , tableHeaderCell "left" "ticket"
        , tableHeaderCell "left" "when (local)"
        ]


{-| Stable identity for a ledger row. (build id, plan id) is the metrics
table's unique key — a build id alone is shared by sibling step rows of the
same build, and a list ordinal changes when the refetch prepends a newer run.
-}
runKey : Agent.RunMetric -> String
runKey r =
    String.fromInt r.buildId ++ ":" ++ r.planId


runRow : Time.Zone -> Set String -> Agent.RunMetric -> Html Message
runRow zone expandedRuns r =
    Html.tr [ class "agent-run-row" ]
        [ runStepCell expandedRuns r
        , tableCell "left" (workflowRef r.workflowName r.workflowVersion)
        , runStatusCell r
        , tableCell "right" ("$" ++ formatUsd r.costUsd)
        , tableCell "right" (String.fromInt r.usage.inputTokens ++ "+" ++ String.fromInt r.usage.outputTokens)
        , tableCell "right" (String.fromInt r.turns)
        , ticketRefCell r
        , tableCell "left" (formatPosix zone (Just (secondsToPosix r.createdAt)))
        ]


{-| The step name plus its summary underneath it — omitted when the summary is
empty so the row does not carry a blank subtext line. The summary is
click-to-expand: collapsed it is a truncated one-liner; expanded it renders the
full run summary as prose (`AgentRunExpandToggled`, keyed by `runKey` so the
expanded row stays put when a 5s refetch prepends a newer run).
-}
runStepCell : Set String -> Agent.RunMetric -> Html Message
runStepCell expandedRuns r =
    let
        rowKey =
            runKey r

        expanded =
            Set.member rowKey expandedRuns

        summaryBlock =
            if r.summary == "" then
                []

            else if expanded then
                [ Html.div
                    [ class "agent-run-summary-full"
                    , onClick (AgentRunExpandToggled rowKey)
                    , style "max-width" "480px"
                    , style "margin-top" "2px"
                    , style "cursor" "pointer"
                    , title "click to collapse"
                    ]
                    [ Views.Prose.view r.summary ]
                ]

            else
                [ Html.div
                    [ class "agent-run-summary"
                    , onClick (AgentRunExpandToggled rowKey)
                    , style "font-size" "11px"
                    , style "color" mutedColor
                    , style "max-width" "320px"
                    , style "white-space" "nowrap"
                    , style "overflow" "hidden"
                    , style "text-overflow" "ellipsis"
                    , style "cursor" "pointer"
                    , title r.summary
                    ]
                    [ Html.text ("▸ " ++ r.summary) ]
                ]
    in
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        , style "vertical-align" "top"
        ]
        (Html.div
            [ style "font-weight" "700", style "color" Colors.text ]
            [ Html.text r.stepName ]
            :: summaryBlock
        )


{-| Render the run's DISPLAY truth as an AgentBadge. The pipeline build status
wins over the agent step status (U3), so a step that exited "ok" inside a
failed build shows Failed, and an "ok" step that delivered no summary shows
"No output" — never a green OK on a build that did not deliver. Falls back to
the raw step status only when the badge can derive nothing.
-}
runStatusCell : Agent.RunMetric -> Html Message
runStatusCell r =
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ case AgentBadge.runOutcome { buildStatus = r.buildStatus, runStatus = r.status, hasResult = r.summary /= "" } of
            Just badgeStatus ->
                AgentBadge.view badgeStatus

            Nothing ->
                Html.text r.status
        ]


{-| "name@version", or just the name when the version is unknown, or "—" when
there is no workflow at all (an ad-hoc / CI run).
-}
workflowRef : String -> Maybe Int -> String
workflowRef name version =
    case ( name, version ) of
        ( "", _ ) ->
            "—"

        ( n, Just v ) ->
            n ++ "@" ++ String.fromInt v

        ( n, Nothing ) ->
            n


{-| A linked "#N" back to the originating ticket for a ticket-backed run, or a
plain "CI" when there is no ticket.
-}
ticketRefCell : Agent.RunMetric -> Html Message
ticketRefCell r =
    Html.td
        [ style "text-align" "left"
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ case r.ticketId of
            Just t ->
                Html.a
                    [ href (Routes.toString (Routes.AgentTicket { id = t }))
                    , title ("Agent ticket #" ++ String.fromInt t)
                    , style "color" "#7a9ac0"
                    , style "text-decoration" "none"
                    ]
                    [ Html.text ("#" ++ String.fromInt t) ]

            Nothing ->
                Html.span
                    [ title "Continuous-integration review run — not tied to an agent ticket"
                    , style "color" subtleColor
                    , style "cursor" "help"
                    ]
                    [ Html.text "CI" ]
        ]


secondsToPosix : Int -> Time.Posix
secondsToPosix seconds =
    Time.millisToPosix (seconds * 1000)



-- WORKFLOWS SECTION


workflowsSection : Model -> Html Message
workflowsSection model =
    sectionBlock "agent-workflows" "Workflows" <|
        case model.workflows of
            Nothing ->
                case model.workflowsError of
                    Just message ->
                        [ errorLine message ]

                    Nothing ->
                        [ mutedLine "loading…" ]

            Just [] ->
                staleDataWarning model.workflowsError
                    ++ [ mutedLine "no workflow definitions — import one with: fly agent workflows import" ]

            Just workflows ->
                staleDataWarning model.workflowsError
                    ++ [ Html.div [ class "agent-workflows" ] (List.map workflowRow workflows) ]


workflowRow : Agent.WorkflowSummary -> Html Message
workflowRow w =
    Html.div
        [ class "agent-workflow-row"
        , style "display" "flex"
        , style "align-items" "baseline"
        , style "gap" "12px"
        , style "padding" "8px 0"
        , style "border-bottom" rowBorder
        ]
        [ Html.div [ style "flex" "1", style "min-width" "0" ]
            [ Html.div []
                (Html.span
                    [ style "font-weight" "700", style "color" Colors.text ]
                    [ Html.text w.name ]
                    :: workflowPills w
                )
            , Html.div
                [ style "font-size" "12px", style "color" mutedColor ]
                [ Html.text w.description ]
            ]
        , Html.div
            [ style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" subtleColor
            , style "text-align" "right"
            , style "white-space" "nowrap"
            ]
            [ liveVersionLine w
            , Html.div [] [ Html.text ("latest v" ++ String.fromInt w.latestVersion) ]
            , Html.div [] [ Html.text ("#" ++ String.left 12 w.contentHash) ]
            ]
        ]


workflowPills : Agent.WorkflowSummary -> List (Html Message)
workflowPills w =
    let
        livePill =
            if w.liveVersion > 0 then
                [ pill "agent-workflow-live"
                    { bg = "#2e4f2e", fg = "#9fdf9f" }
                    "live"
                ]

            else
                []

        candidatePill =
            if w.latestVersion > w.liveVersion then
                [ pill "agent-workflow-candidate"
                    { bg = Colors.background, fg = mutedColor }
                    ("candidate v" ++ String.fromInt w.latestVersion)
                ]

            else
                []
    in
    livePill ++ candidatePill


liveVersionLine : Agent.WorkflowSummary -> Html Message
liveVersionLine w =
    if w.liveVersion == 0 then
        Html.div [ style "color" subtleColor ] [ Html.text "no live version" ]

    else
        Html.div [] [ Html.text ("v" ++ String.fromInt w.liveVersion ++ " live") ]



-- COSTS SECTION


costsSection : Model -> Html Message
costsSection model =
    sectionBlock "agent-costs" "Costs" <|
        case model.costRollup of
            Nothing ->
                case model.costError of
                    Just message ->
                        [ errorLine message ]

                    Nothing ->
                        [ mutedLine "loading…" ]

            Just rollup ->
                staleDataWarning model.costError
                    ++ [ costSummaryLine rollup.summary
                       , dailyCapGauge rollup.summary
                       , costTable rollup.rows
                       , unattributedLine model.unattributedUsd
                       ]


costSummaryLine : Agent.CostSummary -> Html Message
costSummaryLine summary =
    let
        spent =
            "today (UTC day): $" ++ formatUsd summary.dailySpentUsd ++ " spent"

        cap =
            if summary.dailyCapUsd > 0 then
                " / $"
                    ++ formatUsd summary.dailyCapUsd
                    ++ " cap ($"
                    ++ formatUsd summary.dailyRemainingUsd
                    ++ " left)"

            else
                ""

        exhausted =
            if summary.dailyExhausted then
                [ Html.span
                    [ class "agent-budget-exhausted"
                    , style "margin-left" "8px"
                    , style "color" amberColor
                    , style "font-weight" "700"
                    ]
                    [ Html.text "budget exhausted" ]
                ]

            else
                []
    in
    Html.div
        [ style "margin" "0 0 12px 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.span [] [ Html.text (spent ++ cap) ] :: exhausted)


{-| A slim gauge of today's spend against the daily cap, mirroring the ticket
budget bar. Rendered only when a cap is configured; turns amber once the cap is
exhausted. Complements — does not replace — the exact-text summary line above.
-}
dailyCapGauge : Agent.CostSummary -> Html Message
dailyCapGauge summary =
    if summary.dailyCapUsd <= 0 then
        -- An uncapped ledger deserves an explicit statement, not silence:
        -- otherwise "no gauge" is indistinguishable from "under the cap".
        Html.div
            [ class "agent-daily-cap-none"
            , style "margin" "0 0 12px 0"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" subtleColor
            ]
            [ Html.text "no daily cap set — spend is unbounded (web flag: --agent-daily-budget-usd)" ]

    else
        let
            pct =
                min 100 (summary.dailySpentUsd / summary.dailyCapUsd * 100)
        in
        Html.div
            [ class "agent-daily-cap-gauge"
            , style "max-width" "320px"
            , style "margin" "0 0 12px 0"
            , style "height" "6px"
            , style "background" "#3d3c3c"
            ]
            [ Html.div
                [ style "height" "6px"
                , style "width" (String.fromFloat pct ++ "%")
                , style "background"
                    (if summary.dailyExhausted then
                        amberColor

                     else
                        "#7aa37a"
                    )
                ]
                []
            ]


{-| Spend the by-ticket rollup reports under no ticket at all (the CI review
runs, harvest pushes, platform housekeeping — the rollup's empty-string key).
It is a large share of real spend and would otherwise appear nowhere.
-}
unattributedLine : Maybe Float -> Html Message
unattributedLine maybeUsd =
    case maybeUsd of
        Just usd ->
            if usd > 0 then
                Html.div
                    [ class "agent-unattributed-cost"
                    , style "margin" "8px 0 0 0"
                    , style "font-family" "monospace"
                    , style "font-size" "12px"
                    , style "color" mutedColor
                    ]
                    [ Html.text ("unattributed (no ticket, all time): $" ++ formatUsd usd) ]

            else
                Html.text ""

        Nothing ->
            Html.text ""


costTable : List Agent.CostRow -> Html Message
costTable rows =
    if List.isEmpty rows then
        mutedLine "no cost records yet"

    else
        Html.table
            [ class "agent-costs-table"
            , style "border-collapse" "collapse"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "color" Colors.text
            ]
            (costHeaderRow :: List.map costRow rows)


costHeaderRow : Html Message
costHeaderRow =
    Html.tr []
        [ tableHeaderCell "left" "day (UTC)"
        , tableHeaderCell "right" "entries"
        , tableHeaderCell "right" "tokens (in+out)"
        , tableHeaderCell "right" "turns"
        , tableHeaderCell "right" "cost"
        ]


costRow : Agent.CostRow -> Html Message
costRow r =
    Html.tr [ class "agent-cost-row" ]
        [ tableCell "left" r.key
        , tableCell "right" (String.fromInt r.entries)
        , tableCell "right" (String.fromInt r.inputTokens ++ "+" ++ String.fromInt r.outputTokens)
        , tableCell "right" (String.fromInt r.turns)
        , tableCell "right" ("$" ++ formatUsd r.costUsd)
        ]



-- SHARED TABLE + TIME HELPERS


tableHeaderCell : String -> String -> Html Message
tableHeaderCell align content =
    Html.th
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "color" mutedColor
        , style "font-weight" "700"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


tableCell : String -> String -> Html Message
tableCell align content =
    Html.td
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


{-| Humanize an optional epoch timestamp as a yyyy-mm-dd hh:mm in the viewer's
own time zone (from `session.timeZone`), or "—" when absent. Showing local time
is what an operator expects for "which of today's runs came first"; the minutes
matter on an ops console. The server-aggregated cost buckets stay labelled as
UTC days separately.
-}
formatPosix : Time.Zone -> Maybe Time.Posix -> String
formatPosix zone maybe =
    case maybe of
        Nothing ->
            "—"

        Just posix ->
            String.fromInt (Time.toYear zone posix)
                ++ "-"
                ++ pad2 (monthNumber (Time.toMonth zone posix))
                ++ "-"
                ++ pad2 (Time.toDay zone posix)
                ++ " "
                ++ pad2 (Time.toHour zone posix)
                ++ ":"
                ++ pad2 (Time.toMinute zone posix)


pad2 : Int -> String
pad2 n =
    String.padLeft 2 '0' (String.fromInt n)


monthNumber : Time.Month -> Int
monthNumber month =
    case month of
        Time.Jan ->
            1

        Time.Feb ->
            2

        Time.Mar ->
            3

        Time.Apr ->
            4

        Time.May ->
            5

        Time.Jun ->
            6

        Time.Jul ->
            7

        Time.Aug ->
            8

        Time.Sep ->
            9

        Time.Oct ->
            10

        Time.Nov ->
            11

        Time.Dec ->
            12



-- CREDENTIALS SECTION (read-only status)


credentialsSection : Time.Zone -> Model -> Html Message
credentialsSection zone model =
    -- Two distinct slots, each behind its own labelled sub-header (U17): the
    -- vaulted PLATFORM credential dispatched runs authenticate with, and the
    -- viewer's OWN interactive credential. Keeping them apart stops an empty
    -- personal slot from reading as "the platform auth is missing".
    sectionBlock "agent-credentials" "Credentials" <|
        platformCredentialsBlock zone model.platformCredentials
            ++ personalCredentialsBlock zone model


{-| A bold, muted sub-header naming one of the two credential slots so the
platform slot and the personal slot can never be mistaken for one another.
-}
credentialSlotLabel : String -> Html Message
credentialSlotLabel labelText =
    Html.div
        [ style "color" mutedColor
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "font-weight" "700"
        , style "margin" "10px 0 2px 0"
        ]
        [ Html.text labelText ]


{-| The vaulted platform credential dispatched runs actually authenticate
with. Fetched with `?user=platform` (admin-only; a 403 hides the block), so
the section no longer claims "no credentials stored" while dispatch works.
Rendered under its own "Platform credential" header.
-}
platformCredentialsBlock : Time.Zone -> Maybe (List Agent.CredentialStatus) -> List (Html Message)
platformCredentialsBlock zone maybeCreds =
    case maybeCreds of
        Just (cred :: rest) ->
            [ credentialSlotLabel "Platform credential (used by dispatched runs)"
            , Html.div
                [ class "agent-platform-credential"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "color" Colors.text
                , style "margin" "0 0 8px 0"
                ]
                (List.map
                    (\c ->
                        Html.div []
                            [ Html.text
                                (c.kind
                                    ++ " (expires "
                                    ++ formatPosix zone c.expiresAt
                                    ++ ") — active"
                                )
                            ]
                    )
                    (cred :: rest)
                )
            ]

        _ ->
            []


{-| The viewer's own credential, set via `fly agent auth`. Kept under its own
header so an empty personal slot reads as "you have no personal credential",
not "the platform auth is missing" (U17).
-}
personalCredentialsBlock : Time.Zone -> Model -> List (Html Message)
personalCredentialsBlock zone model =
    credentialSlotLabel "Your credential (interactive fly login)"
        :: mutedLine "set or rotate with: fly agent auth"
        :: (case model.credentials of
                Nothing ->
                    case model.credentialsError of
                        Just message ->
                            [ errorLine message ]

                        Nothing ->
                            [ mutedLine "loading…" ]

                Just [] ->
                    staleDataWarning model.credentialsError
                        ++ [ mutedLine "no personal credential stored — run: fly agent auth" ]

                Just creds ->
                    staleDataWarning model.credentialsError
                        ++ [ credentialsTable zone creds ]
           )


credentialsTable : Time.Zone -> List Agent.CredentialStatus -> Html Message
credentialsTable zone creds =
    Html.table
        [ class "agent-credentials-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.tr []
            [ tableHeaderCell "left" "kind"
            , tableHeaderCell "left" "expires"
            , tableHeaderCell "left" "last verified"
            ]
            :: List.map (credentialRow zone) creds
        )


credentialRow : Time.Zone -> Agent.CredentialStatus -> Html Message
credentialRow zone c =
    Html.tr [ class "agent-credential-row" ]
        [ tableCell "left" c.kind
        , tableCell "left" (formatPosix zone c.expiresAt)
        , tableCell "left" (formatPosix zone c.lastVerifiedAt)
        ]



-- PRINCIPALS SECTION (admin: mint / list / revoke)


principalsSection : Time.Zone -> Model -> Html Message
principalsSection zone model =
    sectionBlock "agent-principals" "Principals"
        (mintFence model
            :: revokeErrorLine model
            :: principalsBody zone model
        )


{-| Fence the privileged mint form inside its own bordered, labelled box so it
is unmistakably a credential-issuing control and does not read as just another
read-only panel sitting under the spend tables (U15). -}
mintFence : Model -> Html Message
mintFence model =
    Html.div
        [ class "agent-mint-fence"
        , style "border" ("1px solid " ++ amberColor)
        , style "border-radius" "4px"
        , style "padding" "10px 12px"
        , style "margin" "4px 0 12px 0"
        ]
        (Html.div
            [ style "color" amberColor
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "font-weight" "700"
            , style "margin-bottom" "8px"
            ]
            [ Html.text "Mint a principal — issues a privileged API credential" ]
            :: mintForm model
            :: mintedTokenBox model
        )


{-| Revoke failures render here, in the principals section next to the table —
not inside the mint form, where a revoke error would be far from the row that
triggered it and would only be cleared by a successful mint.
-}
revokeErrorLine : Model -> Html Message
revokeErrorLine model =
    case model.revokeError of
        Just message ->
            Html.div [ class "agent-revoke-error" ] [ errorLine message ]

        Nothing ->
            Html.text ""


mintForm : Model -> Html Message
mintForm model =
    Html.div
        [ class "agent-mint-form"
        , style "margin" "0 0 12px 0"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        [ Html.div [ style "margin-bottom" "6px" ]
            [ mintTextField "name" model.mintName AgentMintNameChanged
            , mintTextField "description (optional)" model.mintDescription AgentMintDescriptionChanged
            ]
        , Html.div
            [ class "agent-mint-scopes"
            , style "display" "flex"
            , style "flex-wrap" "wrap"
            , style "gap" "12px"
            , style "margin-bottom" "6px"
            ]
            (List.map (scopeCheckbox model) mintScopeVocabulary)
        , Html.div
            [ style "display" "flex"
            , style "align-items" "center"
            , style "gap" "10px"
            ]
            [ expiresField model.mintExpiresDays
            , mintButton model
            ]
        , mintErrorLine model
        ]


mintTextField : String -> String -> (String -> Message) -> Html Message
mintTextField ph val toMsg =
    Html.input
        [ type_ "text"
        , placeholder ph
        , value val
        , onInput toMsg
        , style "margin-right" "8px"
        , style "padding" "3px 6px"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "background" Colors.background
        , style "color" Colors.text
        , style "border" ("1px solid " ++ subtleColor)
        ]
        []


scopeCheckbox : Model -> String -> Html Message
scopeCheckbox model scope =
    Html.label
        [ class "agent-mint-scope"
        , style "display" "inline-flex"
        , style "align-items" "center"
        , style "gap" "4px"
        , style "cursor" "pointer"
        ]
        [ Html.input
            [ type_ "checkbox"
            , checked (Set.member scope model.mintScopes)
            , onClick (AgentMintScopeToggled scope)
            ]
            []
        , Html.text scope
        ]


expiresField : String -> Html Message
expiresField val =
    Html.label
        [ style "display" "inline-flex"
        , style "align-items" "center"
        , style "gap" "4px"
        , style "color" mutedColor
        ]
        ([ Html.text "expires in"
         , Html.input
            [ type_ "text"
            , placeholder "N"
            , value val
            , onInput AgentMintExpiresChanged
            , style "width" "48px"
            , style "padding" "3px 6px"
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "background" Colors.background
            , style "color" Colors.text
            , style "border"
                ("1px solid "
                    ++ (if expiresIsValid val then
                            subtleColor

                        else
                            amberColor
                       )
                )
            ]
            []
         , Html.text "days (optional)"
         ]
            ++ expiresHint val
        )


{-| Inline hint shown only when the expires field has non-blank input that does
not parse to a positive integer. Without this the field would silently mean
"never expires", which is a surprising, easy-to-miss failure.
-}
expiresHint : String -> List (Html Message)
expiresHint val =
    if expiresIsValid val then
        []

    else
        [ Html.span
            [ class "agent-mint-expires-hint"
            , style "color" amberColor
            , style "font-size" "11px"
            ]
            [ Html.text "must be a positive number of days; leave blank for no expiry" ]
        ]


mintButton : Model -> Html Message
mintButton model =
    let
        enabled =
            canMint model && not model.minting

        label =
            if model.minting then
                "minting…"

            else
                "mint"
    in
    Html.button
        [ class "agent-mint-button"
        , onClick AgentMintSubmitted
        , disabled (not enabled)
        , style "padding" "4px 12px"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "font-weight" "700"
        , style "border" "none"
        , style "border-radius" "3px"
        , style "cursor"
            (if enabled then
                "pointer"

             else
                "not-allowed"
            )
        , style "background"
            (if enabled then
                "#2e4f2e"

             else
                Colors.background
            )
        , style "color"
            (if enabled then
                "#9fdf9f"

             else
                subtleColor
            )
        ]
        [ Html.text label ]


mintErrorLine : Model -> Html Message
mintErrorLine model =
    case model.mintError of
        Just message ->
            errorLine message

        Nothing ->
            Html.text ""


mintedTokenBox : Model -> List (Html Message)
mintedTokenBox model =
    case model.mintedToken of
        Nothing ->
            []

        Just token ->
            [ Html.div
                [ class "agent-minted-token"
                , style "margin" "0 0 12px 0"
                , style "padding" "8px 12px"
                , style "border" ("1px solid " ++ amberColor)
                , style "border-radius" "3px"
                , style "background" "#3a3320"
                , style "font-family" "monospace"
                , style "font-size" "12px"
                , style "color" Colors.text
                ]
                [ Html.div
                    [ style "color" amberColor
                    , style "font-weight" "700"
                    , style "margin-bottom" "4px"
                    ]
                    [ Html.text "token (shown once — copy it now):" ]
                , Html.div
                    [ class "agent-minted-token-value"
                    , style "word-break" "break-all"
                    ]
                    [ Html.text token ]
                , Html.button
                    [ class "agent-minted-token-dismiss"
                    , onClick AgentMintedTokenDismissed
                    , style "margin-top" "6px"
                    , style "padding" "2px 8px"
                    , style "font-family" "monospace"
                    , style "font-size" "11px"
                    , style "cursor" "pointer"
                    , style "background" Colors.background
                    , style "color" mutedColor
                    , style "border" ("1px solid " ++ subtleColor)
                    , style "border-radius" "3px"
                    ]
                    [ Html.text "dismiss" ]
                ]
            ]


principalsBody : Time.Zone -> Model -> List (Html Message)
principalsBody zone model =
    case model.principals of
        Nothing ->
            case model.principalsError of
                Just message ->
                    [ errorLine message ]

                Nothing ->
                    [ mutedLine "loading…" ]

        Just [] ->
            staleDataWarning model.principalsError
                ++ [ mutedLine "no principals yet — mint one above" ]

        Just principals ->
            let
                ( ephemeral, durable ) =
                    List.partition isEphemeralPrincipal principals
            in
            staleDataWarning model.principalsError
                ++ (if List.isEmpty durable then
                        [ mutedLine "no durable principals — mint one above" ]

                    else
                        [ principalsTable zone durable ]
                   )
                ++ ephemeralPrincipals zone model ephemeral


{-| Per-run agent tokens are named `run-<id>` and are minted/revoked
automatically for every dispatch, so they flood the principals table. Fold them
behind a collapsed toggle, leaving the durable (human/service) principals in the
main table.
-}
isEphemeralPrincipal : Agent.Principal -> Bool
isEphemeralPrincipal p =
    case String.split "-" p.name of
        [ "run", n ] ->
            String.toInt n /= Nothing

        _ ->
            False


ephemeralPrincipals : Time.Zone -> Model -> List Agent.Principal -> List (Html Message)
ephemeralPrincipals zone model ephemeral =
    if List.isEmpty ephemeral then
        []

    else
        Html.button
            [ class "agent-ephemeral-toggle"
            , onClick AgentPrincipalsShowEphemeralToggled
            , style "background" "transparent"
            , style "border" "none"
            , style "color" subtleColor
            , style "font-family" "monospace"
            , style "font-size" "12px"
            , style "cursor" "pointer"
            , style "padding" "8px 0"
            , style "text-align" "left"
            ]
            [ Html.text
                ((if model.showEphemeralPrincipals then
                    "▾ hide "

                  else
                    "▸ show "
                 )
                    ++ String.fromInt (List.length ephemeral)
                    ++ " ephemeral run "
                    ++ (if List.length ephemeral == 1 then
                            "principal"

                        else
                            "principals"
                       )
                )
            ]
            :: (if model.showEphemeralPrincipals then
                    [ principalsTable zone ephemeral ]

                else
                    []
               )


principalsTable : Time.Zone -> List Agent.Principal -> Html Message
principalsTable zone principals =
    Html.table
        [ class "agent-principals-table"
        , style "border-collapse" "collapse"
        , style "font-family" "monospace"
        , style "font-size" "12px"
        , style "color" Colors.text
        ]
        (Html.tr []
            [ tableHeaderCell "left" "name"
            , tableHeaderCell "left" "scopes"
            , tableHeaderCell "left" "team"
            , tableHeaderCell "left" "created"
            , tableHeaderCell "left" "expires"
            , tableHeaderCell "left" "last used"
            , tableHeaderCell "left" ""
            ]
            :: List.map (principalRow zone) principals
        )


principalRow : Time.Zone -> Agent.Principal -> Html Message
principalRow zone p =
    let
        dim =
            case p.revokedAt of
                Just _ ->
                    [ style "opacity" "0.5" ]

                Nothing ->
                    []

        action =
            case p.revokedAt of
                Just revokedAt ->
                    Html.span
                        [ class "agent-principal-revoked"
                        , style "color" subtleColor
                        ]
                        [ Html.text ("revoked " ++ formatPosix zone (Just revokedAt)) ]

                Nothing ->
                    Html.button
                        [ class "agent-principal-revoke"
                        , onClick (AgentPrincipalRevokeClicked p.id)
                        , style "padding" "2px 8px"
                        , style "font-family" "monospace"
                        , style "font-size" "11px"
                        , style "cursor" "pointer"
                        , style "background" "#5c2626"
                        , style "color" "#f0a0a0"
                        , style "border" "none"
                        , style "border-radius" "3px"
                        ]
                        [ Html.text "revoke" ]
    in
    Html.tr (class "agent-principal-row" :: dim)
        [ tableCell "left" p.name
        , tableCell "left" (String.join ", " p.scopes)
        , tableCell "left" p.teamName
        , tableCell "left" (formatPosix zone (Just p.createdAt))
        , tableCell "left" (formatPosix zone p.expiresAt)
        , tableCell "left" (formatPosix zone p.lastUsedAt)
        , Html.td
            [ style "padding" "4px 16px 4px 0"
            , style "border-bottom" rowBorder
            ]
            [ action ]
        ]
