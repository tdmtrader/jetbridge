module ExperimentDecoderTests exposing (all)

import Concourse.Experiment as Experiment
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Concourse.Experiment"
        [ test "decodes experiment, cell, and measurement IDs from the real wire shape" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeCell
                    """{"id":"9007199254741001","experiment_id":"9007199254740999","variant":"candidate","fixture":"repo-a","role":"normal","repetition":5,"status":"valid_measurement","negative_control_passed":false,"cost_usd":1.25,"latency_seconds":12.5,"input_tokens":1000,"output_tokens":200,"human_interventions":1,"measurements":[{"id":"quality","value":8.5,"unit":"score/10","direction":"higher-is-better"}]}"""
                    |> Result.map
                        (\cell ->
                            { id = cell.id
                            , experimentId = cell.experimentId
                            , status = cell.status
                            , measurementNames = List.map .name cell.measurements
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { id = "9007199254741001"
                            , experimentId = "9007199254740999"
                            , status = "valid_measurement"
                            , measurementNames = [ "quality" ]
                            }
                        )
        , test "rejects numeric experiment IDs" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeCell
                    """{"id":"1","experiment_id":2,"variant":"candidate","fixture":"repo-a","role":"normal","repetition":1,"status":"canceled","cost_usd":0,"latency_seconds":0,"input_tokens":0,"output_tokens":0,"human_interventions":0,"measurements":[]}"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "decodes fixture and variant database IDs as exact strings" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeStoredCell
                    """{"id":"9007199254741001","experiment_id":"9007199254740999","fixture_id":"9007199254741003","fixture_label":"repo-a","fixture_role":"normal","variant_id":"9007199254741005","variant_label":"candidate","repetition":1,"status":"candidate_platform_failure","created_at":"2026-07-22T12:00:00Z","updated_at":"2026-07-22T12:01:00Z"}"""
                    |> Result.map (\cell -> ( cell.fixtureId, cell.variantId ))
                    |> Expect.equal (Ok ( "9007199254741003", "9007199254741005" ))
        , test "rejects numeric fixture and variant database IDs" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeStoredCell
                    """{"id":"1","experiment_id":"2","fixture_id":3,"fixture_label":"repo-a","fixture_role":"normal","variant_id":4,"variant_label":"candidate","repetition":1,"status":"candidate_platform_failure","created_at":"2026-07-22T12:00:00Z","updated_at":"2026-07-22T12:01:00Z"}"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "decodes distributions and insufficient-evidence reasons" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeComparison
                    """{"variant":"candidate","control":"control","metric":"quality","direction":"higher","paired_count":4,"wins":3,"ties":0,"losses":1,"mean_delta":0.5,"confidence_low":-0.2,"confidence_high":1.1,"recommendation":"insufficient_evidence","failed_conditions":["at least five valid paired repetitions are required"]}"""
                    |> Result.map (\comparison -> ( comparison.pairedCount, comparison.winner, comparison.failedConditions ))
                    |> Expect.equal (Ok ( 4, Nothing, [ "at least five valid paired repetitions are required" ] ))
        , test "decodes a frozen experiment definition and progress" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeExperiment
                    """{"id":"9007199254740999","team_name":"main","definition":{"name":"review-prompts","state":"running","signature":{"inputs":[{"name":"repo","type":"repository/v1","optional":false}],"outputs":[{"name":"review","type":"review/v1","optional":false}]},"variants":[{"label":"control","control":true,"signature_hash":"hash","target":{"kind":"workflow","workflow_name":"review","definition_id":41,"version":3}},{"label":"candidate","control":false,"signature_hash":"hash","target":{"kind":"function","workflow_name":"review","definition_id":42,"version":4,"function_id":"review"}}],"fixtures":[{"label":"normal","role":"normal","inputs":{"repo":"9007199254741001"},"assertions":[]}],"evaluator":{"target":{"kind":"workflow","workflow_name":"judge","definition_id":51,"version":2},"signature":{"inputs":[],"outputs":[]},"mappings":[],"measurements_port":"measurements"},"repetitions":5,"budget":{"per_cell_usd":1,"total_usd":20,"max_tokens_per_cell":10000}},"revision":4,"created_by":"alice","created_at":"2026-07-22T12:00:00Z","updated_at":"2026-07-22T12:01:00Z"}"""
                    |> Result.map
                        (\experiment ->
                            { id = experiment.id
                            , name = experiment.definition.name
                            , state = experiment.definition.state
                            , variants = List.length experiment.definition.variants
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { id = "9007199254740999"
                            , name = "review-prompts"
                            , state = "running"
                            , variants = 2
                            }
                        )
        , test "decodes scorecard distributions and preserves raw cells" <|
            \_ ->
                Json.Decode.decodeString Experiment.decodeScorecard
                    """{"experiment_id":"9007199254740999","control":"control","variants":{"candidate":{"expected_cells":10,"observed_cells":8,"valid_cells":7,"valid_coverage":0.7,"platform_errors":1,"platform_error_rate":0.1,"budget_skipped":1,"negative_controls_pass":false,"metrics":{"quality":{"count":7,"mean":8,"median":8,"min":6,"max":9,"stddev":1,"p05":6,"p25":7,"p75":9,"p95":9}},"metric_directions":{"quality":"higher"},"cost_usd":{"count":1,"mean":1,"median":1,"min":1,"max":1,"stddev":0,"p05":1,"p25":1,"p75":1,"p95":1},"latency_seconds":{"count":1,"mean":12,"median":12,"min":12,"max":12,"stddev":0,"p05":12,"p25":12,"p75":12,"p95":12},"tokens":{"count":1,"mean":100,"median":100,"min":100,"max":100,"stddev":0,"p05":100,"p25":100,"p75":100,"p95":100},"human_interventions":{"count":1,"mean":1,"median":1,"min":1,"max":1,"stddev":0,"p05":1,"p25":1,"p75":1,"p95":1}}},"comparisons":{"candidate":{"quality":{"variant":"candidate","control":"control","metric":"quality","direction":"higher","paired_count":4,"wins":3,"ties":0,"losses":1,"mean_delta":0.5,"confidence_low":-0.2,"confidence_high":1.1,"recommendation":"insufficient_evidence","failed_conditions":["coverage"]}}},"cells":[{"id":"9007199254741001","variant":"candidate","fixture":"normal","role":"normal","repetition":1,"status":"valid_measurement","cost_usd":1,"latency":12000000000,"input_tokens":80,"output_tokens":20,"human_interventions":1,"measurements":[]}]}"""
                    |> Result.map
                        (\scorecard ->
                            { id = scorecard.experimentId
                            , cells = List.length scorecard.cells
                            , variants = List.length scorecard.variants
                            , comparisons = List.length scorecard.comparisons
                            }
                        )
                    |> Expect.equal
                        (Ok
                            { id = "9007199254740999"
                            , cells = 1
                            , variants = 1
                            , comparisons = 1
                            }
                        )
        ]
