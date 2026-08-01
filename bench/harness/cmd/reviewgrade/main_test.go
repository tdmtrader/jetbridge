package main

import "testing"

// A sealed record.json nests the review under `body`. Decoding those bytes
// into a bare body succeeds and yields nothing, so the failure mode this
// guards is a real, correct review scoring 0% recall with no error printed.
func TestDecodeReviewAcceptsSealedRecordEnvelope(t *testing.T) {
	sealed := []byte(`{
	  "record_version": "1.0.0",
	  "type": "review/v1",
	  "schema": "sha256:aaaa",
	  "subjects": [{"id":"change","role":"primary","input":"change","type":"opaque/v1","digest":"sha256:bbbb"}],
	  "body": {
	    "conclusion": "changes-required",
	    "summary": "reviewed",
	    "findings": [{
	      "id": "a-defect", "severity": "high", "blocking": true, "category": "security",
	      "title": "t", "description": "d",
	      "evidence": [{"subject":"repository","locator":{"kind":"file-lines","path":"atc/db/x.go","start":10,"end":20}}]
	    }]
	  }
	}`)

	review, err := decodeReview(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if review.Conclusion != "changes-required" || len(review.Findings) != 1 {
		t.Fatalf("sealed record decoded to %+v", review)
	}
	if got := review.Findings[0].Evidence[0].Locator.Path; got != "atc/db/x.go" {
		t.Fatalf("anchor path = %q", got)
	}
}

func TestDecodeReviewAcceptsBareBody(t *testing.T) {
	review, err := decodeReview([]byte(`{"conclusion":"accept","summary":"clean","findings":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if review.Conclusion != "accept" {
		t.Fatalf("bare body decoded to %+v", review)
	}
}

func TestDecodeReviewRefusesEmptyAndWrongType(t *testing.T) {
	if _, err := decodeReview([]byte(`{"unrelated":true}`)); err == nil {
		t.Fatal("an empty review was accepted")
	}
	if _, err := decodeReview([]byte(`{"type":"diagnosis/v1","body":{"conclusion":"identified","summary":"s"}}`)); err == nil {
		t.Fatal("a diagnosis record was accepted as a review")
	}
}
