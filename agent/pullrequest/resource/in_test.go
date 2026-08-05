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
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// The version identifies which observation the server selected; it is not a
// promise the provider stood still. The admit job is Serial so builds queue,
// and Concourse consumes a version at build start regardless of outcome, so
// refusing a moved observation here burned the event instead of retrying it.
func TestForgePRInMaterializesWhenTheObservationMovedSinceCheck(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	version := resource.Version{Provider: "github", ExternalID: "42", SourceSHA: sha('a'), TargetSHA: sha('b'), ActionKind: "review_batch", ActionDigest: digest('d'), Cursor: "wrong", BindingRevision: "7"}
	gitRan := false
	var output bytes.Buffer
	err := resource.In(context.Background(), t.TempDir(), bytes.NewReader(checkInput(t, source, &version)), &output, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(_ context.Context, l pullrequest.Locator, c pullrequest.Cursor) (pullrequest.Observation, error) {
		return activeObservation(l, "actual"), nil
	})), Clock: func() time.Time { return now }, GitRunner: runnerFunc(func(context.Context, resource.GitCommand) error { gitRan = true; return nil })})
	if !gitRan {
		t.Fatalf("git did not run: the moved observation was rejected before materialization (err = %v)", err)
	}
	if err != nil && strings.Contains(err.Error(), source.ReadToken) {
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
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
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
	if calls[0].FetchMode != resource.GitFetchNamedRef || calls[1].FetchMode != resource.GitFetchNamedRef {
		t.Fatalf("active pull request did not retain named-ref lease: %#v", calls)
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

func TestForgePRInPreservesDestinationMountAndRollsBackMidInstall(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: source.Repository, ExternalID: source.ExternalID}, "cursor")
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastReconciledAt: source.Monitor.LastReconciledAt})
	if err != nil || !ok {
		t.Fatal("failed to create test action")
	}
	version := resource.Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: observation.SourceSHA, TargetSHA: observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: "7"}
	destination := filepath.Join(t.TempDir(), "mounted-output")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer descriptor.Close()
	calls := 0
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		calls++
		if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600); err != nil {
			return err
		}
		if calls == 2 {
			blocker := filepath.Join(destination, "target-repository")
			if err := os.Mkdir(blocker, 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(blocker, "block"), []byte("block"), 0600)
		}
		return nil
	})
	err = resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
		return observation, nil
	})), Clock: func() time.Time { return now }, GitRunner: runner})
	if err == nil {
		t.Fatal("expected mid-install failure")
	}
	after, statErr := os.Stat(destination)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !os.SameFile(before, after) {
		t.Fatal("In replaced the caller-owned destination inode")
	}
	if descriptorInfo, err := descriptor.Stat(); err != nil || !os.SameFile(before, descriptorInfo) {
		t.Fatalf("mounted descriptor changed: %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed In left destination entries: %v", entries)
	}
}

func TestForgePRInRejectsDestinationSwapBeforeGitWithoutOutsideWrites(t *testing.T) {
	now, source, observation, version := currentInFixture(t)
	parent := t.TempDir()
	destination := filepath.Join(parent, "out")
	movedDestination := filepath.Join(parent, "moved-out")
	outside := filepath.Join(parent, "outside")
	for _, directory := range []string{destination, outside} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	outsideSource := filepath.Join(outside, "source-repository")
	if err := os.Mkdir(outsideSource, 0700); err != nil {
		t.Fatal(err)
	}
	outsideMarker := filepath.Join(outsideSource, "belongs-to-outside")
	if err := os.WriteFile(outsideMarker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	gitCalled := false
	dependencies := resource.Dependencies{
		ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
			return observation, nil
		})),
		Clock: func() time.Time { return now },
		BeforeGit: func(resource.GitCommand) error {
			if err := os.Rename(destination, movedDestination); err != nil {
				return err
			}
			return os.Symlink(outside, destination)
		},
		GitRunner: runnerFunc(func(_ context.Context, command resource.GitCommand) error {
			gitCalled = true
			if err := os.MkdirAll(command.Directory, 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(command.Directory, "outside-write"), []byte("unsafe"), 0600)
		}),
	}

	err := resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, dependencies)
	if err == nil {
		t.Fatal("expected destination identity error")
	}
	if gitCalled {
		t.Fatal("git ran after the destination path stopped naming the retained directory")
	}
	raw, err := os.ReadFile(outsideMarker)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("destination swap deleted outside data: %q, %v", raw, err)
	}
	if _, err := os.Lstat(filepath.Join(outsideSource, "outside-write")); !os.IsNotExist(err) {
		t.Fatalf("destination swap wrote outside retained destination: %v", err)
	}
	movedEntries, err := os.ReadDir(movedDestination)
	if err != nil {
		t.Fatal(err)
	}
	if len(movedEntries) != 0 {
		t.Fatalf("destination swap left owned output behind: %v", movedEntries)
	}
}

