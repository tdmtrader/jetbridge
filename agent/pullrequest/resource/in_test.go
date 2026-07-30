package resource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/pullrequest/resource"
)

func TestForgePRInRejectsStaleVersionBeforeGit(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	version := resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('d'), Cursor: "wrong", BindingRevision: "7"}
	var output bytes.Buffer
	err := resource.In(context.Background(), t.TempDir(), bytes.NewReader(checkInput(t, source, &version)), &output, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(_ context.Context, l pullrequest.Locator, c pullrequest.Cursor) (pullrequest.Observation, error) {
		return activeObservation(l, "actual"), nil
	})), Clock: func() time.Time { return now }, GitRunner: runnerFunc(func(context.Context, resource.GitCommand) error { t.Fatal("git must not run"); return nil })})
	if err == nil {
		t.Fatal("expected stale selection error")
	}
	if strings.Contains(err.Error(), source.ReadToken) {
		t.Fatal("error leaks token")
	}
}

func TestForgePRInWritesCurrentRecordAndExactRepositories(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: source.Repository, ExternalID: source.ExternalID}, "cursor")
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastReconciledAt: source.Monitor.LastReconciledAt})
	if err != nil || !ok {
		t.Fatalf("action = %#v, %t, %v", action, ok, err)
	}
	version := resource.Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: observation.SourceSHA, TargetSHA: observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: "7"}
	destination := filepath.Join(t.TempDir(), "out")
	var calls []resource.GitCommand
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		calls = append(calls, command)
		if command.Operation == "checkout" {
			if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600)
		}
		return nil
	})
	var stdout bytes.Buffer
	err = resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &stdout, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(_ context.Context, _ pullrequest.Locator, _ pullrequest.Cursor) (pullrequest.Observation, error) {
		return observation, nil
	})), Clock: func() time.Time { return now }, GitRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(destination, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"trigger":"review_batch"`) {
		t.Fatalf("record = %s", raw)
	}
	for _, name := range []string{"source-repository", "target-repository"} {
		if _, err := os.Stat(filepath.Join(destination, name, ".git", "HEAD")); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("git calls = %#v", calls)
	}
	if calls[0].Ref != observation.SourceRef || calls[0].SHA != observation.SourceSHA || calls[1].Ref != observation.TargetRef || calls[1].SHA != observation.TargetSHA {
		t.Fatalf("checkout commands = %#v", calls)
	}
	if strings.Contains(string(raw), source.ReadToken) {
		t.Fatal("record leaks token")
	}
	if strings.Contains(stdout.String(), source.ReadToken) {
		t.Fatal("stdout leaks token")
	}
	var result resource.InResult
	if err := json.Unmarshal([]byte(`{"version":`+mustJSON(t, version)+`}`), &result); err != nil {
		t.Fatal(err)
	}
}

func TestForgePRInRejectsStaleBindingBeforeObserver(t *testing.T) {
	source := testSource(time.Now().UTC())
	version := resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('d'), Cursor: "cursor", BindingRevision: "8"}
	err := resource.In(context.Background(), filepath.Join(t.TempDir(), "out"), bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: func(resource.Source) (pullrequest.Observer, error) {
		t.Fatal("observer must not be resolved")
		return nil, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "binding revision") {
		t.Fatalf("error = %v", err)
	}
}

func TestForgePRInSanitizesGitFailureAndRefusesUnsafeDestination(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: source.Repository, ExternalID: source.ExternalID}, "cursor")
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastReconciledAt: source.Monitor.LastReconciledAt})
	if err != nil || !ok {
		t.Fatal("failed to create test action")
	}
	version := resource.Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: observation.SourceSHA, TargetSHA: observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: "7"}
	deps := resource.Dependencies{Clock: func() time.Time { return now }, ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
		return observation, nil
	})), GitRunner: runnerFunc(func(context.Context, resource.GitCommand) error {
		return fmt.Errorf("remote rejected %s", source.ReadToken)
	})}
	err = resource.In(context.Background(), filepath.Join(t.TempDir(), "out"), bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, deps)
	if err == nil || strings.Contains(err.Error(), source.ReadToken) {
		t.Fatalf("unsafe error = %v", err)
	}
	unsafe := filepath.Join(t.TempDir(), "nonempty")
	if err := os.MkdirAll(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsafe, "old"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := resource.In(context.Background(), unsafe, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, deps); err == nil {
		t.Fatal("expected nonempty destination rejection")
	}
}

type runnerFunc func(context.Context, resource.GitCommand) error

func (fn runnerFunc) Run(ctx context.Context, command resource.GitCommand) error {
	return fn(ctx, command)
}
