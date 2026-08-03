module AgentWorkflow.Filters exposing
    ( Filters
    , Scope(..)
    , Status(..)
    , Window(..)
    , default
    , fromQuery
    , overviewQuery
    , runsQuery
    , scopeParam
    , statusParam
    , toQuery
    , toQueryPairs
    , windowParam
    )

{-| Filter and selection state for the workflow overview.

Everything here lives in the URL so back, forward, links, and refresh behave
predictably. Scroll position deliberately does not: it is restored from a
scroll cache instead, which keeps shareable links clean.

Defaults are omitted from the query string, so an untouched page has a bare URL
and a shared link only carries what the sharer actually changed. An
unrecognised value falls back to the default rather than failing the page: a
stale or hand-edited link should still render.

There are **three** query vocabularies here and they are not the same, which is
why they get three functions rather than one:

1.  `toQueryPairs` — the page URL. Its names are the design's
    (`window`, `scope`, `q`, `node`, `node_status`, `status`, `origin`,
    `version`), and defaults are omitted.
2.  `overviewQuery` — `GET .../overview`, which accepts **only** `window`.
    Anything else is rejected with 400 by
    `workflowoverview.parseRoute`, so nothing else may be sent.
3.  `runsQuery` — `GET .../runs`, which accepts `window`, `scope`, `lens`,
    `node`, `node_status`, `q`, `status`, `origin_kind`, `origin_reference`,
    `limit`, and `cursor` (`workflowruns.Handler.List`). Note `origin_kind`,
    not `origin`, `lens` rather than `status` for the attention lens, and the
    absence of a revision filter.

One design-level filter has no server-side expression and is therefore
deliberately _not_ sent: `version` selects which revision the Versions panel
talks about. The run list API exposes no revision filter
(`db.AgentWorkflowRunListFilter` has `WorkflowVersion`, but no HTTP parameter
reaches it), so sending it would 400 the whole list.

The attention lens is `status` in the page URL — the design's vocabulary — and
`lens` on the wire, because the server's `status` parameter is the run-status
vocabulary (`admitting`/`running`/`succeeded`/…) and means something else. It
is a server-side predicate rather than a narrowing of the returned page: the
list is paginated, so filtering rows here would answer "attention-worthy runs
among the newest page" and hide exactly the older unresolved failure the lens
exists to find.

-}

import AgentGraph.Model
import Url.Builder


type Window
    = TwentyFourHours
    | SevenDays
    | ThirtyDays


type Scope
    = Operational
    | Experiment
    | AllScopes


{-| The visible attention lens. `Experiment` is a `Scope`, not a `Status`,
because the server classifies experiment runs as a population rather than a
state.
-}
type Status
    = Attention
    | Active
    | All


type alias Filters =
    { window : Window
    , scope : Scope
    , status : Status
    , search : String
    , selectedNode : Maybe String
    , selectedNodeStatus : Maybe String
    , version : Maybe Int
    , origin : String
    }


default : Filters
default =
    { window = SevenDays
    , scope = Operational
    , status = Attention
    , search = ""
    , selectedNode = Nothing
    , selectedNodeStatus = Nothing
    , version = Nothing
    , origin = ""
    }


{-| The wire spelling of a window. It matches `workflowruns.Windows` exactly;
that map is the complete supported vocabulary for both the run list and the
overview, so these three are the only values either endpoint accepts.
-}
windowParam : Window -> String
windowParam window =
    case window of
        TwentyFourHours ->
            "24h"

        SevenDays ->
            "7d"

        ThirtyDays ->
            "30d"


{-| The wire spelling of a scope, matching `db.AgentWorkflowRunScope`.
-}
scopeParam : Scope -> String
scopeParam scope =
    case scope of
        Operational ->
            "operational"

        Experiment ->
            "experiment"

        AllScopes ->
            "all"


statusParam : Status -> String
statusParam status =
    case status of
        Attention ->
            "attention"

        Active ->
            "active"

        All ->
            "all"


{-| The page URL's query, defaults omitted.
-}
toQueryPairs : Filters -> List ( String, String )
toQueryPairs filters =
    List.filterMap identity
        [ optional "window" (windowParam filters.window) (windowParam default.window)
        , optional "scope" (scopeParam filters.scope) (scopeParam default.scope)
        , optional "status" (statusParam filters.status) (statusParam default.status)
        , optional "q" filters.search ""
        , Maybe.map (Tuple.pair "node") filters.selectedNode
        , Maybe.map (Tuple.pair "node_status") (qualifiedNodeStatus filters)
        , Maybe.map (\version -> ( "version", String.fromInt version )) filters.version
        , optional "origin" filters.origin ""
        ]


