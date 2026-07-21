module AgentTickets.StepDag exposing
    ( Attempt
    , BoxKind(..)
    , StepBox
    , attempts
    , view
    )

{-| The ticket-page step DAG (audit Proposal A / S-1). Composes the per-step
run metrics of one ticket into a per-attempt (per-build) horizontal chain of
pipeline-grammar boxes — ticket → agent steps → gate/judge/push (expanded from
the harvest row's results metadata) → review/merge (from the ticket state, on
the latest attempt only). Boxes are colored by the shared AgentBadge status
palette (the classic pipeline graph is a JS render port, so its Elm-reusable
grammar is the badge/step-tree status color language, reused here via
AgentBadge.tone/toneColor).
-}

import AgentBadge
import Concourse.Agent as Agent
import Dict exposing (Dict)
import Html exposing (Html)
import Html.Attributes exposing (class, href, id, style, title)
import Routes


type BoxKind
    = TicketBox
    | AgentStep
    | GateBox
    | JudgeBox
    | PushBox
    | ReviewBox
    | MergeBox


type alias StepBox =
    { label : String
    , kind : BoxKind
    , tone : AgentBadge.Tone
    , warn : Bool
    , costUsd : Float
    , durationSeconds : Int
    , buildId : Int
    }


type alias Attempt =
    { buildId : Int
    , index : Int
    , outcome : Maybe AgentBadge.Status
    , costUsd : Float
    , createdAt : Int
    , boxes : List StepBox
    }


{-| Group the ticket's builds into attempts. `byBuild` is keyed by build id
with each build's rows already in created_at ASC order (as
AgentTicket.groupMetricsByBuild produces). Ascending build id = chronological
attempt order; the last attempt is the live one and receives the terminal
review/merge boxes derived from the ticket state.
-}
attempts : String -> Dict Int (List Agent.RunMetric) -> List Attempt
attempts ticketState byBuild =
    let
        builds =
            Dict.toList byBuild

        count =
            List.length builds
    in
    builds
        |> List.indexedMap
            (\i ( buildId, rows ) ->
                buildAttempt ticketState (i + 1) (i == count - 1) buildId rows
            )


buildAttempt : String -> Int -> Bool -> Int -> List Agent.RunMetric -> Attempt
buildAttempt ticketState index isLatest buildId rows =
    let
        cost =
            rows |> List.map .costUsd |> List.sum

        createdAt =
            rows |> List.map .createdAt |> List.minimum |> Maybe.withDefault 0

        ticketBox =
            { label = "ticket", kind = TicketBox, tone = AgentBadge.Info, warn = False, costUsd = 0, durationSeconds = 0, buildId = buildId }

        stepBoxes =
            List.concatMap (stepBoxesFor buildId) rows

        tail =
            if isLatest then
                terminalBoxes ticketState buildId

            else
                []
    in
    { buildId = buildId
    , index = index
    , outcome = attemptOutcome rows
    , costUsd = cost
    , createdAt = createdAt
    , boxes = ticketBox :: stepBoxes ++ tail
    }


{-| The harvest step (plan Name "harvest", agent/dispatch/render.go) expands
into its gate/judge/push facts; every other step is a single agent box.
-}
stepBoxesFor : Int -> Agent.RunMetric -> List StepBox
stepBoxesFor buildId rm =
    if rm.stepName == "harvest" then
        harvestBoxes buildId rm

    else
        [ agentStepBox buildId rm ]


agentStepBox : Int -> Agent.RunMetric -> StepBox
agentStepBox buildId rm =
    let
        status =
            AgentBadge.displayOutcome
                { outcome = rm.outcome
                , buildStatus = rm.buildStatus
                , runStatus = rm.status
                , hasResult = rm.summary /= ""
                }

        disp =
            boxDisplay status
    in
    { label = rm.stepName
    , kind = AgentStep
    , tone = disp.tone
    , warn = disp.warn
    , costUsd = rm.costUsd
    , durationSeconds = rm.wallTimeSeconds
    , buildId = buildId
    }


{-| Tone + warn for an agent step's fused status. NoOutput — a green build that
delivered nothing — is rendered as a green (GoodMuted) box with a ⚠, honoring
the audit rule that a recording gap is a warning, never a red failure.

-- L-1 coupling: when AgentBadge gains a delivered-unrecorded/incomplete amber
status token, add a case here mapping it to { tone = GoodMuted, warn = True }.
-}
boxDisplay : Maybe AgentBadge.Status -> { tone : AgentBadge.Tone, warn : Bool }
boxDisplay status =
    case status of
        Just AgentBadge.NoOutput ->
            { tone = AgentBadge.GoodMuted, warn = True }

        Just s ->
            { tone = AgentBadge.tone s, warn = False }

        Nothing ->
            { tone = AgentBadge.Neutral, warn = False }


