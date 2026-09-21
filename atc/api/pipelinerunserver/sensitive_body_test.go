package pipelinerunserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
)

// These handlers own bodies the auditor and policy check never parse
// (atc.RequestBodyIsSensitive). So nothing upstream has called ParseForm, and
// a handler that reads a route parameter out of r.Form sees nothing -- or, for
// a form-encoded body, sees whatever the caller put there first.

type bodyAdmitter struct {
	runs.Admitter
	inputName string
	uploaded  string
	handoffs  int
}

func (a *bodyAdmitter) UploadInput(_ context.Context, _ runs.TemplateRef, _ runs.Principal, name string, _ int64, body io.Reader) (atc.RunInputSource, error) {
	a.inputName = name
	data, _ := io.ReadAll(body)
	a.uploaded = string(data)
	return atc.RunInputSource{SourceID: "source"}, nil
}

func (a *bodyAdmitter) HandoffCredentials(_ context.Context, _ runs.TemplateRef, _ runs.Principal, number int, result string, _ int64, body io.ReadCloser) (atc.RunCredentialSession, error) {
	a.handoffs++
	_, _ = io.Copy(io.Discard, body)
	return atc.RunCredentialSession{RunID: 1, Result: result, Status: "ready"}, nil
}

type templatePipeline struct{ db.Pipeline }

func (templatePipeline) TeamName() string             { return "t" }
func (templatePipeline) PipelineRef() atc.PipelineRef { return atc.PipelineRef{Name: "review"} }
func (templatePipeline) InstanceVars() atc.InstanceVars {
	return nil
}

type memberTeam struct{ db.Team }

func (memberTeam) Name() string { return "t" }
func (memberTeam) Admin() bool  { return false }
func (memberTeam) Auth() atc.TeamAuth {
	return atc.TeamAuth{"member": {"users": {"test:some-user"}}}
}

type memberAccess struct{}

func (memberAccess) Create(*http.Request, string) (accessor.Access, error) {
	return accessor.NewAccessor(accessor.Verification{
		HasToken: true, IsTokenValid: true,
		RawClaims: map[string]any{"name": "some-user", "federated_claims": map[string]any{"connector_id": "test"}},
	}, accessor.MemberRole, "aud", []string{"concourse-worker"}, []db.Team{memberTeam{}}, nil), nil
}

type silentAuditor struct{}

func (silentAuditor) ValidateAction(string) bool          { return false }
func (silentAuditor) Audit(string, string, *http.Request) {}

// serveSensitive serves over a real connection: the handlers set a read
// deadline, which a ResponseRecorder does not support.
func serveSensitive(t *testing.T, action string, handler http.Handler, request *http.Request) *http.Response {
	t.Helper()
	previous := atc.EnablePipelineRunCreation
	atc.EnablePipelineRunCreation = true
	t.Cleanup(func() { atc.EnablePipelineRunCreation = previous })
	server := httptest.NewServer(accessor.NewHandler(lagertest.NewTestLogger("test"), action, handler, memberAccess{}, silentAuditor{}, nil))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL + request.URL.RequestURI())
	if err != nil {
		t.Fatal(err)
	}
	request.URL = target
	request.RequestURI = ""
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestUploadReadsItsInputNameFromTheRoute(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
	}{
		{"tar body", "application/x-tar", "tar-bytes"},
		{"form body cannot rename the input", "application/x-www-form-urlencoded", ":input_name=other&x=y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admitter := &bodyAdmitter{}
			server := NewServer(lagertest.NewTestLogger("test"), nil, "")
			server.SetServices(Services{Admitter: admitter, Epoch: 1})
			request := httptest.NewRequest(http.MethodPost,
				"/api/v1/teams/t/pipelines/review/run-inputs/change?:team_name=t&:pipeline_name=review&:input_name=change",
				strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.contentType)

			response := serveSensitive(t, atc.UploadPipelineRunInput, server.UploadPipelineRunInput(templatePipeline{}), request)

			if response.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d, want 201", response.StatusCode)
			}
			if admitter.inputName != "change" {
				t.Errorf("input name = %q, want the route's %q", admitter.inputName, "change")
			}
			if admitter.uploaded != tc.body {
				t.Errorf("admitter received %q, want the whole body %q", admitter.uploaded, tc.body)
			}
		})
	}
}

func TestCredentialHandoffRequiresJSON(t *testing.T) {
	for _, tc := range []struct {
		contentType string
		want        int
	}{
		{"application/json", http.StatusOK},
		{"application/json; charset=utf-8", http.StatusOK},
		{"application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"text/plain", http.StatusUnsupportedMediaType},
		{"", http.StatusUnsupportedMediaType},
		{"application/json-seq", http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.contentType, func(t *testing.T) {
			admitter := &bodyAdmitter{}
			server := NewServer(lagertest.NewTestLogger("test"), nil, "")
			server.SetServices(Services{Admitter: admitter, Epoch: 1})
			request := httptest.NewRequest(http.MethodPost,
				"/api/v2/teams/t/pipelines/review/runs/1/credentials/findings?:team_name=t&:pipeline_name=review&:number=1&:result_name=findings",
				strings.NewReader(`{"tokens":{"refresh_token":"synthetic"}}`))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}

			response := serveSensitive(t, atc.HandoffPipelineRunCredentials, server.HandoffPipelineRunCredentials(templatePipeline{}), request)

			if response.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.want)
			}
			wantHandoffs := 0
			if tc.want == http.StatusOK {
				wantHandoffs = 1
			}
			if admitter.handoffs != wantHandoffs {
				t.Errorf("handoffs = %d, want %d: a refused media type must not reach the admitter", admitter.handoffs, wantHandoffs)
			}
		})
	}
}
