package metrics_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
)

func TestParseSubmissionRequiresBuildAndPlan(t *testing.T) {
	_, err := metrics.ParseSubmission([]byte(`{"plan_id":"a","step_name":"s","status":"ok"}`))
	if err == nil || err.Error() != "build_id is required" {
		t.Fatalf("expected build_id error, got %v", err)
	}
	_, err = metrics.ParseSubmission([]byte(`{"build_id":1,"step_name":"s","status":"ok"}`))
	if err == nil || err.Error() != "plan_id is required" {
		t.Fatalf("expected plan_id error, got %v", err)
	}
	_, err = metrics.ParseSubmission([]byte(`{"build_id":1,"plan_id":"a","step_name":"s","status":"nope"}`))
	if err == nil {
		t.Fatal("expected status taxonomy error")
	}
	rm, err := metrics.ParseSubmission([]byte(`{"build_id":1,"plan_id":"a","step_name":"s","status":"error","summary":"crashed"}`))
	if err != nil || rm.Status != "error" {
		t.Fatalf("expected valid submission, got %v %v", rm, err)
	}
}

func TestParseSubmissionAcceptsParked(t *testing.T) {
	// PARK-V2 (shared-contracts §1.8, 2026-07-10 amendment): 'parked' is a
	// valid DB/API status — the park-exit partial ingestion writes it.
	rm, err := metrics.ParseSubmission([]byte(`{"build_id":1,"plan_id":"a","step_name":"s","status":"parked"}`))
	if err != nil || rm.Status != "parked" {
		t.Fatalf("expected parked submission accepted, got %v %v", rm, err)
	}
}
