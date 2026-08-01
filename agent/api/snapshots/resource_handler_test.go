package snapshots_test

import (
	"code.cloudfoundry.org/lager/v3/lagertest"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	snapshotsapi "github.com/concourse/concourse/agent/api/snapshots"
	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

type resourceCapturerStub struct {
	capture func(context.Context, resourcecapture.Request) (resourcecapture.CaptureResult, error)
	calls   []resourcecapture.Request
}

func TestCaptureResourceMapsTemplateConflictAndLogsCausesWithoutExposingThem(t *testing.T) {
	logger := lagertest.NewTestLogger("resource-capture")
	factory, err := snapshotsapi.NewHandlerFactory(snapshotsapi.Config{
		Enabled: true, Logger: logger, Creator: &fakeCreator{}, Metadata: &fakeMetadata{}, Content: &fakeContent{}, Locks: &fakeLocks{},
		ArchiveLimits: snapshot.ArchiveLimits{MaxContentBytes: 64, MaxEntries: 4}, TempDir: t.TempDir(),
		Identity: func(*http.Request) (snapshotsapi.RequestIdentity, error) {
			return snapshotsapi.RequestIdentity{Actor: "github:subject-1", DisplayName: "Alice"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	capturer := &resourceCapturerStub{capture: func(context.Context, resourcecapture.Request) (resourcecapture.CaptureResult, error) {
		return resourcecapture.CaptureResult{}, resourcecapture.ErrTemplateConflict
	}}
	request := httptest.NewRequest(http.MethodPost, "/capture-resource", strings.NewReader(`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	factory.CaptureResource(snapshotsapi.TrustedTeam{ID: 7, Name: "main"}, capturer).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"resource capture conflicts with immutable state"`) {
		t.Fatalf("conflict response status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	cause := errors.New("database secret /tmp/private")
	capturer.capture = func(context.Context, resourcecapture.Request) (resourcecapture.CaptureResult, error) {
		return resourcecapture.CaptureResult{}, cause
	}
	request = httptest.NewRequest(http.MethodPost, "/capture-resource", strings.NewReader(`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	factory.CaptureResource(snapshotsapi.TrustedTeam{ID: 7, Name: "main"}, capturer).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "/tmp") {
		t.Fatalf("bounded response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(logger.Errors) != 2 || !errors.Is(logger.Errors[1], cause) || !strings.HasSuffix(logger.LogMessages()[0], ".resource-capture-request-failed") {
		t.Fatalf("logged errors/messages = %#v / %#v", logger.Errors, logger.LogMessages())
	}
}

func (stub *resourceCapturerStub) Capture(ctx context.Context, request resourcecapture.Request) (resourcecapture.CaptureResult, error) {
	stub.calls = append(stub.calls, request)
	return stub.capture(ctx, request)
}

func TestCaptureResourceUsesTrustedTeamAndIdentityAndReturnsPendingOperation(t *testing.T) {
	harness := newHandlerHarness(t)
	capturer := &resourceCapturerStub{capture: func(_ context.Context, request resourcecapture.Request) (resourcecapture.CaptureResult, error) {
		if request.TeamID != 7 || request.TeamName != "main" || request.CreatedBy != "Alice" || request.Actor != "github:subject-1" ||
			request.Pipeline.Name != "delivery" || request.Pipeline.InstanceVars["branch"] != "main" || request.Resource != "repository" ||
			request.Version["ref"] != "abc123" || request.Type != "repository/v1" || request.RetryPipelineRunID != 9007199254740993 {
			t.Fatalf("capture request = %#v", request)
		}
		return resourcecapture.CaptureResult{
			OperationKey: strings.Repeat("a", 64), Created: true,
			Execution: resourcecapture.Execution{PipelineRunID: 51, TemplatePipelineID: 41, InstancePipelineID: 61, Status: db.PipelineRunRunning},
		}, nil
	}}
	body := `{"pipeline":{"name":"delivery","instance_vars":{"branch":"main"}},"resource":"repository","version":{"ref":"abc123"},"type":"repository/v1","retry_pipeline_run_id":"9007199254740993"}`
	request := httptest.NewRequest(http.MethodPost, "/capture-resource", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.factory.CaptureResource(harness.team, capturer).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response resourcecapture.CaptureResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Execution.PipelineRunID != 51 {
		t.Fatalf("response = %#v, %v", response, err)
	}
}

func TestCaptureResourceRejectsMalformedBodiesAndMapsSafeDomainErrors(t *testing.T) {
	harness := newHandlerHarness(t)
	capturer := &resourceCapturerStub{capture: func(context.Context, resourcecapture.Request) (resourcecapture.CaptureResult, error) {
		return resourcecapture.CaptureResult{}, resourcecapture.ErrDisabled
	}}
	for _, body := range []string{
		`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"},"unknown":true}`,
		`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"}} {}`,
		`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"},"retry_pipeline_run_id":51}`,
		`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"},"retry_pipeline_run_id":"051"}`,
		`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"},"retry_pipeline_run_id":"-1"}`,
		`null`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/capture-resource", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		harness.factory.CaptureResource(harness.team, capturer).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status=%d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/capture-resource", strings.NewReader(`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.factory.CaptureResource(harness.team, capturer).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"disabled"`) {
		t.Fatalf("status=%d response=%s", recorder.Code, recorder.Body.String())
	}

	capturer.capture = func(context.Context, resourcecapture.Request) (resourcecapture.CaptureResult, error) {
		return resourcecapture.CaptureResult{}, errors.New("database secret detail")
	}
	request = httptest.NewRequest(http.MethodPost, "/capture-resource", strings.NewReader(`{"pipeline":{"name":"delivery"},"resource":"repository","version":{"ref":"abc"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	harness.factory.CaptureResource(harness.team, capturer).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "database") {
		t.Fatalf("status=%d response=%s", recorder.Code, recorder.Body.String())
	}
}
