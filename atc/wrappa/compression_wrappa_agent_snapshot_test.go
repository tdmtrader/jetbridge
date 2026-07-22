package wrappa_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/tedsuo/rata"
)

func TestSnapshotContentRouteBypassesResponseCompression(t *testing.T) {
	body := make([]byte, 4096)
	for i := range body {
		body[i] = byte(i)
	}
	delegate := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	w := wrappa.NewCompressionWrappa(lagertest.NewTestLogger("snapshot-content"))
	wrapped := w.Wrap(rata.Handlers{atc.DownloadAgentSnapshot: delegate})

	request := httptest.NewRequest(http.MethodGet, "/content", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	wrapped[atc.DownloadAgentSnapshot].ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want no response compression", got)
	}
	if got := recorder.Body.Bytes(); string(got) != string(body) {
		t.Fatal("snapshot content bytes changed by compression wrapper")
	}
}
