module AgentFeedback.Api exposing
    ( ClassifyResult
    , fetchFindings
    , submitFeedback
    , classifyVerdict
    , fetchSummary
    )

import Http
import Json.Decode as Decode
import Json.Encode as Encode
import AgentFeedback.Types as Types exposing
    ( Finding
    , FeedbackRecord
    , VerdictSummary
    , findingDecoder
    , feedbackRecordEncoder
    , verdictSummaryDecoder
    )


-- FETCH FINDINGS


fetchFindings : String -> (Result Http.Error (List Finding) -> msg) -> Cmd msg
fetchFindings commit toMsg =
    Http.send toMsg
        (Http.get
            ("/api/v1/agent/reviews/" ++ commit ++ "/findings")
            (Decode.list findingDecoder)
        )


-- SUBMIT FEEDBACK


submitFeedback : FeedbackRecord -> (Result Http.Error String -> msg) -> Cmd msg
submitFeedback record toMsg =
    Http.send toMsg
        (Http.post
            "/api/v1/agent/feedback"
            (Http.jsonBody (feedbackRecordEncoder record))
            (Decode.field "status" Decode.string)
        )


-- CLASSIFY VERDICT


type alias ClassifyResult =
    { verdict : String
    , confidence : Float
    }


classifyResultDecoder : Decode.Decoder ClassifyResult
classifyResultDecoder =
    Decode.map2 ClassifyResult
        (Decode.field "verdict" Decode.string)
        (Decode.field "confidence" Decode.float)


classifyVerdict : String -> (Result Http.Error ClassifyResult -> msg) -> Cmd msg
classifyVerdict text toMsg =
    Http.send toMsg
        (Http.post
            "/api/v1/agent/feedback/classify"
            (Http.jsonBody (Encode.object [ ( "text", Encode.string text ) ]))
            classifyResultDecoder
        )


-- FETCH SUMMARY


fetchSummary : (Result Http.Error VerdictSummary -> msg) -> Cmd msg
fetchSummary toMsg =
    Http.send toMsg
        (Http.get "/api/v1/agent/feedback/summary" verdictSummaryDecoder)
