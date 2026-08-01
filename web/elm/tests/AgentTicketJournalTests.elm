module AgentTicketJournalTests exposing (all)

import AgentTicket.Journal as Journal
import Expect
import Html.Attributes as Attr
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, tag, text)
import Time


now : Time.Posix
now = Time.millisToPosix 3600000


at : Int -> Time.Posix
at minutes = Time.millisToPosix (minutes * 60000)


entry : String -> String -> Journal.Entry
entry id workflowName =
    { id = id
    , url = "/agent/workflows/" ++ workflowName ++ "/runs/" ++ id
    , workflowName = workflowName
    , workflowVersion = 3
    , status = "succeeded"
    , createdAt = Just (at 10)
    , startedAt = Just (at 10)
    , completedAt = Just (at 12)
    , retryOf = Nothing
    , outcome = ""
    , outstandingAction = ""
    }


{-| One ticket, four occurrences across three workflows, with `small-fix` run
twice — the case the whole surface exists for.
-}
entries : List Journal.Entry
entries =
    [ entry "1" "small-fix"
    , { firstPr | status = "running", createdAt = Just (at 20), startedAt = Just (at 20), completedAt = Nothing, outstandingAction = "waiting at approval" }
    , entry "3" "qa"
    , { retriedFix | createdAt = Just (at 40), startedAt = Just (at 40), completedAt = Just (at 45) }
    ]


firstPr : Journal.Entry
firstPr = entry "2" "pr-create"


retriedFix : Journal.Entry
retriedFix =
    let
        base = entry "4" "small-fix"
    in
    { base | retryOf = Just "1" }


render : List Journal.Entry -> Query.Single msg
render given =
    Journal.view { now = now, emptyMessage = "No runs yet" } given
        |> Query.fromHtml


all : Test
all =
    describe "ticket journal"
        [ test "lists every run occurrence in chronological order" <|
            \_ ->
                render entries
                    |> Query.findAll [ class "agent-journal-entry" ]
                    |> Query.count (Expect.equal 4)
        , test "keeps repeated executions of one workflow as separate entries" <|
            \_ ->
                render entries
                    |> Query.findAll [ class "agent-journal-entry-workflow" ]
                    |> Query.index 3
                    |> Query.has [ text "small-fix" ]
        , test "preserves the order it was given rather than grouping by workflow" <|
            \_ ->
                render entries
                    |> Query.findAll [ class "agent-journal-entry-workflow" ]
                    |> Expect.all
                        [ Query.index 0 >> Query.has [ text "small-fix" ]
                        , Query.index 1 >> Query.has [ text "pr-create" ]
                        , Query.index 2 >> Query.has [ text "qa" ]
                        , Query.index 3 >> Query.has [ text "small-fix" ]
                        ]
        , test "elevates an outstanding action" <|
            \_ ->
                render entries
                    |> Query.find [ class "agent-journal-entry--outstanding" ]
                    |> Query.has [ text "waiting at approval" ]
        , test "leaves a completed entry unelevated" <|
            \_ ->
                render [ entry "1" "small-fix" ]
                    |> Query.findAll [ class "agent-journal-entry--outstanding" ]
                    |> Query.count (Expect.equal 0)
        , test "groups a retry with its source" <|
            \_ ->
                render entries
                    |> Query.find [ class "agent-journal-entry--retry" ]
                    |> Query.has [ text "retry of 1" ]
        , test "leaves an ordinary entry unmarked as a retry" <|
            \_ ->
                render [ entry "1" "small-fix" ]
                    |> Query.findAll [ class "agent-journal-entry--retry" ]
                    |> Query.count (Expect.equal 0)
        , test "draws no causal edges between entries" <|
            \_ ->
                render entries
                    |> Query.hasNot [ class "agent-journal-edge" ]
        , test "renders no svg: the journal is a list, not a graph" <|
            \_ ->
                render entries
                    |> Query.findAll [ tag "svg" ]
                    |> Query.count (Expect.equal 0)
        , test "shows an empty state for a ticket with no runs" <|
            \_ ->
                -- The empty state IS the root node here, so it is asserted on
                -- the root rather than searched for among descendants.
                render []
                    |> Query.has [ class "agent-journal-empty", text "No runs yet" ]
        , test "shows no entries at all when there are none" <|
            \_ ->
                render []
                    |> Query.findAll [ class "agent-journal-entry" ]
                    |> Query.count (Expect.equal 0)
        , test "reports the exact revision each occurrence ran" <|
            \_ ->
                render [ { firstPr | workflowVersion = 11 } ]
                    |> Query.find [ class "agent-journal-entry-revision" ]
                    |> Query.has [ text "v11" ]
        , test "links each entry straight to its own run" <|
            \_ ->
                render [ entry "9007199254740995" "small-fix" ]
                    |> Query.find [ tag "a" ]
                    |> Query.has
                        [ attribute
                            (Attr.href "/agent/workflows/small-fix/runs/9007199254740995")
                        ]
        , test "carries run ids as strings so large identities survive" <|
            \_ ->
                render [ entry "9007199254740995" "small-fix" ]
                    |> Query.find [ class "agent-journal-entry-id" ]
                    |> Query.has [ text "9007199254740995" ]
        , test "says how long ago an occurrence happened" <|
            \_ ->
                render [ entry "1" "small-fix" ]
                    |> Query.find [ class "agent-journal-entry-time" ]
                    |> Query.has [ text "50m ago" ]
        , test "times a still-running occurrence against now rather than leaving it blank" <|
            \_ ->
                render
                    [ { firstPr
                        | status = "running"
                        , startedAt = Just (at 20)
                        , completedAt = Nothing
                      }
                    ]
                    |> Query.find [ class "agent-journal-entry-duration" ]
                    |> Query.has [ text "40m 0s" ]
        , test "says so plainly when an occurrence never started" <|
            \_ ->
                render [ { firstPr | startedAt = Nothing, completedAt = Nothing } ]
                    |> Query.find [ class "agent-journal-entry-duration" ]
                    |> Query.has [ text "not started" ]
        , test "states the status in words, not only in colour" <|
            \_ ->
                render [ { firstPr | status = "failed" } ]
                    |> Query.find [ class "agent-journal-entry-status" ]
                    |> Query.has [ text "failed" ]
        ]
