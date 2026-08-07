package directgit

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	testBaseSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testResultSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testTreeSHA   = "cccccccccccccccccccccccccccccccccccccccc"
	testOperation = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []Command
	run   func(context.Context, Command) (CommandResult, error)
}

type mutateBeforePushRunner struct {
	delegate Runner
	once     sync.Once
	mutate   func()
}

func (runner *mutateBeforePushRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	if len(command.Args) > 0 &&
		command.Args[0] == "push" &&
		!slices.Contains(command.Args, "--dry-run") {
		runner.once.Do(runner.mutate)
	}
	return runner.delegate.Run(ctx, command)
}

func (runner *fakeRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	runner.mu.Lock()
	copy := command.clone()
	runner.calls = append(runner.calls, copy)
	runner.mu.Unlock()
	if runner.run == nil {
		return CommandResult{}, nil
	}
	return runner.run(ctx, command)
}

func (runner *fakeRunner) Commands() []Command {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	commands := make([]Command, len(runner.calls))
	for index := range runner.calls {
		commands[index] = runner.calls[index].clone()
	}
	return commands
}

func TestCurrentBaseUsesThePolicyRemoteAndExactTargetRef(t *testing.T) {
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("narrow-secret"))
	runner := &fakeRunner{}
	runner.run = func(_ context.Context, command Command) (CommandResult, error) {
		switch {
		case slices.Equal(command.Args, []string{"check-ref-format", "refs/heads/main"}):
			return CommandResult{}, nil
		case slices.Equal(command.Args, []string{
			"ls-remote", "--exit-code", "--refs", remote, "refs/heads/main",
		}):
			if !command.NoRepository {
				t.Fatal("remote base lookup permitted repository-local configuration")
			}
			if string(command.Credential) != "narrow-secret" {
				t.Fatalf("credential = %q", command.Credential)
			}
			return CommandResult{
				Stdout: testBaseSHA + "\trefs/heads/main\n",
			}, nil
		default:
			t.Fatalf("unexpected command: %#v", command)
			return CommandResult{}, nil
		}
	}
	backend := newTestBackend(t, runner)

	got, err := backend.CurrentBase(context.Background(), credential, "authored.example/wrong", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got != testBaseSHA {
		t.Fatalf("CurrentBase = %q, want %q", got, testBaseSHA)
	}
	if commands := runner.Commands(); len(commands) != 2 {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestCurrentBaseRejectsAnotherAdapterBeforeRunningGit(t *testing.T) {
	runner := &fakeRunner{}
	backend := newTestBackend(t, runner)
	credential := resolvedCredentialForAdapter(
		t,
		"file:///srv/git/approved.git",
		[]byte("secret"),
		publisher.AdapterGateway,
	)

	if _, err := backend.CurrentBase(context.Background(), credential, "ignored", "main"); err == nil {
		t.Fatal("credential authorized for another adapter was accepted")
	}
	if len(runner.Commands()) != 0 {
		t.Fatalf("wrong adapter ran commands: %#v", runner.Commands())
	}
}

func TestCurrentBaseIgnoresAmbientRepositoryURLRewrites(t *testing.T) {
	tempDir := t.TempDir()
	repository := filepath.Join(tempDir, "source")
	approvedPath := filepath.Join(tempDir, "approved.git")
	redirectedPath := filepath.Join(tempDir, "redirected.git")
	ambient := filepath.Join(tempDir, "ambient")
	runGitTest(t, "", "init", repository)
	runGitTest(t, repository, "config", "user.name", "Publisher Test")
	runGitTest(t, repository, "config", "user.email", "publisher@example.test")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "commit", "-m", "approved")
	approvedSHA := runGitTest(t, repository, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("redirected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "commit", "-am", "redirected")
	redirectedSHA := runGitTest(t, repository, "rev-parse", "HEAD")

	runGitTest(t, "", "init", "--bare", approvedPath)
	runGitTest(t, "", "init", "--bare", redirectedPath)
	approvedRemote := (&url.URL{Scheme: "file", Path: filepath.ToSlash(approvedPath)}).String()
	redirectedRemote := (&url.URL{Scheme: "file", Path: filepath.ToSlash(redirectedPath)}).String()
	runGitTest(t, repository, "push", approvedRemote, approvedSHA+":refs/heads/main")
	runGitTest(t, repository, "push", redirectedRemote, redirectedSHA+":refs/heads/main")

	runGitTest(t, "", "init", ambient)
	runGitTest(t, ambient, "config", "url."+redirectedRemote+".insteadOf", approvedRemote)
	scratch := filepath.Join(ambient, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(ambient)

	runner, err := NewCommandRunner(scratch)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(runner, scratch)
	if err != nil {
		t.Fatal(err)
	}
	got, err := backend.CurrentBase(
		context.Background(),
		resolvedCredential(t, approvedRemote, []byte("unused-local-secret")),
		"ignored",
		"main",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != approvedSHA {
		t.Fatalf("CurrentBase = %s, want approved remote %s (redirect target was %s)", got, approvedSHA, redirectedSHA)
	}
}

func TestPublishRejectsGitObjectAlternatesBeforeAnySourceRepositoryGitOperation(t *testing.T) {
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("secret"))
	marker := publicationMarkerPrefix + strings.TrimPrefix(testOperation, "sha256:")
	targetRef := "refs/heads/agent/change-7"

	for _, test := range []struct {
		name      string
		entryName string
		typeflag  byte
		target    string
	}{
		{name: "regular alternates", entryName: ".git/objects/info/alternates", typeflag: tar.TypeReg, target: "/outside/objects\n"},
		{name: "directory alternates", entryName: ".git/objects/info/alternates", typeflag: tar.TypeDir},
		{name: "symlink alternates", entryName: ".git/objects/info/alternates", typeflag: tar.TypeSymlink, target: "elsewhere"},
		{name: "regular http alternates", entryName: ".git/objects/info/http-alternates", typeflag: tar.TypeReg, target: "https://outside.example/objects\n"},
		{name: "directory http alternates", entryName: ".git/objects/info/http-alternates", typeflag: tar.TypeDir},
		{name: "symlink http alternates", entryName: ".git/objects/info/http-alternates", typeflag: tar.TypeSymlink, target: "elsewhere"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := repositoryChangeFixtureFromPayload(t, tarBytesWithAlternate(t, test.entryName, test.typeflag, test.target))
			runner := publishingRunner(t, remote, targetRef, marker, false)
			backend := newTestBackend(t, runner)

			_, err := backend.Publish(
				context.Background(),
				credential,
				fixture.operation(publisher.ModeBranch, map[string]string{
					"source_branch": "agent/change-7",
					"target_branch": "main",
				}),
			)
			if err == nil || !strings.Contains(err.Error(), "alternate object storage") {
				t.Fatalf("Publish() error = %v, want alternate-object rejection", err)
			}
			for _, command := range runner.Commands() {
				if command.Dir != "" {
					t.Fatalf("source repository Git command ran after alternate-object rejection: %#v", command)
				}
			}
		})
	}
}

func TestBackendRejectsARealSealedRepositoryWithAlternateObjectStorage(t *testing.T) {
	tempDir := t.TempDir()
	repository := filepath.Join(tempDir, "result")
	external := filepath.Join(tempDir, "external")
	remotePath := filepath.Join(tempDir, "remote.git")
	runGitTest(t, "", "init", repository)
	runGitTest(t, repository, "config", "user.name", "Publisher Test")
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

	runGitTest(t, "", "init", external)
	runGitTest(t, external, "config", "user.name", "Publisher Test")
	runGitTest(t, external, "config", "user.email", "publisher@example.test")
	if err := os.WriteFile(filepath.Join(external, "external.txt"), []byte("external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, external, "add", "external.txt")
	runGitTest(t, external, "commit", "-m", "external")
	alternate := filepath.Join(repository, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternate), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alternate, []byte(filepath.Join(external, ".git", "objects")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changeRoot := repositoryChangeFromGit(t, tempDir, repository, base, result, resultTree)
	runGitTest(t, "", "init", "--bare", remotePath)
	remote := (&url.URL{Scheme: "file", Path: filepath.ToSlash(remotePath)}).String()
	runner, err := NewCommandRunner(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(runner, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	operationKey := "sha256:" + strings.Repeat("1", 64)
	marker := publicationMarkerPrefix + strings.Repeat("1", 64)
	_, err = backend.Publish(context.Background(), resolvedCredential(t, remote, []byte("unused-local-secret")), publisher.GitOperation{
		OperationKey: operationKey,
		Destination:  "authored.example/ignored",
		Mode:         publisher.ModeBranch,
		Parameters: map[string]string{
			"source_branch": "agent/change-7",
			"target_branch": "main",
		},
		BaseSHA: base, ResultSHA: result, MaterializedRoot: changeRoot,
		Authority: publisher.Authority{TeamID: 1, TeamName: "main", BuildID: 2, WorkflowRunID: 3, Actor: "alice"},
	})
	if err == nil || !strings.Contains(err.Error(), "alternate object storage") {
		t.Fatalf("Publish() error = %v, want alternate-object rejection", err)
	}
	for _, ref := range []string{"refs/heads/agent/change-7", marker} {
		if gitRefExists(t, remotePath, ref) {
			t.Fatalf("%s was published despite alternate-object rejection", ref)
		}
	}
}

func TestPublishRechecksGitObjectAlternatesBeforeEverySourceRepositoryGitOperation(t *testing.T) {
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("secret"))
	marker := publicationMarkerPrefix + strings.TrimPrefix(testOperation, "sha256:")
	targetRef := "refs/heads/agent/change-7"
	fixture := repositoryChangeFixture(t)
	runner := publishingRunner(t, remote, targetRef, marker, false)
	base := runner.run
	var sourceCommands int
	runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
		if command.Dir != "" && len(command.Args) > 0 && command.Args[0] == "rev-parse" {
			sourceCommands++
			if sourceCommands == 1 {
				alternatePath := filepath.Join(command.Dir, ".git", "objects", "info", "alternates")
				if err := os.MkdirAll(filepath.Dir(alternatePath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(alternatePath, []byte("/outside/objects\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		return base(ctx, command)
	}
	backend := newTestBackend(t, runner)

	_, err := backend.Publish(
		context.Background(),
		credential,
		fixture.operation(publisher.ModeBranch, map[string]string{
			"source_branch": "agent/change-7",
			"target_branch": "main",
		}),
	)
	if err == nil || !strings.Contains(err.Error(), "alternate object storage") {
		t.Fatalf("Publish() error = %v, want alternate-object rejection", err)
	}
	if sourceCommands != 1 {
		t.Fatalf("source repository commands = %d, want one before alternate injection", sourceCommands)
	}
	for _, command := range runner.Commands() {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			t.Fatalf("push ran after a source repository alternate appeared: %#v", command)
		}
	}
}

func TestPublishRejectsRepositoryLocalLazyFetchConfigurationBeforeSourceGit(t *testing.T) {
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("secret"))
	marker := publicationMarkerPrefix + strings.TrimPrefix(testOperation, "sha256:")
	targetRef := "refs/heads/agent/change-7"

	for _, test := range []struct {
		name               string
		config             string
		worktreeConfigFile bool
	}{
		{name: "partial clone extension", config: "[core]\n\trepositoryformatversion = 0\n\tbare = false\n[extensions]\n\tpartialClone = origin\n"},
		{name: "promisor remote", config: "[core]\n\trepositoryformatversion = 0\n\tbare = false\n[remote \"origin\"]\n\tpromisor = true\n"},
		{name: "worktree configuration extension", config: "[core]\n\trepositoryformatversion = 0\n\tbare = false\n[extensions]\n\tworktreeConfig = true\n"},
		{name: "worktree configuration file", config: "[core]\n\trepositoryformatversion = 0\n\tbare = false\n", worktreeConfigFile: true},
		{name: "included local configuration", config: "[core]\n\trepositoryformatversion = 0\n\tbare = false\n[include]\n\tpath = /outside/config\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string][]byte{
				".git/config": []byte(test.config),
				"README.md":   []byte("verified result\n"),
			}
			if test.worktreeConfigFile {
				files[".git/config.worktree"] = []byte("[remote \"origin\"]\n\tpromisor = true\n")
			}
			fixture := repositoryChangeFixtureFromPayload(t, tarBytes(t, files))
			runner := publishingRunner(t, remote, targetRef, marker, false)
			backend := newTestBackend(t, runner)

			_, err := backend.Publish(
				context.Background(),
				credential,
				fixture.operation(publisher.ModeBranch, map[string]string{
					"source_branch": "agent/change-7",
					"target_branch": "main",
				}),
			)
			if err == nil || !strings.Contains(err.Error(), "lazy object fetch") {
				t.Fatalf("Publish() error = %v, want lazy-fetch configuration rejection", err)
			}
			for _, command := range runner.Commands() {
				if command.Dir != "" {
					t.Fatalf("source repository Git command ran after lazy-fetch configuration rejection: %#v", command)
				}
			}
		})
	}
}

func TestPublishRejectsScratchParentReplacementBeforePrivateRepositoryCreation(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "scratch")
	moved := filepath.Join(base, "original-scratch")
	marker := filepath.Join(base, "replacement-repository-used")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}

	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("secret"))
	publicationMarker := publicationMarkerPrefix + strings.TrimPrefix(testOperation, "sha256:")
	targetRef := "refs/heads/agent/change-7"
	runner := publishingRunner(t, remote, targetRef, publicationMarker, false)
	baseRun := runner.run
	var replaced sync.Once
	runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
		if command.Dir != "" && len(command.Args) > 0 && command.Args[0] == "fsck" {
			replaced.Do(func() {
				if err := os.Rename(parent, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
			})
		}
		if len(command.Args) > 0 && command.Args[0] == "init" &&
			strings.HasPrefix(command.Args[len(command.Args)-1], parent+string(filepath.Separator)) {
			if err := os.WriteFile(marker, []byte("replacement used"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return baseRun(ctx, command)
	}
	backend, err := NewBackend(runner, parent)
	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.Publish(
		context.Background(),
		credential,
		repositoryChangeFixture(t).operation(publisher.ModeBranch, map[string]string{
			"source_branch": "agent/change-7",
			"target_branch": "main",
		}),
	)
	if err == nil {
		t.Fatal("Publish() accepted a scratch-parent replacement")
	}
	if _, statErr := os.Lstat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("private repository was created through the replacement scratch path: %v", statErr)
	}
	for _, command := range runner.Commands() {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			t.Fatalf("push ran after scratch parent replacement: %#v", command)
		}
	}
}

func TestObjectVerificationRejectsWhitespaceAroundGitOutput(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, _ Command) (CommandResult, error) {
		return CommandResult{Stdout: " " + testResultSHA + "\n"}, nil
	}}
	backend := newTestBackend(t, runner)

	if _, err := backend.output(context.Background(), "", "result commit", "rev-parse", "HEAD"); err == nil {
		t.Fatal("object verification accepted whitespace around Git output")
	}
}

func TestLookupUsesAHexMarkerAndRequiresAnExactRemoteAnswer(t *testing.T) {
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("narrow-secret"))
	marker := "refs/concourse/publications/" + strings.TrimPrefix(testOperation, "sha256:")

	t.Run("found", func(t *testing.T) {
		runner := &fakeRunner{run: func(_ context.Context, command Command) (CommandResult, error) {
			if !slices.Equal(command.Args, []string{
				"ls-remote", "--exit-code", "--refs", remote, marker,
			}) {
				t.Fatalf("command = %#v", command)
			}
			if !command.NoRepository {
				t.Fatal("marker lookup permitted repository-local configuration")
			}
			return CommandResult{Stdout: testResultSHA + "\t" + marker + "\n"}, nil
		}}
		backend := newTestBackend(t, runner)
		result, found, err := backend.Lookup(context.Background(), credential, testOperation)
		if err != nil || !found {
			t.Fatalf("Lookup = (%+v, %v, %v)", result, found, err)
		}
		if result.HeadSHA != testResultSHA || result.ExternalID != marker || result.URL != remote {
			t.Fatalf("result = %+v", result)
		}
		if strings.Contains(runner.Commands()[0].Args[len(runner.Commands()[0].Args)-1], "sha256:") {
			t.Fatal("raw operation key reached the marker ref")
		}
	})

	t.Run("missing", func(t *testing.T) {
		runner := &fakeRunner{run: func(_ context.Context, command Command) (CommandResult, error) {
			return CommandResult{ExitCode: 2}, nil
		}}
		backend := newTestBackend(t, runner)
		_, found, err := backend.Lookup(context.Background(), credential, testOperation)
		if err != nil || found {
			t.Fatalf("Lookup missing = (%v, %v)", found, err)
		}
	})

	for _, test := range []struct {
		name   string
		stdout string
	}{
		{name: "different ref", stdout: testResultSHA + "\trefs/concourse/publications/eeee\n"},
		{name: "multiple refs", stdout: testResultSHA + "\t" + marker + "\n" + testResultSHA + "\t" + marker + "\n"},
		{name: "abbreviated object", stdout: "bbbbbbbb\t" + marker + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{run: func(_ context.Context, command Command) (CommandResult, error) {
				return CommandResult{Stdout: test.stdout}, nil
			}}
			backend := newTestBackend(t, runner)
			if _, _, err := backend.Lookup(context.Background(), credential, testOperation); err == nil {
				t.Fatal("Lookup accepted a mismatching marker answer")
			}
		})
	}
}

func TestPublishSelectsTheModeRefAndUsesOneAtomicLeaseCheckedPush(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        publisher.Mode
		parameters  map[string]string
		targetRef   string
		leaseObject string
	}{
		{
			name: "branch", mode: publisher.ModeBranch,
			parameters:  map[string]string{"source_branch": "agent/change-7", "target_branch": "main"},
			targetRef:   "refs/heads/agent/change-7",
			leaseObject: strings.Repeat("0", 40),
		},
		{
			name: "direct trunk", mode: publisher.ModeMerge,
			parameters:  map[string]string{"target_branch": "main", publisher.MergeBaseParameter: testBaseSHA},
			targetRef:   "refs/heads/main",
			leaseObject: testBaseSHA,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := repositoryChangeFixture(t)
			remote := "file:///srv/git/approved.git"
			credential := resolvedCredential(t, remote, []byte("narrow-secret"))
			marker := "refs/concourse/publications/" + strings.TrimPrefix(testOperation, "sha256:")
			runner := publishingRunner(t, remote, test.targetRef, marker, false)
			backend := newTestBackend(t, runner)
			operation := fixture.operation(test.mode, test.parameters)

			result, err := backend.Publish(context.Background(), credential, operation)
			if err != nil {
				t.Fatal(err)
			}
			if result.HeadSHA != testResultSHA || result.ExternalID != marker || result.URL != remote {
				t.Fatalf("result = %+v", result)
			}

			var dryRun, push *Command
			for _, command := range runner.Commands() {
				if len(command.Args) == 0 || command.Args[0] != "push" {
					continue
				}
				command := command
				if slices.Contains(command.Args, "--dry-run") {
					dryRun = &command
				} else {
					push = &command
				}
			}
			if dryRun == nil || push == nil {
				t.Fatalf("push commands = %#v", runner.Commands())
			}
			wantLease := "--force-with-lease=" + test.targetRef + ":" + test.leaseObject
			wantMarkerLease := "--force-with-lease=" + marker + ":" + strings.Repeat("0", 40)
			for label, command := range map[string]*Command{"probe": dryRun, "push": push} {
				for _, want := range []string{
					"--atomic", "--porcelain", wantLease, wantMarkerLease,
					testResultSHA + ":" + test.targetRef,
					testResultSHA + ":" + marker,
				} {
					if !slices.Contains(command.Args, want) {
						t.Errorf("%s args missing %q: %#v", label, want, command.Args)
					}
				}
				if string(command.Credential) != "narrow-secret" {
					t.Errorf("%s credential = %q", label, command.Credential)
				}
				if strings.Contains(strings.Join(command.Args, "\x00"), "narrow-secret") {
					t.Errorf("%s placed the credential in argv: %#v", label, command.Args)
				}
			}
		})
	}
}

