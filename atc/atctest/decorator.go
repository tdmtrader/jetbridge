package atctest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/atc"
)

// Fault sees every request to a decorated platform first. It serves the
// request itself -- a refusal, a lost response, a rewritten answer -- or
// passes it to next, the real web node.
type Fault func(w http.ResponseWriter, r *http.Request, next http.Handler)

// Decorate puts a reverse proxy carrying fault in front of the web node and
// returns its URL. It is how a test makes the real platform fail on demand:
// everything fault does not intercept is the platform's own answer.
func (p *Platform) Decorate(t testing.TB, fault Fault) string {
	t.Helper()
	target, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	next := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fault(w, r, next) }))
	t.Cleanup(server.Close)
	return server.URL
}

// Lose forwards r to the platform and discards its answer: the work happened
// and the caller never learns it.
func Lose(next http.Handler, r *http.Request) {
	next.ServeHTTP(httptest.NewRecorder(), r)
}

// RecordInputGrants puts a decorator in front of the web node that passes
// every request through and keeps the bearer of every Run input grant the
// platform issues, so a test can look for the real grant wherever a client
// must not keep it. It returns the decorated URL and the grants so far.
func (p *Platform) RecordInputGrants(t testing.TB) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var grants []string
	url := p.Decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/run-inputs/") {
			next.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r)
		var source atc.RunInputSource
		if json.Unmarshal(recorder.Body.Bytes(), &source) == nil && source.Bearer != "" {
			mu.Lock()
			grants = append(grants, source.Bearer)
			mu.Unlock()
		}
		for name, values := range recorder.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
	return url, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), grants...)
	}
}
