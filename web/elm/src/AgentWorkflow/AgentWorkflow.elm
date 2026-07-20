module AgentWorkflow.AgentWorkflow exposing
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
import Dict
import EffectTransformer exposing (ET)
import Html exposing (Html)
import Html.Attributes exposing (class, disabled, id, placeholder, style, value)
import Html.Events exposing (onClick, onInput)
import Http
import Login.Login as Login
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Delivery(..), Interval(..), Subscription)
import Routes
import SideBar.SideBar as SideBar
import Time
import Tooltip
import Views.Styles
import Views.TopBar as TopBar


type alias Model =
    Login.Model
        { name : String
        , versions : Maybe (List Agent.WorkflowDefinition)
        , versionsError : Maybe String
        , targetVersion : Maybe Int
        , selected : Maybe Agent.WorkflowDefinition
        , predecessor : Maybe Agent.WorkflowDefinition
        , stats : Maybe (List Agent.WorkflowVersionStats)
        , statsError : Maybe String
        , annotationDraft : Maybe String
        }


init : { name : String } -> ( Model, List Effect )
init { name } =
    ( { name = name
      , versions = Nothing
      , versionsError = Nothing
      , targetVersion = Nothing
      , selected = Nothing
      , predecessor = Nothing
      , stats = Nothing
      , statsError = Nothing
      , annotationDraft = Nothing
      , isUserMenuExpanded = False
      }
    , [ FetchAgentWorkflowVersions name
      , FetchAgentWorkflowStats name
      ]
    )


documentTitle : Model -> String
documentTitle model =
    model.name ++ " · workflow"



-- CALLBACKS


{-| The target version to open is the live one, else the highest version. -}
pickTarget : List Agent.WorkflowDefinition -> Maybe Int
pickTarget versions =
    case List.filter .live versions of
        live :: _ ->
            Just live.version

        [] ->
            versions
                |> List.map .version
                |> List.maximum


{-| Fetches for a target version and its structural-diff predecessor (the
version numbered one lower, when it exists in the version list). -}
fetchForTarget : String -> List Agent.WorkflowDefinition -> Int -> List Effect
fetchForTarget name versions target =
    let
        predecessorExists =
            List.any (\d -> d.version == target - 1) versions
    in
    FetchAgentWorkflowVersion name target
        :: (if predecessorExists then
                [ FetchAgentWorkflowVersion name (target - 1) ]

            else
                []
           )


handleCallback : Callback -> ET Model
handleCallback callback ( model, effects ) =
    case callback of
        AgentWorkflowVersionsFetched _ (Ok versions) ->
            case pickTarget versions of
                Just target ->
                    ( { model
                        | versions = Just versions
                        , versionsError = Nothing
                        , targetVersion = Just target
                      }
                    , effects ++ fetchForTarget model.name versions target
                    )

                Nothing ->
                    ( { model | versions = Just versions, versionsError = Nothing }
                    , effects
                    )

        AgentWorkflowVersionsFetched _ (Err err) ->
            ( { model | versionsError = Just (errorMessage "versions" err) }, effects )

        AgentWorkflowVersionFetched _ (Ok def) ->
            if model.targetVersion == Just def.version then
                ( { model | selected = Just def }, effects )

            else if model.targetVersion == Just (def.version + 1) then
                ( { model | predecessor = Just def }, effects )

            else
                ( model, effects )

        AgentWorkflowVersionFetched _ (Err err) ->
            ( { model | versionsError = Just (errorMessage "version" err) }, effects )

        AgentWorkflowStatsFetched _ (Ok stats) ->
            ( { model | stats = Just stats, statsError = Nothing }, effects )

        AgentWorkflowStatsFetched _ (Err err) ->
            ( { model | statsError = Just (errorMessage "stats" err) }, effects )

        AgentWorkflowPromoted _ (Ok ()) ->
            ( model, effects ++ [ FetchAgentWorkflowVersions model.name ] )

        AgentWorkflowPromoted _ (Err err) ->
            ( { model | versionsError = Just (errorMessage "promotion" err) }, effects )

        AgentWorkflowLifecycleUpdated _ (Ok ()) ->
            ( { model | annotationDraft = Nothing }
            , effects ++ [ FetchAgentWorkflowVersions model.name ]
            )

        AgentWorkflowLifecycleUpdated _ (Err err) ->
            ( { model | versionsError = Just (errorMessage "update" err) }, effects )

        _ ->
            ( model, effects )