harvestBoxes : Int -> Agent.RunMetric -> List StepBox
harvestBoxes buildId rm =
    let
        gateBoxes =
            List.map (gateBox buildId) rm.results.gates

        judgeBoxes =
            case rm.results.judge of
                Just j ->
                    [ judgeBox buildId rm.costUsd j ]

                Nothing ->
                    []

        pushBoxes =
            if rm.results.pushedBranch /= "" then
                [ { label = "push", kind = PushBox, tone = AgentBadge.Good, warn = False, costUsd = 0, durationSeconds = 0, buildId = buildId } ]

            else
                []
    in
    gateBoxes ++ judgeBoxes ++ pushBoxes


gateBox : Int -> Agent.GateResult -> StepBox
gateBox buildId g =
    let
        ( tone, warn ) =
            case g.status of
                "ok" ->
                    if g.flaky then
                        ( AgentBadge.GoodMuted, True )

                    else
                        ( AgentBadge.Good, False )

                "failed" ->
                    ( AgentBadge.Bad, False )

                "error" ->
                    ( AgentBadge.Error, False )

                _ ->
                    ( AgentBadge.Neutral, False )
    in
    { label = "gate: " ++ g.gate
    , kind = GateBox
    , tone = tone
    , warn = warn
    , costUsd = 0
    , durationSeconds = round g.durationSeconds
    , buildId = buildId
    }


{-| The judge box carries the harvest row's cost — the judge LLM call is the
priced work in the harvest step; the gate/push boxes are unpriced shell work.
-}
judgeBox : Int -> Float -> Agent.JudgeResult -> StepBox
judgeBox buildId cost j =
    { label = "judge " ++ scoreText j.total ++ "/" ++ scoreText j.maxTotal
    , kind = JudgeBox
    , tone =
        if j.pass then
            AgentBadge.Good

        else
            AgentBadge.Bad
    , warn = False
    , costUsd = cost
    , durationSeconds = 0
    , buildId = buildId
    }


{-| Terminal ticket-lifecycle boxes appended to the LATEST attempt only.
Mirrors the human-visible endpoints of the ticket state machine.

-- W-2 coupling: attempt numbering here (1..N by build id) is the same numbering
W-2 introduces for the run-row identity; share one helper once W-2 lands.
-}
terminalBoxes : String -> Int -> List StepBox
terminalBoxes ticketState buildId =
    let
        box label kind tone =
            { label = label, kind = kind, tone = tone, warn = False, costUsd = 0, durationSeconds = 0, buildId = buildId }
    in
    case ticketState of
        "needs_review" ->
            [ box "review" ReviewBox AgentBadge.Attention ]

        "merged" ->
            [ box "review" ReviewBox AgentBadge.Good, box "merge" MergeBox AgentBadge.Good ]

        "merged_with_fixes" ->
            [ box "review" ReviewBox AgentBadge.Good, box "merge" MergeBox AgentBadge.GoodMuted ]

        "concluded" ->
            [ box "review" ReviewBox AgentBadge.Calm, box "concluded" MergeBox AgentBadge.Calm ]

        "abandoned" ->
            [ box "abandoned" MergeBox AgentBadge.Neutral ]

        _ ->
            []


{-| The attempt-level verdict: the same worst-truth-wins fusion the run rows
use (parked-anywhere, else the last step's status, joined with the build
status and whether anything was delivered). Mirrors AgentTicket.runRow.
-}
attemptOutcome : List Agent.RunMetric -> Maybe AgentBadge.Status
attemptOutcome rows =
    let
        runStatus =
            case List.filter (\m -> m.status == "parked") rows of
                parked :: _ ->
                    parked.status

                [] ->
                    rows |> List.reverse |> List.head |> Maybe.map .status |> Maybe.withDefault ""

        buildStatus =
            rows |> List.head |> Maybe.map .buildStatus |> Maybe.withDefault ""

        hasResult =
            (rows |> List.filterMap (\m -> nonEmpty m.summary) |> List.reverse |> List.head |> Maybe.withDefault "") /= ""
    in
    AgentBadge.runOutcome { buildStatus = buildStatus, runStatus = runStatus, hasResult = hasResult }


nonEmpty : String -> Maybe String
nonEmpty s =
    if s == "" then
        Nothing

    else
        Just s


scoreText : Float -> String
scoreText f =
    let
        n =
            round f
    in
    if toFloat n == f then
        String.fromInt n

    else
        String.fromFloat f


{-| Render the ticket's attempts, newest first, each as a labeled row with a
horizontal chain of connector-separated step boxes.
-}
view : String -> Dict Int (List Agent.RunMetric) -> Html msg
view ticketState byBuild =
    case attempts ticketState byBuild of
        [] ->
            Html.text ""

        atts ->
            Html.div
                [ id "ticket-step-dag", style "margin" "12px 0" ]
                (sectionLabel "attempts"
                    :: (atts |> List.reverse |> List.map attemptView)
                )


