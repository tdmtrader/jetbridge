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
import Concourse.Agent as Agent
import Dict
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, disabled, id, style, title)
import Html.Events exposing (onClick)
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription(..))
import Routes
import SideBar.SideBar as SideBar
import Time
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { teamName : String
        , now : Time.Posix
        , runs : List Agent.RunMetric
        , runsError : Bool
        , costs : Maybe Agent.CostRollup
        , costsError : Bool
        , workflows : List Agent.Workflow
        , workflowsError : Bool
        , principals : List Agent.Principal
        , principalsError : Bool
        , credentials : List Agent.Credential
        , credentialsError : Bool
        }


init : { teamName : String } -> ( Model, List Effect )
init { teamName } =
    ( { teamName = teamName
      , now = Time.millisToPosix 0
      , runs = []
      , runsError = False
      , costs = Nothing
      , costsError = False
      , workflows = []
      , workflowsError = False
      , principals = []
      , principalsError = False
      , credentials = []
      , credentialsError = False
      , isUserMenuExpanded = False
      }
    , [ FetchAgentRunMetrics
      , FetchAgentCosts
      , FetchAgentWorkflows
      , FetchAgentPrincipals
      , FetchAgentCredentials
      ]
    )


documentTitle : String
documentTitle =
    "Agent platform"


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentRunMetricsFetched (Ok runs) ->
            ( { model | runs = runs, runsError = False }, effects )

        AgentRunMetricsFetched (Err _) ->
            ( { model | runsError = True }, effects )

        AgentCostsFetched (Ok costs) ->
            ( { model | costs = Just costs, costsError = False }, effects )

        AgentCostsFetched (Err _) ->
            ( { model | costsError = True }, effects )

        AgentWorkflowsFetched (Ok ws) ->
            ( { model | workflows = ws, workflowsError = False }, effects )

        AgentWorkflowsFetched (Err _) ->
            ( { model | workflowsError = True }, effects )

        AgentPrincipalsFetched (Ok ps) ->
            ( { model | principals = ps, principalsError = False }, effects )

        AgentPrincipalsFetched (Err _) ->
            ( { model | principalsError = True }, effects )

        AgentCredentialsFetched (Ok cs) ->
            ( { model | credentials = cs, credentialsError = False }, effects )

        AgentCredentialsFetched (Err _) ->
            ( { model | credentialsError = True }, effects )

        AgentPrincipalRevoked _ (Ok ()) ->
            -- re-fetch so the row shows its revoked state authoritatively
            ( model, effects ++ [ FetchAgentPrincipals ] )

        AgentWorkflowPromoted _ _ (Ok ()) ->
            ( model, effects ++ [ FetchAgentWorkflows ] )

        _ ->
            ( model, effects )


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        AgentRevokePrincipalClicked principalId ->
            ( model, effects ++ [ RevokeAgentPrincipal principalId ] )

        AgentPromoteWorkflowClicked name version ->
            ( model, effects ++ [ PromoteAgentWorkflow name version ] )

        _ ->
            ( model, effects )


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked _ time ->
            ( { model | now = time }, effects )

        _ ->
            ( model, effects )


subscriptions : List Subscription
subscriptions =
    [ OnClockTick FiveSeconds ]


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing



-- VIEW ------------------------------------------------------------------------


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.Agent { teamName = model.teamName }
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
                [ id "agent-dashboard"
                , style "padding" "20px 24px"
                , style "width" "100%"
                , style "overflow-y" "auto"
                , style "color" "#e6e7e8"
                ]
                [ Html.h1
                    [ style "font-size" "20px", style "font-weight" "700", style "margin-bottom" "4px" ]
                    [ Html.text "Agent platform" ]
                , Html.p
                    [ style "color" "#9b9b9b", style "margin-bottom" "20px", style "font-size" "13px" ]
                    [ Html.text "Runs, cost, workflow versions, principals, and credentials for agentic work." ]
                , costSummaryView model
                , sectionView "Recent agent runs"
                    model.runsError
                    (List.isEmpty model.runs)
                    "No agent runs recorded yet."
                    (runsTable model)
                , sectionView "Spend by day"
                    model.costsError
                    (model.costs == Nothing || List.isEmpty (costsRows model))
                    "No cost entries yet."
                    (costsTable model)
                , sectionView "Workflow definitions"
                    model.workflowsError
                    (List.isEmpty model.workflows)
                    "No workflow definitions imported."
                    (workflowsTable model)
                , sectionView "Principals"
                    model.principalsError
                    (List.isEmpty model.principals)
                    "No agent principals."
                    (principalsTable model)
                , sectionView "User credentials"
                    model.credentialsError
                    (List.isEmpty model.credentials)
                    "No vaulted credentials."
                    (credentialsTable model)
                ]
            ]
        ]



