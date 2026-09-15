package concourse_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"
)

func TestConditionalConfigClient(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		version, body string
		inputVersion  string
		want          error
	}{
		{"receipt", 201, "27", `{"warnings":[]}`, "0", nil},
		{"conflict", 409, "", `{"code":"config_version_conflict","errors":["stale"]}`, "0", concourse.ErrConfigVersionConflict},
		{"other conflict", 409, "", `{"errors":["template restriction"]}`, "0", concourse.ErrConfigWriteRejected},
		{"old replica", 404, "", ``, "0", concourse.ErrStrictConfigUnsupported},
		{"missing receipt", 201, "", `{}`, "0", concourse.ErrConfigOutcomeUnknown},
		{"malformed receipt", 201, "27x", `{}`, "0", concourse.ErrConfigOutcomeUnknown},
		{"update receipt", 200, "27", `{}`, "17", nil},
		{"wrong create status", 200, "27", `{}`, "0", concourse.ErrConfigOutcomeUnknown},
		{"wrong update status", 201, "27", `{}`, "17", concourse.ErrConfigOutcomeUnknown},
		{"unchanged receipt", 200, "17", `{}`, "17", concourse.ErrConfigOutcomeUnknown},
		{"lost response", 0, "", ``, "17", concourse.ErrConfigOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/teams/main/pipelines/deploy/config/conditional" || r.Method != "PUT" || r.Header.Get(atc.ConfigVersionHeader) != test.inputVersion {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if test.status == 0 {
					_, _ = io.Copy(io.Discard, r.Body)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(atc.ConfigVersionHeader, test.version)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			receipt, err := concourse.NewClient(server.URL, server.Client(), false).Team("main").SetPipelineConfigConditional(atc.PipelineRef{Name: "deploy"}, test.inputVersion, []byte("jobs: []"), false)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
			if err == nil && (receipt.Version != "27" || receipt.Created != (test.inputVersion == "0")) {
				t.Fatalf("wrong receipt: %+v", receipt)
			}
			if calls != 1 {
				t.Fatalf("retried or fell back: %d calls", calls)
			}
		})
	}
}