func TestForgePRInDoesNotDeleteCollidingOutputItDidNotCreate(t *testing.T) {
	now, source, observation, version := currentInFixture(t)
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(destination, "target-repository")
	dependencies := resource.Dependencies{
		ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
			return observation, nil
		})),
		Clock: func() time.Time { return now },
		BeforeMaterialize: func() error {
			if err := os.Mkdir(blocker, 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(blocker, "belongs-to-another-writer"), []byte("keep"), 0600)
		},
		GitRunner: runnerFunc(func(context.Context, resource.GitCommand) error {
			t.Fatal("git must not run after an output-name collision")
			return nil
		}),
	}

	err := resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, dependencies)
	if err == nil {
		t.Fatal("expected output-name collision")
	}
	raw, readErr := os.ReadFile(filepath.Join(blocker, "belongs-to-another-writer"))
	if readErr != nil {
		t.Fatalf("colliding output was deleted: %v", readErr)
	}
	if string(raw) != "keep" {
		t.Fatalf("colliding output changed: %q", raw)
	}
	if _, statErr := os.Lstat(filepath.Join(destination, "source-repository")); !os.IsNotExist(statErr) {
		t.Fatalf("owned source output was not rolled back: %v", statErr)
	}
}

func TestForgePRInPublishesRecordWithoutReplacingACollision(t *testing.T) {
	now, source, observation, version := currentInFixture(t)
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(destination, "record.json")
	dependencies := resource.Dependencies{
		ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
			return observation, nil
		})),
		Clock: func() time.Time { return now },
		BeforeMaterialize: func() error {
			return os.WriteFile(recordPath, []byte("belongs-to-another-writer"), 0600)
		},
		GitRunner: runnerFunc(func(_ context.Context, command resource.GitCommand) error {
			if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600)
		}),
	}

	err := resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, dependencies)
	if err == nil {
		t.Fatal("expected record-name collision")
	}
	raw, readErr := os.ReadFile(recordPath)
	if readErr != nil {
		t.Fatalf("colliding record was deleted: %v", readErr)
	}
	if string(raw) != "belongs-to-another-writer" {
		t.Fatalf("colliding record changed: %q", raw)
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "record.json" {
		t.Fatalf("owned output was not rolled back around record collision: %v", entries)
	}
}

func TestForgePRInRejectsUnexpectedFinalDestinationMember(t *testing.T) {
	now, source, observation, version := currentInFixture(t)
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		calls++
		if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600); err != nil {
			return err
		}
		if calls == 2 {
			return os.WriteFile(filepath.Join(destination, "belongs-to-another-writer"), []byte("keep"), 0600)
		}
		return nil
	})
	err := resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{
		ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
			return observation, nil
		})),
		Clock:     func() time.Time { return now },
		GitRunner: runner,
	})
	if err == nil {
		t.Fatal("unexpected top-level destination member was accepted")
	}
	raw, readErr := os.ReadFile(filepath.Join(destination, "belongs-to-another-writer"))
	if readErr != nil || string(raw) != "keep" {
		t.Fatalf("foreign destination member was altered: %q, %v", raw, readErr)
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "belongs-to-another-writer" {
		t.Fatalf("owned output was not rolled back around foreign member: %v", entries)
	}
}

func TestForgePRInRechecksPublishedRecordAfterProtocolWrite(t *testing.T) {
	now, source, observation, version := currentInFixture(t)
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(destination, "record.json")
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600)
	})
	stdout := writerFunc(func(payload []byte) (int, error) {
		if err := os.Remove(recordPath); err != nil {
			return 0, err
		}
		if err := os.WriteFile(recordPath, []byte("belongs-to-another-writer"), 0600); err != nil {
			return 0, err
		}
		return len(payload), nil
	})
	err := resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), stdout, &bytes.Buffer{}, resource.Dependencies{
		ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
			return observation, nil
		})),
		Clock:     func() time.Time { return now },
		GitRunner: runner,
	})
	if err == nil {
		t.Fatal("record replacement across the protocol boundary was accepted")
	}
	raw, readErr := os.ReadFile(recordPath)
	if readErr != nil || string(raw) != "belongs-to-another-writer" {
		t.Fatalf("replacement record was altered: %q, %v", raw, readErr)
	}
	for _, name := range []string{"source-repository", "target-repository"} {
		if _, statErr := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(statErr) {
			t.Fatalf("owned %s was not rolled back: %v", name, statErr)
		}
	}
}

