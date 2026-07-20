module Concourse.AgentDiff exposing
    ( DiffFile
    , DiffPage
    , LineKind(..)
    , classifyLine
    , decodeDiffPage
    )

{-| Wire types + decoder for GET /api/v1/agent/tickets/:id/diff (the §1.11.1
file-windowed diff). Each `DiffFile.patch` is raw unified-diff text; the render
layer splits it into lines and asks `classifyLine` how to color each one.
-}

import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias DiffFile =
    { path : String
    , patch : String
    , truncated : Bool
    }


type alias DiffPage =
    { files : List DiffFile
    , offset : Int
    , limit : Int
    , totalFiles : Int
    , hasMore : Bool
    }


{-| The kind of one unified-diff line, used purely to pick a color.

  - `Addition` — a `+` line (but not the `+++` file header).
  - `Deletion` — a `-` line (but not the `---` file header).
  - `HunkHeader` — an `@@ ... @@` locator.
  - `Meta` — `diff `/`index `/`+++ `/`--- `/`new file`/`deleted file` framing.
  - `Context` — everything else (unchanged lines, blank lines).

-}
type LineKind
    = Addition
    | Deletion
    | HunkHeader
    | Meta
    | Context


classifyLine : String -> LineKind
classifyLine line =
    if String.startsWith "@@" line then
        HunkHeader

    else if String.startsWith "+++" line || String.startsWith "---" line then
        Meta

    else if
        String.startsWith "diff " line
            || String.startsWith "index " line
            || String.startsWith "new file" line
            || String.startsWith "deleted file" line
            || String.startsWith "similarity index" line
            || String.startsWith "rename " line
    then
        Meta

    else if String.startsWith "+" line then
        Addition

    else if String.startsWith "-" line then
        Deletion

    else
        Context


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo fallback decoder =
    Json.Decode.oneOf [ decoder, Json.Decode.succeed fallback ]


decodeDiffFile : Json.Decode.Decoder DiffFile
decodeDiffFile =
    Json.Decode.succeed DiffFile
        |> andMap (defaultTo "" <| Json.Decode.field "path" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "patch" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "truncated" Json.Decode.bool)


decodeDiffPage : Json.Decode.Decoder DiffPage
decodeDiffPage =
    Json.Decode.succeed DiffPage
        |> andMap (defaultTo [] <| Json.Decode.field "files" (Json.Decode.list decodeDiffFile))
        |> andMap (defaultTo 0 <| Json.Decode.field "offset" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "limit" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "total_files" Json.Decode.int)
        |> andMap (defaultTo False <| Json.Decode.field "has_more" Json.Decode.bool)
