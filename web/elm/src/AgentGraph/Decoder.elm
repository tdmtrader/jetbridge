module AgentGraph.Decoder exposing (decoration, edge, graph, node, nodeKind)

{-| Decoding for the semantic workflow graph served by
`agent/workflow/graph`.

Two rules shape this module.

An unrecognised kind or decoration **fails the decode**. Silently dropping it
would render a graph missing a node or a wrapper, and a graph that lies about
its own shape is worse than a page that says it could not read the graph.

`nodes` and `edges` are **required**. The server initializes both slices
precisely so an empty graph marshals as `{"nodes":[],"edges":[]}` rather than
`null`; treating them as optional here would turn a future regression into a
silently empty canvas instead of a visible error.

-}

import AgentGraph.Model as Model
import Json.Decode


graph : Json.Decode.Decoder Model.Graph
graph =
    Json.Decode.map2 Model.Graph
        (Json.Decode.field "nodes" (Json.Decode.list node))
        (Json.Decode.field "edges" (Json.Decode.list edge))


node : Json.Decode.Decoder Model.Node
node =
    Json.Decode.map6 Model.Node
        (Json.Decode.field "id" Json.Decode.string)
        (Json.Decode.field "kind" nodeKind)
        (Json.Decode.field "display_name" Json.Decode.string)
        (optionalField "type_ref" Json.Decode.string "")
        (optionalField "optional" Json.Decode.bool False)
        (optionalField "decorations" (Json.Decode.list decoration) [])


edge : Json.Decode.Decoder Model.Edge
edge =
    Json.Decode.map5 Model.Edge
        (Json.Decode.field "from" Json.Decode.string)
        (Json.Decode.field "to" Json.Decode.string)
        (Json.Decode.field "port_name" Json.Decode.string)
        (optionalField "type_ref" Json.Decode.string "")
        (optionalField "optional" Json.Decode.bool False)


{-| A field the server elides with `omitempty`. Absent means the zero value;
present but ill-typed is still an error, because that is a contract break
rather than an omission.
-}
optionalField : String -> Json.Decode.Decoder a -> a -> Json.Decode.Decoder a
optionalField name decoder fallback =
    Json.Decode.oneOf
        [ Json.Decode.field name decoder
        , Json.Decode.field name (Json.Decode.null fallback)
        , Json.Decode.succeed fallback
            |> withoutField name
        ]


{-| Succeeds only when the field really is absent, so a present-but-ill-typed
field falls through to a decode error instead of quietly taking the default.
-}
withoutField : String -> Json.Decode.Decoder a -> Json.Decode.Decoder a
withoutField name decoder =
    Json.Decode.maybe (Json.Decode.field name Json.Decode.value)
        |> Json.Decode.andThen
            (\present ->
                case present of
                    Nothing ->
                        decoder

                    Just _ ->
                        Json.Decode.fail ("unexpected value for field: " ++ name)
            )


nodeKind : Json.Decode.Decoder Model.NodeKind
nodeKind =
    Json.Decode.string
        |> Json.Decode.andThen
            (\raw ->
                case raw of
                    "input" ->
                        Json.Decode.succeed Model.Input

                    "resource_source" ->
                        Json.Decode.succeed Model.ResourceSource

                    "load" ->
                        Json.Decode.succeed Model.Load

                    "agent" ->
                        Json.Decode.succeed Model.Agent

                    "task" ->
                        Json.Decode.succeed Model.Task

                    "await" ->
                        Json.Decode.succeed Model.Await

                    "publish" ->
                        Json.Decode.succeed Model.Publish

                    "output" ->
                        Json.Decode.succeed Model.Output

                    other ->
                        Json.Decode.fail ("unknown node kind: " ++ other)
            )


decoration : Json.Decode.Decoder Model.Decoration
decoration =
    Json.Decode.string
        |> Json.Decode.andThen
            (\raw ->
                case raw of
                    "retry" ->
                        Json.Decode.succeed Model.Retry

                    "timeout" ->
                        Json.Decode.succeed Model.Timeout

                    "try" ->
                        Json.Decode.succeed Model.Try

                    "ensure" ->
                        Json.Decode.succeed Model.Ensure

                    "on_failure" ->
                        Json.Decode.succeed Model.OnFailure

                    "on_error" ->
                        Json.Decode.succeed Model.OnError

                    "on_abort" ->
                        Json.Decode.succeed Model.OnAbort

                    "on_success" ->
                        Json.Decode.succeed Model.OnSuccess

                    other ->
                        Json.Decode.fail ("unknown decoration: " ++ other)
            )
