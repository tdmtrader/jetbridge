module Agent.Agent exposing
    ( Model
    , changeSection
    , documentTitle
    , handleCallback
    , handleDelivery
    , init
    , subscriptions
    , tooltip
    , update
    , view
    )

import Agent.Admin as Admin
import Agent.Nav as Nav
import Agent.Runs as Runs
import Agent.Shared as Shared
import Agent.Spend as Spend
import Agent.Workflows as Workflows
import Application.Models exposing (Session)
import Concourse.Agent as Agent
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (id, style)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription
    exposing
        ( Delivery(..)
        , Interval(..)
        , Subscription
        )
import Polling
import Routes
import Set exposing (Set)
import SideBar.SideBar as SideBar
import Time
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


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
        , now : Maybe Time.Posix
        , section : Routes.AgentSection
        }


init : Routes.AgentSection -> ( Model, List Effect )
init section =
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
      , now = Nothing
      , section = section
      , isUserMenuExpanded = False
      }
      -- Fetch every section's data on load so switching tabs is instant against
      -- already-loaded data (the 1-min poll keeps it fresh). Per-section fetch
      -- is a deferred optimization.
    , [ FetchAgentRunMetrics
      , FetchAgentWorkflows
      , FetchAgentCostRollup
      , FetchAgentTicketCosts
      , FetchAgentCredentials
      , FetchAgentPlatformCredentials
      , FetchAgentPrincipals
      ]
    )


{-| A same-page section switch (e.g. /agent/runs → /agent/spend). Only the
active section changes; the fetched ledger/workflows/costs data is preserved so
the switch is instant. `Application.routeMatchesModel` routes these through
`urlUpdate` (data-preserving) rather than re-`init`.
-}
changeSection : Routes.AgentSection -> ET Model
changeSection section ( model, effects ) =
    ( { model | section = section }, effects )


documentTitle : Routes.AgentSection -> String
documentTitle section =
    case section of
        Routes.AgentRuns ->
            "Agent runs"

        Routes.AgentWorkflows ->
            "Agent workflows"

        Routes.AgentSpend ->
            "Agent spend"

        Routes.AgentAdmin ->
            "Agent admin"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentRunMetricsFetched (Ok fresh) ->
            ( { model
                | runs =
                    -- Keep the old list when the refetch decoded identical
                    -- data, so the lazy runs table below skips its render.
                    if model.runs == Just fresh then
                        model.runs

                    else
                        Just fresh
                , runsError = Nothing
              }
            , effects
            )

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


{-| The mint button is enabled only with a non-empty name, at least one scope
selected, and a valid expiry field — the shared rule the mint form and API both
enforce.
-}
canMint : Model -> Bool
canMint model =
    Shared.canMint
        { name = model.mintName
        , scopes = model.mintScopes
        , expiresDays = model.mintExpiresDays
        }


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


{-| Self-healing refresh. These fetches only replace the fetched data (and
clear their own errors); the mint form and the one-time token box live
outside the poll, so a tick can't wipe them. One minute is plenty: this is
near-static admin data (the cost rollup alone is a 30-day ledger
aggregation), and mutations (mint/revoke/promote) already refetch explicitly.
-}
polls : List (Polling.Poll Model)
polls =
    [ { interval = OneMinute
      , fetch =
            \_ ->
                [ FetchAgentRunMetrics
                , FetchAgentWorkflows
                , FetchAgentCostRollup
                , FetchAgentTicketCosts
                , FetchAgentCredentials
                , FetchAgentPlatformCredentials
                , FetchAgentPrincipals
                ]
      }
    ]


handleDelivery : Delivery -> ET Model
handleDelivery delivery =
    advanceNow delivery >> Polling.handleDelivery polls delivery


{-| Advance the "now" clock that feeds the runs table's relative "N ago"
timestamps. Kept out of `polls` (which stays fetch-only) and driven off the
existing OneMinute poll tick this page already subscribes to — minute
granularity is plenty for "3h ago" labels on a near-static admin console.
-}
advanceNow : Delivery -> ET Model
advanceNow delivery ( model, effects ) =
    case delivery of
        ClockTicked OneMinute time ->
            ( { model | now = Just time }, effects )

        _ ->
            ( model, effects )


subscriptions : List Subscription
subscriptions =
    Polling.subscriptions polls



-- VIEW


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.Agent model.section
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
                [ id "agent-content"
                , style "padding" "16px"
                , style "width" "100%"
                , style "box-sizing" "border-box"
                , style "overflow-y" "auto"
                ]
                [ Nav.view route
                , sectionView session model
                ]
            ]
        ]


sectionView : Session -> Model -> Html Message
sectionView session model =
    case model.section of
        Routes.AgentRuns ->
            Runs.view session.timeZone model.now model.expandedRuns model.runs model.runsError

        Routes.AgentWorkflows ->
            Workflows.view model.workflows model.workflowsError

        Routes.AgentSpend ->
            Spend.view
                { costRollup = model.costRollup
                , costError = model.costError
                , unattributedUsd = model.unattributedUsd
                }

        Routes.AgentAdmin ->
            Admin.view session.userState
                session.timeZone
                { credentials = model.credentials
                , credentialsError = model.credentialsError
                , platformCredentials = model.platformCredentials
                , principals = model.principals
                , principalsError = model.principalsError
                , revokeError = model.revokeError
                , showEphemeralPrincipals = model.showEphemeralPrincipals
                , mintName = model.mintName
                , mintDescription = model.mintDescription
                , mintScopes = model.mintScopes
                , mintExpiresDays = model.mintExpiresDays
                , mintedToken = model.mintedToken
                , mintError = model.mintError
                , minting = model.minting
                }
