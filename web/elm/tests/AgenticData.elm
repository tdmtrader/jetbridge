module AgenticData exposing
    ( experiment
    , repositoryChange
    , runDetail
    , runSummary
    , scorecard
    , snapshotDetail
    , storedCell
    , wait
    , workflowOverview
    , workflowVersion
    )

import AgentGraph.Model as Graph
import Concourse.Agent as Agent
import Concourse.Experiment as Experiment
import Concourse.Snapshot as Snapshot
import Concourse.WorkflowOverview as WorkflowOverview
import Concourse.WorkflowRun as WorkflowRun
import Time


workflowVersion : Agent.WorkflowVersion
workflowVersion =
    { id = 41
    , name = "review-api"
    , version = 3
    , schemaVersion = 3
    , signatureVersion = 1
    , contentHash = "abcdef0123456789abcdef0123456789"
    , live = True
    , description = "review a repository snapshot"
    , createdBy = "alice"
    , createdAt = Time.millisToPosix 0
    , promotedAt = Just (Time.millisToPosix 1750000000000)
    , promotedBy = "carol"
    , signature =
        { inputs = [ { name = "repository", typeRef = "repository/v1", optional = False } ]
        , outputs = [ { name = "review", typeRef = "review/v1", optional = False } ]
        }
    }


runSummary : WorkflowRun.Summary
runSummary =
    { id = "9007199254740993"
    , pipelineRunId = Just 12
    , workflowName = "review-api"
    , workflowVersion = 3
    , schemaVersion = 3
    , signatureVersion = 1
    , definitionContentHash = workflowVersion.contentHash
    , functionId = Nothing
    , status = "running"
    , executionStatus = Just "started"
    , originKind = "manual"
    , originReference = ""
    , ticketId = Nothing
    , ticketReference = ""
    , createdBy = "alice"
    , retryOf = Nothing
    , createdAt = "2026-07-22T12:00:00Z"
    , updatedAt = "2026-07-22T12:01:00Z"
    , startedAt = Just "2026-07-22T12:00:05Z"
    , completedAt = Nothing
    , parameterizedConfigHash = "parameterized-hash"
    , instanceConfigHash = Just "instance-hash"
    , actualPlanHash = Just "actual-plan-hash"
    , plannedBuildId = Just 42
    }


{-| A promoted workflow with one agent node that needs attention.

The shape mirrors `workflowoverview.Response`: separate active and windowed
history counts, no aggregate status, and a graph that is always present even
when it is empty.

-}
workflowOverview : WorkflowOverview.Overview
workflowOverview =
    { workflow =
        { name = "review-api"
        , hasPromotedVersion = True
        , graphVersion = 3
        , contentHash = workflowVersion.contentHash
        }
    , window =
        { kind = "7d"
        , from = "2026-07-24T12:00:00Z"
        , to = "2026-07-31T12:00:00Z"
        , includesActiveBeforeWindow = True
        }
    , graph =
        { nodes =
            [ { id = "input:repository"
              , kind = Graph.Input
              , displayName = "repository"
              , typeRef = "repository/v1"
              , optional = False
              , decorations = []
              }
            , { id = "implement"
              , kind = Graph.Agent
              , displayName = "implement"
              , typeRef = "review/v1"
              , optional = False
              , decorations = []
              }
            ]
        , edges =
            [ { from = "input:repository"
              , to = "implement"
              , portName = "repository"
              , typeRef = "repository/v1"
              , optional = False
              }
            ]
        }
    , graphUnavailable = False
    , nodeState =
        [ { nodeId = "implement"
          , running = 1
          , waiting = 0
          , pending = 0
          , succeeded = 4
          , failed = 1
          , errored = 0
          , aborted = 0
          , skipped = 0
          , needsAttention = True
          , hasWindowActivity = True
          }
        ]
    , revisionBoundaries =
        [ { version = 3
          , promotedAt = Just "2026-07-30T09:00:00Z"
          , firstRunId = "9007199254740993"
          , firstRunAt = "2026-07-30T10:00:00Z"
          }
        ]
    , hasHistoricalOnlyNodes = False
    }


