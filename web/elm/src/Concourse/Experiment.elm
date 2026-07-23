module Concourse.Experiment exposing
    ( Assertion
    , Budget
    , Cell
    , Comparison
    , Definition
    , Distribution
    , Evaluator
    , EvaluatorMapping
    , Experiment
    , Fixture
    , Measurement
    , Port
    , Scorecard
    , Signature
    , StoredCell
    , Target
    , Variant
    , VariantScore
    , decodeCell
    , decodeComparison
    , decodeDistribution
    , decodeExperiment
    , decodeMeasurement
    , decodeScorecard
    , decodeStoredCell
    )

import Concourse.Snapshot as Snapshot
import Dict
import Json.Decode exposing (Decoder)
import Json.Decode.Extra exposing (andMap)


type alias Measurement =
    { name : String
    , value : Float
    , unit : String
    , direction : String
    }


type alias Cell =
    { id : String
    , experimentId : String
    , variant : String
    , fixture : String
    , role : String
    , repetition : Int
    , status : String
    , negativeControlPassed : Bool
    , costUsd : Float
    , latencySeconds : Float
    , inputTokens : Int
    , outputTokens : Int
    , humanInterventions : Int
    , measurements : List Measurement
    }


type alias StoredCell =
    { id : String
    , experimentId : String
    , fixtureId : String
    , fixtureLabel : String
    , fixtureRole : String
    , variantId : String
    , variantLabel : String
    , repetition : Int
    , status : String
    , candidateWorkflowRunId : Maybe String
    , evaluatorWorkflowRunId : Maybe String
    , measurementSnapshotId : Maybe String
    , candidateFailureCategory : String
    , createdAt : String
    , updatedAt : String
    , completedAt : Maybe String
    }


type alias Distribution =
    { count : Int
    , mean : Float
    , median : Float
    , min : Float
    , max : Float
    , stddev : Float
    , p05 : Float
    , p25 : Float
    , p75 : Float
    , p95 : Float
    }


type alias Comparison =
    { variant : String
    , control : String
    , metric : String
    , direction : String
    , pairedCount : Int
    , wins : Int
    , ties : Int
    , losses : Int
    , meanDelta : Float
    , confidenceLow : Float
    , confidenceHigh : Float
    , winner : Maybe String
    , recommendation : String
    , failedConditions : List String
    }


type alias Port =
    { name : String
    , typeRef : String
    , optional : Bool
    }


type alias Signature =
    { inputs : List Port
    , outputs : List Port
    }


type alias Target =
    { kind : String
    , workflowName : String
    , definitionId : Int
    , version : Int
    , functionId : Maybe String
    }


type alias Variant =
    { label : String
    , control : Bool
    , signatureHash : String
    , target : Target
    }


type alias Assertion =
    { metric : String
    , comparator : String
    , thresholds : List Float
    }


type alias Fixture =
    { label : String
    , role : String
    , inputs : List ( String, String )
    , assertions : List Assertion
    }


type alias EvaluatorMapping =
    { evaluatorPort : String
    , sourceDirection : String
    , sourcePort : String
    }


type alias Evaluator =
    { target : Target
    , signature : Signature
    , mappings : List EvaluatorMapping
    , measurementsPort : String
    }


type alias Budget =
    { perCellUsd : Float
    , totalUsd : Float
    , maxTokensPerCell : Int
    }


type alias Definition =
    { name : String
    , state : String
    , signature : Signature
    , variants : List Variant
    , fixtures : List Fixture
    , evaluator : Evaluator
    , repetitions : Int
    , budget : Budget
    }


type alias Experiment =
    { id : String
    , teamName : String
    , definition : Definition
    , revision : Int
    , createdBy : String
    , createdAt : String
    , updatedAt : String
    , startedAt : Maybe String
    , completedAt : Maybe String
    }


type alias VariantScore =
    { label : String
    , expectedCells : Int
    , observedCells : Int
    , validCells : Int
    , validCoverage : Float
    , platformErrors : Int
    , platformErrorRate : Float
    , budgetSkipped : Int
    , negativeControlsPass : Bool
    , metrics : List ( String, Distribution )
    , metricDirections : List ( String, String )
    , costUsd : Distribution
    , latencySeconds : Distribution
    , tokens : Distribution
    , humanInterventions : Distribution
    }


type alias Scorecard =
    { experimentId : String
    , control : String
    , variants : List VariantScore
    , comparisons : List Comparison
    , cells : List Cell
    }


