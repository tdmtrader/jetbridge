package pipelinerunserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
)

// Requirement 20: a committed replay answers 200 with an explicit replay
// outcome and the Idempotency-Replayed header; a new Run answers 201, says it
// was created, and carries no replay header. The real-database admission path
// behind this shape is exercised by the brine run-invocation-api feature; this
// pins only the HTTP signalling for both outcomes.

type replayAdmitter struct {
	runs.Admitter
	replayed bool
}

type admissionTx struct{ runs.Transaction }

func (admissionTx) Commit() error   { return nil }
func (admissionTx) Rollback() error { return nil }

func (a replayAdmitter) Begin(context.Context) (runs.Transaction, error) { return admissionTx{}, nil }

func (a replayAdmitter) AdmitVersionedRun(context.Context, runs.Tx, runs.Admission, int64) (runs.Run, bool, error) {
	return runs.Run{ID: 41, Number: 3}, a.replayed, nil
}

type admittedRun struct{ db.PipelineRun }

func (admittedRun) ID() int                                 { return 41 }
func (admittedRun) Number() int                             { return 3 }
func (admittedRun) ContractVersion() atc.RunContractVersion { return atc.RunContractV2 }
func (admittedRun) ActivationEpoch() int64                  { return 1 }
func (admittedRun) TemplatePipelineID() int                 { return 7 }
func (admittedRun) Status() atc.RunStatus                   { return atc.RunStatusRunning }
func (admittedRun) CreatedBy() string                       { return "some-user" }
func (admittedRun) Params() atc.Params                      { return atc.Params{} }
func (admittedRun) ConfigHash() string                      { return "hash" }
func (admittedRun) CancellationRequested() bool             { return false }
func (admittedRun) CancellationRequest() *atc.RunCancellationRequest {
	return nil
}
func (admittedRun) CausedByRun() *int   { return nil }
func (admittedRun) Correlation() string { return "" }
func (admittedRun) CompletedAt() *time.Time {
	return nil
}
func (admittedRun) ReclaimRetryAfter() *time.Time { return nil }
func (admittedRun) CreatedAt() time.Time          { return time.Unix(1, 0) }

type admittedRunFactory struct{ db.PipelineRunFactory }

func (admittedRunFactory) GetRun(db.Pipeline, int) (db.PipelineRun, bool, error) {
	return admittedRun{}, true, nil
}
func (admittedRunFactory) AfterRunCreated(context.Context, db.RunCreation) error { return nil }
func (admittedRunFactory) InstancePipeline(db.PipelineRun) (db.Pipeline, bool, error) {
	return nil, false, nil
}
func (admittedRunFactory) CaptureProgress(context.Context, int) ([]atc.RunCaptureProgress, error) {
	return nil, nil
}

func TestVersionedCreateSignalsReplay(t *testing.T) {
	for _, tc := range []struct {
		replayed bool
		status   int
		outcome  atc.RunAdmissionOutcome
		header   string
	}{
		{false, http.StatusCreated, atc.RunAdmissionCreated, ""},
		{true, http.StatusOK, atc.RunAdmissionReplayed, "true"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			server := NewServer(lagertest.NewTestLogger("test"), admittedRunFactory{}, "")
			server.SetServices(Services{Admitter: replayAdmitter{replayed: tc.replayed}, Epoch: 1})
			request := httptest.NewRequest(http.MethodPost,
				"/api/v2/teams/t/pipelines/review/runs?:team_name=t&:pipeline_name=review",
				strings.NewReader(`{"invocation_key":"replay-signal"}`))
			request.Header.Set("Content-Type", "application/json")

			response := serveSensitive(t, atc.CreatePipelineRunV2, server.CreatePipelineRunV2(templatePipeline{}), request)

			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
			if got := response.Header.Get(atc.IdempotencyReplayedHeader); got != tc.header {
				t.Errorf("%s header = %q, want %q", atc.IdempotencyReplayedHeader, got, tc.header)
			}
			var run atc.PipelineRun
			if err := json.NewDecoder(response.Body).Decode(&run); err != nil {
				t.Fatal(err)
			}
			if run.AdmissionOutcome != tc.outcome || run.ID != 41 || run.Number != 3 {
				t.Errorf("response = outcome %q, Run %d #%d; want %q, Run 41 #3", run.AdmissionOutcome, run.ID, run.Number, tc.outcome)
			}
		})
	}
}

type refusingAdmitter struct {
	replayAdmitter
	err error
}

func (a refusingAdmitter) AdmitVersionedRun(context.Context, runs.Tx, runs.Admission, int64) (runs.Run, bool, error) {
	return runs.Run{}, false, a.err
}

// A refusal about the call or the template's state carries its reason, so fly
// and the web UI can say what to change; an authorization or existence refusal
// carries nothing.
func TestVersionedRefusalsCarryTheirReasonOnlyAfterAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		reason string
	}{
		{"invalid params", runs.InvalidParamsError{Err: errors.New("parameter environment is required")}, http.StatusBadRequest, "parameter environment is required"},
		{"paused template", runs.ErrTemplatePaused, http.StatusConflict, runs.ErrTemplatePaused.Error()},
		{"changed intent", runs.ErrInvocationConflict, http.StatusConflict, runs.ErrInvocationConflict.Error()},
		{"held admission", runs.ErrVersionedAdmissionUnavailable, http.StatusConflict, "not activated"},
		{"unauthorized", runs.ErrUnauthorized, http.StatusForbidden, ""},
		{"not found", runs.ErrTemplateNotFound, http.StatusNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(lagertest.NewTestLogger("test"), admittedRunFactory{}, "")
			server.SetServices(Services{Admitter: refusingAdmitter{err: tc.err}, Epoch: 1})
			request := httptest.NewRequest(http.MethodPost,
				"/api/v2/teams/t/pipelines/review/runs?:team_name=t&:pipeline_name=review",
				strings.NewReader(`{"invocation_key":"refused"}`))
			request.Header.Set("Content-Type", "application/json")

			response := serveSensitive(t, atc.CreatePipelineRunV2, server.CreatePipelineRunV2(templatePipeline{}), request)

			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
			body, _ := io.ReadAll(response.Body)
			if tc.reason == "" {
				if len(body) != 0 {
					t.Errorf("refusal disclosed %q", body)
				}
				return
			}
			var envelope atc.SaveConfigResponse
			if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Errors) != 1 || !strings.Contains(envelope.Errors[0], tc.reason) {
				t.Errorf("body = %q, want an error envelope naming %q", body, tc.reason)
			}
		})
	}
}
