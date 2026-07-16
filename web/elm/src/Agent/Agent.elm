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

import Application.Models exposing (Session)
import Colors
import Concourse.Agent as Agent
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (checked, class, disabled, id, placeholder, style, type_, value)
import Html.Events exposing (onClick, onInput)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
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
        { workflows : Maybe (List Agent.WorkflowSummary)
        , costRollup : Maybe Agent.CostRollup
        , workflowsError : Maybe String
        , costError : Maybe String
        , credentials : Maybe (List Agent.CredentialStatus)
        , credentialsError : Maybe String
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
        }


init : ( Model, List Effect )
init =
    ( { workflows = Nothing
      , costRollup = Nothing
      , workflowsError = Nothing
      , costError = Nothing
      , credentials = Nothing
      , credentialsError = Nothing
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
      , isUserMenuExpanded = False
      }
    , [ FetchAgentWorkflows
      , FetchAgentCostRollup
      , FetchAgentCredentials
      , FetchAgentPrincipals
      ]
    )


documentTitle : String
documentTitle =
    "Agent"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentWorkflowsFetched (Ok workflows) ->
            ( { model | workflows = Just workflows, workflowsError = Nothing }, effects )

        AgentWorkflowsFetched (Err err) ->
            ( { model | workflowsError = Just (errorMessage "workflows" err) }, effects )

        AgentCostRollupFetched (Ok costRollup) ->
            ( { model | costRollup = Just costRollup, costError = Nothing }, effects )

        AgentCostRollupFetched (Err err) ->
            ( { model | costError = Just (errorMessage "costs" err) }, effects )

        AgentCredentialsFetched (Ok credentials) ->
            ( { model | credentials = Just credentials, credentialsError = Nothing }, effects )

        AgentCredentialsFetched (Err err) ->
            ( { model | credentialsError = Just (errorMessage "credentials" err) }, effects )

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
                ++ [ FetchAgentWorkflows
                   , FetchAgentCostRollup
                   , FetchAgentCredentials
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


sectionBlock : String -> List (Html Message) -> Html Message
sectionBlock title children =
    Html.div
        [ style "margin-top" "24px" ]
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
                [ style "padding" "16px", style "width" "100%" ]
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
                , workflowsSection model
                , costsSection model
                , credentialsSection model
                , principalsSection model
                ]
            ]
        ]



-- WORKFLOWS SECTION


workflowsSection : Model -> Html Message
workflowsSection model =
    sectionBlock "Workflows" <|
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
    sectionBlock "Costs" <|
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
                       , costTable rollup.rows
                       ]


costSummaryLine : Agent.CostSummary -> Html Message
costSummaryLine summary =
    let
        spent =
            "today: $" ++ formatUsd summary.dailySpentUsd ++ " spent"

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
        [ tableHeaderCell "left" "day"
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


{-| Humanize an optional epoch timestamp as a UTC yyyy-mm-dd date, or "—"
when absent. Deliberately timezone-independent (UTC) so it is deterministic.
-}
formatPosix : Maybe Time.Posix -> String
formatPosix maybe =
    case maybe of
        Nothing ->
            "—"

        Just posix ->
            String.fromInt (Time.toYear Time.utc posix)
                ++ "-"
                ++ pad2 (monthNumber (Time.toMonth Time.utc posix))
                ++ "-"
                ++ pad2 (Time.toDay Time.utc posix)


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


credentialsSection : Model -> Html Message
credentialsSection model =
    sectionBlock "Credentials" <|
        mutedLine "set or rotate with: fly agent auth"
            :: (case model.credentials of
                    Nothing ->
                        case model.credentialsError of
                            Just message ->
                                [ errorLine message ]

                            Nothing ->
                                [ mutedLine "loading…" ]

                    Just [] ->
                        staleDataWarning model.credentialsError
                            ++ [ mutedLine "no credentials stored — run: fly agent auth" ]

                    Just creds ->
                        staleDataWarning model.credentialsError
                            ++ [ credentialsTable creds ]
               )


credentialsTable : List Agent.CredentialStatus -> Html Message
credentialsTable creds =
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
            :: List.map credentialRow creds
        )


credentialRow : Agent.CredentialStatus -> Html Message
credentialRow c =
    Html.tr [ class "agent-credential-row" ]
        [ tableCell "left" c.kind
        , tableCell "left" (formatPosix c.expiresAt)
        , tableCell "left" (formatPosix c.lastVerifiedAt)
        ]



-- PRINCIPALS SECTION (admin: mint / list / revoke)


principalsSection : Model -> Html Message
principalsSection model =
    sectionBlock "Principals"
        ([ mintForm model ]
            ++ mintedTokenBox model
            ++ [ revokeErrorLine model ]
            ++ principalsBody model
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


principalsBody : Model -> List (Html Message)
principalsBody model =
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
            staleDataWarning model.principalsError
                ++ [ principalsTable principals ]


principalsTable : List Agent.Principal -> Html Message
principalsTable principals =
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
            :: List.map principalRow principals
        )


principalRow : Agent.Principal -> Html Message
principalRow p =
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
                        [ Html.text ("revoked " ++ formatPosix (Just revokedAt)) ]

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
        , tableCell "left" (formatPosix (Just p.createdAt))
        , tableCell "left" (formatPosix p.expiresAt)
        , tableCell "left" (formatPosix p.lastUsedAt)
        , Html.td
            [ style "padding" "4px 16px 4px 0"
            , style "border-bottom" rowBorder
            ]
            [ action ]
        ]
