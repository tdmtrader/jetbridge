module AgentFeedback.Types exposing
    ( Finding
    , FeedbackRecord
    , Verdict(..)
    , ConversationMessage
    , SessionState
    , ReviewRef
    , VerdictSummary
    , verdictToString
    , verdictFromString
    , verdictLabel
    , allVerdicts
    , findingDecoder
    , feedbackRecordEncoder
    , sessionStateDecoder
    , verdictSummaryDecoder
    )

import Json.Decode as Decode exposing (Decoder)
import Json.Encode as Encode


-- VERDICT


type Verdict
    = Accurate
    | FalsePositive
    | Noisy
    | OverlyStrict
    | PartiallyCorrect
    | MissedContext


allVerdicts : List Verdict
allVerdicts =
    [ Accurate
    , FalsePositive
    , Noisy
    , OverlyStrict
    , PartiallyCorrect
    , MissedContext
    ]


verdictToString : Verdict -> String
verdictToString v =
    case v of
        Accurate -> "accurate"
        FalsePositive -> "false_positive"
        Noisy -> "noisy"
        OverlyStrict -> "overly_strict"
        PartiallyCorrect -> "partially_correct"
        MissedContext -> "missed_context"


verdictFromString : String -> Maybe Verdict
verdictFromString s =
    case s of
        "accurate" -> Just Accurate
        "false_positive" -> Just FalsePositive
        "noisy" -> Just Noisy
        "overly_strict" -> Just OverlyStrict
        "partially_correct" -> Just PartiallyCorrect
        "missed_context" -> Just MissedContext
        _ -> Nothing


verdictLabel : Verdict -> String
verdictLabel v =
    case v of
        Accurate -> "Accurate"
        FalsePositive -> "False Positive"
        Noisy -> "Noisy"
        OverlyStrict -> "Overly Strict"
        PartiallyCorrect -> "Partially Correct"
        MissedContext -> "Missed Context"


-- TYPES


type alias ReviewRef =
    { repo : String
    , commit : String
    , reviewTimestamp : String
    }


type alias Finding =
    { id : String
    , findingType : String
    , severity : String
    , category : String
    , title : String
    , file : String
    , line : Int
    , description : String
    , testCode : String
    }


type alias ConversationMessage =
    { role : String
    , content : String
    }


type alias FeedbackRecord =
    { reviewRef : ReviewRef
    , findingId : String
    , findingType : String
    , verdict : Verdict
    , confidence : Float
    , notes : String
    , conversation : List ConversationMessage
    , reviewer : String
    , source : String
    }


type alias SessionState =
    { reviewRef : ReviewRef
    , findings : List Finding
    , reviewed : List String
    , pending : List String
    , totalFindings : Int
    , reviewedCount : Int
    }


type alias VerdictSummary =
    { total : Int
    , accuracyRate : Float
    , fpRate : Float
    , byVerdict : List ( String, Int )
    }


-- DECODERS


findingDecoder : Decoder Finding
findingDecoder =
    Decode.map8 findingWithoutTestCode
        (Decode.field "id" Decode.string)
        (Decode.field "finding_type" Decode.string)
        (Decode.field "severity" Decode.string)
        (Decode.field "category" Decode.string)
        (Decode.field "title" Decode.string)
        (Decode.field "file" Decode.string)
        (Decode.field "line" Decode.int)
        (optionalField "description" Decode.string "")
        |> Decode.andThen
            (\partial ->
                Decode.map (\tc -> partial tc)
                    (optionalField "test_code" Decode.string "")
            )


findingWithoutTestCode : String -> String -> String -> String -> String -> String -> Int -> String -> (String -> Finding)
findingWithoutTestCode id ft sev cat title file line desc =
    \testCode -> Finding id ft sev cat title file line desc testCode


reviewRefDecoder : Decoder ReviewRef
reviewRefDecoder =
    Decode.map3 ReviewRef
        (Decode.field "repo" Decode.string)
        (Decode.field "commit" Decode.string)
        (optionalField "review_timestamp" Decode.string "")


sessionStateDecoder : Decoder SessionState
sessionStateDecoder =
    Decode.map6 SessionState
        (Decode.field "review_ref" reviewRefDecoder)
        (Decode.field "findings" (Decode.list findingDecoder))
        (Decode.field "reviewed" (Decode.list Decode.string))
        (Decode.field "pending" (Decode.list Decode.string))
        (Decode.field "total_findings" Decode.int)
        (Decode.field "reviewed_count" Decode.int)


verdictSummaryDecoder : Decoder VerdictSummary
verdictSummaryDecoder =
    Decode.map4 VerdictSummary
        (Decode.field "total" Decode.int)
        (Decode.field "accuracy_rate" Decode.float)
        (Decode.field "false_positive_rate" Decode.float)
        (Decode.field "by_verdict" (Decode.keyValuePairs Decode.int))


optionalField : String -> Decoder a -> a -> Decoder a
optionalField fieldName decoder default =
    Decode.oneOf
        [ Decode.field fieldName decoder
        , Decode.succeed default
        ]


-- ENCODERS


feedbackRecordEncoder : FeedbackRecord -> Encode.Value
feedbackRecordEncoder rec =
    Encode.object
        [ ( "review_ref", reviewRefEncoder rec.reviewRef )
        , ( "finding_id", Encode.string rec.findingId )
        , ( "finding_type", Encode.string rec.findingType )
        , ( "verdict", Encode.string (verdictToString rec.verdict) )
        , ( "confidence", Encode.float rec.confidence )
        , ( "notes", Encode.string rec.notes )
        , ( "conversation", Encode.list conversationMessageEncoder rec.conversation )
        , ( "reviewer", Encode.string rec.reviewer )
        , ( "source", Encode.string rec.source )
        ]


reviewRefEncoder : ReviewRef -> Encode.Value
reviewRefEncoder ref =
    Encode.object
        [ ( "repo", Encode.string ref.repo )
        , ( "commit", Encode.string ref.commit )
        , ( "review_timestamp", Encode.string ref.reviewTimestamp )
        ]


conversationMessageEncoder : ConversationMessage -> Encode.Value
conversationMessageEncoder msg =
    Encode.object
        [ ( "role", Encode.string msg.role )
        , ( "content", Encode.string msg.content )
        ]
