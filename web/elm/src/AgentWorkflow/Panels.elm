module AgentWorkflow.Panels exposing
    ( Panel(..)
    , StartConfig
    , VersionsConfig
    , fromQuery
    , fromString
    , toQueryPairs
    , toString
    , viewStart
    , viewVersions
    )

{-| The Start and Versions overlays.

Definition management is not page furniture. Starting a run, promoting a
revision, and comparing revisions are things you do occasionally and
deliberately; the canvas and the run list are what you look at. So they live
behind two header actions that open overlays above the graph, and nothing
leaves the page.

Panel state is URL state (`?panel=versions`) so a panel is linkable and the
browser's back button closes it, which is what people press.

The Versions panel is also where **removed nodes** are discoverable. The
canvas never renders a union graph across revisions, so when the overview
reports `has_historical_only_nodes` — runs in this window touched nodes the
promoted graph no longer contains — the panel says so and points at the
revision that still has them.

-}

import Concourse.Agent as Agent
import DateFormat
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (class, disabled, id, name, placeholder, type_, value)
import Html.Events exposing (onClick, onInput)
import Message.Message exposing (Message(..))
import Time


type Panel
    = Start
    | Versions


toString : Panel -> String
toString panel =
    case panel of
        Start ->
            "start"

        Versions ->
            "versions"


{-| An unrecognised panel name opens nothing rather than failing the page, the
same rule the filters follow.
-}
fromString : String -> Maybe Panel
fromString raw =
    case raw of
        "start" ->
            Just Start

        "versions" ->
            Just Versions

        _ ->
            Nothing


fromQuery : List ( String, String ) -> Maybe Panel
fromQuery pairs =
    pairs
        |> List.filter (\( key, _ ) -> key == "panel")
        |> List.head
        |> Maybe.map Tuple.second
        |> Maybe.andThen fromString


toQueryPairs : Maybe Panel -> List ( String, String )
toQueryPairs panel =
    case panel of
        Nothing ->
            []

        Just open ->
            [ ( "panel", toString open ) ]


type alias StartConfig =
    { versions : List Agent.WorkflowVersion
    , selectedVersion : Maybe Agent.WorkflowVersion
    , selectedVersionRaw : String
    , inputValues : Dict String String
    , canStart : Bool
    , starting : Bool
    , startError : Bool
    }


type alias VersionsConfig =
    { zone : Time.Zone
    , versions : Maybe (List Agent.WorkflowVersion)
    , promotionError : Bool
    , hasPromotedVersion : Bool
    , graphVersion : Int
    , hasHistoricalOnlyNodes : Bool
    }


shell : String -> String -> List (Html Message) -> Html Message
shell panelId title body =
    Html.section
        [ class "agent-workflow-panel", id panelId ]
        (Html.header [ class "agent-workflow-panel-head" ]
            [ Html.h2 [] [ Html.text title ]
            , Html.button
                [ type_ "button"
                , class "agent-workflow-panel-close"
                , onClick AgentWorkflowPanelClosed
                ]
                [ Html.text "close" ]
            ]
            :: body
        )


viewStart : StartConfig -> Html Message
viewStart config =
    shell "agent-workflow-start-panel"
        "Start a durable run"
        (case config.selectedVersion of
            Nothing ->
                [ Html.p [ class "agent-workflow-panel-note" ]
                    [ Html.text "This workflow has no imported version to run." ]
                ]

            Just version ->
                [ Html.section [ class "agent-workflow-signature-detail" ]
                    [ Html.p [ class "agent-workflow-signature-hashes" ]
                        [ Html.text
                            ("schema v"
                                ++ String.fromInt version.schemaVersion
                                ++ " · signature v"
                                ++ String.fromInt version.signatureVersion
                                ++ " · #"
                                ++ String.left 16 version.contentHash
                            )
                        ]
                    , Html.div [ class "agent-workflow-signature-ports" ]
                        [ portList "inputs" version.signature.inputs
                        , portList "outputs" version.signature.outputs
                        ]
                    ]
                , startForm config version
                ]
        )


portList : String -> List Agent.WorkflowPort -> Html Message
portList label ports =
    Html.div [ class ("agent-workflow-" ++ label) ]
        [ Html.h3 [] [ Html.text label ]
        , if List.isEmpty ports then
            Html.p [ class "agent-workflow-panel-note" ] [ Html.text "none" ]

          else
            Html.ul []
                (List.map
                    (\signaturePort ->
                        Html.li [ class "agent-workflow-port" ]
                            [ Html.text
                                (signaturePort.name
                                    ++ " : "
                                    ++ signaturePort.typeRef
                                    ++ (if signaturePort.optional then
                                            " (optional)"

                                        else
                                            ""
                                       )
                                )
                            ]
                    )
                    ports
                )
        ]


