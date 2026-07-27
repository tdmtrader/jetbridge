module Concourse.AgentReview exposing
    ( BuildReview
    , Finding
    , FindingFeedback
    , Summary
    , allVerdicts
    , conclusionLabel
    , conclusionTone
    , decodeBuildReview
    , decodeSummary
    , findingTotal
    , observationCount
    , substantiveCount
    , verdictLabel
    )

import Concourse.Snapshot as Snapshot
import Dict exposing (Dict)
import Json.Decode
import Json.Decode.Extra exposing (andMap)


{-| Summary is one review/v1 snapshot as the API renders it.

The build/pipeline/job/workflow fields describe the production occurrence the
server selected, not the review: the same sealed review can be produced by more
than one run. `conclusion` is the record's verdict verbatim — there is no score,
no maximum and no pass flag, because review/v1 states none of those.

-}
type alias Summary =
    { buildId : Int
    , buildName : String
    , teamName : String
    , pipelineName : String
    , jobName : String
    , workflowName : String
    , conclusion : String
    , summary : String
    , severityCounts : Dict String Int
    , createdAt : Int
    , evaluatedCount : Int
    , snapshotId : Maybe String
    , workflowRunId : Maybe String
    , productionId : Maybe String
    }


type alias Finding =
    { id : String
    , severity : String
    , blocking : Bool
    , title : String
    , description : String
    , file : String
    , line : Int
    , category : String
    }


type alias FindingFeedback =
    { verdict : String
    , notes : String
    , reviewer : String
    }


type alias BuildReview =
    { info : Summary
    , provenIssues : List Finding
    , observations : List Finding
    , feedback : Dict String FindingFeedback
    , findingCount : Int
    }


allVerdicts : List String
allVerdicts =
    [ "accurate"
    , "false_positive"
    , "noisy"
    , "overly_strict"
    , "partially_correct"
    , "missed_context"
    ]


verdictLabel : String -> String
verdictLabel verdict =
    String.replace "_" " " verdict


{-| The three conclusions review/v1 admits, spelled for a reader. An unknown
value is shown as-is rather than mapped onto one of the three — a review whose
conclusion we don't recognise has not been judged by us.
-}
conclusionLabel : String -> String
conclusionLabel conclusion =
    case conclusion of
        "accept" ->
            "accept"

        "changes-required" ->
            "changes required"

        "inconclusive" ->
            "inconclusive"

        "" ->
            "no conclusion"

        other ->
            other


{-| ( background, foreground ) for the conclusion badge. Inconclusive is amber
rather than red: it is the reviewer declining to decide, not a rejection.
-}
conclusionTone : String -> ( String, String )
conclusionTone conclusion =
    case conclusion of
        "accept" ->
            ( "#2e4f2e", "#9fdf9f" )

        "changes-required" ->
            ( "#5c2626", "#f0a0a0" )

        "inconclusive" ->
            ( "#5c4a26", "#f0d0a0" )

        _ ->
            ( "#3d3c3c", "#b0b0b0" )


findingTotal : Summary -> Int
findingTotal info =
    Dict.foldl (\_ count total -> total + count) 0 info.severityCounts


observationCount : Summary -> Int
observationCount info =
    Dict.get "observation" info.severityCounts |> Maybe.withDefault 0


{-| Everything that is not an observation. review/v1 makes observation the exact
complement of a substantive finding, so this needs no severity list of its own —
a severity added to the contract lands here without a code change.
-}
substantiveCount : Summary -> Int
substantiveCount info =
    findingTotal info - observationCount info


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


decodeSummary : Json.Decode.Decoder Summary
decodeSummary =
    Json.Decode.succeed Summary
        |> andMap (defaultTo 0 <| Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "build_name" Json.Decode.string)
        |> andMap (defaultTo "main" <| Json.Decode.field "team_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "pipeline_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "job_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "conclusion" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "summary" Json.Decode.string)
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "severity_counts" (Json.Decode.dict Json.Decode.int))
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "evaluated_count" Json.Decode.int)
        |> andMap (Snapshot.decodeOptionalIdField "snapshot_id")
        |> andMap (Snapshot.decodeOptionalIdField "workflow_run_id")
        |> andMap (Snapshot.decodeOptionalIdField "production_id")


{-| Prose fields stay tolerant so one unexpected finding can't empty the panel,
but id/severity/category come straight from a record the server already put
through the read-time contract gate.
-}
decodeFinding : Json.Decode.Decoder Finding
decodeFinding =
    Json.Decode.succeed Finding
        |> andMap (defaultTo "" <| Json.Decode.field "id" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "severity" Json.Decode.string)
        |> andMap (defaultTo False <| Json.Decode.field "blocking" Json.Decode.bool)
        |> andMap (defaultTo "" <| Json.Decode.field "title" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "file" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "line" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "category" Json.Decode.string)


decodeFeedback : Json.Decode.Decoder FindingFeedback
decodeFeedback =
    Json.Decode.succeed FindingFeedback
        |> andMap (Json.Decode.field "verdict" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "notes" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "reviewer" Json.Decode.string)


decodeBuildReview : Json.Decode.Decoder BuildReview
decodeBuildReview =
    Json.Decode.succeed BuildReview
        |> andMap decodeSummary
        |> andMap (defaultTo [] <| Json.Decode.field "proven_issues" (Json.Decode.list decodeFinding))
        |> andMap (defaultTo [] <| Json.Decode.field "observations" (Json.Decode.list decodeFinding))
        |> andMap (defaultTo Dict.empty <| Json.Decode.field "feedback" (Json.Decode.dict decodeFeedback))
        |> andMap (defaultTo 0 <| Json.Decode.field "finding_count" Json.Decode.int)
