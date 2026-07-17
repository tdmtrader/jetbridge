module Concourse.AgentReview exposing
    ( BuildReview
    , Finding
    , FindingFeedback
    , Summary
    , allVerdicts
    , decodeBuildReview
    , decodeSummary
    , repoBlobUrl
    , verdictLabel
    )

import Dict exposing (Dict)
import Json.Decode
import Json.Decode.Extra exposing (andMap)


type alias Summary =
    { buildId : Int
    , buildName : String
    , teamName : String
    , pipelineName : String
    , jobName : String
    , repo : String
    , commitSha : String
    , branch : String
    , score : Float
    , maxScore : Float
    , pass : Bool
    , provenCount : Int
    , observationCount : Int
    , summary : String
    , createdAt : Int
    , evaluatedCount : Int
    }


type alias Finding =
    { id : String
    , severity : String
    , title : String
    , description : String
    , file : String
    , line : Int
    , category : String
    , testName : String
    , testOutput : String
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


{-| Build a GitHub-style blob URL for a finding's file at the reviewed sha.
The review's `repo` is a full clone URL (e.g. <https://github.com/org/repo.git>);
strip the trailing `.git` and append `/blob/<sha>/<file>#L<line>`. Returns
Nothing for empty or non-http repos so the caller falls back to plain text
rather than emitting a broken link.
-}
repoBlobUrl : String -> String -> String -> Int -> Maybe String
repoBlobUrl repo sha file line =
    if not (String.startsWith "http" repo) || sha == "" || file == "" then
        Nothing

    else
        let
            base =
                if String.endsWith ".git" repo then
                    String.dropRight 4 repo

                else
                    repo

            anchor =
                if line > 0 then
                    "#L" ++ String.fromInt line

                else
                    ""
        in
        Just (base ++ "/blob/" ++ sha ++ "/" ++ file ++ anchor)


defaultTo : a -> Json.Decode.Decoder a -> Json.Decode.Decoder a
defaultTo default =
    Json.Decode.maybe >> Json.Decode.map (Maybe.withDefault default)


decodeSummary : Json.Decode.Decoder Summary
decodeSummary =
    Json.Decode.succeed Summary
        |> andMap (Json.Decode.field "build_id" Json.Decode.int)
        |> andMap (Json.Decode.field "build_name" Json.Decode.string)
        |> andMap (Json.Decode.field "team_name" Json.Decode.string)
        |> andMap (Json.Decode.field "pipeline_name" Json.Decode.string)
        |> andMap (Json.Decode.field "job_name" Json.Decode.string)
        |> andMap (Json.Decode.field "repo" Json.Decode.string)
        |> andMap (Json.Decode.field "commit_sha" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "branch" Json.Decode.string)
        |> andMap (Json.Decode.field "score" Json.Decode.float)
        |> andMap (Json.Decode.field "max_score" Json.Decode.float)
        |> andMap (Json.Decode.field "pass" Json.Decode.bool)
        |> andMap (Json.Decode.field "proven_count" Json.Decode.int)
        |> andMap (Json.Decode.field "observation_count" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "summary" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "created_at" Json.Decode.int)
        |> andMap (defaultTo 0 <| Json.Decode.field "evaluated_count" Json.Decode.int)


{-| All fields tolerant: the ATC keeps partially-decoded findings rather than
dropping them, so nothing here may be required.
-}
decodeFinding : Json.Decode.Decoder Finding
decodeFinding =
    Json.Decode.succeed Finding
        |> andMap (defaultTo "" <| Json.Decode.field "id" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "severity" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "title" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "description" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "file" Json.Decode.string)
        |> andMap (defaultTo 0 <| Json.Decode.field "line" Json.Decode.int)
        |> andMap (defaultTo "" <| Json.Decode.field "category" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "test_name" Json.Decode.string)
        |> andMap (defaultTo "" <| Json.Decode.field "test_output" Json.Decode.string)


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