repositoryRef : Snapshot.Ref
repositoryRef =
    { id = "9007199254740995"
    , typeRef = "repository/v1"
    , digest = "sha256:repository"
    }


changeManifest : Snapshot.Manifest
changeManifest =
    { id = "9007199254740997"
    , typeRef = "repository-change/v1"
    , digest = "sha256:change"
    , byteSize = 120
    , fileCount = 2
    , representation = "git-bundle"
    , intrinsicMetadata = Nothing
    , contentState = "available"
    , createdAt = "2026-07-22T12:02:00Z"
    }


runDetail : WorkflowRun.Detail
runDetail =
    { summary = runSummary
    , inputs = [ { portName = "repository", snapshot = repositoryRef } ]
    , outputs = [ { portName = "change", snapshot = changeManifest } ]
    }


wait : WorkflowRun.Wait
wait =
    { id = "9007199254741001"
    , outputName = "answer"
    , questionName = "approval"
    , question =
        { id = "9007199254741003"
        , typeRef = "question/v1"
        , digest = "sha256:question"
        }
    , prompt = "Ship this change?"
    , context = "The validation suite passed."
    , options = [ "approve", "reject" ]
    , default = "reject"
    , expectedType = "human-answer/v1"
    , deadline = "2026-07-23T12:00:00Z"
    , timeoutPolicy = "park"
    , hasDefault = False
    , workflowPort = "approval"
    , status = "waiting"
    , answer = Nothing
    , resolvedBy = ""
    , resolvedByDisplayName = ""
    , resolutionSource = ""
    , createdAt = "2026-07-22T12:00:00Z"
    , updatedAt = "2026-07-22T12:00:00Z"
    , resolvedAt = Nothing
    }


repositoryChange : WorkflowRun.RepositoryChange
repositoryChange =
    { snapshotId = changeManifest.id
    , status = "ready"
    , repositoryId = "repo"
    , baseSha = "base"
    , resultSha = "result"
    , resultTreeSha = "tree"
    , representation = "git-bundle"
    , files =
        [ { path = "src/a.go"
          , previousPath = ""
          , status = "modified"
          , linesAdded = 3
          , linesDeleted = 1
          , binary = False
          , patch = ""
          , truncated = False
          }
        ]
    , fileCount = 1
    , linesAdded = 3
    , linesDeleted = 1
    , unifiedDiff = "diff --git a/src/a.go b/src/a.go\n+fixed"
    , truncated = False
    , truncationReason = ""
    }


snapshotDetail : Snapshot.Detail
snapshotDetail =
    { manifest = changeManifest
    , replicaCount = 2
    , retentionClaims =
        [ { id = "1"
          , snapshotId = changeManifest.id
          , class = "pin"
          , expiresAt = Nothing
          , actor = "alice"
          , reason = "review evidence"
          , createdAt = "2026-07-22T12:03:00Z"
          }
        ]
    , productions =
        [ { id = "4"
          , kind = "build"
          , createdBy = "alice"
          , build =
                Just
                    { id = 42
                    , planId = "plan-review"
                    , attempt = "1"
                    , stepKind = "agent"
                    , stepName = "review"
                    , workflowDefinitionId = Just workflowVersion.id
                    , workflowRunId = Just runSummary.id
                    , workflowName = Just workflowVersion.name
                    }
          , outputPort = "change"
          , sourceMetadata = Nothing
          , createdAt = "2026-07-22T12:02:00Z"
          , inputs = [ { position = 0, portName = "repository", snapshot = repositoryRef } ]
          }
        ]
    , downstream = []
    }


target : Experiment.Target
target =
    { kind = "workflow"
    , workflowName = "review-api"
    , definitionId = 41
    , version = 3
    , functionId = Nothing
    }


signature : Experiment.Signature
signature =
    { inputs = [ { name = "repository", typeRef = "repository/v1", optional = False } ]
    , outputs = [ { name = "review", typeRef = "review/v1", optional = False } ]
    }


