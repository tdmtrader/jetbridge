module AgentGraphViewTests exposing (all)

import AgentGraph.Layout as Layout
import AgentGraph.Model as Model
import AgentGraph.View as View
import Expect
import Html.Attributes
import Test exposing (Test, describe, test)
import Test.Html.Event as Event
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, style, tag, text)


all : Test
all =
    describe "agent graph view"
        [ describe "the colour-independent state language" stateLanguage
        , describe "aggregate honesty" aggregateHonesty
        , describe "selection" selection
        , describe "decorations and optionality" decorations
        , describe "endpoint nodes" endpoints
        , describe "structure" structure
        ]



-- the colour-independent state language ---------------------------------------


stateLanguage : List Test
stateLanguage =
    [ test "states running and waiting counts as words, not colour alone" <|
        \_ ->
            render (stateFor { counts | running = 2, waiting = 1, activity = True })
                |> Query.find [ class "agent-graph-node-state" ]
                |> Expect.all
                    [ Query.has [ text "2 running" ]
                    , Query.has [ text "1 waiting" ]
                    ]
    , test "states pending as a word too" <|
        \_ ->
            render (stateFor { counts | pending = 3, activity = True })
                |> Query.find [ class "agent-graph-node-state" ]
                |> Query.has [ text "3 pending" ]
    , test "every glyph is accompanied by a count and a word" <|
        \_ ->
            View.stateLabel
                (stateFor { counts | running = 2, waiting = 1, failed = 4, aborted = 5, activity = True })
                |> String.split " · "
                |> List.map (String.split " " >> List.length)
                |> Expect.equal [ 3, 3, 3, 3 ]
    , test "marks a node with no window activity as no-data, not success" <|
        \_ ->
            render (stateFor { counts | activity = False })
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has [ class "agent-graph-node--no-data" ]
    , test "a node with no window activity carries no state badge at all" <|
        \_ ->
            render (stateFor { counts | activity = False })
                |> Query.findAll [ class "agent-graph-node-state" ]
                |> Query.count (Expect.equal 0)
    , test "shows a resolved indicator when nothing needs attention" <|
        \_ ->
            render (stateFor { counts | activity = True })
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has [ class "agent-graph-node--resolved" ]
    , test "a resolved node says so in words rather than relying on a green fill" <|
        \_ ->
            render (stateFor { counts | succeeded = 4, activity = True })
                |> Query.find [ class "agent-graph-node-state" ]
                |> Query.has [ text "ok" ]
    , test "an attention state with no counts to show still says what it means" <|
        \_ ->
            View.stateLabel { emptyState | needsAttention = True, hasWindowActivity = True }
                |> Expect.equal "\u{26A0} needs attention"
    , test "the accessible name carries the node, its kind, and its state as text" <|
        \_ ->
            render (stateFor { counts | running = 2, activity = True })
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has
                    [ attribute
                        (Html.Attributes.attribute "aria-label" "implement, agent, 2 running")
                    ]
    , test "the accessible name says no activity rather than leaving the state silent" <|
        \_ ->
            render (stateFor { counts | activity = False })
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has
                    [ attribute
                        (Html.Attributes.attribute "aria-label" "implement, agent, no activity in this window")
                    ]
    ]



-- aggregate honesty ------------------------------------------------------------


aggregateHonesty : List Test
aggregateHonesty =
    [ test "never fills the whole node with one aggregate status colour" <|
        \_ ->
            render (stateFor { counts | running = 2, failed = 1, activity = True })
                |> Query.find [ class "agent-graph-node" ]
                |> Expect.all
                    [ Query.hasNot [ class "agent-graph-node--failed" ]
                    , Query.hasNot [ class "agent-graph-node--running" ]
                    , Query.hasNot [ class "agent-graph-node--succeeded" ]
                    , Query.hasNot [ class "agent-graph-node--errored" ]
                    ]
    , test "reports concurrent running and failed states side by side, not one winner" <|
        \_ ->
            render (stateFor { counts | running = 2, failed = 1, activity = True })
                |> Query.find [ class "agent-graph-node-state" ]
                |> Expect.all
                    [ Query.has [ text "2 running" ]
                    , Query.has [ text "1 failed" ]
                    ]
    , test "folds errored into the failed count rather than inventing a rank between them" <|
        \_ ->
            View.stateLabel (stateFor { counts | failed = 1, errored = 2, activity = True })
                |> Expect.equal "\u{2715} 3 failed"
    , test "a retry that resolved an earlier failure does not keep the canvas red" <|
        \_ ->
            -- needsAttention is the retry-chain-resolved answer. History still
            -- holds the failure and selection still surfaces it, but the node
            -- must not keep reporting work that no longer needs action.
            render
                { emptyState
                    | failed = 3
                    , succeeded = 1
                    , hasWindowActivity = True
                    , needsAttention = False
                }
                |> Query.find [ class "agent-graph-node-state" ]
                |> Expect.all
                    [ Query.hasNot [ text "3 failed" ]
                    , Query.has [ text "ok" ]
                    ]
    , test "an unresolved failure is reported" <|
        \_ ->
            render
                { emptyState | failed = 3, hasWindowActivity = True, needsAttention = True }
                |> Query.find [ class "agent-graph-node-state" ]
                |> Query.has [ text "3 failed" ]
    , test "a node that needs attention is marked as needing it, not as resolved" <|
        \_ ->
            render
                { emptyState | failed = 3, hasWindowActivity = True, needsAttention = True }
                |> Query.find [ class "agent-graph-node" ]
                |> Expect.all
                    [ Query.has [ class "agent-graph-node--attention" ]
                    , Query.hasNot [ class "agent-graph-node--resolved" ]
                    ]
    , test "an unresolved abort is reported as its own word" <|
        \_ ->
            render
                { emptyState | aborted = 2, hasWindowActivity = True, needsAttention = True }
                |> Query.find [ class "agent-graph-node-state" ]
                |> Query.has [ text "2 aborted" ]
    ]



