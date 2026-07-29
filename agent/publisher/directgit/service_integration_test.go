package directgit

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/publishertest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

type fixedChangeInspector struct {
	change publisher.RepositoryChange
}

func (inspector fixedChangeInspector) Inspect(
	context.Context,
	publisher.Request,
) (publisher.RepositoryChange, error) {
	return inspector.change, nil
}

type crashAfterSuccessfulPushBackend struct {
	delegate  publisher.GitBackend
	publishes int
	crashNext bool
}

func (backend *crashAfterSuccessfulPushBackend) Lookup(
	ctx context.Context,
	credential publisher.Credential,
	operationKey string,
) (publisher.GitResult, bool, error) {
	return backend.delegate.Lookup(ctx, credential, operationKey)
}

func (backend *crashAfterSuccessfulPushBackend) CurrentBase(
	ctx context.Context,
	credential publisher.Credential,
	destination string,
	targetBranch string,
) (string, error) {
	return backend.delegate.CurrentBase(ctx, credential, destination, targetBranch)
}

func (backend *crashAfterSuccessfulPushBackend) Publish(
	ctx context.Context,
	credential publisher.Credential,
	operation publisher.GitOperation,
) (publisher.GitResult, error) {
	backend.publishes++
	result, err := backend.delegate.Publish(ctx, credential, operation)
	if err != nil {
		return publisher.GitResult{}, err
	}
	if backend.crashNext {
		backend.crashNext = false
		return publisher.GitResult{}, context.DeadlineExceeded
	}
	return result, nil
}