func TestForgePRInUsesExactObjectFetchForTerminalObservation(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: source.Repository, ExternalID: source.ExternalID}, "terminal-cursor")
	observation.State = contracts.PullRequestCompleted
	observation.ReviewBatches = nil
	observation.Threads = nil
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastReconciledAt: source.Monitor.LastReconciledAt})
	if err != nil || !ok {
		t.Fatalf("terminal action = %#v, %t, %v", action, ok, err)
	}
	version := resource.Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: observation.SourceSHA, TargetSHA: observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: "7"}
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	var calls []resource.GitCommand
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		calls = append(calls, command)
		if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600)
	})
	err = resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
		return observation, nil
	})), Clock: func() time.Time { return now }, GitRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("git calls = %#v", calls)
	}
	for _, command := range calls {
		if command.FetchMode != resource.GitFetchExactObject || command.Ref != "" {
			t.Fatalf("terminal checkout still depends on branch ref: %#v", command)
		}
	}
}

func TestForgePRInRollsBackPublishedTreeWhenProtocolWriteFails(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: source.Repository, ExternalID: source.ExternalID}, "cursor")
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastReconciledAt: source.Monitor.LastReconciledAt})
	if err != nil || !ok {
		t.Fatal("failed to create test action")
	}
	version := resource.Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: observation.SourceSHA, TargetSHA: observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: "7"}
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		if err := os.MkdirAll(filepath.Join(command.Directory, ".git"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(command.Directory, ".git", "HEAD"), []byte(command.SHA+"\n"), 0600)
	})
	err = resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), errorWriter{}, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
		return observation, nil
	})), Clock: func() time.Time { return now }, GitRunner: runner})
	if err == nil {
		t.Fatal("expected protocol write error")
	}
	after, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("protocol failure replaced destination inode")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("protocol failure left authoritative output: %v", entries)
	}
}

func TestForgePRInCleansDestinationWhenRepositoryExceedsBounds(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: source.Repository, ExternalID: source.ExternalID}, "cursor")
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{Now: now, PollInterval: 5 * time.Minute, FreshnessInterval: 6 * time.Hour, LastReconciledAt: source.Monitor.LastReconciledAt})
	if err != nil || !ok {
		t.Fatal("failed to create test action")
	}
	version := resource.Version{Provider: source.Provider, ExternalID: source.ExternalID, SourceSHA: observation.SourceSHA, TargetSHA: observation.TargetSHA, ActionKind: string(action.Kind), ActionDigest: action.Digest, Cursor: string(action.Cursor), BindingRevision: "7"}
	destination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	runner := runnerFunc(func(_ context.Context, command resource.GitCommand) error {
		packDirectory := filepath.Join(command.Directory, ".git", "objects", "pack")
		if err := os.MkdirAll(packDirectory, 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(packDirectory, "oversized.pack"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err := file.Truncate(snapshot.DefaultMaxSnapshotContentBytes + 1); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
	err = resource.In(context.Background(), destination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: fixedObserver(observerFunc(func(context.Context, pullrequest.Locator, pullrequest.Cursor) (pullrequest.Observation, error) {
		return observation, nil
	})), Clock: func() time.Time { return now }, GitRunner: runner})
	if err == nil || !strings.Contains(err.Error(), "content bounds") {
		t.Fatalf("bounds error = %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bounds failure left destination entries: %v", entries)
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
	failedDestination := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(failedDestination, 0700); err != nil {
		t.Fatal(err)
	}
	err = resource.In(context.Background(), failedDestination, bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, deps)
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
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(unsafe), link); err != nil {
		t.Fatal(err)
	}
	if err := resource.In(context.Background(), filepath.Join(link, "out"), bytes.NewReader(checkInput(t, source, &version)), &bytes.Buffer{}, &bytes.Buffer{}, deps); err == nil {
		t.Fatal("expected symlink parent rejection")
	}
}

type runnerFunc func(context.Context, resource.GitCommand) error

func (fn runnerFunc) Run(ctx context.Context, command resource.GitCommand) error {
	return fn(ctx, command)
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("write failed") }

type writerFunc func([]byte) (int, error)

func (function writerFunc) Write(value []byte) (int, error) {
	return function(value)
}

func currentInFixture(t *testing.T) (time.Time, resource.Source, pullrequest.Observation, resource.Version) {
	t.Helper()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := testSource(now)
	observation := activeObservation(pullrequest.Locator{
		Provider:   pullrequest.ProviderGitHub,
		Repository: source.Repository,
		ExternalID: source.ExternalID,
	}, "cursor")
	action, ok, err := pullrequest.ActionFor(observation, pullrequest.TriggerPolicy{
		Now:               now,
		PollInterval:      5 * time.Minute,
		FreshnessInterval: 6 * time.Hour,
		LastReconciledAt:  source.Monitor.LastReconciledAt,
	})
	if err != nil || !ok {
		t.Fatalf("failed to create test action: %#v, %t, %v", action, ok, err)
	}
	version := resource.Version{
		Provider:        source.Provider,
		ExternalID:      source.ExternalID,
		SourceSHA:       observation.SourceSHA,
		TargetSHA:       observation.TargetSHA,
		ActionKind:      string(action.Kind),
		ActionDigest:    action.Digest,
		Cursor:          string(action.Cursor),
		BindingRevision: "7",
	}
	return now, source, observation, version
}
