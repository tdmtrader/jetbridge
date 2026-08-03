module WorkflowRunGraphTests exposing (all)

import Concourse.WorkflowRunGraph as WorkflowRunGraph
import Expect
import Test exposing (Test, test)


all : Test
all =
    test "node occurrence history orders retry copies before recovery attempts" <|
        \_ ->
            runGraph
                |> (\graph -> WorkflowRunGraph.occurrencesForNode graph "implement")
                |> List.map (\occurrence -> ( occurrence.retryAttempt, occurrence.attempt ))
                |> Expect.equal
                    [ ( 1, 1 )
                    , ( 1, 2 )
                    , ( 2, 1 )
                    ]


runGraph : WorkflowRunGraph.RunGraph
runGraph =
    { workflowRunId = "9007199254740993"
    , workflowName = "small-fix-v3"
    , workflowVersion = 3
    , graph = { nodes = [], edges = [] }
    , graphUnavailable = False
    , occurrences =
        [ makeOccurrence 1 2
        , makeOccurrence 2 1
        , makeOccurrence 1 1
        ]
    }


makeOccurrence : Int -> Int -> WorkflowRunGraph.Occurrence
makeOccurrence retryAttempt attempt =
    { nodeId = "implement"
    , nodeKind = "agent"
    , retryAttempt = retryAttempt
    , attempt = attempt
    , status = "succeeded"
    , planId = "plan"
    , waitId = Nothing
    , publicationId = Nothing
    , startedAt = Nothing
    , completedAt = Nothing
    , durationSeconds = 0
    , costUsd = 0
    }
