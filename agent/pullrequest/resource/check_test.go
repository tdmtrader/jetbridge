package resource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/pullrequest/resource"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestForgePRCheckUsesProjectedBindingState(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	observer := observerFunc(func(_ context.Context, locator pullrequest.Locator, cursor pullrequest.Cursor) (pullrequest.Observation, error) {
		if cursor != "acknowledged" {
			t.Fatalf("cursor = %q", cursor)
		}
		return activeObservation(locator, "new-cursor"), nil
	})
	source := testSource(now)
	source.Monitor.AcknowledgedCursor = "acknowledged"
	previous := resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('c'), Cursor: "unacknowledged", BindingRevision: "7"}
	input := checkInput(t, source, &previous)
	var out, stderr bytes.Buffer
	err := resource.Check(context.Background(), bytes.NewReader(input), &out, &stderr, resource.Dependencies{ObserverFactory: fixedObserver(observer), Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	var versions []resource.Version
	if err := json.Unmarshal(out.Bytes(), &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Cursor != "new-cursor" {
		t.Fatalf("versions = %#v", versions)
	}
	if versions[0].BindingRevision != "7" {
		t.Fatalf("binding revision = %q", versions[0].BindingRevision)
	}
}

func TestForgePRCheckShortCircuitsPausedState(t *testing.T) {
	for _, stop := range []func(*resource.MonitorCheckState){func(state *resource.MonitorCheckState) { state.Paused = true }, func(state *resource.MonitorCheckState) { state.AttentionRequired = true }, func(state *resource.MonitorCheckState) { state.OperatorTerminated = true }} {
		source := testSource(time.Now().UTC())
		stop(&source.Monitor)
		previous := resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('c'), Cursor: "old", BindingRevision: "7"}
		var out bytes.Buffer
		err := resource.Check(context.Background(), bytes.NewReader(checkInput(t, source, &previous)), &out, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: func(resource.Source) (pullrequest.Observer, error) {
			t.Fatal("observer must not be resolved")
			return nil, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != "["+mustJSON(t, previous)+"]" {
			t.Fatalf("output = %s", got)
		}
	}
}

func TestForgePRCheckRejectsUnsafeSourceAndBindingRevision(t *testing.T) {
	source := testSource(time.Now().UTC())
	source.RepositoryURL = "https://token@example.invalid/repository"
	if err := resource.Check(context.Background(), bytes.NewReader(checkInput(t, source, nil)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{}); err == nil {
		t.Fatal("expected unsafe URL error")
	}
	source = testSource(time.Now().UTC())
	source.Monitor.BindingRevision = 0
	if err := resource.Check(context.Background(), bytes.NewReader(checkInput(t, source, nil)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{}); err == nil {
		t.Fatal("expected binding revision error")
	}
	source = testSource(time.Now().UTC())
	source.Monitor.ActiveActionDigest = "not-a-digest"
	if err := resource.Check(context.Background(), bytes.NewReader(checkInput(t, source, nil)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{}); err == nil {
		t.Fatal("expected active digest error")
	}
}

func TestForgePRCheckPendingAndFreshnessSemantics(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	prior := &resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('c'), Cursor: "prior", BindingRevision: "7"}
	for _, test := range []struct {
		name        string
		source      resource.Source
		observation pullrequest.Observation
		want        string
	}{
		{"pending", testSource(now), observationWithoutBatches("new"), "prior"},
		{"freshness", sourceWithOldTarget(now), observationWithoutBatches("new"), "freshness"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			err := resource.Check(context.Background(), bytes.NewReader(checkInput(t, test.source, prior)), &out, &bytes.Buffer{}, resource.Dependencies{Clock: func() time.Time { return now }, ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
				return test.observation, nil
			}))})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), test.want) {
				t.Fatalf("output = %s; want %q", out.String(), test.want)
			}
		})
	}
}

func TestForgePRCheckRejectsNonCanonicalSourceAndVersion(t *testing.T) {
	source := testSource(time.Now().UTC())
	source.Monitor.Paused = true
	raw := string(checkInput(t, source, nil))
	canonical := source.Monitor.LastReconciledAt.Format(time.RFC3339Nano)
	raw = strings.Replace(raw, canonical, strings.TrimSuffix(canonical, "Z")+"+00:00", 1)
	if err := resource.Check(context.Background(), strings.NewReader(raw), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{}); err == nil {
		t.Fatal("expected offset timestamp rejection")
	}
	bad := resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('d'), Cursor: "cursor", BindingRevision: "07"}
	if err := resource.Check(context.Background(), bytes.NewReader(checkInput(t, source, &bad)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{}); err == nil {
		t.Fatal("expected noncanonical binding revision rejection")
	}
}

func observationWithoutBatches(cursor pullrequest.Cursor) pullrequest.Observation {
	return pullrequest.Observation{Locator: pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget", ExternalID: "42"}, Cursor: cursor, URL: "https://github.example/acme/widget/pull/42", State: contracts.PullRequestActive, Mergeability: contracts.PullRequestMergeable, SourceRef: "refs/heads/change", SourceSHA: sha('a'), TargetRef: "refs/heads/main", TargetSHA: sha('b'), Iteration: "42"}
}
func sourceWithOldTarget(now time.Time) resource.Source {
	source := testSource(now)
	source.Monitor.LastReconciledTarget = sha('c')
	return source
}

func testSource(now time.Time) resource.Source {
	return resource.Source{Provider: "github", Repository: "acme/widget", ExternalID: "42", APIBaseURL: "https://api.github.example", RepositoryURL: "https://git.example/acme/widget.git", ReadToken: "super-secret-token", PollInterval: "5m", FreshnessInterval: "6h", Monitor: resource.MonitorCheckState{BindingID: 9, BindingRevision: 7, LastReconciledAt: now.Add(-7 * time.Hour)}}
}

func activeObservation(locator pullrequest.Locator, cursor pullrequest.Cursor) pullrequest.Observation {
	return pullrequest.Observation{Locator: locator, Cursor: cursor, URL: "https://github.example/acme/widget/pull/42", State: contracts.PullRequestActive, Mergeability: contracts.PullRequestMergeable, SourceRef: "refs/heads/change", SourceSHA: sha('a'), TargetRef: "refs/heads/main", TargetSHA: sha('b'), Iteration: "42", ReviewBatches: []pullrequest.ReviewBatch{{ID: "batch-1", ReviewID: "review-1", CommitSHA: sha('a'), Reviewer: "reviewer", Ready: true}}}
}

type observerFunc func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error)

func (fn observerFunc) Observe(ctx context.Context, l pullrequest.Locator, c pullrequest.Cursor) (pullrequest.Observation, error) {
	return fn(ctx, l, c)
}
func fixedObserver(value pullrequest.Observer) resource.ObserverFactory {
	return func(resource.Source) (pullrequest.Observer, error) { return value, nil }
}
func checkInput(t *testing.T, source resource.Source, version *resource.Version) []byte {
	t.Helper()
	raw, err := json.Marshal(resource.Request{Source: source, Version: version})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func sha(char rune) string    { return strings.Repeat(string(char), 40) }
func digest(char rune) string { return "sha256:" + strings.Repeat(string(char), 64) }
