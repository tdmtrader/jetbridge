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
