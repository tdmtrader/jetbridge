module SnapshotDecoderTests exposing (all)

import Concourse.Snapshot as Snapshot
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


manifestJson : String
manifestJson =
    """
    { "id": "9007199254740993"
    , "type": "repository/v1"
    , "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    , "byte_size": 4294967296
    , "file_count": 120000
    , "representation": "application/vnd.concourse.snapshot.tar"
    , "intrinsic_metadata": {"head_sha":"abc"}
    , "content_state": "available"
    , "created_at": "2026-07-22T12:00:00Z"
    }
    """


all : Test
all =
    describe "Concourse.Snapshot"
        [ test "keeps quoted IDs above 2^53 exact" <|
            \_ ->
                Json.Decode.decodeString Snapshot.decodeManifest manifestJson
                    |> Result.map (\manifest -> ( manifest.id, manifest.byteSize, manifest.contentState ))
                    |> Expect.equal (Ok ( "9007199254740993", 4294967296, "available" ))
        , test "rejects a numeric snapshot ID" <|
            \_ ->
                Json.Decode.decodeString Snapshot.decodeManifest
                    (String.replace "\"9007199254740993\"" "9007199254740993" manifestJson)
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "rejects noncanonical quoted IDs" <|
            \_ ->
                Json.Decode.decodeString Snapshot.decodeRef
                    """{"id":"01","type":"review/v1","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        , test "decodes team-scoped detail and lineage summaries" <|
            \_ ->
                Json.Decode.decodeString Snapshot.decodeDetail
                    ("""{"manifest":""" ++ manifestJson ++ """, "replica_count":2, "retention_claims":[{"id":"9007199254740995","snapshot_id":"9007199254740993","class":"workflow","reason":"run output","created_at":"2026-07-22T12:00:00Z"}], "productions":[], "downstream":[]}""")
                    |> Result.map
                        (\detail ->
                            ( detail.manifest.id
                            , detail.replicaCount
                            , List.head detail.retentionClaims |> Maybe.map .id
                            )
                        )
                    |> Expect.equal (Ok ( "9007199254740993", 2, Just "9007199254740995" ))
        , test "decodes producer build and workflow navigation identity without losing the run ID" <|
            \_ ->
                Json.Decode.decodeString Snapshot.decodeProduction
                    """{"id":"9007199254740997","kind":"build","created_by":"alice","build":{"build_id":42,"plan_id":"plan-review","attempt":"1","step_kind":"agent","step_name":"review","workflow_definition_id":41,"workflow_run_id":"9007199254740995","workflow_name":"review-api"},"output_port":"review","created_at":"2026-07-22T12:00:00Z","inputs":[]}"""
                    |> Result.map
                        (\production ->
                            ( production.id
                            , production.build
                                |> Maybe.map
                                    (\build ->
                                        ( build.id
                                        , build.workflowRunId
                                        , build.workflowName
                                        )
                                    )
                            )
                        )
                    |> Expect.equal
                        (Ok ( "9007199254740997", Just ( 42, Just "9007199254740995", Just "review-api" ) ))
        , test "rejects numeric retention and production IDs" <|
            \_ ->
                Expect.all
                    [ \_ ->
                        Json.Decode.decodeString Snapshot.decodeRetentionClaim
                            """{"id":7,"snapshot_id":"9","class":"workflow","reason":"run output","created_at":"2026-07-22T12:00:00Z"}"""
                            |> Result.toMaybe
                            |> Expect.equal Nothing
                    , \_ ->
                        Json.Decode.decodeString Snapshot.decodeProduction
                            """{"id":7,"kind":"upload","created_by":"alice","created_at":"2026-07-22T12:00:00Z","inputs":[]}"""
                            |> Result.toMaybe
                            |> Expect.equal Nothing
                    ]
                    ()
        , test "rejects a numeric producer workflow-run ID instead of hiding malformed lineage" <|
            \_ ->
                Json.Decode.decodeString Snapshot.decodeProduction
                    """{"id":"7","kind":"build","created_by":"alice","build":{"build_id":42,"plan_id":"plan-review","attempt":"1","step_kind":"agent","step_name":"review","workflow_definition_id":41,"workflow_run_id":9007199254740995,"workflow_name":"review-api"},"output_port":"review","created_at":"2026-07-22T12:00:00Z","inputs":[]}"""
                    |> Result.toMaybe
                    |> Expect.equal Nothing
        ]
