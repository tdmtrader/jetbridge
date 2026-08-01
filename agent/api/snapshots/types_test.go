package snapshots_test

import (
	"encoding/json"
	"testing"

	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
)

func TestErrorResponseHasStableBoundedWireShape(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(snapshotsapi.ErrorResponse{
		Error:   "invalid_request",
		Message: "the request is invalid",
	})
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	if got, want := string(payload), `{"error":"invalid_request","message":"the request is invalid"}`; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
}

func TestErrorResponseOmitsReasonUnlessValidationIsSafelyClassified(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(snapshotsapi.ErrorResponse{
		Error:   "validation_failed",
		Message: "repository work tree and index must be clean",
		Reason:  "repository_dirty",
	})
	if err != nil {
		t.Fatalf("marshal classified error response: %v", err)
	}
	if got, want := string(payload), `{"error":"validation_failed","message":"repository work tree and index must be clean","reason":"repository_dirty"}`; got != want {
		t.Fatalf("payload = %s, want %s", got, want)
	}
}
