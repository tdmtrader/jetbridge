package prmonitor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/functions/prmonitor"
	"github.com/concourse/concourse/agent/functions/repositorymerge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestMaterializeVerifiesAndCopiesExactPRRepositories(t *testing.T) {
	fixture := newMaterializeFixture(t)

	result, err := prmonitor.Materialize(
		context.Background(),
		fixture.request(),
	)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if result.Source.HeadSHA != fixture.sourceSHA ||
		result.Target.HeadSHA != fixture.targetSHA ||
		result.Source.RepositoryID != result.Target.RepositoryID {
		t.Fatalf("materialization result = %#v", result)
	}
	for name, want := range map[string]string{
		"source-output": fixture.sourceSHA,
		"target-output": fixture.targetSHA,
	} {
		output := filepath.Join(fixture.root, name)
		if got := materializeGit(t, output, "rev-parse", "HEAD"); got != want {
			t.Fatalf("%s HEAD = %s, want %s", name, got, want)
		}
		if got := materializeGit(t, output, "status", "--porcelain=v1"); got != "" {
			t.Fatalf("%s is dirty: %q", name, got)
		}
		if got := materializeGit(t, output, "remote"); got != "" {
			t.Fatalf("%s retained a remote: %q", name, got)
		}
	}
}

func TestMaterializeRejectsRepositoryHeadThatDoesNotMatchObservation(t *testing.T) {
	fixture := newMaterializeFixture(t)
	fixture.observation.Body.SourceSHA = materializeObjectID("other")
	fixture.rewriteObservation(t)

	_, err := prmonitor.Materialize(context.Background(), fixture.request())
	if err == nil || !strings.Contains(err.Error(), "source HEAD") {
		t.Fatalf("Materialize error = %v, want exact source-head rejection", err)
	}
	fixture.requireOutputsEmpty(t)
}

func TestMaterializeRejectsDirtyOrDifferentRepositories(t *testing.T) {
	t.Run("dirty", func(t *testing.T) {
		fixture := newMaterializeFixture(t)
		if err := os.WriteFile(
			filepath.Join(fixture.resourceRoot, "source-repository", "untracked.txt"),
			[]byte("not observed\n"),
			0600,
		); err != nil {
			t.Fatal(err)
		}
		fixture.refreshDigest(t)

		_, err := prmonitor.Materialize(context.Background(), fixture.request())
		if err == nil || !strings.Contains(err.Error(), "clean") {
			t.Fatalf("Materialize error = %v, want dirty-repository rejection", err)
		}
		fixture.requireOutputsEmpty(t)
	})

	t.Run("different repository", func(t *testing.T) {
		fixture := newMaterializeFixture(t)
		other := filepath.Join(t.TempDir(), "other")
		materializeInitRepo(t, other, "unrelated.txt", "unrelated\n")
		replaceDirectory(
			t,
			filepath.Join(fixture.resourceRoot, "target-repository"),
			other,
		)
		fixture.observation.Body.TargetSHA = materializeGit(t, other, "rev-parse", "HEAD")
		fixture.rewriteObservation(t)

		_, err := prmonitor.Materialize(context.Background(), fixture.request())
		if err == nil || !strings.Contains(err.Error(), "same repository") {
			t.Fatalf("Materialize error = %v, want repository-identity rejection", err)
		}
		fixture.requireOutputsEmpty(t)
	})
}

func TestMaterializeRejectsChangedOuterSnapshotAndNonemptyOutput(t *testing.T) {
	t.Run("changed outer snapshot", func(t *testing.T) {
		fixture := newMaterializeFixture(t)
		fixture.observationRef.Digest = materializeDigest("wrong outer")
		_, err := prmonitor.Materialize(context.Background(), fixture.request())
		if err == nil || !strings.Contains(err.Error(), "exact observation snapshot") {
			t.Fatalf("Materialize error = %v, want outer snapshot rejection", err)
		}
		fixture.requireOutputsEmpty(t)
	})

	t.Run("nonempty output", func(t *testing.T) {
		fixture := newMaterializeFixture(t)
		if err := os.WriteFile(
			filepath.Join(fixture.root, "source-output", "occupied"),
			[]byte("do not replace"),
			0600,
		); err != nil {
			t.Fatal(err)
		}
		_, err := prmonitor.Materialize(context.Background(), fixture.request())
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("Materialize error = %v, want nonempty-output rejection", err)
		}
	})

	t.Run("symlinked output root", func(t *testing.T) {
		fixture := newMaterializeFixture(t)
		external := t.TempDir()
		if err := os.Remove(fixture.sourceOutput); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, fixture.sourceOutput); err != nil {
			t.Fatal(err)
		}

		_, err := prmonitor.Materialize(context.Background(), fixture.request())
		if err == nil || !strings.Contains(err.Error(), "output root") {
			t.Fatalf("Materialize error = %v, want symlinked-root rejection", err)
		}
		if entries, readErr := os.ReadDir(external); readErr != nil {
			t.Fatal(readErr)
		} else if len(entries) != 0 {
			t.Fatalf("external output was modified: %#v", entries)
		}
	})
}