experiment : Experiment.Experiment
experiment =
    { id = "9007199254741011"
    , teamName = "main"
    , definition =
        { name = "review-prompts"
        , state = "running"
        , signature = signature
        , variants =
            [ { label = "control", control = True, signatureHash = "control-hash", target = target }
            , { label = "candidate", control = False, signatureHash = "candidate-hash", target = { target | definitionId = 42, version = 4 } }
            ]
        , fixtures =
            [ { label = "normal"
              , role = "normal"
              , inputs = [ ( "repository", repositoryRef.id ) ]
              , assertions = []
              }
            , { label = "negative"
              , role = "negative_control"
              , inputs = [ ( "repository", repositoryRef.id ) ]
              , assertions = [ { metric = "quality", comparator = "lte", thresholds = [ 1 ] } ]
              }
            ]
        , evaluator =
            { target = { target | workflowName = "review-evaluator", definitionId = 51 }
            , signature = signature
            , mappings =
                [ { evaluatorPort = "candidate"
                  , sourceDirection = "candidate_output"
                  , sourcePort = "review"
                  }
                ]
            , measurementsPort = "measurements"
            }
        , repetitions = 5
        , budget = { perCellUsd = 1, totalUsd = 20, maxTokensPerCell = 10000 }
        }
    , revision = 4
    , createdBy = "alice"
    , createdAt = "2026-07-22T12:00:00Z"
    , updatedAt = "2026-07-22T12:01:00Z"
    , startedAt = Just "2026-07-22T12:00:30Z"
    , completedAt = Nothing
    }


storedCell : Experiment.StoredCell
storedCell =
    { id = "9007199254741013"
    , experimentId = experiment.id
    , fixtureId = "1"
    , fixtureLabel = "normal"
    , fixtureRole = "normal"
    , variantId = "2"
    , variantLabel = "candidate"
    , repetition = 1
    , status = "candidate_platform_failure"
    , candidateWorkflowRunId = Just runSummary.id
    , evaluatorWorkflowRunId = Nothing
    , measurementSnapshotId = Nothing
    , candidateFailureCategory = "platform"
    , createdAt = "2026-07-22T12:00:00Z"
    , updatedAt = "2026-07-22T12:01:00Z"
    , completedAt = Just "2026-07-22T12:01:00Z"
    }


emptyDistribution : Experiment.Distribution
emptyDistribution =
    { count = 4
    , mean = 1
    , median = 1
    , min = 0
    , max = 2
    , stddev = 0.5
    , p05 = 0.2
    , p25 = 0.5
    , p75 = 1.5
    , p95 = 1.8
    }


scorecard : Experiment.Scorecard
scorecard =
    { experimentId = experiment.id
    , control = "control"
    , variants =
        [ { label = "candidate"
          , expectedCells = 10
          , observedCells = 8
          , validCells = 7
          , validCoverage = 0.7
          , platformErrors = 1
          , platformErrorRate = 0.1
          , budgetSkipped = 1
          , negativeControlsPass = False
          , metrics = [ ( "quality", emptyDistribution ) ]
          , metricDirections = [ ( "quality", "higher" ) ]
          , costUsd = emptyDistribution
          , latencySeconds = emptyDistribution
          , tokens = emptyDistribution
          , humanInterventions = emptyDistribution
          }
        ]
    , comparisons =
        [ { variant = "candidate"
          , control = "control"
          , metric = "quality"
          , direction = "higher"
          , pairedCount = 4
          , wins = 3
          , ties = 0
          , losses = 1
          , meanDelta = 0.5
          , confidenceLow = -0.2
          , confidenceHigh = 1.1
          , winner = Nothing
          , recommendation = "insufficient_evidence"
          , failedConditions = [ "at least five pairs", "80% coverage" ]
          }
        ]
    , cells =
        [ { id = storedCell.id
          , experimentId = experiment.id
          , variant = "candidate"
          , fixture = "normal"
          , role = "normal"
          , repetition = 1
          , status = "valid_measurement"
          , negativeControlPassed = False
          , costUsd = 1
          , latencySeconds = 12
          , inputTokens = 80
          , outputTokens = 20
          , humanInterventions = 1
          , measurements = [ { name = "quality", value = 8, unit = "score", direction = "higher" } ]
          }
        ]
    }