startForm : StartConfig -> Agent.WorkflowVersion -> Html Message
startForm config version =
    Html.div [ class "agent-workflow-start-form" ]
        ([ Html.label []
            [ Html.text "definition version "
            , Html.select
                [ onInput AgentWorkflowVersionSelected, value config.selectedVersionRaw ]
                (config.versions
                    |> List.sortBy (negate << .version)
                    |> List.map
                        (\candidate ->
                            Html.option [ value (String.fromInt candidate.version) ]
                                [ Html.text
                                    ("v"
                                        ++ String.fromInt candidate.version
                                        ++ (if candidate.live then
                                                " · live"

                                            else
                                                ""
                                           )
                                    )
                                ]
                        )
                )
            ]
         ]
            ++ List.map (inputBinding config) version.signature.inputs
            ++ [ Html.button
                    [ type_ "button"
                    , class "agent-workflow-start"
                    , onClick AgentWorkflowStartClicked
                    , disabled (config.starting || not config.canStart)
                    ]
                    [ Html.text
                        (if config.starting then
                            "starting…"

                         else
                            "start run"
                        )
                    ]
               , if config.startError then
                    errorLine "The run was not admitted. Check the immutable input bindings and try again."

                 else
                    Html.text ""
               ]
        )


inputBinding : StartConfig -> Agent.WorkflowPort -> Html Message
inputBinding config signaturePort =
    Html.label [ class "agent-workflow-input" ]
        [ Html.span [] [ Html.text (signaturePort.name ++ " · " ++ signaturePort.typeRef) ]
        , Html.input
            [ name signaturePort.name
            , value (Dict.get signaturePort.name config.inputValues |> Maybe.withDefault "")
            , placeholder "snapshot ID"
            , onInput (AgentWorkflowInputChanged signaturePort.name)
            ]
            []
        ]


viewVersions : VersionsConfig -> Html Message
viewVersions config =
    shell "agent-workflow-versions-panel"
        "Definition versions"
        (List.filterMap identity
            [ if config.hasPromotedVersion then
                Nothing

              else
                Just
                    (Html.p [ class "agent-workflow-panel-note" ]
                        [ Html.text
                            ("No version is promoted. The canvas is showing version "
                                ++ String.fromInt config.graphVersion
                                ++ ", the latest imported one. Promote it to make it live."
                            )
                        ]
                    )
            , if config.hasHistoricalOnlyNodes then
                Just (historicalOnlyNote config.graphVersion)

              else
                Nothing
            , Just
                (case config.versions of
                    Nothing ->
                        Html.p [ class "agent-workflow-panel-note" ] [ Html.text "loading versions…" ]

                    Just versions ->
                        Html.div [ class "agent-workflow-version-timeline" ]
                            (versions
                                |> List.sortBy (negate << .version)
                                |> List.map (versionRow config.zone)
                            )
                )
            , if config.promotionError then
                Just (errorLine "Version promotion failed.")

              else
                Nothing
            ]
        )


{-| The discovery affordance for nodes that have left the promoted graph. The
canvas deliberately never renders a union graph, so without this the reader
would see runs that touched a node the picture does not contain and have no
way to find out what it was.
-}
historicalOnlyNote : Int -> Html Message
historicalOnlyNote graphVersion =
    Html.p [ class "agent-workflow-historical-only" ]
        [ Html.text
            ("Runs in this window touched nodes version "
                ++ String.fromInt graphVersion
                ++ " no longer contains. Open one of those runs to see its own graph — the canvas shows only the current revision."
            )
        ]


versionRow : Time.Zone -> Agent.WorkflowVersion -> Html Message
versionRow zone version =
    Html.div [ class "agent-workflow-version" ]
        [ Html.strong []
            [ Html.text
                ("v"
                    ++ String.fromInt version.version
                    ++ (if version.live then
                            " · live"

                        else
                            ""
                       )
                )
            ]
        , Html.span [ class "agent-workflow-version-detail" ]
            [ Html.text
                ("schema "
                    ++ String.fromInt version.schemaVersion
                    ++ "/signature "
                    ++ String.fromInt version.signatureVersion
                    ++ " · #"
                    ++ String.left 16 version.contentHash
                    ++ " · by "
                    ++ version.createdBy
                    ++ promotionSuffix zone version
                )
            ]
        , if version.live then
            Html.text ""

          else
            Html.button
                [ type_ "button", onClick (AgentWorkflowPromoteClicked version.version) ]
                [ Html.text "promote live" ]
        ]


promotionSuffix : Time.Zone -> Agent.WorkflowVersion -> String
promotionSuffix zone version =
    case version.promotedAt of
        Nothing ->
            ""

        Just promotedAt ->
            " · promoted by "
                ++ (if version.promotedBy == "" then
                        "unknown"

                    else
                        version.promotedBy
                   )
                ++ " on "
                ++ formatPromotedAt zone promotedAt


formatPromotedAt : Time.Zone -> Time.Posix -> String
formatPromotedAt zone posix =
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
        posix


errorLine : String -> Html Message
errorLine message =
    Html.p [ class "agent-page-error" ] [ Html.text message ]
