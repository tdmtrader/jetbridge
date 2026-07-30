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
