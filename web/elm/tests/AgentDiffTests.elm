module AgentDiffTests exposing (all)

import Concourse.AgentDiff as AgentDiff exposing (LineKind(..))
import Expect
import Json.Decode
import Test exposing (Test, describe, test)


all : Test
all =
    describe "Concourse.AgentDiff"
        [ describe "classifyLine"
            [ test "classifies an addition" <|
                \_ -> AgentDiff.classifyLine "+new code" |> Expect.equal Addition
            , test "classifies a deletion" <|
                \_ -> AgentDiff.classifyLine "-old code" |> Expect.equal Deletion
            , test "treats +++ as meta, not addition" <|
                \_ -> AgentDiff.classifyLine "+++ b/foo.go" |> Expect.equal Meta
            , test "treats --- as meta, not deletion" <|
                \_ -> AgentDiff.classifyLine "--- a/foo.go" |> Expect.equal Meta
            , test "classifies a hunk header" <|
                \_ -> AgentDiff.classifyLine "@@ -1,3 +1,4 @@ func x()" |> Expect.equal HunkHeader
            , test "classifies a context line" <|
                \_ -> AgentDiff.classifyLine " unchanged" |> Expect.equal Context
            ]
        , describe "decodeDiffPage"
            [ test "decodes the DiffPage wire shape" <|
                \_ ->
                    let
                        json =
                            """
                            { "files": [ { "path": "atc/foo.go", "patch": "@@ -1 +1 @@\\n-old\\n+new\\n", "truncated": true } ]
                            , "offset": 0, "limit": 50, "total_files": 1, "has_more": false }
                            """
                    in
                    Json.Decode.decodeString AgentDiff.decodeDiffPage json
                        |> Result.map (\p -> ( List.length p.files, p.totalFiles, List.head p.files |> Maybe.map .truncated ))
                        |> Expect.equal (Ok ( 1, 1, Just True ))
            , test "tolerates a missing files field" <|
                \_ ->
                    Json.Decode.decodeString AgentDiff.decodeDiffPage "{}"
                        |> Result.map (\p -> List.length p.files)
                        |> Expect.equal (Ok 0)
            ]
        ]