func TestGitServiceReconcilesARealAtomicRemoteAfterCrashWithoutASecondPush(t *testing.T) {
	fixture := newServiceGitFixture(t)
	runner, err := NewCommandRunner(fixture.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := NewBackend(runner, fixture.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	backend := &crashAfterSuccessfulPushBackend{
		delegate: direct, crashNext: true,
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	request := servicePublicationRequest(publisher.ModeBranch, fixture.base)
	service, err := publisher.NewGitService(
		publishertest.NewMemoryStore(func() time.Time { return now }),
		serviceCredentialProvider(t, fixture.remote, publisher.ModeBranch),
		fixedChangeInspector{change: publisher.RepositoryChange{
			BaseSHA: fixture.base, ResultSHA: fixture.result, MaterializedRoot: fixture.changeRoot,
		}},
		backend,
		time.Minute,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	operationKey, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	marker := publicationMarkerPrefix + strings.TrimPrefix(operationKey, "sha256:")

	if _, err := service.Execute(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("simulated crash = %v, want deadline exceeded", err)
	}
	for _, ref := range []string{"refs/heads/agent/change-7", marker} {
		if got := runGitTest(t, "", "--git-dir="+fixture.remotePath, "rev-parse", ref); got != fixture.result {
			t.Fatalf("post-crash %s = %s, want %s", ref, got, fixture.result)
		}
	}

	now = now.Add(2 * time.Minute)
	reconciled, err := service.Execute(context.Background(), request)
	if err != nil || reconciled.Status != publisher.StatusSucceeded ||
		reconciled.Attempt != 2 || reconciled.Result.HeadSHA != fixture.result {
		t.Fatalf("reconciled publication = (%+v, %v)", reconciled, err)
	}
	if backend.publishes != 1 {
		t.Fatalf("provider pushes = %d, want exactly one", backend.publishes)
	}
	for _, ref := range []string{"refs/heads/agent/change-7", marker} {
		if got := runGitTest(t, "", "--git-dir="+fixture.remotePath, "rev-parse", ref); got != fixture.result {
			t.Fatalf("reconciled %s = %s, want %s", ref, got, fixture.result)
		}
	}
}

func TestGitServiceTurnsARealTargetHeadRaceIntoTerminalStaleBase(t *testing.T) {
	fixture := newServiceGitFixture(t)
	commandRunner, err := NewCommandRunner(fixture.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	runner := &mutateBeforePushRunner{
		delegate: commandRunner,
		mutate: func() {
			runGitTest(
				t,
				"",
				"--git-dir="+fixture.remotePath,
				"update-ref",
				"refs/heads/main",
				fixture.racer,
				fixture.base,
			)
		},
	}
	backend, err := NewBackend(runner, fixture.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	request := servicePublicationRequest(publisher.ModeMerge, fixture.base)
	service, err := publisher.NewGitService(
		publishertest.NewMemoryStore(time.Now),
		serviceCredentialProvider(t, fixture.remote, publisher.ModeMerge),
		fixedChangeInspector{change: publisher.RepositoryChange{
			BaseSHA: fixture.base, ResultSHA: fixture.result, MaterializedRoot: fixture.changeRoot,
		}},
		backend,
		time.Minute,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	operationKey, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	marker := publicationMarkerPrefix + strings.TrimPrefix(operationKey, "sha256:")

	publication, err := service.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusStaleBase ||
		publication.Result.HeadSHA != fixture.result ||
		!strings.Contains(publication.Result.Detail, "no publication refs were committed") {
		t.Fatalf("target race = (%+v, %v)", publication, err)
	}
	if got := runGitTest(t, "", "--git-dir="+fixture.remotePath, "rev-parse", "refs/heads/main"); got != fixture.racer {
		t.Fatalf("target after race = %s, want concurrent head %s", got, fixture.racer)
	}
	if gitRefExists(t, fixture.remotePath, marker) {
		t.Fatal("target race committed its publication marker")
	}

	replayed, err := service.Execute(context.Background(), request)
	if err != nil || replayed.Status != publisher.StatusStaleBase {
		t.Fatalf("terminal stale replay = (%+v, %v)", replayed, err)
	}
}

type serviceGitFixture struct {
	tempDir    string
	remote     string
	remotePath string
	changeRoot string
	base       string
	result     string
	racer      string
}

func newServiceGitFixture(t *testing.T) serviceGitFixture {
	t.Helper()
	tempDir := t.TempDir()
	repository := filepath.Join(tempDir, "result")
	remotePath := filepath.Join(tempDir, "remote.git")
	runGitTest(t, "", "init", repository)
	runGitTest(t, repository, "config", "user.name", "Publisher Integration")
	runGitTest(t, repository, "config", "user.email", "publisher@example.test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "commit", "-m", "base")
	base := runGitTest(t, repository, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "commit", "-am", "result")
	result := runGitTest(t, repository, "rev-parse", "HEAD")
	resultTree := runGitTest(t, repository, "rev-parse", "HEAD^{tree}")

	runGitTest(t, repository, "checkout", "--detach", base)
	if err := os.WriteFile(filepath.Join(repository, "RACER.md"), []byte("concurrent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "RACER.md")
	runGitTest(t, repository, "commit", "-m", "concurrent target")
	racer := runGitTest(t, repository, "rev-parse", "HEAD")
	runGitTest(t, repository, "checkout", "--detach", result)

	changeRoot := repositoryChangeFromGit(t, tempDir, repository, base, result, resultTree)
	runGitTest(t, "", "init", "--bare", remotePath)
	remote := (&url.URL{Scheme: "file", Path: filepath.ToSlash(remotePath)}).String()
	runGitTest(t, repository, "push", remote, base+":refs/heads/main")
	runGitTest(t, repository, "push", remote, racer+":refs/concourse/test/racer")
	return serviceGitFixture{
		tempDir: tempDir, remote: remote, remotePath: remotePath,
		changeRoot: changeRoot, base: base, result: result, racer: racer,
	}
}

func serviceCredentialProvider(
	t *testing.T,
	remote string,
	mode publisher.Mode,
) publisher.CredentialProvider {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(root, "git-token")
	if err := os.WriteFile(credentialPath, []byte("unused-local-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := publisher.NewFileCredentialProvider(
		publisher.Policy{
			SchemaVersion: 1,
			Rules: []publisher.PolicyRule{{
				Team: "main", Publisher: publisher.GitPublisher, Mode: mode,
				ApprovalPolicyVersion: "engineering/v1", TargetBranch: "main",
				Destination: "approved-repository", Adapter: publisher.AdapterDirectGit,
				CredentialReference: "git-token", RemoteURL: remote,
			}},
		},
		root,
		map[string]string{"git-token": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func servicePublicationRequest(mode publisher.Mode, base string) publisher.Request {
	request := publisher.Request{
		Publisher: publisher.GitPublisher,
		Input: snapshot.SnapshotRef{
			ID: 1, Type: "repository-change/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("3", 64)),
		},
		Destination:           "approved-repository",
		Mode:                  mode,
		ApprovalPolicyVersion: "engineering/v1",
		Authority: publisher.Authority{
			TeamID: 1, TeamName: "main", BuildID: 2,
			WorkflowRunID: 3, Actor: "alice",
		},
	}
	switch mode {
	case publisher.ModeBranch:
		request.Parameters = map[string]string{
			"source_branch": "agent/change-7",
			"target_branch": "main",
		}
	case publisher.ModeMerge:
		request.Parameters = map[string]string{
			"target_branch":              "main",
			publisher.MergeBaseParameter: base,
		}
		request.ApprovedBy = "alice"
		request.Approval = &publisher.ApprovalEvidence{
			WaitID: workflowwait.ID(11),
			Question: snapshot.SnapshotRef{
				ID: 101, Type: "question/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64)),
			},
			Answer: snapshot.SnapshotRef{
				ID: 102, Type: "human-answer/v1",
				Digest: snapshot.Digest("sha256:" + strings.Repeat("5", 64)),
			},
			ResolvedBy: "alice",
			ResolvedAt: time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC),
		}
	}
	return request
}