func TestBranchPublicationLeasesTheSeparatelyObservedSourceRef(t *testing.T) {
	const existingSourceSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	fixture := repositoryChangeFixture(t)
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("narrow-secret"))
	targetRef := "refs/heads/agent/change-7"
	marker := "refs/concourse/publications/" + strings.TrimPrefix(testOperation, "sha256:")
	parameters := map[string]string{"source_branch": "agent/change-7", "target_branch": "main"}

	for _, test := range []struct {
		name               string
		observation        CommandResult
		wantExpected       string
		raceAfterObserve   bool
		wantPublicationErr error
	}{
		{
			name:         "absent source is create-only",
			observation:  CommandResult{ExitCode: 2},
			wantExpected: strings.Repeat("0", 40),
		},
		{
			name:         "existing source uses its own exact head",
			observation:  CommandResult{Stdout: existingSourceSHA + "\t" + targetRef + "\n"},
			wantExpected: existingSourceSHA,
		},
		{
			name:               "concurrent source update fails its lease",
			observation:        CommandResult{Stdout: existingSourceSHA + "\t" + targetRef + "\n"},
			wantExpected:       existingSourceSHA,
			raceAfterObserve:   true,
			wantPublicationErr: ErrStaleLease,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := publishingRunner(t, remote, targetRef, marker, false)
			base := runner.run
			observations := 0
			runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
				if slices.Equal(command.Args, []string{
					"ls-remote", "--exit-code", "--refs", remote, targetRef,
				}) {
					observations++
					return test.observation, nil
				}
				if test.raceAfterObserve &&
					len(command.Args) > 0 &&
					command.Args[0] == "push" &&
					!slices.Contains(command.Args, "--dry-run") {
					return CommandResult{ExitCode: 1, Stderr: "rejected: stale info"}, nil
				}
				return base(ctx, command)
			}
			backend := newTestBackend(t, runner)

			_, err := backend.Publish(
				context.Background(),
				credential,
				fixture.operation(publisher.ModeBranch, parameters),
			)
			if !errors.Is(err, test.wantPublicationErr) {
				t.Fatalf("Publish error = %v, want %v", err, test.wantPublicationErr)
			}
			if observations != 1 {
				t.Fatalf("source observations = %d, want 1", observations)
			}
			wantLease := "--force-with-lease=" + targetRef + ":" + test.wantExpected
			var found bool
			for _, command := range runner.Commands() {
				if len(command.Args) > 0 &&
					command.Args[0] == "push" &&
					!slices.Contains(command.Args, "--dry-run") {
					found = slices.Contains(command.Args, wantLease)
				}
			}
			if !found {
				t.Fatalf("publication did not use %q: %#v", wantLease, runner.Commands())
			}
		})
	}
}

