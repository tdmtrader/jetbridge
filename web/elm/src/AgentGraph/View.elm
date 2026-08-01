module AgentGraph.View exposing
    ( Config
    , NodeState
    , emptyState
    , stateLabel
    , view
    )

{-| Rendering for a laid-out semantic graph.

The renderer is parameterised by a `NodeState` lookup rather than owning
state. That is the mechanism that stops the individual run page from becoming
a second graph model: aggregate counts across many runs and one run's own
state are two lookups into one renderer.

Two rules govern what it draws.

**Node bodies stay neutral.** State is a glyph *and* a count *and* a word, so
colour is only ever reinforcement and the canvas survives being read in
greyscale or by a screen reader. There is deliberately no `--failed` or
`--running` modifier on the node: several concurrent runs of one node have no
canonical status, and a whole-node status fill would assert one the platform
does not have. Selection is a border and halo, never a colour swap. A node
with no activity in the window renders distinctly from a healthy one rather
than as a grey that reads as green.

**Only execution nodes carry state.** Inputs, outputs, and resource sources
never run and never have an occurrence, so they render neutral. Showing them
as no-data would fill the canvas with dashed boxes that look like missing
history.

Nodes are HTML positioned over an SVG edge layer rather than SVG text. Long
display names have to truncate to a tooltip (the design's scale section), and
`text-overflow` does not exist in SVG.

-}

import AgentGraph.Layout as Layout
import AgentGraph.Model as Model
import Html exposing (Html)
import Html.Attributes exposing (class, style)
import Html.Events
import Svg
import Svg.Attributes


{-| One node's state.

The active counts (`running`, `waiting`, `pending`) are what is happening now,
across runs that are themselves still executing. The terminal counts are the
window's history. `needsAttention` is the retry-chain-resolved answer to "does
this need action now", which is why a successful retry clears the canvas
without erasing the failure from history.

There is deliberately no aggregate `status` field, mirroring the server's
`workflowoverview.NodeState`. Inventing one here would reintroduce exactly
what the API refuses to emit.

-}
type alias NodeState =
    { running : Int
    , waiting : Int
    , pending : Int
    , succeeded : Int
    , failed : Int
    , errored : Int
    , aborted : Int
    , skipped : Int
    , needsAttention : Bool
    , hasWindowActivity : Bool
    }


emptyState : NodeState
emptyState =
    { running = 0
    , waiting = 0
    , pending = 0
    , succeeded = 0
    , failed = 0
    , errored = 0
    , aborted = 0
    , skipped = 0
    , needsAttention = False
    , hasWindowActivity = False
    }


type alias Config msg =
    { selected : Maybe String
    , nodeState : String -> NodeState
    , onSelect : String -> msg
    }


view : Config msg -> Layout.LaidOut -> Html msg
view config laidOut =
    Html.div
        [ class "agent-graph"
        , style "width" (px laidOut.width)
        , style "height" (px laidOut.height)
        ]
        [ Svg.svg
            [ Svg.Attributes.class "agent-graph-edges"
            , Svg.Attributes.viewBox
                ("0 0 " ++ String.fromFloat laidOut.width ++ " " ++ String.fromFloat laidOut.height)
            , Svg.Attributes.width (String.fromFloat laidOut.width)
            , Svg.Attributes.height (String.fromFloat laidOut.height)
            ]
            (List.map viewEdge laidOut.edges)
        , Html.div [ class "agent-graph-nodes" ] (List.map (viewNode config) laidOut.nodes)
        ]


viewNode : Config msg -> Layout.LaidOutNode -> Html msg
viewNode config item =
    let
        node =
            item.node

        state =
            if Model.isEndpoint node.kind then
                Nothing

            else
                Just (config.nodeState node.id)

        isSelected =
            config.selected == Just node.id
    in
    Html.button
        [ class (String.join " " (nodeClasses node state isSelected))
        , Html.Attributes.type_ "button"
        , Html.Attributes.attribute "aria-pressed" (boolean isSelected)
        , Html.Attributes.attribute "aria-label" (accessibleName node state)
        , style "left" (px item.x)
        , style "top" (px item.y)
        , style "width" (px Layout.nodeWidth)
        , style "height" (px Layout.nodeHeight)
        , Html.Events.onClick (config.onSelect node.id)
        ]
        (Html.div [ class "agent-graph-node-head" ]
            [ Html.div [ class "agent-graph-node-name", Html.Attributes.title node.displayName ]
                [ Html.text node.displayName ]
            , Html.div [ class "agent-graph-node-kind" ] [ Html.text (Model.kindName node.kind) ]
            ]
            :: viewState state
            ++ viewFlags node
        )


{-| The state row exists only when there is state to report. A node with no
activity in the window is distinguished by its dashed border, not by a badge
claiming something happened.
-}
viewState : Maybe NodeState -> List (Html msg)
viewState state =
    case state of
        Just present ->
            if present.hasWindowActivity then
                [ Html.div [ class "agent-graph-node-state" ] [ Html.text (stateLabel present) ] ]

            else
                []

        Nothing ->
            []


{-| Wrappers decorate a node instead of becoming one, so they are badges. So is
optionality: a node whose production is not guaranteed says so in a word, not
by a faded colour nobody can name.
-}
viewFlags : Model.Node -> List (Html msg)
viewFlags node =
    let
        badges =
            (if node.optional then
                [ Html.span [ class "agent-graph-node-flag" ] [ Html.text "optional" ] ]

             else
                []
            )
                ++ List.map
                    (\decoration ->
                        Html.span [ class "agent-graph-node-decoration" ]
                            [ Html.text (Model.decorationName decoration) ]
                    )
                    node.decorations
    in
    if List.isEmpty badges then
        []

    else
        [ Html.div [ class "agent-graph-node-decorations" ] badges ]


{-| Classes describe the *presence* of state, never a single winning status.
-}
nodeClasses : Model.Node -> Maybe NodeState -> Bool -> List String
nodeClasses node state isSelected =
    List.concat
        [ [ "agent-graph-node", "agent-graph-node--" ++ Model.kindName node.kind ]
        , case state of
            Nothing ->
                [ "agent-graph-node--endpoint" ]

            Just present ->
                if not present.hasWindowActivity then
                    [ "agent-graph-node--no-data" ]

                else if present.needsAttention then
                    [ "agent-graph-node--attention" ]

                else
                    [ "agent-graph-node--resolved" ]
        , if node.optional then
            [ "agent-graph-node--optional" ]

          else
            []
        , if isSelected then
            [ "agent-graph-node--selected" ]

          else
            []
        ]


{-| Name, kind, and state as plain words. The glyphs are decoration for the
eye; reading them aloud would be noise.
-}
accessibleName : Model.Node -> Maybe NodeState -> String
accessibleName node state =
    [ Just node.displayName
    , Just (Model.kindName node.kind)
    , if node.optional then
        Just "optional"

      else
        Nothing
    , case state of
        Nothing ->
            Nothing

        Just present ->
            if present.hasWindowActivity then
                Just (segments present |> List.map .label |> String.join ", ")

            else
                Just "no activity in this window"
    ]
        |> List.filterMap identity
        |> String.join ", "


{-| The visible badge: every segment is a glyph, a count, and a word.
-}
stateLabel : NodeState -> String
stateLabel state =
    segments state
        |> List.map (\part -> part.glyph ++ " " ++ part.label)
        |> String.join " \u{00B7} "


type alias Segment =
    { glyph : String
    , label : String
    }


{-| What the node reports right now.

Active counts always show. Terminal counts show only while the node still
needs attention: the design distinguishes what needs action now from what
happened recently, and a failure a later retry resolved must not keep the
canvas red. That history stays available through selection and in the
evaluation statistics — it is withheld from the canvas, not discarded.

-}
segments : NodeState -> List Segment
segments state =
    let
        present =
            List.filterMap identity
                [ segment "\u{25B6}" state.running "running"
                , segment "\u{23F8}" state.waiting "waiting"
                , segment "\u{25CB}" state.pending "pending"
                , if state.needsAttention then
                    segment "\u{2715}" (state.failed + state.errored) "failed"

                  else
                    Nothing
                , if state.needsAttention then
                    segment "\u{2298}" state.aborted "aborted"

                  else
                    Nothing
                ]
    in
    if not (List.isEmpty present) then
        present

    else if state.needsAttention then
        [ { glyph = "\u{26A0}", label = "needs attention" } ]

    else
        [ { glyph = "\u{2713}", label = "ok" } ]


segment : String -> Int -> String -> Maybe Segment
segment glyph count word =
    if count <= 0 then
        Nothing

    else
        Just { glyph = glyph, label = String.fromInt count ++ " " ++ word }


{-| Optionality rides on a data attribute rather than a class modifier. On an
SVG element `class` is a plain attribute, not the `className` property, so a
modifier class there is invisible to CSS class selectors in some engines and
to the DOM query helpers; `[data-optional="true"]` is unambiguous in both.
-}
viewEdge : Layout.LaidOutEdge -> Svg.Svg msg
viewEdge item =
    Svg.path
        [ Svg.Attributes.class "agent-graph-edge"
        , Html.Attributes.attribute "data-optional" (boolean item.edge.optional)
        , Svg.Attributes.d item.path
        ]
        []


px : Float -> String
px value =
    String.fromFloat value ++ "px"


boolean : Bool -> String
boolean value =
    if value then
        "true"

    else
        "false"