errorMessage : String -> Http.Error -> String
errorMessage what err =
    case err of
        Http.BadStatus { status } ->
            if status.code == 403 then
                "not authorized — the agent " ++ what ++ " API is admin-only"

            else if status.code == 404 then
                "unknown workflow"

            else
                "couldn't load " ++ what

        _ ->
            "couldn't load " ++ what



-- UPDATE


update : Message -> ET Model
update msg ( model, effects ) =
    case msg of
        SelectWorkflowVersion v ->
            ( { model
                | targetVersion = Just v
                , selected = Nothing
                , predecessor = Nothing
              }
            , effects
                ++ (case model.versions of
                        Just versions ->
                            fetchForTarget model.name versions v

                        Nothing ->
                            [ FetchAgentWorkflowVersion model.name v ]
                   )
            )

        ClickPromoteWorkflowVersion name v ->
            ( model, effects ++ [ PromoteAgentWorkflowVersion name v ] )

        EditWorkflowAnnotation s ->
            ( { model | annotationDraft = Just s }, effects )

        SaveWorkflowAnnotation name ->
            ( model
            , effects
                ++ [ UpdateAgentWorkflowLifecycle name
                        { annotation = Just (currentAnnotationDraft model)
                        , hidden = Nothing
                        }
                   ]
            )

        ClickDeprecateWorkflow name hidden ->
            ( model
            , effects
                ++ [ UpdateAgentWorkflowLifecycle name
                        { annotation = Nothing, hidden = Just hidden }
                   ]
            )

        _ ->
            ( model, effects )


handleDelivery : Delivery -> ET Model
handleDelivery delivery ( model, effects ) =
    case delivery of
        ClockTicked FiveSeconds _ ->
            ( model, effects ++ [ FetchAgentWorkflowStats model.name ] )

        _ ->
            ( model, effects )


subscriptions : List Subscription
subscriptions =
    [ Message.Subscription.OnClockTick FiveSeconds ]


tooltip : Model -> a -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing



-- lifecycle helpers (name-level; read off the selected version) ---------------


currentHidden : Model -> Bool
currentHidden model =
    model.selected |> Maybe.map .hidden |> Maybe.withDefault False


currentAnnotation : Model -> String
currentAnnotation model =
    model.selected |> Maybe.map .annotation |> Maybe.withDefault ""


currentAnnotationDraft : Model -> String
currentAnnotationDraft model =
    Maybe.withDefault (currentAnnotation model) model.annotationDraft



-- colors / small view helpers -------------------------------------------------


mutedColor : String
mutedColor =
    "#9b9b9b"


panelBorder : String
panelBorder =
    "1px solid #2b2b2b"


sectionTitle : String -> Html Message
sectionTitle t =
    Html.h2
        [ style "font-size" "14px"
        , style "text-transform" "uppercase"
        , style "letter-spacing" "0.5px"
        , style "color" mutedColor
        , style "margin" "20px 0 8px 0"
        ]
        [ Html.text t ]


mutedLine : String -> Html Message
mutedLine t =
    Html.div [ style "color" mutedColor, style "font-size" "13px" ] [ Html.text t ]


errorOr : Maybe String -> Maybe a -> (a -> Html Message) -> Html Message
errorOr maybeError maybeData render =
    case ( maybeError, maybeData ) of
        ( Just message, _ ) ->
            mutedLine ("couldn't load — " ++ message)

        ( Nothing, Just data ) ->
            render data

        ( Nothing, Nothing ) ->
            mutedLine "loading…"


pill : { bg : String, fg : String } -> String -> Html Message
pill { bg, fg } labelText =
    Html.span
        [ style "margin-left" "8px"
        , style "padding" "1px 8px"
        , style "border-radius" "3px"
        , style "font-size" "11px"
        , style "font-weight" "700"
        , style "background" bg
        , style "color" fg
        ]
        [ Html.text labelText ]


money : Float -> String
money v =
    "$" ++ roundTo2 v


roundTo2 : Float -> String
roundTo2 v =
    let
        cents =
            round (v * 100)

        whole =
            cents // 100

        frac =
            modBy 100 (abs cents)

        pad n =
            if n < 10 then
                "0" ++ String.fromInt n

            else
                String.fromInt n
    in
    String.fromInt whole ++ "." ++ pad frac