func TestPublishFailsClosedForRemoteAndVerificationFailures(t *testing.T) {
	fixture := repositoryChangeFixture(t)
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("do-not-log-this"))
	targetRef := "refs/heads/agent/change-7"
	marker := "refs/concourse/publications/" + strings.TrimPrefix(testOperation, "sha256:")
	parameters := map[string]string{"source_branch": "agent/change-7", "target_branch": "main"}

	tests := []struct {
		name       string
		configure  func(*fakeRunner)
		want       error
		wantNoPush bool
	}{
		{
			name: "atomic unsupported",
			configure: func(runner *fakeRunner) {
				base := runner.run
				runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
					if len(command.Args) > 0 && command.Args[0] == "push" && slices.Contains(command.Args, "--dry-run") {
						return CommandResult{ExitCode: 1, Stderr: "the receiving end does not support --atomic push"}, nil
					}
					return base(ctx, command)
				}
			},
			want:       ErrAtomicPushUnsupported,
			wantNoPush: true,
		},
		{
			name: "stale lease",
			configure: func(runner *fakeRunner) {
				base := runner.run
				runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
					if len(command.Args) > 0 && command.Args[0] == "push" && !slices.Contains(command.Args, "--dry-run") {
						return CommandResult{ExitCode: 1, Stderr: "rejected: stale info; do-not-log-this"}, nil
					}
					return base(ctx, command)
				}
			},
			want: ErrStaleLease,
		},
		{
			name: "non-fast-forward",
			configure: func(runner *fakeRunner) {
				base := runner.run
				runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
					if len(command.Args) > 0 && command.Args[0] == "push" && !slices.Contains(command.Args, "--dry-run") {
						return CommandResult{ExitCode: 1, Stderr: "rejected: non-fast-forward"}, nil
					}
					return base(ctx, command)
				}
			},
			want: ErrNonFastForward,
		},
		{
			name: "post-push partial refs",
			configure: func(runner *fakeRunner) {
				base := runner.run
				runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
					if len(command.Args) > 0 && command.Args[0] == "ls-remote" {
						return CommandResult{Stdout: testResultSHA + "\t" + targetRef + "\n"}, nil
					}
					return base(ctx, command)
				}
			},
			want: ErrAtomicityViolation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := publishingRunner(t, remote, targetRef, marker, false)
			test.configure(runner)
			backend := newTestBackend(t, runner)
			_, err := backend.Publish(context.Background(), credential, fixture.operation(publisher.ModeBranch, parameters))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if strings.Contains(fmt.Sprint(err), "do-not-log-this") {
				t.Fatalf("credential leaked in error: %v", err)
			}
			if test.wantNoPush {
				for _, command := range runner.Commands() {
					if len(command.Args) > 0 && command.Args[0] == "push" && !slices.Contains(command.Args, "--dry-run") {
						t.Fatalf("side-effecting push ran after failed capability probe: %#v", command)
					}
				}
			}
		})
	}
}

