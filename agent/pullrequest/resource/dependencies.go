package resource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/pullrequest/github"
	"github.com/concourse/concourse/agent/snapshot"
)

func observerFor(source Source, dependencies Dependencies) (pullrequest.Observer, error) {
	factory := dependencies.ObserverFactory
	if factory == nil {
		factory = productionObserver
	}
	observer, err := factory(source)
	if err != nil || observer == nil {
		return nil, fmt.Errorf("forge-pr: provider observer is unavailable")
	}
	return observer, nil
}

func productionObserver(source Source) (pullrequest.Observer, error) {
	switch pullrequest.Provider(source.Provider) {
	case pullrequest.ProviderGitHub:
		return github.NewObserver(source.APIBaseURL, staticToken(source.ReadToken), nil)
	case pullrequest.ProviderAzureDevOps:
		return nil, fmt.Errorf("forge-pr: azure-devops observer is not available in this image")
	default:
		return nil, fmt.Errorf("forge-pr: unsupported provider")
	}
}

type staticToken string

func (token staticToken) Token(context.Context) (string, error) { return string(token), nil }

type controlledGit struct{ runner directgit.Runner }

func defaultGitRunner() (GitRunner, error) {
	runner, err := directgit.NewCommandRunner(os.TempDir())
	if err != nil {
		return nil, fmt.Errorf("forge-pr: initialize controlled git")
	}
	return controlledGit{runner: runner}, nil
}

func (git controlledGit) Run(ctx context.Context, command GitCommand) error {
	if git.runner == nil {
		return fmt.Errorf("forge-pr: controlled git is unavailable")
	}
	if command.Operation != "checkout" || !validFetchAuthority(command) || !objectID(command.SHA) || safeURL(command.RemoteURL) != nil || command.Directory == "" || !filepath.IsAbs(command.Directory) || filepath.Clean(command.Directory) != command.Directory {
		return fmt.Errorf("forge-pr: invalid git materialization command")
	}
	parent := filepath.Dir(command.Directory)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return fmt.Errorf("forge-pr: create checkout parent")
	}
	if _, err := os.Lstat(command.Directory); !os.IsNotExist(err) {
		return fmt.Errorf("forge-pr: checkout destination already exists")
	}
	// Every command uses the controlled Runner, which supplies a private askpass
	// secret and disables ambient/repository Git configuration.
	if err := git.run(ctx, directgit.Command{Args: []string{"init", "--quiet", "--initial-branch=concourse-materialized", command.Directory}, NoRepository: true}, "initialize checkout"); err != nil {
		return err
	}
	internal := "refs/concourse/materialized/head"
	fetchAuthority := command.Ref
	if command.FetchMode == GitFetchExactObject {
		fetchAuthority = command.SHA
	}
	if err := git.run(ctx, directgit.Command{Dir: command.Directory, Args: []string{"fetch", "--no-tags", "--no-recurse-submodules", command.RemoteURL, "+" + fetchAuthority + ":" + internal}, Credential: command.Credential}, "fetch checkout"); err != nil {
		return err
	}
	if err := git.expect(ctx, command.Directory, []string{"rev-parse", internal}, command.SHA); err != nil {
		return fmt.Errorf("forge-pr: fetched ref no longer matches observation")
	}
	if err := git.run(ctx, directgit.Command{Dir: command.Directory, Args: []string{"checkout", "--quiet", "--detach", command.SHA}}, "checkout exact commit"); err != nil {
		return err
	}
	if err := git.expect(ctx, command.Directory, []string{"rev-parse", "HEAD"}, command.SHA); err != nil {
		return err
	}
	if err := git.expect(ctx, command.Directory, []string{"status", "--porcelain=v1", "--untracked-files=no"}, ""); err != nil {
		return err
	}
	// A fetch by URL must not persist a remote. Refuse any saved remote, shallow
	// boundary, alternate object database, worktree indirection, or gitlink.
	for _, args := range [][]string{{"config", "--get-regexp", "^remote\\."}, {"rev-parse", "--is-shallow-repository"}, {"config", "--get", "core.worktree"}} {
		result, err := git.runner.Run(ctx, directgit.Command{Dir: command.Directory, Args: args})
		if err != nil {
			return fmt.Errorf("forge-pr: inspect checkout")
		}
		if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "false" {
			return fmt.Errorf("forge-pr: checkout has forbidden git state")
		}
		if result.ExitCode != 0 && args[0] == "rev-parse" {
			return fmt.Errorf("forge-pr: inspect checkout")
		}
	}
	for _, forbidden := range []string{filepath.Join(command.Directory, ".git", "shallow"), filepath.Join(command.Directory, ".git", "objects", "info", "alternates"), filepath.Join(command.Directory, ".git", "info", "sparse-checkout"), filepath.Join(command.Directory, ".git", "gitdir")} {
		if _, err := os.Lstat(forbidden); err == nil {
			return fmt.Errorf("forge-pr: checkout has forbidden git state")
		}
	}
	if err := validateMaterializationBounds(ctx, command.Directory, snapshot.DefaultMaxSnapshotEntries, snapshot.DefaultMaxSnapshotContentBytes, snapshot.MaxSnapshotSymlinkTargetBytes); err != nil {
		return err
	}
	return validateRepositoryEvidence(ctx, command.Directory)
}

func validFetchAuthority(command GitCommand) bool {
	switch command.FetchMode {
	case GitFetchNamedRef:
		return safeGitRef(command.Ref)
	case GitFetchExactObject:
		return command.Ref == ""
	default:
		return false
	}
}

func (git controlledGit) run(ctx context.Context, command directgit.Command, operation string) error {
	result, err := git.runner.Run(ctx, command)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("forge-pr: %s", operation)
	}
	return nil
}
func (git controlledGit) expect(ctx context.Context, dir string, args []string, expected string) error {
	result, err := git.runner.Run(ctx, directgit.Command{Dir: dir, Args: args})
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != expected {
		return fmt.Errorf("forge-pr: verify checkout")
	}
	return nil
}

func safeGitRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) > 512 || strings.ContainsAny(ref, " \t\r\n\x00\\~^:?*[") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || part == "." || part == "@" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}