-- shared building blocks ------------------------------------------------------


card : List (Html Message) -> Html Message
card children =
    Html.div
        [ style "background" "#2e2c2c"
        , style "border" "1px solid #3d3c3c"
        , style "border-radius" "4px"
        , style "margin-bottom" "20px"
        , style "overflow-x" "auto"
        ]
        children


sectionView : String -> Bool -> Bool -> String -> Html Message -> Html Message
sectionView heading hasError isEmpty emptyText body =
    Html.div [ style "margin-bottom" "24px" ]
        [ Html.h2
            [ style "font-size" "14px"
            , style "font-weight" "700"
            , style "text-transform" "uppercase"
            , style "letter-spacing" "0.06em"
            , style "color" "#c8c8c8"
            , style "margin-bottom" "8px"
            ]
            [ Html.text heading ]
        , if hasError then
            Html.p [ style "color" "#f0a0a0", style "font-size" "13px" ]
                [ Html.text ("Couldn't load " ++ String.toLower heading ++ ".") ]

          else if isEmpty then
            Html.p [ style "color" "#8a8a8a", style "font-size" "13px" ] [ Html.text emptyText ]

          else
            card [ body ]
        ]


th : String -> Html Message
th label =
    Html.th
        [ style "text-align" "left"
        , style "padding" "8px 12px"
        , style "font-size" "11px"
        , style "text-transform" "uppercase"
        , style "letter-spacing" "0.05em"
        , style "color" "#8a8a8a"
        , style "border-bottom" "1px solid #3d3c3c"
        , style "white-space" "nowrap"
        ]
        [ Html.text label ]


td : List (Html.Attribute Message) -> List (Html Message) -> Html Message
td attrs children =
    Html.td
        ([ style "padding" "8px 12px"
         , style "font-size" "13px"
         , style "border-bottom" "1px solid #363535"
         , style "white-space" "nowrap"
         ]
            ++ attrs
        )
        children


tableEl : List (Html Message) -> List (Html Message) -> Html Message
tableEl headers rows =
    Html.table
        [ style "width" "100%"
        , style "border-collapse" "collapse"
        , style "font-variant-numeric" "tabular-nums"
        ]
        [ Html.thead [] [ Html.tr [] headers ]
        , Html.tbody [] rows
        ]


badge : String -> String -> Html Message
badge bg label =
    Html.span
        [ style "background" bg
        , style "color" "#fff"
        , style "padding" "1px 7px"
        , style "border-radius" "3px"
        , style "font-size" "11px"
        , style "font-weight" "700"
        , style "text-transform" "uppercase"
        , style "letter-spacing" "0.03em"
        ]
        [ Html.text label ]



-- cost summary ----------------------------------------------------------------