func TestPublishRejectsUnsupportedModeAndInvalidAuthorityBeforeRemoteEffects(t *testing.T) {
	fixture := repositoryChangeFixture(t)
	credential := resolvedCredential(t, "file:///srv/git/approved.git", []byte("secret"))

	t.Run("mode outside the direct Git lane", func(t *testing.T) {
		runner := &fakeRunner{}
		backend := newTestBackend(t, runner)
		operation := fixture.operation(publisher.ModeComment, map[string]string{
			"source_branch": "agent/change-7", "target_branch": "main",
		})
		if _, err := backend.Publish(context.Background(), credential, operation); !errors.Is(err, ErrUnsupportedMode) {
			t.Fatalf("error = %v, want ErrUnsupportedMode", err)
		}
		if len(runner.Commands()) != 0 {
			t.Fatalf("unsupported mode ran commands: %#v", runner.Commands())
		}
	})

	t.Run("record result mismatch", func(t *testing.T) {
		runner := &fakeRunner{}
		backend := newTestBackend(t, runner)
		operation := fixture.operation(publisher.ModeBranch, map[string]string{
			"source_branch": "agent/change-7", "target_branch": "main",
		})
		operation.ResultSHA = strings.Repeat("e", 40)
		if _, err := backend.Publish(context.Background(), credential, operation); err == nil {
			t.Fatal("mismatching result commit was accepted")
		}
		if len(runner.Commands()) != 0 {
			t.Fatalf("mismatching record ran commands: %#v", runner.Commands())
		}
	})

	t.Run("invalid branch ref", func(t *testing.T) {
		runner := &fakeRunner{run: func(_ context.Context, command Command) (CommandResult, error) {
			if len(command.Args) > 0 && command.Args[0] == "check-ref-format" {
				return CommandResult{ExitCode: 1}, nil
			}
			t.Fatalf("remote command ran after invalid ref: %#v", command)
			return CommandResult{}, nil
		}}
		backend := newTestBackend(t, runner)
		operation := fixture.operation(publisher.ModeBranch, map[string]string{
			"source_branch": "../main", "target_branch": "main",
		})
		if _, err := backend.Publish(context.Background(), credential, operation); err == nil {
			t.Fatal("invalid source ref was accepted")
		}
	})

	t.Run("credential for a different adapter", func(t *testing.T) {
		runner := &fakeRunner{}
		backend := newTestBackend(t, runner)
		operation := fixture.operation(publisher.ModeBranch, map[string]string{
			"source_branch": "agent/change-7", "target_branch": "main",
		})
		wrongAdapter := resolvedCredentialForAdapter(
			t,
			"file:///srv/git/approved.git",
			[]byte("secret"),
			publisher.AdapterGateway,
		)
		if _, err := backend.Publish(context.Background(), wrongAdapter, operation); err == nil {
			t.Fatal("credential authorized for another adapter was accepted")
		}
		if len(runner.Commands()) != 0 {
			t.Fatalf("wrong adapter ran commands: %#v", runner.Commands())
		}
	})
}

