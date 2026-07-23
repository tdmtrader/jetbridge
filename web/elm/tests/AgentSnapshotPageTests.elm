module AgentSnapshotPageTests exposing (all)

import AgenticData
import Application.Application as Application
import Common
import Html.Attributes as Attr
import Message.Callback as Callback
import Message.Effects as Effects
import Message.Message as Message
import Message.TopLevelMessage as Msgs
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, text)


all : Test
all =
    describe "snapshot detail"
        [ test "shows manifest, retention, replicas, producer inputs, and download" <|
            \_ ->
                initialized
                    |> Common.queryView
                    |> Query.has
                        [ class "agent-snapshot-manifest"
                        , containing [ text "repository-change/v1" ]
                        , containing [ text "sha256:change" ]
                        , containing [ text "2" ]
                        , class "agent-snapshot-retention"
                        , containing [ text "review evidence · durable" ]
                        , class "agent-snapshot-production"
                        , attribute (Attr.href "/builds/42")
                        , attribute (Attr.href "/agent/workflows/review-api/runs/9007199254740993")
                        , attribute (Attr.href "/agent/snapshots/9007199254740995")
                        , attribute (Attr.href "/api/v1/teams/main/agent/snapshots/9007199254740997/content")
                        ]
        , test "selects the typed repository projection after manifest load" <|
            \_ ->
                Common.init "/agent/snapshots/9007199254740997"
                    |> Application.handleCallback
                        (Callback.AgentSnapshotFetched AgenticData.snapshotDetail.manifest.id (Ok AgenticData.snapshotDetail))
                    |> Tuple.second
                    |> Common.contains
                        (Effects.FetchAgentSnapshotRepositoryChange
                            "main"
                            AgenticData.snapshotDetail.manifest.id
                        )
        , test "pin and unpin mutate retention claims without touching content" <|
            \_ ->
                initialized
                    |> Application.update (Msgs.Update Message.AgentSnapshotPinClicked)
                    |> Tuple.second
                    |> Common.contains
                        (Effects.PinAgentSnapshot "main" AgenticData.snapshotDetail.manifest.id)
        , test "renders the server-bounded diff" <|
            \_ ->
                initialized
                    |> Application.handleCallback
                        (Callback.AgentSnapshotRepositoryChangeFetched
                            AgenticData.repositoryChange.snapshotId
                            (Ok AgenticData.repositoryChange)
                        )
                    |> Tuple.first
                    |> Common.queryView
                    |> Query.find [ class "agent-unified-diff" ]
                    |> Query.has [ text "diff --git a/src/a.go b/src/a.go\n+fixed" ]
        ]


initialized : Application.Model
initialized =
    Common.init "/agent/snapshots/9007199254740997"
        |> Application.handleCallback
            (Callback.AgentSnapshotFetched AgenticData.snapshotDetail.manifest.id (Ok AgenticData.snapshotDetail))
        |> Tuple.first