costSummaryView : Model -> Html Message
costSummaryView model =
    case model.costs of
        Nothing ->
            Html.text ""

        Just c ->
            let
                stat label value sub =
                    Html.div
                        [ style "flex" "1"
                        , style "min-width" "150px"
                        , style "padding" "14px 18px"
                        , style "border-right" "1px solid #3d3c3c"
                        ]
                        [ Html.div [ style "font-size" "11px", style "text-transform" "uppercase", style "letter-spacing" "0.05em", style "color" "#8a8a8a" ] [ Html.text label ]
                        , Html.div [ style "font-size" "22px", style "font-weight" "700", style "margin-top" "4px" ] [ Html.text value ]
                        , Html.div [ style "font-size" "11px", style "color" "#8a8a8a", style "margin-top" "2px" ] [ Html.text sub ]
                        ]
            in
            Html.div
                [ style "display" "flex"
                , style "flex-wrap" "wrap"
                , style "background" "#2e2c2c"
                , style "border" "1px solid #3d3c3c"
                , style "border-radius" "4px"
                , style "margin-bottom" "24px"
                ]
                [ stat "Spent today" (usd c.summary.dailySpentUsd) "across all agent sources"
                , stat "Daily cap"
                    (if c.summary.dailyCapUsd > 0 then
                        usd c.summary.dailyCapUsd

                     else
                        "none"
                    )
                    (if c.summary.dailyExhausted then
                        "exhausted"

                     else if c.summary.dailyCapUsd > 0 then
                        usd c.summary.dailyRemainingUsd ++ " remaining"

                     else
                        "uncapped"
                    )
                , stat "Runs recorded" (String.fromInt (List.length model.runs)) "most recent shown below"
                , stat "Workflows" (String.fromInt (List.length model.workflows)) "definitions"
                ]



-- runs ------------------------------------------------------------------------


runsTable : Model -> Html Message
runsTable model =
    tableEl
        [ th "Step", th "Workflow", th "Status", th "Cost", th "Tokens (in/out)", th "Turns", th "Wall", th "Ticket", th "When" ]
        (List.map (runRow model.now) model.runs)


runRow : Time.Posix -> Agent.RunMetric -> Html Message
runRow now r =
    Html.tr []
        [ td [ style "font-weight" "600" ]
            [ Html.text r.stepName
            , Html.div [ style "font-size" "11px", style "color" "#8a8a8a", style "white-space" "normal", style "max-width" "320px" ] [ Html.text r.summary ]
            ]
        , td [] [ Html.text (workflowRef r.workflowName r.workflowVersion) ]
        , td [] [ statusBadge r.status ]
        , td [ style "text-align" "right" ] [ Html.text (usd r.costUsd) ]
        , td [] [ Html.text (tokens r.usage.inputTokens ++ " / " ++ tokens r.usage.outputTokens) ]
        , td [ style "text-align" "right" ] [ Html.text (String.fromInt r.turns) ]
        , td [] [ Html.text (walltime r.wallTimeSeconds) ]
        , td [ style "color" "#9b9b9b" ] [ Html.text (ticketRef r) ]
        , td [ style "color" "#9b9b9b", title (String.fromInt r.createdAt) ] [ Html.text (relativeAge now r.createdAt) ]
        ]


statusBadge : String -> Html Message
statusBadge status =
    badge (statusColor status) status


statusColor : String -> String
statusColor status =
    case status of
        "ok" ->
            "#11825b"

        "failed" ->
            "#c78f28"

        "parked" ->
            "#3a6ea5"

        _ ->
            "#a5433a"


workflowRef : String -> Maybe Int -> String
workflowRef name version =
    case ( name, version ) of
        ( "", _ ) ->
            "—"

        ( n, Just v ) ->
            n ++ "@" ++ String.fromInt v

        ( n, Nothing ) ->
            n


ticketRef : Agent.RunMetric -> String
ticketRef r =
    case r.ticketId of
        Just t ->
            "#" ++ String.fromInt t

        Nothing ->
            "CI"



-- spend by day ----------------------------------------------------------------


costsRows : Model -> List Agent.CostRow
costsRows model =
    model.costs |> Maybe.map .rows |> Maybe.withDefault []


costsTable : Model -> Html Message
costsTable model =
    tableEl
        [ th "Day", th "Entries", th "Tokens (in/out)", th "Turns", th "Cost" ]
        (List.map costRow (costsRows model))