func TestPublishReverifiesPayloadObjectsTreeAndAncestryBeforePush(t *testing.T) {
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte("secret"))
	targetRef := "refs/heads/agent/change-7"
	marker := "refs/concourse/publications/" + strings.TrimPrefix(testOperation, "sha256:")
	parameters := map[string]string{"source_branch": "agent/change-7", "target_branch": "main"}

	tests := []struct {
		name      string
		configure func(*testing.T, changeFixture, *fakeRunner)
	}{
		{
			name: "payload digest",
			configure: func(t *testing.T, fixture changeFixture, _ *fakeRunner) {
				payload := filepath.Join(fixture.root, "content", "result.tar")
				file, err := os.OpenFile(payload, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("tampered")); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "result tree",
			configure: func(t *testing.T, _ changeFixture, runner *fakeRunner) {
				base := runner.run
				runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
					if slices.Equal(command.Args, []string{"rev-parse", "--verify", testResultSHA + "^{tree}"}) {
						return CommandResult{Stdout: strings.Repeat("e", 40) + "\n"}, nil
					}
					return base(ctx, command)
				}
			},
		},
		{
			name: "ancestry",
			configure: func(t *testing.T, _ changeFixture, runner *fakeRunner) {
				base := runner.run
				runner.run = func(ctx context.Context, command Command) (CommandResult, error) {
					if slices.Equal(command.Args, []string{
						"merge-base", "--is-ancestor", testBaseSHA, testResultSHA,
					}) {
						return CommandResult{ExitCode: 1, Stderr: "not an ancestor"}, nil
					}
					return base(ctx, command)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := repositoryChangeFixture(t)
			runner := publishingRunner(t, remote, targetRef, marker, false)
			test.configure(t, fixture, runner)
			backend := newTestBackend(t, runner)
			_, err := backend.Publish(
				context.Background(),
				credential,
				fixture.operation(publisher.ModeBranch, parameters),
			)
			if err == nil {
				t.Fatal("corrupt repository change was published")
			}
			for _, command := range runner.Commands() {
				if len(command.Args) > 0 && command.Args[0] == "push" {
					t.Fatalf("push ran after verification failed: %#v", command)
				}
			}
		})
	}
}