percent : Float -> String
percent rate =
    String.fromInt (round (rate * 100)) ++ "%"



-- VIEW


view : Session -> Model -> Html Message
view session model =
    let
        route =
            Routes.AgentWorkflow { name = model.name }
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
                [ id "agent-workflow-content"
                , style "padding" "16px"
                , style "width" "100%"
                , style "box-sizing" "border-box"
                , style "overflow-y" "auto"
                , style "color" Colors.text
                ]
                [ header model
                , definitionSection model
                , diffSection model
                , historySection model
                , statsSection session.timeZone model
                ]
            ]
        ]


header : Model -> Html Message
header model =
    Html.div []
        [ Html.div
            [ style "display" "flex", style "align-items" "baseline" ]
            [ Html.h1
                [ style "font-size" "20px", style "margin" "0", style "color" Colors.text ]
                [ Html.text model.name ]
            , if currentHidden model then
                pill { bg = "#4f2e2e", fg = "#df9f9f" } "deprecated"

              else
                Html.text ""
            ]
        , annotationEditor model
        ]


annotationEditor : Model -> Html Message
annotationEditor model =
    Html.div
        [ style "margin" "12px 0", style "display" "flex", style "align-items" "center", style "gap" "8px" ]
        [ Html.input
            [ placeholder "operator note…"
            , value (currentAnnotationDraft model)
            , onInput EditWorkflowAnnotation
            , style "flex" "1"
            , style "max-width" "480px"
            , style "padding" "6px 8px"
            , style "background" Colors.background
            , style "color" Colors.text
            , style "border" panelBorder
            ]
            []
        , button (SaveWorkflowAnnotation model.name) "Save note"
        , if currentHidden model then
            button (ClickDeprecateWorkflow model.name False) "Restore"

          else
            button (ClickDeprecateWorkflow model.name True) "Deprecate"
        ]


button : Message -> String -> Html Message
button msg labelText =
    Html.button
        [ onClick msg
        , style "padding" "6px 12px"
        , style "background" Colors.frame
        , style "color" Colors.text
        , style "border" panelBorder
        , style "cursor" "pointer"
        , style "font-size" "12px"
        ]
        [ Html.text labelText ]


definitionSection : Model -> Html Message
definitionSection model =
    Html.div []
        [ sectionTitle "definition"
        , errorOr model.versionsError model.selected definitionPanel
        ]


definitionPanel : Agent.WorkflowDefinition -> Html Message
definitionPanel def =
    Html.div []
        [ Html.div
            [ style "font-size" "13px", style "color" mutedColor, style "margin-bottom" "8px" ]
            [ Html.text
                ("version "
                    ++ String.fromInt def.version
                    ++ (if def.live then
                            " (live)"

                        else
                            ""
                       )
                    ++ "  ·  #"
                    ++ String.left 12 def.contentHash
                )
            ]
        , dagPreview def.config.steps
        , budgetPanel def.config
        , gatePanel def.config
        , promptsPanel def.config
        ]


dagPreview : List Agent.WorkflowStep -> Html Message
dagPreview steps =
    Html.div
        [ style "display" "flex"
        , style "align-items" "stretch"
        , style "flex-wrap" "wrap"
        , style "gap" "6px"
        , style "margin" "8px 0"
        ]
        (steps
            |> List.indexedMap
                (\i step ->
                    let
                        arrow =
                            if i == 0 then
                                []

                            else
                                [ Html.span
                                    [ style "align-self" "center", style "color" mutedColor, style "margin" "0 2px" ]
                                    [ Html.text "→" ]
                                ]
                    in
                    arrow ++ [ stepBox step ]
                )
            |> List.concat
        )


stepBox : Agent.WorkflowStep -> Html Message
stepBox step =
    let
        name =
            if step.agent /= "" then
                step.agent

            else if step.checkpoint /= "" then
                "⏸ " ++ step.checkpoint

            else
                "step"

        line label t =
            if t == "" then
                Html.text ""

            else
                Html.div [ style "font-size" "11px", style "color" mutedColor ] [ Html.text (label ++ t) ]
    in
    Html.div
        [ style "border" panelBorder
        , style "border-radius" "4px"
        , style "padding" "8px 10px"
        , style "min-width" "120px"
        , style "background" Colors.background
        ]
        [ Html.div [ style "font-weight" "700", style "color" Colors.text ] [ Html.text name ]
        , line "" step.model
        , if step.budgetSliceUsd > 0 then
            line "budget " (money step.budgetSliceUsd)

          else
            Html.text ""
        ]


