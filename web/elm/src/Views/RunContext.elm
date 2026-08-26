module Views.RunContext exposing (Context(..), isCompleted, view)
import Concourse
import Concourse.BuildStatus as BuildStatus
import Concourse.PipelineRun as PipelineRun exposing (PipelineRun)
import Html exposing (Html)
import Html.Attributes exposing (class, id)
import Html.Events exposing (onClick)
import Message.Message exposing (Message(..))
type Context
    = Live PipelineRun Concourse.Pipeline
    | Completed PipelineRun Concourse.Pipeline
    | RecordOnly PipelineRun
    | Reclaimed PipelineRun
isCompleted : Context -> Bool
isCompleted context =
    case context of
        Completed _ _ ->
            True

        _ ->
            False
view : Maybe String -> Context -> Html Message
view error context =
    let
        run =
            case context of
                Live value _ ->
                    value

                Completed value _ ->
                    value

                RecordOnly value ->
                    value

                Reclaimed value ->
                    value
    in
    Html.div [ id "run-context", class (BuildStatus.show run.status) ]
        [ Html.h1 [] [ Html.text ("Run #" ++ String.fromInt run.number) ]
        , Html.p [] [ Html.text ("Status: " ++ PipelineRun.showStatus run.status) ]
        , Html.p [] [ Html.text ("Parameters: " ++ Concourse.hyphenNotation run.params) ]
        , case context of
            RecordOnly _ ->
                Html.p [] [ Html.text "The run payload is unavailable to this viewer." ]

            Reclaimed _ ->
                Html.p [] [ Html.text "This run payload has been reclaimed." ]

            _ ->
                Html.text ""
        , case error of
            Just message ->
                Html.p [] [ Html.text message, Html.button [ onClick RetryPipelineRuns ] [ Html.text "Retry" ] ]

            Nothing ->
                Html.text ""
        ]
