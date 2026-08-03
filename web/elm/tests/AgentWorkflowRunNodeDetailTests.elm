module AgentWorkflowRunNodeDetailTests exposing (all)

import AgentWorkflowRun.NodeDetail as NodeDetail
import Expect
import Html
import Html.Attributes
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, text)


all : Test
all =
    describe "run node detail"
        [ test "shows attempts and effective state for an agent node" <|
            \_ ->
                render agentDetail
                    |> Query.find [ class "agent-node-detail-attempts" ]
                    |> Expect.all
                        [ Query.has [ text "attempt 2" ]
                        , Query.has [ text "succeeded" ]
                        ]
        , test "keeps every attempt rather than summarizing the recovery away" <|
            \_ ->
                render agentDetail
                    |> Query.findAll [ class "agent-node-detail-attempt" ]
                    |> Query.count (Expect.equal 2)
        , test "reports the recovery attempt's own failure, not just the outcome" <|
            \_ ->
                render agentDetail
                    |> Query.find [ class "agent-node-detail-attempts" ]
                    |> Query.has [ text "failed" ]
        , test "shows the human question and resolution audit for a wait" <|
            \_ ->
                render waitDetail
                    |> Query.find [ class "agent-node-detail-wait" ]
                    |> Expect.all
                        [ Query.has [ text "Approve this change?" ]
                        , Query.has [ text "resolved by alice" ]
                        ]
        , test "says so when a wait was resolved with no attributable human" <|
            \_ ->
                render unattributedWaitDetail
                    |> Query.find [ class "agent-node-detail-wait" ]
                    |> Query.has [ text "resolved without an attributable human" ]
        , test "shows the publish result for a publish node" <|
            \_ ->
                render publishDetail
                    |> Query.find [ class "agent-node-detail-publication" ]
                    |> Query.has [ text "succeeded" ]
        , test "omits the publish section entirely for a node that publishes nothing" <|
            \_ ->
                render agentDetail
                    |> Query.hasNot [ class "agent-node-detail-publication" ]
        , test "shows sealed outputs and cost" <|
            \_ ->
                render agentDetail
                    |> Expect.all
                        [ Query.has [ class "agent-node-detail-outputs" ]
                        , Query.has [ text "$1.25" ]
                        ]
        , test "links node input and output evidence through the registered snapshot route" <|
            \_ ->
                render agentDetail
                    |> Query.has
                        [ attribute (Html.Attributes.href "/agent/snapshots/9007199254740993")
                        , attribute (Html.Attributes.href "/agent/snapshots/9007199254740995")
                        ]
        , test "links a human answer through the registered snapshot route" <|
            \_ ->
                render waitDetail
                    |> Query.has
                        [ attribute (Html.Attributes.href "/agent/snapshots/9007199254740997") ]
        , test "renders a whole-dollar cost with its cents rather than as a bare integer" <|
            \_ ->
                render wholeDollarDetail
                    |> Query.find [ class "agent-node-detail-cost" ]
                    |> Query.has [ text "$3.00" ]
        , test "carries the relocated repository-change projection under its output" <|
            \_ ->
                render agentDetail
                    |> Query.find [ class "agent-node-detail-outputs" ]
                    |> Query.has [ class "relocated-projection" ]
        , test "carries the relocated transcript disclosure" <|
            \_ ->
                render agentDetail
                    |> Query.find [ class "agent-node-detail-transcripts" ]
                    |> Query.has [ class "relocated-transcript" ]
        , test "says a node was never reached rather than showing an empty attempt list" <|
            \_ ->
                render unreachedDetail
                    |> Query.find [ class "agent-node-detail-attempts" ]
                    |> Query.has [ text "never reached" ]
        , test "prompts an empty selection rather than rendering a blank pane" <|
            \_ ->
                NodeDetail.view Nothing
                    |> Query.fromHtml
                    |> Query.find [ class "agent-node-detail-empty" ]
                    |> Query.has [ text "Select a node" ]
        ]


render : NodeDetail.Detail msg -> Query.Single msg
render detail =
    NodeDetail.view (Just detail) |> Query.fromHtml


emptyDetail : NodeDetail.Detail msg
emptyDetail =
    { nodeId = "implement"
    , kind = "agent"
    , displayName = "implement"
    , optional = False
    , decorations = []
    , attempts = []
    , inputs = []
    , outputs = []
    , waits = []
    , publication = Nothing
    , transcripts = []
    , turns = 0
    , tokens = 0
    }


agentDetail : NodeDetail.Detail msg
agentDetail =
    { emptyDetail
        | attempts =
            [ { attempt = 1
              , retryAttempt = 1
              , status = "failed"
              , startedAt = Just "2026-08-01T12:00:00Z"
              , completedAt = Just "2026-08-01T12:01:00Z"
              , durationSeconds = 60
              , costUsd = 0.25
              }
            , { attempt = 2
              , retryAttempt = 1
              , status = "succeeded"
              , startedAt = Just "2026-08-01T12:02:00Z"
              , completedAt = Just "2026-08-01T12:04:00Z"
              , durationSeconds = 120
              , costUsd = 1.0
              }
            ]
        , inputs =
            [ { portName = "repository", snapshotId = "9007199254740993", typeRef = "repository/v1" } ]
        , outputs =
            [ { portName = "draft"
              , snapshotId = "9007199254740995"
              , typeRef = "repository-change/v1"
              , contentState = "sealed"
              , projection = Just (Html.div [ Html.Attributes.class "relocated-projection" ] [])
              }
            ]
        , transcripts = [ Html.div [ Html.Attributes.class "relocated-transcript" ] [] ]
        , turns = 7
        , tokens = 4096
    }


wholeDollarDetail : NodeDetail.Detail msg
wholeDollarDetail =
    { emptyDetail
        | attempts =
            [ { attempt = 1
              , retryAttempt = 1
              , status = "succeeded"
              , startedAt = Nothing
              , completedAt = Nothing
              , durationSeconds = 90
              , costUsd = 3.0
              }
            ]
    }


unreachedDetail : NodeDetail.Detail msg
unreachedDetail =
    emptyDetail


waitDetail : NodeDetail.Detail msg
waitDetail =
    { emptyDetail
        | nodeId = "approve"
        , kind = "await"
        , waits =
            [ { questionName = "approval"
              , prompt = "Approve this change?"
              , context = "the change touches production configuration"
              , status = "resolved"
              , expectedType = "decision/v1"
              , deadline = "2026-08-02T12:00:00Z"
              , answerSnapshotId = Just "9007199254740997"
              , resolvedByDisplayName = "alice"
              , resolution = Nothing
              }
            ]
    }


unattributedWaitDetail : NodeDetail.Detail msg
unattributedWaitDetail =
    let
        base =
            waitDetail
    in
    { base
        | waits =
            List.map (\wait -> { wait | resolvedByDisplayName = "" }) base.waits
    }


publishDetail : NodeDetail.Detail msg
publishDetail =
    { emptyDetail
        | nodeId = "publish"
        , kind = "publish"
        , publication = Just { status = "succeeded", reference = "github.com/example/repo#42" }
    }