budgetPanel : Agent.WorkflowConfig -> Html Message
budgetPanel config =
    Html.div
        [ style "margin" "8px 0", style "font-size" "13px", style "color" mutedColor ]
        [ Html.text
            ("budget: ticket "
                ++ money config.budgetTicketUsd
                ++ " · judge "
                ++ money config.budgetJudgeUsd
                ++ (if config.defaultModel /= "" then
                        " · default model " ++ config.defaultModel

                    else
                        ""
                   )
                ++ (if config.defaultMaxTurns > 0 then
                        " · max turns " ++ String.fromInt config.defaultMaxTurns

                    else
                        ""
                   )
            )
        ]


gatePanel : Agent.WorkflowConfig -> Html Message
gatePanel config =
    if List.isEmpty config.gates then
        Html.text ""

    else
        Html.div [ style "margin" "8px 0" ]
            [ Html.div [ style "font-size" "12px", style "color" mutedColor ] [ Html.text "gate policy" ]
            , Html.ul [ style "margin" "4px 0", style "padding-left" "18px" ]
                (List.map
                    (\g ->
                        Html.li [ style "font-size" "12px", style "color" Colors.text ]
                            [ Html.text (g.gate ++ " / " ++ g.scope ++ scopeFocus g.focus) ]
                    )
                    config.gates
                )
            , if config.onGateFailure /= "" then
                mutedLine ("on failure: " ++ config.onGateFailure)

              else
                Html.text ""
            ]


scopeFocus : String -> String
scopeFocus focus =
    if focus == "" then
        ""

    else
        " (" ++ focus ++ ")"


promptsPanel : Agent.WorkflowConfig -> Html Message
promptsPanel config =
    let
        entries =
            Dict.toList config.prompts
    in
    if List.isEmpty entries then
        Html.text ""

    else
        Html.div [ style "margin" "8px 0" ]
            [ Html.div [ style "font-size" "12px", style "color" mutedColor ] [ Html.text "prompts" ]
            , Html.div []
                (List.map
                    (\( key, body ) ->
                        Html.div [ style "margin" "6px 0" ]
                            [ Html.div [ style "font-weight" "700", style "font-size" "12px", style "color" Colors.text ] [ Html.text key ]
                            , Html.pre
                                [ style "margin" "2px 0"
                                , style "padding" "8px"
                                , style "background" Colors.background
                                , style "border" panelBorder
                                , style "white-space" "pre-wrap"
                                , style "font-size" "12px"
                                , style "color" Colors.text
                                ]
                                [ Html.text body ]
                            ]
                    )
                    entries
                )
            ]



-- structural diff -------------------------------------------------------------


diffSection : Model -> Html Message
diffSection model =
    case model.selected of
        Nothing ->
            Html.text ""

        Just selected ->
            Html.div []
                [ sectionTitle "changes vs. previous version"
                , case model.predecessor of
                    Nothing ->
                        mutedLine "no predecessor (first version)"

                    Just prev ->
                        case diffLines prev.config selected.config of
                            [] ->
                                mutedLine "no structural changes"

                            lines ->
                                Html.ul [ style "margin" "4px 0", style "padding-left" "18px" ]
                                    (List.map (\l -> Html.li [ style "font-size" "13px", style "color" Colors.text ] [ Html.text l ]) lines)
                ]


diffLines : Agent.WorkflowConfig -> Agent.WorkflowConfig -> List String
diffLines prev cur =
    List.filterMap identity
        [ deltaInt "steps" (List.length prev.steps) (List.length cur.steps)
        , deltaString "default model" prev.defaultModel cur.defaultModel
        , deltaMoney "ticket budget" prev.budgetTicketUsd cur.budgetTicketUsd
        , deltaInt "gates" (List.length prev.gates) (List.length cur.gates)
        , if prev.description /= cur.description then
            Just "description changed"

          else
            Nothing
        ]


deltaInt : String -> Int -> Int -> Maybe String
deltaInt label a b =
    if a /= b then
        Just (label ++ ": " ++ String.fromInt a ++ " → " ++ String.fromInt b)

    else
        Nothing


