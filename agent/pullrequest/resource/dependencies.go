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
	if err := verifyGitDirectory(command); err != nil {
		return err
	}
	if info, err := os.Lstat(command.Directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("forge-pr: checkout destination is unsafe")
		}
		entries, readErr := os.ReadDir(command.Directory)
		if readErr != nil || len(entries) != 0 {
			return fmt.Errorf("forge-pr: checkout destination is not empty")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("forge-pr: inspect checkout destination")
	}
	// Every command uses the controlled Runner, which supplies a private askpass
	// secret and disables ambient/repository Git configuration.
	//
	// init deliberately does NOT set NoRepository. That flag exists to point
	// GIT_DIR at a nonexistent path so a command cannot discover an ambient
	// repository -- but `git init` honours GIT_DIR over its own path argument,
	// so it would create the repository inside the ephemeral credential scratch
	// and leave the destination empty, and the fetch below would then fail with
	// "not a git repository". init creates rather than discovers, so there is no
	// ambient repository for the flag to protect against; running it in the
	// destination matches every other command in this function.
	if err := git.run(ctx, command, directgit.Command{Dir: command.Directory, Args: []string{"init", "--quiet", "--initial-branch=concourse-materialized", "."}}, "initialize checkout"); err != nil {
		return err
	}
	internal := "refs/concourse/materialized/head"
	fetchAuthority := command.Ref
	if command.FetchMode == GitFetchExactObject {
		fetchAuthority = command.SHA
	}
	if err := git.run(ctx, command, directgit.Command{Dir: command.Directory, Args: []string{"fetch", "--no-tags", "--no-recurse-submodules", command.RemoteURL, "+" + fetchAuthority + ":" + internal}, Credential: command.Credential}, "fetch checkout"); err != nil {
		return err
	}
	if err := git.expect(ctx, command, []string{"rev-parse", internal}, command.SHA); err != nil {
		return fmt.Errorf("forge-pr: fetched ref no longer matches observation")
	}
	if err := git.run(ctx, command, directgit.Command{Dir: command.Directory, Args: []string{"checkout", "--quiet", "--detach", command.SHA}}, "checkout exact commit"); err != nil {
		return err
	}
	if err := git.expect(ctx, command, []string{"rev-parse", "HEAD"}, command.SHA); err != nil {
		return err
	}
	if err := git.expect(ctx, command, []string{"status", "--porcelain=v1", "--untracked-files=no"}, ""); err != nil {
		return err
	}
	// A fetch by URL must not persist a remote. Refuse any saved remote, shallow
	// boundary, alternate object database, worktree indirection, or gitlink.
	for _, args := range [][]string{{"config", "--get-regexp", "^remote\\."}, {"rev-parse", "--is-shallow-repository"}, {"config", "--get", "core.worktree"}} {
		result, err := git.execute(ctx, command, directgit.Command{Dir: command.Directory, Args: args})
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
	if err := verifyGitDirectory(command); err != nil {
		return err
	}
	for _, forbidden := range []string{filepath.Join(command.Directory, ".git", "shallow"), filepath.Join(command.Directory, ".git", "objects", "info", "alternates"), filepath.Join(command.Directory, ".git", "info", "sparse-checkout"), filepath.Join(command.Directory, ".git", "gitdir")} {
		if _, err := os.Lstat(forbidden); err == nil {
			return fmt.Errorf("forge-pr: checkout has forbidden git state")
		}
	}
	if err := verifyGitDirectory(command); err != nil {
		return err
	}
	boundsErr := validateMaterializationBounds(ctx, command.Directory, snapshot.DefaultMaxSnapshotEntries, snapshot.DefaultMaxSnapshotContentBytes, snapshot.MaxSnapshotSymlinkTargetBytes)
	if err := verifyGitDirectory(command); err != nil {
		return err
	}
	if boundsErr != nil {
		return boundsErr
	}
	evidenceErr := validateRepositoryEvidence(ctx, command.Directory)
	if err := verifyGitDirectory(command); err != nil {
		return err
	}
	if evidenceErr != nil {
		return evidenceErr
	}
	return nil
}

func verifyGitDirectory(command GitCommand) error {
	if command.verifyDirectory == nil {
		return nil
	}
	if err := command.verifyDirectory(); err != nil {
		return fmt.Errorf("forge-pr: checkout destination identity changed")
	}
	return nil
}

func (git controlledGit) execute(ctx context.Context, command GitCommand, process directgit.Command) (directgit.CommandResult, error) {
	if err := verifyGitDirectory(command); err != nil {
		return directgit.CommandResult{}, err
	}
	result, runErr := git.runner.Run(ctx, process)
	if err := verifyGitDirectory(command); err != nil {
		return directgit.CommandResult{}, err
	}
	return result, runErr
}

func (git controlledGit) run(ctx context.Context, command GitCommand, process directgit.Command, operation string) error {
	result, err := git.execute(ctx, command, process)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("forge-pr: %s", operation)
	}
	return nil
}

func (git controlledGit) expect(ctx context.Context, command GitCommand, args []string, expected string) error {
	result, err := git.execute(ctx, command, directgit.Command{Dir: command.Directory, Args: args})
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != expected {
		return fmt.Errorf("forge-pr: verify checkout")
	}
	return nil
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