attemptView : Attempt -> Html msg
attemptView att =
    Html.div
        [ class "agent-attempt"
        , style "margin" "10px 0"
        , style "border" "1px solid #2a2929"
        , style "padding" "8px"
        ]
        [ attemptHeader att
        , Html.div
            [ class "agent-step-dag-row"
            , style "display" "flex"
            , style "flex-wrap" "wrap"
            , style "align-items" "stretch"
            , style "gap" "0"
            , style "margin-top" "6px"
            ]
            (att.boxes
                |> List.indexedMap
                    (\i b ->
                        if i == 0 then
                            [ boxView b ]

                        else
                            [ connector, boxView b ]
                    )
                |> List.concat
            )
        ]


attemptHeader : Attempt -> Html msg
attemptHeader att =
    Html.div
        [ style "display" "flex", style "align-items" "center", style "gap" "8px", style "font-size" "12px" ]
        [ Html.span [ style "color" "#d0d0d0" ] [ Html.text ("attempt " ++ String.fromInt att.index) ]
        , case att.outcome of
            Just s ->
                AgentBadge.view s

            Nothing ->
                Html.text ""
        , Html.a
            [ href (buildHref att.buildId)
            , style "font-family" "monospace"
            , style "color" "#7aa37a"
            , style "text-decoration" "none"
            ]
            [ Html.text ("build " ++ String.fromInt att.buildId) ]
        , Html.span [ style "flex" "1" ] []
        , Html.span [ style "font-family" "monospace", style "color" "#b0b0b0" ] [ Html.text ("$" ++ usd att.costUsd) ]
        ]


boxView : StepBox -> Html msg
boxView b =
    let
        color =
            AgentBadge.toneColor b.tone
    in
    Html.a
        [ class "agent-step-box"
        , href (buildHref b.buildId)
        , title b.label
        , style "display" "inline-flex"
        , style "flex-direction" "column"
        , style "justify-content" "center"
        , style "border" ("1px solid " ++ color)
        , style "border-left" ("3px solid " ++ color)
        , style "background" "#1b1b1b"
        , style "padding" "4px 8px"
        , style "text-decoration" "none"
        ]
        [ Html.span
            [ style "display" "flex", style "align-items" "center", style "gap" "4px" ]
            ((if b.warn then
                [ Html.span
                    [ class "agent-step-box-warn"
                    , title "recording incomplete — delivered, but the run record is partial"
                    , style "color" (AgentBadge.toneColor AgentBadge.Attention)
                    ]
                    [ Html.text "⚠" ]
                ]

              else
                []
             )
                ++ [ Html.span [ style "color" color, style "font-size" "12px" ] [ Html.text b.label ] ]
            )
        , boxSublabel b
        ]


boxSublabel : StepBox -> Html msg
boxSublabel b =
    let
        parts =
            List.filterMap identity
                [ if b.costUsd > 0 then
                    Just ("$" ++ usd b.costUsd)

                  else
                    Nothing
                , if b.durationSeconds > 0 then
                    Just (duration b.durationSeconds)

                  else
                    Nothing
                ]
    in
    if List.isEmpty parts then
        Html.text ""

    else
        Html.span
            [ style "font-family" "monospace", style "color" "#7a7a7a", style "font-size" "10px", style "margin-top" "2px" ]
            [ Html.text (String.join " · " parts) ]


connector : Html msg
connector =
    Html.span
        [ style "align-self" "center", style "color" "#5a5a5a", style "padding" "0 4px" ]
        [ Html.text "→" ]


sectionLabel : String -> Html msg
sectionLabel txt =
    Html.div
        [ style "font-size" "11px", style "text-transform" "uppercase", style "letter-spacing" "0.08em", style "color" "#9aa39b", style "margin" "8px 0 4px" ]
        [ Html.text txt ]


buildHref : Int -> String
buildHref buildId =
    Routes.toString (Routes.OneOffBuild { id = buildId, highlight = Routes.HighlightNothing })


{-| Compact duration: "77s" → "1m 17s", "5s" → "5s".
-}
duration : Int -> String
duration secs =
    if secs < 60 then
        String.fromInt secs ++ "s"

    else
        String.fromInt (secs // 60) ++ "m " ++ String.fromInt (modBy 60 secs) ++ "s"


{-| Two-decimal USD, matching AgentTicket.formatUsd.
-}
usd : Float -> String
usd amount =
    let
        cents =
            round (amount * 100)

        absCents =
            abs cents

        dollars =
            absCents // 100

        rem =
            modBy 100 absCents

        frac =
            if rem < 10 then
                "0" ++ String.fromInt rem

            else
                String.fromInt rem
    in
    String.fromInt dollars ++ "." ++ frac
