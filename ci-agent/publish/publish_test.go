package publish_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/concourse/ci-agent/publish"
)

func writeReview(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	content := `{"schema_version":"1.0.0","metadata":{"repo":"r","commit":"c"},"score":{"value":8,"max":10,"pass":true},"proven_issues":[],"observations":[],"summary":"ok"}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublishSuccess(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/reviews" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL:     srv.URL,
		BuildID:    "42",
		Token:      "cap1.17.review",
		ReviewPath: writeReview(t),
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotAuth.Load() != "Bearer cap1.17.review" {
		t.Errorf("auth header = %v", gotAuth.Load())
	}
}

func TestPublishRequiresReviewPrincipalToken(t *testing.T) {
	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: "http://unused.invalid", BuildID: "42", ReviewPath: writeReview(t),
	})
	if err == nil || err.Error() != "AGENT_REVIEW_PRINCIPAL_TOKEN is not set" {
		t.Fatalf("error = %v, want exact missing review principal message", err)
	}
}

func TestPublishRetriesOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: srv.URL, BuildID: "42", Token: "tok",
		ReviewPath: writeReview(t), RetryDelay: -1,
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestPublishGivesUpAfterRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: srv.URL, BuildID: "42", Token: "tok",
		ReviewPath: writeReview(t), RetryDelay: -1,
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestPublishDoesNotRetry4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := publish.Publish(context.Background(), publish.Options{
		ATCURL: srv.URL, BuildID: "42", Token: "tok",
		ReviewPath: writeReview(t), RetryDelay: -1,
	})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("want 1 call and error, got calls=%d err=%v", calls.Load(), err)
	}
}

func TestPublishValidatesInputs(t *testing.T) {
	base := publish.Options{ATCURL: "http://x", BuildID: "42", Token: "t", ReviewPath: writeReview(t)}
	for name, mutate := range map[string]func(*publish.Options){
		"no url":     func(o *publish.Options) { o.ATCURL = "" },
		"no build":   func(o *publish.Options) { o.BuildID = "" },
		"no token":   func(o *publish.Options) { o.Token = "" },
		"bad review": func(o *publish.Options) { o.ReviewPath = "/nonexistent" },
	} {
		opts := base
		mutate(&opts)
		if err := publish.Publish(context.Background(), opts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestPublishCostsPostsEachRecord(t *testing.T) {
	var bodies [][]byte
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/costs" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		auths = append(auths, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	dir := t.TempDir()
	costsPath := filepath.Join(dir, "costs.json")
	if err := os.WriteFile(costsPath, []byte(`[
		{"step":"analyze","model":"claude-sonnet-5","input_tokens":100,"output_tokens":50,"cache_read_tokens":10,"cache_creation_tokens":5,"turns":4,"cost_usd":0.25,"duration_ms":1200}
	]`), 0644); err != nil {
		t.Fatal(err)
	}

	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL:    srv.URL,
		BuildID:   "1234",
		Token:     "cap1.18.cost",
		CostsPath: costsPath,
		Phase:     "review",
		UserName:  "tdmtrader",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("posted %d records", len(bodies))
	}
	if auths[0] != "Bearer cap1.18.cost" {
		t.Fatalf("auth: %q", auths[0])
	}

	var rec map[string]any
	if err := json.Unmarshal(bodies[0], &rec); err != nil {
		t.Fatal(err)
	}
	if rec["source"] != "ci_agent" || rec["build_id"] != float64(1234) ||
		rec["step_name"] != "review/analyze" || rec["cost_usd"] != 0.25 ||
		rec["user_name"] != "tdmtrader" || rec["provider"] != "anthropic" ||
		rec["turns"] != float64(4) || rec["input_tokens"] != float64(100) {
		t.Fatalf("record: %v", rec)
	}
}

func TestPublishCostsRequiresCostPrincipalToken(t *testing.T) {
	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL: "http://unused.invalid", BuildID: "42",
	})
	if err == nil || err.Error() != "AGENT_COST_PRINCIPAL_TOKEN is not set" {
		t.Fatalf("error = %v, want exact missing cost principal message", err)
	}
}

func TestPublishCostsSkipsMissingFile(t *testing.T) {
	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL:    "http://unused.invalid",
		BuildID:   "1",
		Token:     "t",
		CostsPath: filepath.Join(t.TempDir(), "absent.json"),
	})
	if err != nil {
		t.Fatalf("missing costs.json must be a silent skip: %v", err)
	}
}

func TestPublishCostsReturnsErrorOnServerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer srv.Close()

	dir := t.TempDir()
	costsPath := filepath.Join(dir, "costs.json")
	os.WriteFile(costsPath, []byte(`[{"step":"s","cost_usd":1}]`), 0644)

	err := publish.PublishCosts(context.Background(), publish.CostsOptions{
		ATCURL: srv.URL, BuildID: "1", Token: "t", CostsPath: costsPath,
	})
	if err == nil {
		t.Fatal("server 4xx must surface as an error (caller downgrades to a warning)")
	}
}