type materializeFixture struct {
	root           string
	resourceRoot   string
	observationRef snapshot.SnapshotRef
	observation    contracts.Record[contracts.PullRequestBody]
	sourceSHA      string
	targetSHA      string
	canonicalizer  snapshot.Canonicalizer
	sourceOutput   string
	targetOutput   string
}

func newMaterializeFixture(t *testing.T) *materializeFixture {
	t.Helper()
	root := t.TempDir()
	resourceRoot := filepath.Join(root, "pull-request")
	sourceOutput := filepath.Join(root, "source-output")
	targetOutput := filepath.Join(root, "target-output")
	for _, directory := range []string{
		resourceRoot, sourceOutput, targetOutput,
	} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(t.TempDir(), "base")
	materializeInitRepo(t, base, "base.txt", "base\n")
	source := filepath.Join(resourceRoot, "source-repository")
	materializeGit(t, filepath.Dir(source), "clone", "--no-hardlinks", base, source)
	materializeGit(t, source, "remote", "remove", "origin")
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	materializeGit(t, source, "add", ".")
	materializeGit(t, source, "commit", "-m", "source change")
	target := filepath.Join(resourceRoot, "target-repository")
	materializeGit(t, filepath.Dir(target), "clone", "--no-hardlinks", base, target)
	materializeGit(t, target, "remote", "remove", "origin")
	if err := os.WriteFile(filepath.Join(target, "target.txt"), []byte("target\n"), 0600); err != nil {
		t.Fatal(err)
	}
	materializeGit(t, target, "add", ".")
	materializeGit(t, target, "commit", "-m", "target change")
	sourceSHA := materializeGit(t, source, "rev-parse", "HEAD")
	targetSHA := materializeGit(t, target, "rev-parse", "HEAD")

	observation, err := contracts.NewRecord(
		snapshot.TypeRef("pull-request/v1"),
		nil,
		contracts.PullRequestBody{
			Provider:     "github",
			Repository:   "acme/widget",
			ExternalID:   "17",
			URL:          "https://github.example/acme/widget/pull/17",
			State:        contracts.PullRequestActive,
			Mergeability: contracts.PullRequestMergeable,
			SourceRef:    "refs/heads/agent/change",
			SourceSHA:    sourceSHA,
			TargetRef:    "refs/heads/main",
			TargetSHA:    targetSHA,
			Iteration:    "iteration-1",
			Trigger:      contracts.PullRequestConflictTrigger,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &materializeFixture{
		root:          root,
		resourceRoot:  resourceRoot,
		observation:   observation,
		sourceSHA:     sourceSHA,
		targetSHA:     targetSHA,
		canonicalizer: snapshot.Canonicalizer{TempDir: t.TempDir()},
		sourceOutput:  sourceOutput,
		targetOutput:  targetOutput,
	}
	fixture.rewriteObservation(t)
	return fixture
}

func (fixture *materializeFixture) request() prmonitor.MaterializeRequest {
	return prmonitor.MaterializeRequest{
		Observation:      fixture.observationRef,
		ObservationInput: "pull-request",
		ObservationRoot:  fixture.resourceRoot,
		SourceOutput:     fixture.sourceOutput,
		TargetOutput:     fixture.targetOutput,
		Canonicalizer:    fixture.canonicalizer,
	}
}

func (fixture *materializeFixture) rewriteObservation(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(fixture.observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.resourceRoot, "record.json"),
		raw,
		0600,
	); err != nil {
		t.Fatal(err)
	}
	fixture.refreshDigest(t)
}

func (fixture *materializeFixture) refreshDigest(t *testing.T) {
	t.Helper()
	tree, err := repositorymerge.CaptureDirectory(
		context.Background(),
		fixture.canonicalizer,
		fixture.resourceRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	fixture.observationRef = snapshot.SnapshotRef{
		ID: 1, Type: "pull-request/v1", Digest: tree.Digest,
	}
}

func (fixture *materializeFixture) requireOutputsEmpty(t *testing.T) {
	t.Helper()
	for _, output := range []string{fixture.sourceOutput, fixture.targetOutput} {
		entries, err := os.ReadDir(output)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("output %s is not empty: %#v", output, entries)
		}
	}
}

func materializeInitRepo(t *testing.T, directory, name, body string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	materializeGit(t, directory, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	materializeGit(t, directory, "add", ".")
	materializeGit(t, directory, "commit", "-m", "initial")
}

func replaceDirectory(t *testing.T, target, source string) {
	t.Helper()
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	materializeGit(t, filepath.Dir(target), "clone", "--no-hardlinks", source, target)
	materializeGit(t, target, "remote", "remove", "origin")
}

func materializeGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Jetbridge Test",
		"GIT_AUTHOR_EMAIL=jetbridge@example.invalid",
		"GIT_COMMITTER_NAME=Jetbridge Test",
		"GIT_COMMITTER_EMAIL=jetbridge@example.invalid",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func materializeDigest(seed string) snapshot.Digest {
	sum := sha256.Sum256([]byte(seed))
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func materializeObjectID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:20])
}