-- selection --------------------------------------------------------------------


selection : List Test
selection =
    [ test "marks the selected node with a selection class rather than a colour swap" <|
        \_ ->
            selected (Just "implement")
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has [ class "agent-graph-node--selected" ]
    , test "selection leaves the state classes exactly as they were" <|
        \_ ->
            selected (Just "implement")
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has [ class "agent-graph-node--resolved" ]
    , test "an unselected node carries no selection class" <|
        \_ ->
            selected Nothing
                |> Query.findAll [ class "agent-graph-node--selected" ]
                |> Query.count (Expect.equal 0)
    , test "selecting a different node does not select this one" <|
        \_ ->
            selected (Just "something-else")
                |> Query.findAll [ class "agent-graph-node--selected" ]
                |> Query.count (Expect.equal 0)
    , test "selection is exposed to assistive technology, not only to the eye" <|
        \_ ->
            selected (Just "implement")
                |> Query.find [ class "agent-graph-node" ]
                |> Query.has [ attribute (Html.Attributes.attribute "aria-pressed" "true") ]
    , test "clicking a node reports that node's id" <|
        \_ ->
            selected Nothing
                |> Query.find [ class "agent-graph-node" ]
                |> Event.simulate Event.click
                |> Event.expect "implement"
    ]



-- decorations and optionality --------------------------------------------------


decorations : List Test
decorations =
    [ test "renders decorations as badges on the node" <|
        \_ ->
            render (stateFor { counts | activity = True })
                |> Query.find [ class "agent-graph-node-decorations" ]
                |> Query.has [ text "retry" ]
    , test "renders every decoration the node carries" <|
        \_ ->
            renderGraph decoratedGraph (stateFor { counts | activity = True }) Nothing
                |> Query.find [ class "agent-graph-node-decorations" ]
                |> Expect.all
                    [ Query.has [ text "retry" ]
                    , Query.has [ text "timeout" ]
                    , Query.has [ text "on_failure" ]
                    ]
    , test "a node with no decorations renders no decoration row" <|
        \_ ->
            renderGraph plainGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll [ class "agent-graph-node-decorations" ]
                |> Query.count (Expect.equal 0)
    , test "an optional node says optional in words" <|
        \_ ->
            renderGraph optionalGraph (stateFor { counts | activity = True }) Nothing
                |> Query.find [ class "agent-graph-node" ]
                |> Expect.all
                    [ Query.has [ class "agent-graph-node--optional" ]
                    , Query.has [ text "optional" ]
                    ]
    , test "a node that is not optional does not say optional" <|
        \_ ->
            renderGraph plainGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll [ class "agent-graph-node--optional" ]
                |> Query.count (Expect.equal 0)
    , test "an optional edge is marked so a run that carried nothing does not read as broken" <|
        \_ ->
            renderGraph edgedGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll
                    [ tag "path", attribute (Html.Attributes.attribute "data-optional" "true") ]
                |> Query.count (Expect.equal 1)
    , test "a guaranteed edge is not marked optional" <|
        \_ ->
            renderGraph edgedGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll
                    [ tag "path", attribute (Html.Attributes.attribute "data-optional" "false") ]
                |> Query.count (Expect.equal 1)
    ]



-- endpoint nodes ---------------------------------------------------------------


endpoints : List Test
endpoints =
    [ test "an endpoint node carries no state, because it never executes" <|
        \_ ->
            -- Inputs, outputs, and resource sources have no occurrence to
            -- report. Rendering them as no-data would fill the canvas with
            -- dashed boxes that look like missing history.
            renderGraph endpointGraph emptyState Nothing
                |> Query.find [ class "agent-graph-node" ]
                |> Expect.all
                    [ Query.has [ class "agent-graph-node--endpoint" ]
                    , Query.hasNot [ class "agent-graph-node--no-data" ]
                    , Query.hasNot [ class "agent-graph-node--resolved" ]
                    , Query.hasNot [ class "agent-graph-node--attention" ]
                    ]
    , test "an endpoint node renders no state badge even when a lookup offers one" <|
        \_ ->
            renderGraph endpointGraph (stateFor { counts | running = 9, activity = True }) Nothing
                |> Query.findAll [ class "agent-graph-node-state" ]
                |> Query.count (Expect.equal 0)
    , test "an execution node is not marked as an endpoint" <|
        \_ ->
            renderGraph plainGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll [ class "agent-graph-node--endpoint" ]
                |> Query.count (Expect.equal 0)
    , test "the node names its kind, so the shape is not the only cue" <|
        \_ ->
            renderGraph endpointGraph emptyState Nothing
                |> Query.find [ class "agent-graph-node-kind" ]
                |> Query.has [ text "input" ]
    ]