decodeMeasurement : Decoder Measurement
decodeMeasurement =
    Json.Decode.succeed Measurement
        |> andMap (Json.Decode.field "name" Json.Decode.string)
        |> andMap (Json.Decode.field "value" Json.Decode.float)
        |> andMap (Json.Decode.field "unit" Json.Decode.string)
        |> andMap (Json.Decode.field "direction" Json.Decode.string)


decodeCell : Decoder Cell
decodeCell =
    decodeCellWithExperimentId (Json.Decode.field "experiment_id" Snapshot.decodeId)


decodeCellWithExperimentId : Decoder String -> Decoder Cell
decodeCellWithExperimentId experimentIdDecoder =
    Json.Decode.succeed Cell
        |> andMap (Json.Decode.field "id" Snapshot.decodeId)
        |> andMap experimentIdDecoder
        |> andMap (oneOfFields [ "variant", "variant_label" ] Json.Decode.string)
        |> andMap (oneOfFields [ "fixture", "fixture_label" ] Json.Decode.string)
        |> andMap (defaultTo "normal" (Json.Decode.field "role" Json.Decode.string))
        |> andMap (Json.Decode.field "repetition" Json.Decode.int)
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (defaultTo False (Json.Decode.field "negative_control_passed" Json.Decode.bool))
        |> andMap (defaultTo 0 (Json.Decode.field "cost_usd" Json.Decode.float))
        |> andMap
            (Json.Decode.oneOf
                [ Json.Decode.field "latency_seconds" Json.Decode.float
                , Json.Decode.field "latency" Json.Decode.float |> Json.Decode.map (\nanoseconds -> nanoseconds / 1000000000)
                , Json.Decode.succeed 0
                ]
            )
        |> andMap (defaultTo 0 (Json.Decode.field "input_tokens" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "output_tokens" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "human_interventions" Json.Decode.int))
        |> andMap (defaultTo [] (Json.Decode.field "measurements" (Json.Decode.list decodeMeasurement)))


