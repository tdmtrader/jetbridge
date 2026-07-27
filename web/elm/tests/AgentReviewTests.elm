module AgentReviewTests exposing (all)

import Concourse.AgentReview as AgentReview
import Dict
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


sealedReview : String
sealedReview =
    """
    [{"build_id":42,"build_name":"3","team_name":"main","pipeline_name":"cs","job_name":"ar",
      "workflow_name":"code-review",
      "conclusion":"changes-required","summary":"one bug",
      "severity_counts":{"critical":1,"observation":1},
      "created_at":1700000000,"evaluated_count":1,"finding_count":2,
      "snapshot_id":"9007199254740993","workflow_run_id":"9007199254740995",
      "production_id":"9007199254740997",
      "proven_issues":[{"id":"PI-1","severity":"critical","blocking":true,"title":"nil deref","description":"boom","file":"a.go","line":10,"category":"correctness"}],
      "observations":[{"id":"OB-1","severity":"observation","title":"long func","file":"b.go","line":5,"category":"maintainability"}],
      "feedback":{"PI-1":{"verdict":"accurate","notes":"","reviewer":"tdm"}}}]
    """


all : Test
all =
    describe "agent review decoders"
        [ test "decodes a sealed review with findings and feedback" <|
            \_ ->
                sealedReview
                    |> Json.Decode.decodeString (Json.Decode.list AgentReview.decodeBuildReview)
                    |> Expect.all
                        [ Result.map List.length >> Expect.equal (Ok 1)
                        , Result.map (List.head >> Maybe.map (.info >> .conclusion))
                            >> Expect.equal (Ok (Just "changes-required"))
                        , Result.map (List.head >> Maybe.map (.info >> .workflowName))
                            >> Expect.equal (Ok (Just "code-review"))
                        , Result.map (List.head >> Maybe.map (.provenIssues >> List.length))
                            >> Expect.equal (Ok (Just 1))
                        , Result.map (List.head >> Maybe.map (.provenIssues >> List.map .blocking))
                            >> Expect.equal (Ok (Just [ True ]))
                        , Result.map (List.head >> Maybe.map .findingCount) >> Expect.equal (Ok (Just 2))
                        ]
        , test "tolerates an empty finding object without failing the list" <|
            \_ ->
                """
                [{"team_name":"main","conclusion":"accept","severity_counts":{},
                  "proven_issues":[{}],"observations":[],"feedback":{}}]
                """
                    |> Json.Decode.decodeString (Json.Decode.list AgentReview.decodeBuildReview)
                    |> Result.map (List.head >> Maybe.map (.provenIssues >> List.map (\f -> ( f.id, f.title ))))
                    |> Expect.equal (Ok (Just [ ( "", "" ) ]))
        , test "decodes the production identity exactly" <|
            \_ ->
                sealedReview
                    |> Json.Decode.decodeString (Json.Decode.list AgentReview.decodeSummary)
                    |> Result.map (List.head >> Maybe.map .productionId)
                    |> Expect.equal (Ok (Just (Just "9007199254740997")))
        , test "counts split findings into substantive and observation" <|
            \_ ->
                let
                    info =
                        { buildId = 0
                        , buildName = ""
                        , teamName = "main"
                        , pipelineName = ""
                        , jobName = ""
                        , workflowName = ""
                        , conclusion = "changes-required"
                        , summary = ""
                        , severityCounts =
                            Dict.fromList [ ( "critical", 1 ), ( "low", 2 ), ( "observation", 3 ) ]
                        , createdAt = 0
                        , evaluatedCount = 0
                        , snapshotId = Nothing
                        , workflowRunId = Nothing
                        , productionId = Nothing
                        }
                in
                Expect.all
                    [ \_ -> AgentReview.findingTotal info |> Expect.equal 6
                    , \_ -> AgentReview.observationCount info |> Expect.equal 3
                    , \_ -> AgentReview.substantiveCount info |> Expect.equal 3
                    ]
                    ()
        , test "spells each review/v1 conclusion and passes an unknown one through" <|
            \_ ->
                Expect.all
                    [ \_ -> AgentReview.conclusionLabel "accept" |> Expect.equal "accept"
                    , \_ -> AgentReview.conclusionLabel "changes-required" |> Expect.equal "changes required"
                    , \_ -> AgentReview.conclusionLabel "inconclusive" |> Expect.equal "inconclusive"
                    , \_ -> AgentReview.conclusionLabel "" |> Expect.equal "no conclusion"
                    , \_ -> AgentReview.conclusionLabel "something-else" |> Expect.equal "something-else"
                    ]
                    ()
        ]
