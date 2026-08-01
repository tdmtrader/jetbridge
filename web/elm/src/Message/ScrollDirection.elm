module Message.ScrollDirection exposing (ScrollDirection(..))


type ScrollDirection
    = ToTop
    | Down
    | Up
    | ToBottom
    | Sideways Float
    | ToId String
      -- ToOffset restores a remembered scroll position. It is how a list
      -- keeps its place across a refetch without putting scroll state in the
      -- URL, where it would pollute every shared link.
    | ToOffset Float