toQuery : Filters -> List Url.Builder.QueryParameter
toQuery filters =
    toQueryPairs filters |> List.map (\( key, value ) -> Url.Builder.string key value)


{-| The overview endpoint's entire accepted query. The window is stated even at
the default: the response echoes the window it aggregated over, and asking for
it explicitly keeps the page and the server from disagreeing about what "the
default" means.
-}
overviewQuery : Filters -> List ( String, String )
overviewQuery filters =
    [ ( "window", windowParam filters.window ) ]


{-| The run list endpoint's query. Window and scope are stated explicitly for
the same reason the overview's window is: the two surfaces share one scope by
design, and an implied default is a place for them to drift apart.
-}
runsQuery : Filters -> List ( String, String )
runsQuery filters =
    List.filterMap identity
        [ Just ( "window", windowParam filters.window )
        , Just ( "scope", scopeParam filters.scope )
        , Just ( "lens", statusParam filters.status )
        , Maybe.map (Tuple.pair "node") filters.selectedNode
        , Maybe.map (Tuple.pair "node_status") (qualifiedNodeStatus filters)
        , optional "q" filters.search ""
        , optional "origin_kind" filters.origin ""
        ]


optional : String -> String -> String -> Maybe ( String, String )
optional key value fallback =
    if value == fallback then
        Nothing

    else
        Just ( key, value )


{-| A node status with no node would filter nothing while looking like it
filtered something, and the server rejects the pair outright
(`parseListFilter`'s `invalid_node_status`). One function answers it for the
URL and for the API so they cannot disagree.
-}
qualifiedNodeStatus : Filters -> Maybe String
qualifiedNodeStatus filters =
    case filters.selectedNode of
        Nothing ->
            Nothing

        Just _ ->
            filters.selectedNodeStatus


fromQuery : List ( String, String ) -> Filters
fromQuery pairs =
    let
        find key =
            pairs
                |> List.filter (\( candidate, _ ) -> candidate == key)
                |> List.head
                |> Maybe.map Tuple.second

        selectedNode =
            find "node" |> Maybe.andThen occurrenceBearingNode
    in
    { window =
        case find "window" of
            Just "24h" ->
                TwentyFourHours

            Just "30d" ->
                ThirtyDays

            _ ->
                SevenDays
    , scope =
        case find "scope" of
            Just "experiment" ->
                Experiment

            Just "all" ->
                AllScopes

            _ ->
                Operational
    , status =
        case find "status" of
            Just "active" ->
                Active

            Just "all" ->
                All

            _ ->
                Attention
    , search = find "q" |> Maybe.withDefault ""
    , selectedNode = selectedNode
    , selectedNodeStatus =
        case selectedNode of
            Nothing ->
                Nothing

            Just _ ->
                find "node_status" |> Maybe.andThen knownNodeStatus
    , version = find "version" |> Maybe.andThen String.toInt
    , origin = find "origin" |> Maybe.withDefault ""
    }


{-| Only an execution node can narrow the run list, so an endpoint id is
dropped rather than forwarded — for the same reason an unknown node status is.

The server's node filter is `EXISTS (… agent_workflow_run_node_occurrences …
o.node_id = $n)`, and that projection contains only occurrence-bearing nodes
with bare ids (`occurrence.ExecutionNodesOf`). `?node=input:repository` is
therefore an unsatisfiable predicate, not a narrow one: it returns an empty
list that the page then captions as a server fact ("No runs need attention in
this window") about a population it never examined. The value is
URL-addressable and survives sharing, so a shared link would reproduce the
false statement for a reader who never made the click.

-}
occurrenceBearingNode : String -> Maybe String
occurrenceBearingNode nodeId =
    if AgentGraph.Model.isEndpointId nodeId then
        Nothing

    else
        Just nodeId


{-| The occurrence status vocabulary `workflowruns.validateNodeStatus`
enforces. A value outside it is dropped rather than forwarded, so a
hand-edited link narrows nothing instead of 400ing the run list.
-}
knownNodeStatus : String -> Maybe String
knownNodeStatus status =
    if
        List.member status
            [ "pending", "running", "waiting", "succeeded", "failed", "errored", "aborted", "skipped" ]
    then
        Just status

    else
        Nothing
