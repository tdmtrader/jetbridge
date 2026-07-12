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
import Html.Attributes exposing (class, id, style)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message)
import Message.Subscription
    exposing
        ( Delivery(..)
        , Interval(..)
        , Subscription(..)
        )
import Routes
import SideBar.SideBar as SideBar
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { workflows : Maybe (List Agent.WorkflowSummary)
        , costRollup : Maybe Agent.CostRollup
        , workflowsError : Maybe String
        , costError : Maybe String
        }


init : ( Model, List Effect )
init =
    ( { workflows = Nothing
      , costRollup = Nothing
      , workflowsError = Nothing
      , costError = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentWorkflows, FetchAgentCostRollup ]
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


update : Message -> ET Model
update _ ( model, effects ) =
    ( model, effects )


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked FiveSeconds _ ->
            ( model
            , effects ++ [ FetchAgentWorkflows, FetchAgentCostRollup ]
            )

        _ ->
            ( model, effects )


subscriptions : List Subscription
subscriptions =
    [ OnClockTick FiveSeconds ]



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
                [ mutedLine "no workflow definitions — import one with: fly agent workflows import" ]

            Just workflows ->
                [ Html.div [ class "agent-workflows" ] (List.map workflowRow workflows) ]


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
                [ costSummaryLine rollup.summary
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
        [ costHeaderCell "left" "day"
        , costHeaderCell "right" "entries"
        , costHeaderCell "right" "tokens (in+out)"
        , costHeaderCell "right" "turns"
        , costHeaderCell "right" "cost"
        ]


costHeaderCell : String -> String -> Html Message
costHeaderCell align content =
    Html.th
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "color" mutedColor
        , style "font-weight" "700"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]


costRow : Agent.CostRow -> Html Message
costRow r =
    Html.tr [ class "agent-cost-row" ]
        [ costCell "left" r.key
        , costCell "right" (String.fromInt r.entries)
        , costCell "right" (String.fromInt r.inputTokens ++ "+" ++ String.fromInt r.outputTokens)
        , costCell "right" (String.fromInt r.turns)
        , costCell "right" ("$" ++ formatUsd r.costUsd)
        ]


costCell : String -> String -> Html Message
costCell align content =
    Html.td
        [ style "text-align" align
        , style "padding" "4px 16px 4px 0"
        , style "border-bottom" rowBorder
        ]
        [ Html.text content ]
