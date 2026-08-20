module PipelineRuns.Styles exposing (body, button, error, form, hold, table)
import Colors
import Html exposing (Attribute)
import Html.Attributes exposing (style)
body : List (Attribute msg)
body =
    [ style "padding" "24px"
    , style "overflow-y" "auto"
    , style "height" "100%"
    , style "box-sizing" "border-box"
    ]
form : List (Attribute msg)
form =
    [ style "max-width" "560px"
    , style "margin-bottom" "24px"
    ]
table : List (Attribute msg)
table =
    [ style "border-collapse" "collapse"
    , style "width" "100%"
    , style "max-width" "960px"
    ]
button : List (Attribute msg)
button =
    [ style "background-color" Colors.paginationHover
    , style "color" "white"
    , style "border" "1px solid #8b66d9"
    , style "padding" "8px 12px"
    , style "cursor" "pointer"
    ]
error : List (Attribute msg)
error =
    [ style "color" "#ff8080"
    , style "min-height" "1.2em"
    ]
hold : List (Attribute msg)
hold =
    [ style "color" Colors.text
    , style "margin-left" "8px"
    ]
