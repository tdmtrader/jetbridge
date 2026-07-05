module AgentReviewTests exposing (all)

import Concourse.AgentReview as AgentReview
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "agent review decoders"
        [ test "decodes a build review with findings and feedback" <|
            \_ ->
                """
                [{"build_id":42,"build_name":"3","team_name":"main","pipeline_name":"cs","job_name":"ar",
                  "repo":"concourse","commit_sha":"abc123","branch":"jetbridge",
                  "score":7.5,"max_score":10,"pass":true,"proven_count":1,"observation_count":1,
                  "summary":"one bug","agent_model":"m","duration_seconds":60,"created_at":1700000000,
                  "evaluated_count":1,"finding_count":2,
                  "proven_issues":[{"id":"PI-1","severity":"high","title":"nil deref","description":"boom","file":"a.go","line":10,"category":"correctness","test_name":"TestNil","test_output":"FAIL"}],
                  "observations":[{"id":"OB-1","title":"long func","file":"b.go","line":5,"category":"maintainability"}],
                  "feedback":{"PI-1":{"verdict":"accurate","notes":"","reviewer":"tdm"}}}]
                """
                    |> Json.Decode.decodeString (Json.Decode.list AgentReview.decodeBuildReview)
                    |> Expect.all
                        [ Result.map List.length >> Expect.equal (Ok 1)
                        , Result.map (List.head >> Maybe.map (.info >> .score)) >> Expect.equal (Ok (Just 7.5))
                        , Result.map (List.head >> Maybe.map (.provenIssues >> List.length)) >> Expect.equal (Ok (Just 1))
                        , Result.map (List.head >> Maybe.map .findingCount) >> Expect.equal (Ok (Just 2))
                        ]
        , test "tolerates an empty finding object without failing the list" <|
            \_ ->
                """
                [{"build_id":42,"build_name":"3","team_name":"main","pipeline_name":"cs","job_name":"ar",
                  "repo":"concourse","commit_sha":"abc123","branch":"jetbridge",
                  "score":7.5,"max_score":10,"pass":true,"proven_count":1,"observation_count":0,
                  "summary":"one bug","agent_model":"m","duration_seconds":60,"created_at":1700000000,
                  "evaluated_count":1,"finding_count":1,
                  "proven_issues":[{}],
                  "observations":[],
                  "feedback":{}}]
                """
                    |> Json.Decode.decodeString (Json.Decode.list AgentReview.decodeBuildReview)
                    |> Result.map (List.head >> Maybe.map (.provenIssues >> List.map (\f -> ( f.id, f.title ))))
                    |> Expect.equal (Ok (Just [ ( "", "" ) ]))
        , test "decodes a review summary row" <|
            \_ ->
                """
                {"build_id":42,"build_name":"3","team_name":"main","pipeline_name":"cs","job_name":"ar",
                 "repo":"concourse","commit_sha":"abc123","branch":"jetbridge",
                 "score":4.0,"max_score":10,"pass":false,"proven_count":4,"observation_count":1,
                 "summary":"several bugs","agent_model":"m","duration_seconds":60,"created_at":1700000000,
                 "evaluated_count":5}
                """
                    |> Json.Decode.decodeString AgentReview.decodeSummary
                    |> Result.map .pass
                    |> Expect.equal (Ok False)
        ]
