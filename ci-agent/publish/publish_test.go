package publish_test

import (
	"context"
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
		Token:      "tok",
		ReviewPath: writeReview(t),
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotAuth.Load() != "Bearer tok" {
		t.Errorf("auth header = %v", gotAuth.Load())
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