-- structure --------------------------------------------------------------------


structure : List Test
structure =
    [ test "renders one node element per laid-out node" <|
        \_ ->
            renderGraph edgedGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll [ class "agent-graph-node" ]
                |> Query.count (Expect.equal 3)
    , test "renders one path per laid-out edge" <|
        \_ ->
            renderGraph edgedGraph (stateFor { counts | activity = True }) Nothing
                |> Query.findAll [ tag "path" ]
                |> Query.count (Expect.equal 2)
    , test "sizes the canvas to the layout" <|
        \_ ->
            let
                laid =
                    Layout.layout edgedGraph
            in
            renderGraph edgedGraph (stateFor { counts | activity = True }) Nothing
                |> Query.has [ style "width" (String.fromFloat laid.width ++ "px") ]
    , test "an empty graph renders an empty canvas rather than failing" <|
        \_ ->
            renderGraph { nodes = [], edges = [] } emptyState Nothing
                |> Query.findAll [ class "agent-graph-node" ]
                |> Query.count (Expect.equal 0)
    ]



-- helpers ----------------------------------------------------------------------


type alias Counts =
    { running : Int
    , waiting : Int
    , pending : Int
    , succeeded : Int
    , failed : Int
    , errored : Int
    , aborted : Int
    , activity : Bool
    }


counts : Counts
counts =
    { running = 0
    , waiting = 0
    , pending = 0
    , succeeded = 0
    , failed = 0
    , errored = 0
    , aborted = 0
    , activity = False
    }


emptyState : View.NodeState
emptyState =
    View.emptyState


{-| needsAttention is derived here the way the retry-chain resolver derives it
server-side: anything unfinished or unresolved needs action now.
-}
stateFor : Counts -> View.NodeState
stateFor c =
    { running = c.running
    , waiting = c.waiting
    , pending = c.pending
    , succeeded = c.succeeded
    , failed = c.failed
    , errored = c.errored
    , aborted = c.aborted
    , skipped = 0
    , needsAttention = c.running + c.waiting + c.pending + c.failed + c.errored + c.aborted > 0
    , hasWindowActivity = c.activity
    }


render : View.NodeState -> Query.Single String
render state =
    renderGraph graph state Nothing


selected : Maybe String -> Query.Single String
selected id =
    renderGraph graph (stateFor { counts | activity = True }) id


renderGraph : Model.Graph -> View.NodeState -> Maybe String -> Query.Single String
renderGraph source state selection_ =
    View.view
        { selected = selection_
        , nodeState = \_ -> state
        , onSelect = identity
        }
        (Layout.layout source)
        |> Query.fromHtml


node : String -> Model.NodeKind -> List Model.Decoration -> Model.Node
node id kind nodeDecorations =
    { id = id
    , kind = kind
    , displayName = id
    , typeRef = ""
    , optional = False
    , decorations = nodeDecorations
    }


graph : Model.Graph
graph =
    { nodes = [ node "implement" Model.Agent [ Model.Retry ] ], edges = [] }


decoratedGraph : Model.Graph
decoratedGraph =
    { nodes = [ node "implement" Model.Agent [ Model.Retry, Model.Timeout, Model.OnFailure ] ]
    , edges = []
    }


plainGraph : Model.Graph
plainGraph =
    { nodes = [ node "implement" Model.Agent [] ], edges = [] }


optionalGraph : Model.Graph
optionalGraph =
    { nodes = [ { id = "maybe", kind = Model.Await, displayName = "maybe", typeRef = "", optional = True, decorations = [] } ]
    , edges = []
    }


{-| The display name deliberately does not contain the word "input", so a kind
row that echoed the display name instead of the kind would be visible.
-}
endpointGraph : Model.Graph
endpointGraph =
    { nodes =
        [ { id = "input:repository"
          , kind = Model.Input
          , displayName = "repository"
          , typeRef = "repository/v1"
          , optional = False
          , decorations = []
          }
        ]
    , edges = []
    }


edgedGraph : Model.Graph
edgedGraph =
    { nodes =
        [ node "input:repository" Model.Input []
        , node "implement" Model.Agent []
        , node "output:change" Model.Output []
        ]
    , edges =
        [ { from = "input:repository", to = "implement", portName = "repository", typeRef = "", optional = False }
        , { from = "implement", to = "output:change", portName = "change", typeRef = "", optional = True }
        ]
    }
