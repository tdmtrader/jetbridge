module AgentWorkflowRunListTests exposing (all)

import AgentWorkflow.RunList as RunList
import Expect
import Html.Attributes as Attr
import Test exposing (Test, describe, test)
import Test.Html.Query as Query
import Test.Html.Selector exposing (attribute, class, containing, tag, text)
import Time


all : Test
all =
    describe "workflow run list"
        [ describe "row content is deliberately small"
            [ test "shows the identity, timing, and state a row needs before selection" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.find [ class "agent-run-row" ]
                        |> Expect.all
                            [ Query.has [ text "9007199254740995" ]
                            , Query.has [ text "4m 0s" ]
                            , Query.has [ text "succeeded" ]
                            ]
            , test "keeps cost off the row, where selection is supposed to answer it" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.find [ class "agent-run-row" ]
                        |> Query.hasNot [ text "$" ]
            , test "clicking a run navigates straight to the run page" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.find [ class "agent-run-row" ]
                        |> Query.find [ tag "a" ]
                        |> Query.has
                            [ attribute
                                (Attr.href "/agent/workflows/review-api/runs/9007199254740995")
                            ]
            , test "shows how long ago a run started" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.find [ class "agent-run-row-time" ]
                        |> Query.has [ text "10m ago" ]
            , test "times a still-running run against now rather than leaving it blank" <|
                \_ ->
                    render [ waitingRow ]
                        |> Query.find [ class "agent-run-row-duration" ]
                        |> Query.has [ text "10m 0s" ]
            , test "never reports a negative elapsed time before the clock has ticked" <|
                \_ ->
                    RunList.view { now = Time.millisToPosix 0, emptyMessage = "No runs" }
                        [ waitingRow ]
                        |> Query.fromHtml
                        |> Query.find [ class "agent-run-row-duration" ]
                        |> Query.has [ text "0s" ]
            , test "says so plainly when a run never started" <|
                \_ ->
                    render [ { succeededRow | startedAt = Nothing, completedAt = Nothing } ]
                        |> Query.find [ class "agent-run-row-duration" ]
                        |> Query.has [ text "not started" ]
            ]
        , describe "attention, tickets, and revisions"
            [ test "shows an attention cue for a waiting run" <|
                \_ ->
                    render [ waitingRow ]
                        |> Query.find [ class "agent-run-row-attention" ]
                        |> Query.has [ text "waiting at approval" ]
            , test "omits the attention element entirely when there is nothing to say" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.hasNot [ class "agent-run-row-attention" ]
            , test "shows the ticket reference when one is associated" <|
                \_ ->
                    render [ { succeededRow | ticketReference = "ticket-42" } ]
                        |> Query.find [ class "agent-run-row-ticket" ]
                        |> Query.has [ text "ticket-42" ]
            , test "omits the ticket element when no ticket is associated" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.hasNot [ class "agent-run-row-ticket" ]
            , test "marks a revision boundary only on the row where the revision changes" <|
                \_ ->
                    render
                        [ { succeededRow | workflowVersion = 4 }
                        , succeededRow
                        , { succeededRow | id = "3" }
                        ]
                        |> Query.findAll [ class "agent-run-row-revision-boundary" ]
                        |> Query.count (Expect.equal 2)
            , test "names the revision it is crossing into" <|
                \_ ->
                    render [ { succeededRow | workflowVersion = 4 }, succeededRow ]
                        |> Query.findAll [ class "agent-run-row-revision-boundary" ]
                        |> Query.index 1
                        |> Query.has [ text "v3" ]
            , test "the very first row is always a boundary, because it opens a revision" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.findAll [ class "agent-run-row-revision-boundary" ]
                        |> Query.count (Expect.equal 1)
            ]
        , describe "state language"
            [ test "spells the state as a word beside the glyph, never colour alone" <|
                \_ ->
                    [ "succeeded", "failed", "errored", "aborted", "running", "admitting" ]
                        |> List.map
                            (\status _ ->
                                render [ { succeededRow | status = status } ]
                                    |> Query.find [ class "agent-run-row-status" ]
                                    |> Query.has [ text status ]
                            )
                        |> (\assertions -> Expect.all assertions ())
            , test "gives a terminal failure a distinct glyph from a success" <|
                \_ ->
                    render [ { succeededRow | status = "failed" } ]
                        |> Query.find [ class "agent-run-row-status" ]
                        |> Query.has [ text "✕ failed" ]
            , test "carries an accessible label naming the run and its state" <|
                \_ ->
                    render [ succeededRow ]
                        |> Query.find [ class "agent-run-row" ]
                        |> Query.find [ tag "a" ]
                        |> Query.has
                            [ attribute
                                (Attr.attribute "aria-label"
                                    "run 9007199254740995, succeeded, 10m ago"
                                )
                            ]
            ]
        , describe "empty states"
            [ test "renders the caller's empty message rather than a bare list" <|
                \_ ->
                    RunList.view
                        { now = now, emptyMessage = "No runs need attention in this window" }
                        []
                        |> Query.fromHtml
                        |> Expect.all
                            [ Query.has [ class "agent-run-list-empty" ]
                            , Query.has [ text "No runs need attention in this window" ]
                            ]
            , test "renders no list at all when there is nothing to list" <|
                \_ ->
                    RunList.view { now = now, emptyMessage = "No runs" } []
                        |> Query.fromHtml
                        |> Query.hasNot [ class "agent-run-list" ]
            ]
        ]


now : Time.Posix
now =
    Time.millisToPosix 1785501296000


succeededRow : RunList.Row
succeededRow =
    { id = "9007199254740995"
    , url = "/agent/workflows/review-api/runs/9007199254740995"
    , status = "succeeded"
    , workflowVersion = 3
    , startedAt = Just (Time.millisToPosix (1785501296000 - 600000))
    , completedAt = Just (Time.millisToPosix (1785501296000 - 360000))
    , ticketReference = ""
    , attentionCue = ""
    }


waitingRow : RunList.Row
waitingRow =
    { succeededRow
        | status = "running"
        , attentionCue = "waiting at approval"
        , completedAt = Nothing
    }


render : List RunList.Row -> Query.Single msg
render rows =
    RunList.view { now = now, emptyMessage = "No runs" } rows |> Query.fromHtml
