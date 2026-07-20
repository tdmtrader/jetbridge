module Concourse.AgentEvent exposing
    ( CostRecord
    , EntryBody(..)
    , GateResult
    , JudgeDimension
    , JudgeVerdict
    , StepEnd
    , TimelineEntry
    , Transcript
    , parseTranscript
    )

{-| Pure decoding of a run's flight-events NDJSON (L-3 / #43 read API) into a
typed timeline. One NDJSON line = one `{"ts","event","data"}` envelope. Lines
that are blank or fail to decode are skipped and counted (`Transcript.skipped`)
rather than aborting the whole transcript — the stream is appended live, so a
half-written trailing line during a poll is normal.

Decoders cover the FULL event taxonomy (agent/schema/event.go +
event_payloads.go), not just what the runner emits today, so the viewer lights
up automatically if the runner ever streams per-turn tool/thinking events. Any
event type without a specific decoder becomes `Unknown`, preserving its raw
JSON for the raw-toggle instead of being dropped.
-}

import Json.Decode as D


type alias Transcript =
    { entries : List TimelineEntry
    , skipped : Int
    }


type alias TimelineEntry =
    { seq : Int
    , ts : String
    , eventType : String
    , raw : String
    , body : EntryBody
    }


type EntryBody
    = StepStarted { stepName : String }
    | StepEnded StepEnd
    | CostRecorded CostRecord
    | GateStarted { gate : String, component : String, scope : String }
    | GateResulted GateResult
    | JudgeScored JudgeVerdict
    | Pushed { branch : String, sha : String }
    | HumanAsked { questionId : Int, question : String, options : List String }
    | HumanAnswered { questionId : Int, answer : String, answeredBy : String, timedOut : Bool }
    | CheckpointWaited { questionId : Int, checkpoint : String }
    | CheckpointReleased { questionId : Int, approved : Bool, answeredBy : String }
    | Errored { message : String }
    | ToolCalled { tool : String, input : String }
    | ToolResulted { tool : String, output : String, isError : Bool }
    | ArtifactWritten { path : String, bytes : Int }
    | Thought { summary : String }
    | Unknown


type alias StepEnd =
    { stepName : String
    , status : String
    , summary : String
    , wallTimeSeconds : Int
    , costUsd : Float
    , turns : Int
    }


type alias CostRecord =
    { source : String
    , model : String
    , inputTokens : Int
    , outputTokens : Int
    , turns : Int
    , costUsd : Float
    }


type alias GateResult =
    { gate : String
    , component : String
    , scope : String
    , status : String
    , durationSeconds : Float
    , summary : String
    , flaky : Bool
    }


type alias JudgeDimension =
    { name : String, score : Float, max : Float, rationale : String }


type alias JudgeVerdict =
    { total : Float, maxTotal : Float, model : String, dimensions : List JudgeDimension }


parseTranscript : String -> Transcript
parseTranscript raw =
    let
        lines =
            String.split "\n" raw
                |> List.map String.trim
                |> List.filter (\l -> l /= "")

        step line ( accEntries, skipped, seq ) =
            case D.decodeString (envelopeDecoder line seq) line of
                Ok entry ->
                    ( entry :: accEntries, skipped, seq + 1 )

                Err _ ->
                    ( accEntries, skipped + 1, seq )

        ( revEntries, totalSkipped, _ ) =
            List.foldl step ( [], 0, 0 ) lines
    in
    { entries = List.reverse revEntries, skipped = totalSkipped }


envelopeDecoder : String -> Int -> D.Decoder TimelineEntry
envelopeDecoder rawLine seq =
    D.map3 (\ts eventType body -> TimelineEntry seq ts eventType rawLine body)
        (D.field "ts" D.string)
        (D.field "event" D.string)
        (D.field "event" D.string |> D.andThen (\t -> D.field "data" (bodyDecoder t)))


