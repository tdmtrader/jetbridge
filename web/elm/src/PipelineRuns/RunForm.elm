module PipelineRuns.RunForm exposing (Model, ValidationError, encode, init, set, value)
import Concourse exposing (InstanceVars, JsonValue(..), ParamSchema, ParamType(..))
import Dict exposing (Dict)
import Json.Encode
type alias Model =
    Dict String String
type alias ValidationError =
    { fieldId : Maybe String
    , message : String
    }
init : List ParamSchema -> Model
init schemas =
    schemas
        |> List.filterMap
            (\schema ->
                schema.default
                    |> Maybe.map (\default -> ( schema.name, displayValue default ))
            )
        |> Dict.fromList
set : String -> String -> Model -> Model
set =
    Dict.insert
value : String -> Model -> String
value name =
    Dict.get name >> Maybe.withDefault ""
encode : List ParamSchema -> Model -> Result ValidationError InstanceVars
encode schemas form =
    schemas
        |> List.foldl (encodeField form) (Ok Dict.empty)
encodeField : Model -> ParamSchema -> Result ValidationError InstanceVars -> Result ValidationError InstanceVars
encodeField form schema result =
    result
        |> Result.andThen
            (\vars ->
                let
                    input =
                        value schema.name form
                in
                if String.trim input == "" then
                    if schema.required then
                        Err (invalid schema (schema.name ++ " is required"))
                    else
                        Ok vars
                else
                    coerce schema input
                        |> Result.map (Dict.insert schema.name >> (|>) vars)
            )
coerce : ParamSchema -> String -> Result ValidationError JsonValue
coerce schema input =
    case schema.type_ of
        StringParam ->
            Ok (JsonString input)
        NumberParam ->
            String.toFloat input
                |> Maybe.map (JsonNumber >> Ok)
                |> Maybe.withDefault (Err <| invalid schema (schema.name ++ " must be a number"))
        BoolParam ->
            case input of
                "true" ->
                    Ok <| JsonRaw <| Json.Encode.bool True
                "false" ->
                    Ok <| JsonRaw <| Json.Encode.bool False
                _ ->
                    Err <| invalid schema (schema.name ++ " must be true or false")
        EnumParam ->
            schema.values
                |> List.filter (\candidate -> displayValue candidate == input)
                |> List.head
                |> Maybe.map Ok
                |> Maybe.withDefault
                    (Err <| invalid schema (schema.name ++ " must be one of " ++ String.join ", " (List.map displayValue schema.values)))
invalid : ParamSchema -> String -> ValidationError
invalid schema message =
    { fieldId = Just ("run-param-" ++ schema.name)
    , message = message
    }
displayValue : JsonValue -> String
displayValue json =
    case json of
        JsonString string ->
            string
        JsonNumber number ->
            String.fromFloat number
        JsonObject _ ->
            Json.Encode.encode 0 (Concourse.encodeJsonValue json)
        JsonRaw raw ->
            Json.Encode.encode 0 raw
