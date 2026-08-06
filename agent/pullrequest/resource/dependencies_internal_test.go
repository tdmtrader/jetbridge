package resource

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher/directgit"
)

func TestControlledGitExactObjectFetchFailsClosedOnObservedSHAMismatch(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "checkout")
	wantSHA := strings.Repeat("a", 40)
	otherSHA := strings.Repeat("b", 40)
	var fetchArguments []string
	runner := directRunnerFunc(func(_ context.Context, command directgit.Command) (directgit.CommandResult, error) {
		switch command.Args[0] {
		case "init":
			if err := os.MkdirAll(filepath.Join(directory, ".git"), 0700); err != nil {
				return directgit.CommandResult{}, err
			}
		case "fetch":
			fetchArguments = append([]string(nil), command.Args...)
		case "rev-parse":
			return directgit.CommandResult{Stdout: otherSHA + "\n"}, nil
		}
		return directgit.CommandResult{}, nil
	})
	err := (controlledGit{runner: runner}).Run(context.Background(), GitCommand{
		Operation:  "checkout",
		FetchMode:  GitFetchExactObject,
		Directory:  directory,
		RemoteURL:  "https://git.example/acme/widget.git",
		SHA:        wantSHA,
		Credential: []byte("secret"),
	})
	if err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("exact-object mismatch error = %v", err)
	}
	if !slices.Contains(fetchArguments, "+"+wantSHA+":refs/concourse/materialized/head") {
		t.Fatalf("fetch does not carry exact sealed SHA: %#v", fetchArguments)
	}
}

type directRunnerFunc func(context.Context, directgit.Command) (directgit.CommandResult, error)

func (function directRunnerFunc) Run(ctx context.Context, command directgit.Command) (directgit.CommandResult, error) {
	return function(ctx, command)
}

// The materialization must create its repository AT THE DESTINATION. This
// exercises the real git binary through the real controlled runner, because
// the stubbed runner used elsewhere in this file emulates `init` with
// os.MkdirAll and therefore cannot observe where git actually puts the
// repository. The overall Run is expected to fail at the fetch (the remote is
// unreachable from a test); what is asserted is the init side effect.
func TestControlledGitInitCreatesTheRepositoryAtTheDestination(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("/usr/bin/git is required for this test")
	}
	runner, err := directgit.NewCommandRunner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "source-repository")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}

	// Failure here is expected and irrelevant: the remote is unreachable.
	_ = controlledGit{runner: runner}.Run(context.Background(), GitCommand{
		Operation: "checkout", FetchMode: GitFetchNamedRef, Directory: destination,
		RemoteURL: "https://unreachable.invalid/acme/widget.git",
		Ref:       "refs/heads/main", SHA: strings.Repeat("a", 40),
		verifyDirectory: func() error { return nil },
	})

	if _, err := os.Stat(filepath.Join(destination, ".git")); err != nil {
		entries, _ := os.ReadDir(destination)
		t.Fatalf("git init did not create a repository at the destination: %v (destination contains %d entries)", err, len(entries))
	}
}