func TestAuthorizedRunnerErrorsAreRedacted(t *testing.T) {
	const secret = "runner-error-secret"
	remote := "file:///srv/git/approved.git"
	credential := resolvedCredential(t, remote, []byte(secret))
	runner := &fakeRunner{run: func(_ context.Context, command Command) (CommandResult, error) {
		if len(command.Args) > 0 && command.Args[0] == "check-ref-format" {
			return CommandResult{}, nil
		}
		return CommandResult{}, fmt.Errorf("transport exposed %s", secret)
	}}
	backend := newTestBackend(t, runner)
	_, err := backend.CurrentBase(context.Background(), credential, "ignored", "main")
	if err == nil {
		t.Fatal("runner error was ignored")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("runner error was not redacted: %v", err)
	}
}

func TestBackendPropagatesCancellationAndDeadline(t *testing.T) {
	for _, test := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    error
	}{
		{
			name: "cancelled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			want: context.Canceled,
		},
		{
			name: "deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{run: func(ctx context.Context, command Command) (CommandResult, error) {
				<-ctx.Done()
				return CommandResult{}, ctx.Err()
			}}
			backend := newTestBackend(t, runner)
			ctx, cancel := test.context()
			defer cancel()
			_, err := backend.CurrentBase(ctx, resolvedCredential(t, "file:///srv/git/approved.git", []byte("secret")), "ignored", "main")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBackendPublishesAndReconcilesAgainstALocalAtomicRemote(t *testing.T) {
	tempDir := t.TempDir()
	repository := filepath.Join(tempDir, "result")
	remotePath := filepath.Join(tempDir, "remote.git")
	runGitTest(t, "", "init", repository)
	runGitTest(t, repository, "config", "user.name", "Publisher Test")
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
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "commit", "-m", "result")
	result := runGitTest(t, repository, "rev-parse", "HEAD")
	resultTree := runGitTest(t, repository, "rev-parse", "HEAD^{tree}")
	runGitTest(t, repository, "checkout", "-b", "prior-source", base)
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("prior source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "commit", "-am", "prior source")
	priorSource := runGitTest(t, repository, "rev-parse", "HEAD")
	runGitTest(t, repository, "checkout", "--detach", result)

	changeRoot := repositoryChangeFromGit(t, tempDir, repository, base, result, resultTree)
	runGitTest(t, "", "init", "--bare", remotePath)
	remote := (&url.URL{Scheme: "file", Path: filepath.ToSlash(remotePath)}).String()
	runGitTest(t, repository, "push", remote, base+":refs/heads/main")
	runGitTest(t, repository, "push", remote, priorSource+":refs/concourse/test/prior-source")
	credential := resolvedCredential(t, remote, []byte("unused-local-secret"))
	runner, err := NewCommandRunner(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(runner, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	operationKey := "sha256:" + strings.Repeat("e", 64)
	marker := publicationMarkerPrefix + strings.Repeat("e", 64)
	operation := publisher.GitOperation{
		OperationKey: operationKey,
		Destination:  "authored.example/ignored",
		Mode:         publisher.ModeBranch,
		Parameters: map[string]string{
			"source_branch": "agent/change-7",
			"target_branch": "main",
		},
		BaseSHA: base, ResultSHA: result, MaterializedRoot: changeRoot,
		Authority: publisher.Authority{
			TeamID: 1, TeamName: "main", BuildID: 2, WorkflowRunID: 3, Actor: "alice",
		},
	}

	published, err := backend.Publish(context.Background(), credential, operation)
	if err != nil {
		t.Fatal(err)
	}
	if published.HeadSHA != result || published.ExternalID != marker {
		t.Fatalf("published = %+v", published)
	}
	if got := runGitTest(t, "", "--git-dir="+remotePath, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("main = %s, want base %s", got, base)
	}
	for _, ref := range []string{"refs/heads/agent/change-7", marker} {
		if got := runGitTest(t, "", "--git-dir="+remotePath, "rev-parse", ref); got != result {
			t.Fatalf("%s = %s, want result %s", ref, got, result)
		}
	}
	currentBase, err := backend.CurrentBase(context.Background(), credential, "ignored", "main")
	if err != nil || currentBase != base {
		t.Fatalf("CurrentBase = (%q, %v), want %q", currentBase, err, base)
	}
	reconciled, found, err := backend.Lookup(context.Background(), credential, operationKey)
	if err != nil || !found || reconciled.HeadSHA != result {
		t.Fatalf("Lookup = (%+v, %v, %v)", reconciled, found, err)
	}

	sourceRef := "refs/heads/agent/change-7"
	runGitTest(t, "", "--git-dir="+remotePath, "update-ref", sourceRef, priorSource, result)
	updateOperation := operation
	updateOperation.OperationKey = "sha256:" + strings.Repeat("f", 64)
	updateMarker := publicationMarkerPrefix + strings.Repeat("f", 64)
	if _, err := backend.Publish(context.Background(), credential, updateOperation); err != nil {
		t.Fatalf("update existing source branch: %v", err)
	}
	for _, ref := range []string{sourceRef, updateMarker} {
		if got := runGitTest(t, "", "--git-dir="+remotePath, "rev-parse", ref); got != result {
			t.Fatalf("updated %s = %s, want result %s", ref, got, result)
		}
	}

	runGitTest(t, "", "--git-dir="+remotePath, "update-ref", sourceRef, priorSource, result)
	raceOperation := operation
	raceOperation.OperationKey = "sha256:" + strings.Repeat("6", 64)
	raceMarker := publicationMarkerPrefix + strings.Repeat("6", 64)
	backend.runner = &mutateBeforePushRunner{
		delegate: backend.runner,
		mutate: func() {
			runGitTest(t, "", "--git-dir="+remotePath, "update-ref", sourceRef, base, priorSource)
		},
	}
	if _, err := backend.Publish(context.Background(), credential, raceOperation); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("concurrent branch update error = %v, want ErrStaleLease", err)
	}
	if got := runGitTest(t, "", "--git-dir="+remotePath, "rev-parse", sourceRef); got != base {
		t.Fatalf("raced source = %s, want concurrent value %s", got, base)
	}
	if gitRefExists(t, remotePath, raceMarker) {
		t.Fatal("failed atomic race published its operation marker")
	}
}

type changeFixture struct {
	root string
}

func (fixture changeFixture) operation(mode publisher.Mode, parameters map[string]string) publisher.GitOperation {
	return publisher.GitOperation{
		OperationKey:     testOperation,
		Destination:      "authored.example/ignored",
		Mode:             mode,
		Parameters:       parameters,
		BaseSHA:          testBaseSHA,
		ResultSHA:        testResultSHA,
		MaterializedRoot: fixture.root,
		Authority: publisher.Authority{
			TeamID: 1, TeamName: "main", BuildID: 2, WorkflowRunID: 3, Actor: "alice",
		},
	}
}

func repositoryChangeFixture(t *testing.T) changeFixture {
	t.Helper()
	rawPayload := tarBytes(t, map[string][]byte{
		".git/config": []byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n"),
		"README.md":   []byte("verified result\n"),
	})
	return repositoryChangeFixtureFromPayload(t, rawPayload)
}

func repositoryChangeFixtureFromPayload(t *testing.T, rawPayload []byte) changeFixture {
	t.Helper()
	tempDir := t.TempDir()
	canonicalizer := snapshot.Canonicalizer{TempDir: tempDir}
	tree, err := canonicalizer.Capture(context.Background(), bytes.NewReader(rawPayload))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := tree.Digest
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(tempDir, "change")
	if err := os.MkdirAll(filepath.Join(root, "content"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "content", "result.tar"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	record, err := contracts.NewRecord(
		"repository-change/v1",
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: "base",
			Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("1", 64)),
		}},
		contracts.RepositoryChangeBody{
			RepositoryID:   "sha256:" + strings.Repeat("2", 64),
			BaseSHA:        testBaseSHA,
			Representation: "git-tree",
			Payload: contracts.ContentRef{
				Path: "content/result.tar", Digest: payloadDigest, MediaType: "application/x-tar",
			},
			ResultTree: testTreeSHA, ResultCommit: testResultSHA,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "record.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	return changeFixture{root: root}
}

func publishingRunner(t *testing.T, remote, targetRef, marker string, partial bool) *fakeRunner {
	t.Helper()
	runner := &fakeRunner{}
	runner.run = func(_ context.Context, command Command) (CommandResult, error) {
		args := command.Args
		if len(args) == 0 {
			t.Fatal("empty command")
		}
		switch args[0] {
		case "check-ref-format", "init", "bundle", "merge-base", "fsck", "cat-file":
			if args[0] == "bundle" && len(args) >= 4 && args[1] == "create" {
				if err := os.WriteFile(args[2], []byte("verified bundle"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			return CommandResult{}, nil
		case "rev-parse":
			switch args[len(args)-1] {
			case "--is-inside-work-tree":
				return CommandResult{Stdout: "true\n"}, nil
			case "--show-object-format=storage":
				return CommandResult{Stdout: "sha1\n"}, nil
			case "HEAD^{commit}":
				return CommandResult{Stdout: testResultSHA + "\n"}, nil
			case testBaseSHA + "^{commit}":
				return CommandResult{Stdout: testBaseSHA + "\n"}, nil
			case testResultSHA + "^{commit}":
				return CommandResult{Stdout: testResultSHA + "\n"}, nil
			case testResultSHA + "^{tree}":
				return CommandResult{Stdout: testTreeSHA + "\n"}, nil
			default:
				t.Fatalf("unexpected rev-parse: %#v", args)
			}
		case "push":
			return CommandResult{}, nil
		case "ls-remote":
			if !command.NoRepository {
				t.Fatal("remote observation permitted repository-local configuration")
			}
			if slices.Equal(args, []string{
				"ls-remote", "--exit-code", "--refs", remote, targetRef,
			}) {
				return CommandResult{ExitCode: 2}, nil
			}
			stdout := testResultSHA + "\t" + targetRef + "\n"
			if !partial {
				stdout += testResultSHA + "\t" + marker + "\n"
			}
			return CommandResult{Stdout: stdout}, nil
		default:
			t.Fatalf("unexpected command: %#v", command)
		}
		return CommandResult{}, nil
	}
	return runner
}

func newTestBackend(t *testing.T, runner Runner) *Backend {
	t.Helper()
	backend, err := NewBackend(runner, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func resolvedCredential(t *testing.T, remote string, secret []byte) publisher.Credential {
	return resolvedCredentialForAdapter(t, remote, secret, publisher.AdapterDirectGit)
}

func resolvedCredentialForAdapter(
	t *testing.T,
	remote string,
	secret []byte,
	adapter publisher.AdapterKind,
) publisher.Credential {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(root, "git-token")
	if err := os.WriteFile(credentialPath, secret, 0600); err != nil {
		t.Fatal(err)
	}
	policy := publisher.Policy{
		SchemaVersion: 1,
		Rules: []publisher.PolicyRule{{
			Team: "main", Publisher: publisher.GitPublisher, Mode: publisher.ModeBranch,
			ApprovalPolicyVersion: "engineering/v1", TargetBranch: "main",
			Destination: "approved-repository", Adapter: adapter,
			CredentialReference: "git-token", RemoteURL: remote,
		}},
	}
	provider, err := publisher.NewFileCredentialProvider(
		policy,
		root,
		map[string]string{"git-token": credentialPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := provider.AuthorizeDestination(context.Background(), publisher.Request{
		Publisher: publisher.GitPublisher,
		Input: snapshot.SnapshotRef{
			ID: 1, Type: "repository-change/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("3", 64)),
		},
		Destination: "approved-repository", Mode: publisher.ModeBranch,
		Parameters:            map[string]string{"source_branch": "agent/change-7", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v1",
		Authority: publisher.Authority{
			TeamID: 1, TeamName: "main", BuildID: 2, WorkflowRunID: 3, Actor: "alice",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

func tarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	directories := map[string]bool{}
	for _, name := range names {
		for directory := filepath.ToSlash(filepath.Dir(name)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			directories[directory] = true
		}
	}
	directoryNames := make([]string, 0, len(directories))
	for name := range directories {
		directoryNames = append(directoryNames, name)
	}
	slices.Sort(directoryNames)
	for _, name := range directoryNames {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		body := files[name]
		if err := writer.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func tarBytesWithAlternate(t *testing.T, alternateName string, typeflag byte, value string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, directory := range []string{
		".git",
		".git/objects",
		".git/objects/info",
	} {
		if err := writer.WriteHeader(&tar.Header{Name: directory, Mode: 0700, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	config := []byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n")
	if err := writer.WriteHeader(&tar.Header{
		Name: ".git/config", Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(config)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(config); err != nil {
		t.Fatal(err)
	}
	header := tar.Header{Name: alternateName, Mode: 0600, Typeflag: typeflag}
	switch typeflag {
	case tar.TypeReg:
		header.Size = int64(len(value))
	case tar.TypeDir:
		header.Mode = 0700
	case tar.TypeSymlink:
		header.Mode = 0777
		header.Linkname = value
	default:
		t.Fatalf("unsupported alternate tar type %d", typeflag)
	}
	if err := writer.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if typeflag == tar.TypeReg {
		if _, err := writer.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func repositoryChangeFromGit(
	t *testing.T,
	tempDir, repository, base, result, resultTree string,
) string {
	t.Helper()
	rawPayload := tarDirectory(t, repository)
	canonicalizer := snapshot.Canonicalizer{TempDir: tempDir}
	tree, captureErr := canonicalizer.Capture(context.Background(), bytes.NewReader(rawPayload))
	if captureErr != nil {
		t.Fatal(captureErr)
	}
	payload, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := tree.Digest
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	changeRoot := filepath.Join(tempDir, "sealed-change")
	if err := os.MkdirAll(filepath.Join(changeRoot, "content"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, "content", "result.tar"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	record, err := contracts.NewRecord(
		"repository-change/v1",
		[]contracts.Subject{{
			ID: "base", Role: contracts.SubjectRoleBase, Input: "base",
			Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64)),
		}},
		contracts.RepositoryChangeBody{
			RepositoryID:   "sha256:" + strings.Repeat("5", 64),
			BaseSHA:        base,
			Representation: "git-tree",
			Payload: contracts.ContentRef{
				Path: "content/result.tar", Digest: payloadDigest, MediaType: "application/x-tar",
			},
			ResultTree: resultTree, ResultCommit: result,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changeRoot, "record.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	return changeRoot
}

func runGitTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitRefExists(t *testing.T, repository, ref string) bool {
	t.Helper()
	command := exec.Command("git", "--git-dir="+repository, "show-ref", "--verify", "--quiet", ref)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
	err := command.Run()
	if err == nil {
		return true
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false
	}
	t.Fatalf("inspect Git ref %s: %v", ref, err)
	return false
}

func tarDirectory(t *testing.T, repository string) []byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.Walk(repository, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected repository entry %q", path)
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tarBytes(t, files)
}