deltaString : String -> String -> String -> Maybe String
deltaString label a b =
    if a /= b then
        Just (label ++ ": " ++ blankDash a ++ " → " ++ blankDash b)

    else
        Nothing


deltaMoney : String -> Float -> Float -> Maybe String
deltaMoney label a b =
    if a /= b then
        Just (label ++ ": " ++ money a ++ " → " ++ money b)

    else
        Nothing


blankDash : String -> String
blankDash s =
    if s == "" then
        "—"

    else
        s



-- version history -------------------------------------------------------------


historySection : Model -> Html Message
historySection model =
    Html.div []
        [ sectionTitle "version history"
        , errorOr model.versionsError model.versions (historyTable model)
        ]


historyTable : Model -> List Agent.WorkflowDefinition -> Html Message
historyTable model versions =
    if List.isEmpty versions then
        mutedLine "no versions"

    else
        Html.div []
            (versions
                |> List.sortBy (\d -> -d.version)
                |> List.map (historyRow model)
            )


historyRow : Model -> Agent.WorkflowDefinition -> Html Message
historyRow model def =
    Html.div
        [ style "display" "flex"
        , style "align-items" "baseline"
        , style "gap" "12px"
        , style "padding" "6px 0"
        , style "border-bottom" panelBorder
        ]
        [ Html.div [ style "font-weight" "700", style "min-width" "40px" ] [ Html.text ("v" ++ String.fromInt def.version) ]
        , if def.live then
            pill { bg = "#2e4f2e", fg = "#9fdf9f" } "live"

          else
            pill { bg = Colors.background, fg = mutedColor } "candidate"
        , Html.div [ style "flex" "1", style "font-size" "12px", style "color" mutedColor ]
            [ Html.text (def.createdBy ++ "  ·  #" ++ String.left 12 def.contentHash) ]
        , Html.button
            [ onClick (SelectWorkflowVersion def.version)
            , style "padding" "2px 8px"
            , style "background" Colors.frame
            , style "color" Colors.text
            , style "border" panelBorder
            , style "cursor" "pointer"
            , style "font-size" "11px"
            ]
            [ Html.text "View" ]
        , if def.live then
            Html.text ""

          else
            Html.button
                [ onClick (ClickPromoteWorkflowVersion model.name def.version)
                , style "padding" "2px 8px"
                , style "background" Colors.frame
                , style "color" Colors.text
                , style "border" panelBorder
                , style "cursor" "pointer"
                , style "font-size" "11px"
                ]
                [ Html.text "Set live" ]
        ]



-- stats -----------------------------------------------------------------------


statsSection : Time.Zone -> Model -> Html Message
statsSection _ model =
    Html.div []
        [ sectionTitle "run statistics"
        , errorOr model.statsError model.stats statsTable
        ]


statsTable : List Agent.WorkflowVersionStats -> Html Message
statsTable rows =
    if List.isEmpty rows then
        mutedLine "no runs yet"

    else
        Html.div []
            (statsHeaderRow :: List.map statsRow rows)


statsHeaderRow : Html Message
statsHeaderRow =
    Html.div
        [ style "display" "flex", style "gap" "16px", style "padding" "4px 0", style "color" mutedColor, style "font-size" "12px", style "font-weight" "700" ]
        [ statsCell "version"
        , statsCell "runs"
        , statsCell "tickets"
        , statsCell "success"
        , statsCell "avg cost"
        , statsCell "avg turns"
        ]


statsRow : Agent.WorkflowVersionStats -> Html Message
statsRow s =
    let
        version =
            case s.version of
                Just v ->
                    "v" ++ String.fromInt v

                Nothing ->
                    "ad-hoc"
    in
    Html.div
        [ style "display" "flex", style "gap" "16px", style "padding" "4px 0", style "font-size" "13px", style "color" Colors.text, style "border-bottom" panelBorder ]
        [ statsCell version
        , statsCell (String.fromInt s.runs)
        , statsCell (String.fromInt s.tickets)
        , statsCell (percent s.successRate)
        , statsCell (money s.avgCostUsd)
        , statsCell (roundTo1 s.avgTurns)
        ]


statsCell : String -> Html Message
statsCell t =
    Html.div [ style "min-width" "80px" ] [ Html.text t ]


roundTo1 : Float -> String
roundTo1 v =
    let
        tenths =
            round (v * 10)
    in
    String.fromInt (tenths // 10) ++ "." ++ String.fromInt (modBy 10 (abs tenths))