costRow : Agent.CostRow -> Html Message
costRow c =
    Html.tr []
        [ td [ style "font-weight" "600" ] [ Html.text c.key ]
        , td [ style "text-align" "right" ] [ Html.text (String.fromInt c.entries) ]
        , td [] [ Html.text (tokens c.inputTokens ++ " / " ++ tokens c.outputTokens) ]
        , td [ style "text-align" "right" ] [ Html.text (String.fromInt c.turns) ]
        , td [ style "text-align" "right", style "font-weight" "600" ] [ Html.text (usd c.costUsd) ]
        ]



-- workflows -------------------------------------------------------------------


workflowsTable : Model -> Html Message
workflowsTable model =
    tableEl
        [ th "Name", th "Live", th "Latest", th "Hash", th "Description", th "Action" ]
        (List.map workflowRow model.workflows)


workflowRow : Agent.Workflow -> Html Message
workflowRow w =
    let
        liveCell =
            case w.liveVersion of
                Just v ->
                    badge "#11825b" ("v" ++ String.fromInt v)

                Nothing ->
                    Html.span [ style "color" "#8a8a8a" ] [ Html.text "none" ]

        canPromote =
            w.liveVersion /= Just w.latestVersion && w.latestVersion > 0
    in
    Html.tr []
        [ td [ style "font-weight" "600" ] [ Html.text w.name ]
        , td [] [ liveCell ]
        , td [] [ Html.text ("v" ++ String.fromInt w.latestVersion) ]
        , td [ style "font-family" "monospace", style "color" "#9b9b9b", style "font-size" "12px" ] [ Html.text (shortHash w.contentHash) ]
        , td [ style "white-space" "normal", style "max-width" "360px", style "color" "#c8c8c8" ] [ Html.text w.description ]
        , td []
            [ if canPromote then
                actionButton "Promote latest" (AgentPromoteWorkflowClicked w.name w.latestVersion)

              else
                Html.span [ style "color" "#6a6a6a", style "font-size" "12px" ] [ Html.text "live" ]
            ]
        ]


shortHash : String -> String
shortHash h =
    String.left 12 h



-- principals ------------------------------------------------------------------


principalsTable : Model -> Html Message
principalsTable model =
    tableEl
        [ th "Name", th "Scopes", th "Prefix", th "Status", th "Last used", th "Action" ]
        (List.map (principalRow model.now) model.principals)


principalRow : Time.Posix -> Agent.Principal -> Html Message
principalRow now p =
    let
        active =
            Agent.principalActive now p
    in
    Html.tr []
        [ td [ style "font-weight" "600" ]
            [ Html.text p.name
            , Html.div [ style "font-size" "11px", style "color" "#8a8a8a", style "white-space" "normal", style "max-width" "280px" ] [ Html.text p.description ]
            ]
        , td [ style "white-space" "normal", style "max-width" "220px" ]
            [ Html.text
                (if List.isEmpty p.scopes then
                    "—"

                 else
                    String.join ", " p.scopes
                )
            ]
        , td [ style "font-family" "monospace", style "color" "#9b9b9b", style "font-size" "12px" ]
            [ Html.text
                (if p.tokenPrefix == "" then
                    "—"

                 else
                    p.tokenPrefix
                )
            ]
        , td [] [ principalStatus now p ]
        , td [ style "color" "#9b9b9b" ] [ Html.text (maybeAge now p.lastUsedAt) ]
        , td []
            [ if active then
                actionButton "Revoke" (AgentRevokePrincipalClicked p.id)

              else
                Html.span [ style "color" "#6a6a6a", style "font-size" "12px" ] [ Html.text "—" ]
            ]
        ]