decodeStoredCell : Decoder StoredCell
decodeStoredCell =
    Json.Decode.succeed StoredCell
        |> andMap (Json.Decode.field "id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "experiment_id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "fixture_id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "fixture_label" Json.Decode.string)
        |> andMap (Json.Decode.field "fixture_role" Json.Decode.string)
        |> andMap (Json.Decode.field "variant_id" Snapshot.decodeId)
        |> andMap (Json.Decode.field "variant_label" Json.Decode.string)
        |> andMap (Json.Decode.field "repetition" Json.Decode.int)
        |> andMap (Json.Decode.field "status" Json.Decode.string)
        |> andMap (Snapshot.decodeOptionalIdField "candidate_workflow_run_id")
        |> andMap (Snapshot.decodeOptionalIdField "evaluator_workflow_run_id")
        |> andMap (Snapshot.decodeOptionalIdField "measurement_snapshot_id")
        |> andMap (defaultTo "" (Json.Decode.field "candidate_failure_category" Json.Decode.string))
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)
        |> andMap (Json.Decode.field "updated_at" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" Json.Decode.string))


decodeDistribution : Decoder Distribution
decodeDistribution =
    Json.Decode.succeed Distribution
        |> andMap (Json.Decode.field "count" Json.Decode.int)
        |> andMap (Json.Decode.field "mean" Json.Decode.float)
        |> andMap (Json.Decode.field "median" Json.Decode.float)
        |> andMap (Json.Decode.field "min" Json.Decode.float)
        |> andMap (Json.Decode.field "max" Json.Decode.float)
        |> andMap (Json.Decode.field "stddev" Json.Decode.float)
        |> andMap (Json.Decode.field "p05" Json.Decode.float)
        |> andMap (Json.Decode.field "p25" Json.Decode.float)
        |> andMap (Json.Decode.field "p75" Json.Decode.float)
        |> andMap (Json.Decode.field "p95" Json.Decode.float)


decodeComparison : Decoder Comparison
decodeComparison =
    Json.Decode.succeed Comparison
        |> andMap (Json.Decode.field "variant" Json.Decode.string)
        |> andMap (Json.Decode.field "control" Json.Decode.string)
        |> andMap (Json.Decode.field "metric" Json.Decode.string)
        |> andMap (Json.Decode.field "direction" Json.Decode.string)
        |> andMap (Json.Decode.field "paired_count" Json.Decode.int)
        |> andMap (Json.Decode.field "wins" Json.Decode.int)
        |> andMap (Json.Decode.field "ties" Json.Decode.int)
        |> andMap (Json.Decode.field "losses" Json.Decode.int)
        |> andMap (Json.Decode.field "mean_delta" Json.Decode.float)
        |> andMap (Json.Decode.field "confidence_low" Json.Decode.float)
        |> andMap (Json.Decode.field "confidence_high" Json.Decode.float)
        |> andMap (Json.Decode.maybe (Json.Decode.field "winner" Json.Decode.string))
        |> andMap (Json.Decode.field "recommendation" Json.Decode.string)
        |> andMap (defaultTo [] (Json.Decode.field "failed_conditions" (Json.Decode.list Json.Decode.string)))


decodePort : Decoder Port
decodePort =
    Json.Decode.succeed Port
        |> andMap (Json.Decode.field "name" Json.Decode.string)
        |> andMap (Json.Decode.field "type" Json.Decode.string)
        |> andMap (defaultTo False (Json.Decode.field "optional" Json.Decode.bool))


decodeSignature : Decoder Signature
decodeSignature =
    Json.Decode.succeed Signature
        |> andMap (defaultTo [] (Json.Decode.field "inputs" (Json.Decode.list decodePort)))
        |> andMap (defaultTo [] (Json.Decode.field "outputs" (Json.Decode.list decodePort)))


decodeTarget : Decoder Target
decodeTarget =
    Json.Decode.succeed Target
        |> andMap (Json.Decode.field "kind" Json.Decode.string)
        |> andMap (Json.Decode.field "workflow_name" Json.Decode.string)
        |> andMap (Json.Decode.field "definition_id" Json.Decode.int)
        |> andMap (Json.Decode.field "version" Json.Decode.int)
        |> andMap (Json.Decode.maybe (Json.Decode.field "function_id" Json.Decode.string))


decodeVariant : Decoder Variant
decodeVariant =
    Json.Decode.succeed Variant
        |> andMap (Json.Decode.field "label" Json.Decode.string)
        |> andMap (defaultTo False (Json.Decode.field "control" Json.Decode.bool))
        |> andMap (Json.Decode.field "signature_hash" Json.Decode.string)
        |> andMap (Json.Decode.field "target" decodeTarget)


decodeAssertion : Decoder Assertion
decodeAssertion =
    Json.Decode.succeed Assertion
        |> andMap (Json.Decode.field "metric" Json.Decode.string)
        |> andMap (Json.Decode.field "comparator" Json.Decode.string)
        |> andMap (Json.Decode.field "thresholds" (Json.Decode.list Json.Decode.float))


decodeFixture : Decoder Fixture
decodeFixture =
    Json.Decode.succeed Fixture
        |> andMap (Json.Decode.field "label" Json.Decode.string)
        |> andMap (Json.Decode.field "role" Json.Decode.string)
        |> andMap
            (Json.Decode.field "inputs" (Json.Decode.dict Snapshot.decodeId)
                |> Json.Decode.map Dict.toList
            )
        |> andMap (defaultTo [] (Json.Decode.field "assertions" (Json.Decode.list decodeAssertion)))


decodeEvaluatorMapping : Decoder EvaluatorMapping
decodeEvaluatorMapping =
    Json.Decode.succeed EvaluatorMapping
        |> andMap (Json.Decode.field "evaluator_port" Json.Decode.string)
        |> andMap (Json.Decode.field "source_direction" Json.Decode.string)
        |> andMap (Json.Decode.field "source_port" Json.Decode.string)


decodeEvaluator : Decoder Evaluator
decodeEvaluator =
    Json.Decode.succeed Evaluator
        |> andMap (Json.Decode.field "target" decodeTarget)
        |> andMap (Json.Decode.field "signature" decodeSignature)
        |> andMap (defaultTo [] (Json.Decode.field "mappings" (Json.Decode.list decodeEvaluatorMapping)))
        |> andMap (Json.Decode.field "measurements_port" Json.Decode.string)


decodeBudget : Decoder Budget
decodeBudget =
    Json.Decode.succeed Budget
        |> andMap (defaultTo 0 (Json.Decode.field "per_cell_usd" Json.Decode.float))
        |> andMap (defaultTo 0 (Json.Decode.field "total_usd" Json.Decode.float))
        |> andMap (defaultTo 0 (Json.Decode.field "max_tokens_per_cell" Json.Decode.int))


decodeDefinition : Decoder Definition
decodeDefinition =
    Json.Decode.succeed Definition
        |> andMap (Json.Decode.field "name" Json.Decode.string)
        |> andMap (Json.Decode.field "state" Json.Decode.string)
        |> andMap (Json.Decode.field "signature" decodeSignature)
        |> andMap (Json.Decode.field "variants" (Json.Decode.list decodeVariant))
        |> andMap (Json.Decode.field "fixtures" (Json.Decode.list decodeFixture))
        |> andMap (Json.Decode.field "evaluator" decodeEvaluator)
        |> andMap (Json.Decode.field "repetitions" Json.Decode.int)
        |> andMap (Json.Decode.field "budget" decodeBudget)


decodeExperiment : Decoder Experiment
decodeExperiment =
    Json.Decode.succeed Experiment
        |> andMap (Json.Decode.field "id" Snapshot.decodeId)
        |> andMap (defaultTo "main" (Json.Decode.field "team_name" Json.Decode.string))
        |> andMap (Json.Decode.field "definition" decodeDefinition)
        |> andMap (Json.Decode.field "revision" Json.Decode.int)
        |> andMap (Json.Decode.field "created_by" Json.Decode.string)
        |> andMap (Json.Decode.field "created_at" Json.Decode.string)
        |> andMap (Json.Decode.field "updated_at" Json.Decode.string)
        |> andMap (Json.Decode.maybe (Json.Decode.field "started_at" Json.Decode.string))
        |> andMap (Json.Decode.maybe (Json.Decode.field "completed_at" Json.Decode.string))


emptyDistribution : Distribution
emptyDistribution =
    Distribution 0 0 0 0 0 0 0 0 0 0


decodeVariantScore : String -> Decoder VariantScore
decodeVariantScore label =
    Json.Decode.succeed (VariantScore label)
        |> andMap (defaultTo 0 (Json.Decode.field "expected_cells" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "observed_cells" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "valid_cells" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "valid_coverage" Json.Decode.float))
        |> andMap (defaultTo 0 (Json.Decode.field "platform_errors" Json.Decode.int))
        |> andMap (defaultTo 0 (Json.Decode.field "platform_error_rate" Json.Decode.float))
        |> andMap (defaultTo 0 (Json.Decode.field "budget_skipped" Json.Decode.int))
        |> andMap (defaultTo False (Json.Decode.field "negative_controls_pass" Json.Decode.bool))
        |> andMap (defaultTo [] (Json.Decode.field "metrics" (Json.Decode.keyValuePairs decodeDistribution)))
        |> andMap (defaultTo [] (Json.Decode.field "metric_directions" (Json.Decode.keyValuePairs Json.Decode.string)))
        |> andMap (defaultTo emptyDistribution (Json.Decode.field "cost_usd" decodeDistribution))
        |> andMap (defaultTo emptyDistribution (Json.Decode.field "latency_seconds" decodeDistribution))
        |> andMap (defaultTo emptyDistribution (Json.Decode.field "tokens" decodeDistribution))
        |> andMap (defaultTo emptyDistribution (Json.Decode.field "human_interventions" decodeDistribution))


decodeVariants : Decoder (List VariantScore)
decodeVariants =
    Json.Decode.keyValuePairs Json.Decode.value
        |> Json.Decode.andThen
            (List.map (\( label, value ) -> Json.Decode.decodeValue (decodeVariantScore label) value)
                >> collectResults
            )


decodeComparisons : Decoder (List Comparison)
decodeComparisons =
    Json.Decode.keyValuePairs (Json.Decode.keyValuePairs decodeComparison)
        |> Json.Decode.map
            (List.concatMap
                (\( _, comparisons ) -> List.map Tuple.second comparisons)
            )


decodeScorecard : Decoder Scorecard
decodeScorecard =
    Json.Decode.map5
        (\experimentId control variants comparisons cells ->
            { experimentId = experimentId
            , control = control
            , variants = variants
            , comparisons = comparisons
            , cells = List.map (\cell -> { cell | experimentId = experimentId }) cells
            }
        )
        (Json.Decode.field "experiment_id" Snapshot.decodeId)
        (Json.Decode.field "control" Json.Decode.string)
        (Json.Decode.field "variants" decodeVariants)
        (defaultTo [] (Json.Decode.field "comparisons" decodeComparisons))
        (defaultTo []
            (Json.Decode.field "cells"
                (Json.Decode.list (decodeCellWithExperimentId (Json.Decode.succeed "")))
            )
        )


collectResults : List (Result Json.Decode.Error a) -> Decoder (List a)
collectResults results =
    case results of
        [] ->
            Json.Decode.succeed []

        result :: rest ->
            case result of
                Ok value ->
                    collectResults rest |> Json.Decode.map ((::) value)

                Err error ->
                    Json.Decode.fail (Json.Decode.errorToString error)


oneOfFields : List String -> Decoder a -> Decoder a
oneOfFields names decoder =
    names
        |> List.map (\name -> Json.Decode.field name decoder)
        |> Json.Decode.oneOf


defaultTo : a -> Decoder a -> Decoder a
defaultTo fallback decoder =
    Json.Decode.maybe decoder
        |> Json.Decode.map (Maybe.withDefault fallback)