bodyDecoder : String -> D.Decoder EntryBody
bodyDecoder eventType =
    let
        f name dec =
            D.field name dec

        s name =
            D.oneOf [ f name D.string, D.succeed "" ]

        i name =
            D.oneOf [ f name D.int, D.succeed 0 ]

        fl name =
            D.oneOf [ f name D.float, D.succeed 0 ]

        b name =
            D.oneOf [ f name D.bool, D.succeed False ]

        list name =
            D.oneOf [ f name (D.list D.string), D.succeed [] ]
    in
    case eventType of
        "step.start" ->
            D.map (\n -> StepStarted { stepName = n }) (s "step_name")

        "step.end" ->
            D.map6 (\n st su w c tu -> StepEnded (StepEnd n st su w c tu))
                (s "step_name")
                (s "status")
                (s "summary")
                (i "wall_time_seconds")
                (fl "cost_usd")
                (i "turns")

        "cost.record" ->
            D.map6 (\src m it ot tu c -> CostRecorded (CostRecord src m it ot tu c))
                (s "source")
                (s "model")
                (i "input_tokens")
                (i "output_tokens")
                (i "turns")
                (fl "cost_usd")

        "gate.start" ->
            D.map3 (\g c sc -> GateStarted { gate = g, component = c, scope = sc })
                (s "gate")
                (s "component")
                (s "scope")

        "gate.result" ->
            D.map7 (\g c sc st dur su fk -> GateResulted (GateResult g c sc st dur su fk))
                (s "gate")
                (s "component")
                (s "scope")
                (s "status")
                (fl "duration_seconds")
                (s "summary")
                (b "flaky")

        "judge.score" ->
            D.map4 (\tot mx m dims -> JudgeScored (JudgeVerdict tot mx m dims))
                (fl "total")
                (fl "max_total")
                (s "model")
                (D.oneOf [ f "dimensions" (D.list judgeDimensionDecoder), D.succeed [] ])

        "push.done" ->
            D.map2 (\br sha -> Pushed { branch = br, sha = sha }) (s "branch") (s "sha")

        "human.ask" ->
            D.map3 (\q qn opts -> HumanAsked { questionId = q, question = qn, options = opts })
                (i "question_id")
                (s "question")
                (list "options")

        "human.answer" ->
            D.map4 (\q a by to -> HumanAnswered { questionId = q, answer = a, answeredBy = by, timedOut = to })
                (i "question_id")
                (s "answer")
                (s "answered_by")
                (b "timed_out")

        "checkpoint.wait" ->
            D.map2 (\q ck -> CheckpointWaited { questionId = q, checkpoint = ck }) (i "question_id") (s "checkpoint")

        "checkpoint.release" ->
            D.map3 (\q ap by -> CheckpointReleased { questionId = q, approved = ap, answeredBy = by })
                (i "question_id")
                (b "approved")
                (s "answered_by")

        "error" ->
            D.map (\m -> Errored { message = m }) (s "message")

        "tool.call" ->
            D.map2 (\t inp -> ToolCalled { tool = t, input = inp }) (s "tool") (s "input")

        "tool.result" ->
            D.map3 (\t o e -> ToolResulted { tool = t, output = o, isError = e }) (s "tool") (s "output") (b "is_error")

        "artifact.written" ->
            D.map2 (\p n -> ArtifactWritten { path = p, bytes = n }) (s "path") (i "bytes")

        "decision" ->
            D.map (\su -> Thought { summary = su }) (s "summary")

        _ ->
            D.succeed Unknown


judgeDimensionDecoder : D.Decoder JudgeDimension
judgeDimensionDecoder =
    D.map4 JudgeDimension
        (D.oneOf [ D.field "name" D.string, D.succeed "" ])
        (D.oneOf [ D.field "score" D.float, D.succeed 0 ])
        (D.oneOf [ D.field "max" D.float, D.succeed 0 ])
        (D.oneOf [ D.field "rationale" D.string, D.succeed "" ])