principalStatus : Time.Posix -> Agent.Principal -> Html Message
principalStatus now p =
    case p.revokedAt of
        Just _ ->
            badge "#6a6a6a" "revoked"

        Nothing ->
            case p.expiresAt of
                Nothing ->
                    badge "#11825b" "active"

                Just e ->
                    let
                        secs =
                            e - (Time.posixToMillis now // 1000)
                    in
                    if secs <= 0 then
                        badge "#a5433a" "expired"

                    else if secs < 86400 then
                        Html.span [ title ("expires in " ++ humanizeDuration secs) ] [ badge "#c78f28" "expiring" ]

                    else
                        Html.span [ title ("expires in " ++ humanizeDuration secs) ] [ badge "#11825b" "active" ]



-- credentials -----------------------------------------------------------------


credentialsTable : Model -> Html Message
credentialsTable model =
    tableEl
        [ th "User", th "Kind", th "Expires", th "Last verified" ]
        (List.map (credentialRow model.now) model.credentials)


credentialRow : Time.Posix -> Agent.Credential -> Html Message
credentialRow now c =
    Html.tr []
        [ td [ style "font-weight" "600" ] [ Html.text c.userName ]
        , td [] [ Html.text c.kind ]
        , td [] [ expiryCell now c.expiresAt ]
        , td [ style "color" "#9b9b9b" ] [ Html.text (maybeAge now c.lastVerifiedAt) ]
        ]


expiryCell : Time.Posix -> Maybe Int -> Html Message
expiryCell now maybeExpiry =
    case maybeExpiry of
        Nothing ->
            Html.span [ style "color" "#8a8a8a" ] [ Html.text "no expiry" ]

        Just e ->
            let
                secs =
                    e - (Time.posixToMillis now // 1000)
            in
            if secs <= 0 then
                badge "#a5433a" "expired"

            else if secs < 3 * 86400 then
                Html.span [ style "color" "#c78f28" ] [ Html.text ("in " ++ humanizeDuration secs) ]

            else
                Html.span [ style "color" "#c8c8c8" ] [ Html.text ("in " ++ humanizeDuration secs) ]



-- action button ---------------------------------------------------------------


actionButton : String -> Message -> Html Message
actionButton label msg =
    Html.button
        [ onClick msg
        , disabled False
        , style "background" "transparent"
        , style "border" "1px solid #5a5a5a"
        , style "color" "#d8d8d8"
        , style "padding" "3px 10px"
        , style "border-radius" "3px"
        , style "font-size" "12px"
        , style "cursor" "pointer"
        ]
        [ Html.text label ]



-- formatting helpers ----------------------------------------------------------


usd : Float -> String
usd x =
    let
        cents =
            round (x * 100)

        dollars =
            cents // 100

        c =
            remainderBy 100 (abs cents)
    in
    "$" ++ String.fromInt dollars ++ "." ++ String.padLeft 2 '0' (String.fromInt c)


tokens : Int -> String
tokens n =
    if n >= 1000000 then
        String.fromInt (n // 1000000) ++ "." ++ String.left 1 (String.padLeft 2 '0' (String.fromInt (remainderBy 1000000 n // 10000))) ++ "M"

    else if n >= 1000 then
        String.fromInt (n // 1000) ++ "k"

    else
        String.fromInt n


walltime : Int -> String
walltime secs =
    if secs <= 0 then
        "—"

    else if secs < 60 then
        String.fromInt secs ++ "s"

    else
        String.fromInt (secs // 60) ++ "m" ++ String.padLeft 2 '0' (String.fromInt (remainderBy 60 secs)) ++ "s"


relativeAge : Time.Posix -> Int -> String
relativeAge now unixSecs =
    if unixSecs <= 0 then
        "—"

    else
        let
            secs =
                (Time.posixToMillis now // 1000) - unixSecs
        in
        if secs < 0 then
            "just now"

        else if secs < 60 then
            String.fromInt secs ++ "s ago"

        else if secs < 3600 then
            String.fromInt (secs // 60) ++ "m ago"

        else if secs < 86400 then
            String.fromInt (secs // 3600) ++ "h ago"

        else
            String.fromInt (secs // 86400) ++ "d ago"


maybeAge : Time.Posix -> Maybe Int -> String
maybeAge now m =
    case m of
        Just t ->
            relativeAge now t

        Nothing ->
            "never"


humanizeDuration : Int -> String
humanizeDuration secs =
    if secs < 3600 then
        String.fromInt (secs // 60) ++ "m"

    else if secs < 86400 then
        String.fromInt (secs // 3600) ++ "h"

    else
        String.fromInt (secs // 86400) ++ "d"
