package gittransport_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/publisher/gittransport"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestRefLeaseRejectsCallerStaleSourceAndTargetWithoutPushing(t *testing.T) {
	const (
		remote = "https://github.example/acme/widget.git"
		source = "refs/heads/agent/upgrade"
		target = "refs/heads/main"
	)
	for _, test := range []struct {
		name           string
		expectedSource string
		expectedTarget string
		remoteSource   string
		remoteTarget   string
		want           error
	}{
		{
			name:           "source",
			expectedSource: objectID('a'),
			expectedTarget: objectID('b'),
			remoteSource:   objectID('d'),
			remoteTarget:   objectID('b'),
			want:           gittransport.ErrStaleSource,
		},
		{
			name:           "target",
			expectedSource: objectID('a'),
			expectedTarget: objectID('b'),
			remoteSource:   objectID('a'),
			remoteTarget:   objectID('d'),
			want:           gittransport.ErrStaleTarget,
		},
		{
			name:           "target after source already equals requested head",
			expectedSource: objectID('c'),
			expectedTarget: objectID('b'),
			remoteSource:   objectID('c'),
			remoteTarget:   objectID('d'),
			want:           gittransport.ErrStaleTarget,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{run: func(command directgit.Command) directgit.CommandResult {
				switch command.Args[0] {
				case "cat-file":
					return directgit.CommandResult{}
				case "ls-remote":
					return directgit.CommandResult{Stdout: test.remoteSource + "\t" + source + "\n" + test.remoteTarget + "\t" + target + "\n"}
				default:
					t.Fatalf("unexpected Git command: %#v", command.Args)
					return directgit.CommandResult{}
				}
			}}
			transport := newRefLease(t, runner, remote)
			_, err := transport.CompareAndSwapBranch(context.Background(), pullrequest.BranchMutation{
				Locator:           pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget"},
				Ref:               source,
				TargetRef:         target,
				ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: true, SHA: test.expectedSource},
				ExpectedTargetSHA: test.expectedTarget,
				NewSourceSHA:      objectID('c'),
				OperationKey:      operationKey('1'),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("CompareAndSwapBranch error = %v, want %v", err, test.want)
			}
			for _, command := range runner.commands() {
				if len(command.Args) > 0 && command.Args[0] == "push" {
					t.Fatalf("stale mutation attempted push: %#v", command.Args)
				}
			}
		})
	}
}

func TestRefLeaseUsesOnlyTheCallerSealedSourceExpectation(t *testing.T) {
	const (
		remote = "https://github.example/acme/widget.git"
		source = "refs/heads/agent/upgrade"
		target = "refs/heads/main"
		secret = "narrow-token"
	)
	pushed := false
	runner := &recordingRunner{run: func(command directgit.Command) directgit.CommandResult {
		switch command.Args[0] {
		case "cat-file":
			return directgit.CommandResult{}
		case "ls-remote":
			if pushed {
				return directgit.CommandResult{Stdout: objectID('c') + "\t" + source + "\n" + objectID('b') + "\t" + target + "\n"}
			}
			return directgit.CommandResult{Stdout: objectID('a') + "\t" + source + "\n" + objectID('b') + "\t" + target + "\n"}
		case "push":
			pushed = true
			return directgit.CommandResult{}
		default:
			t.Fatalf("unexpected Git command: %#v", command.Args)
			return directgit.CommandResult{}
		}
	}}
	transport := newRefLeaseWithToken(t, runner, remote, secret)
	result, err := transport.CompareAndSwapBranch(context.Background(), pullrequest.BranchMutation{
		Locator:           pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget"},
		Ref:               source,
		TargetRef:         target,
		ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: true, SHA: objectID('a')},
		ExpectedTargetSHA: objectID('b'),
		NewSourceSHA:      objectID('c'),
		OperationKey:      operationKey('2'),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.HeadSHA != objectID('c') || !result.Applied {
		t.Fatalf("result = %+v", result)
	}

	var push *directgit.Command
	for _, command := range runner.commands() {
		if len(command.Args) > 0 && command.Args[0] == "push" {
			value := command
			push = &value
		}
	}
	if push == nil {
		t.Fatal("no push command")
	}
	for _, want := range []string{
		"--force-with-lease=" + source + ":" + objectID('a'),
		objectID('c') + ":" + source,
	} {
		if !slices.Contains(push.Args, want) {
			t.Fatalf("push args missing %q: %#v", want, push.Args)
		}
	}
	if string(push.Credential) != secret {
		t.Fatalf("push credential = %q", push.Credential)
	}
	for _, argument := range push.Args {
		if argument == secret {
			t.Fatalf("credential appeared in argv: %#v", push.Args)
		}
	}
}

func TestRefLeaseAcceptsTheSafeTargetRaceAfterTheExactPreWriteCheck(t *testing.T) {
	const (
		remote = "https://github.example/acme/widget.git"
		source = "refs/heads/agent/upgrade"
		target = "refs/heads/main"
	)
	pushed := false
	runner := &recordingRunner{run: func(command directgit.Command) directgit.CommandResult {
		switch command.Args[0] {
		case "cat-file":
			return directgit.CommandResult{}
		case "ls-remote":
			if pushed {
				return directgit.CommandResult{
					Stdout: objectID('c') + "\t" + source + "\n" +
						objectID('d') + "\t" + target + "\n",
				}
			}
			return directgit.CommandResult{
				Stdout: objectID('a') + "\t" + source + "\n" +
					objectID('b') + "\t" + target + "\n",
			}
		case "push":
			pushed = true
			return directgit.CommandResult{}
		default:
			t.Fatalf("unexpected Git command: %#v", command.Args)
			return directgit.CommandResult{}
		}
	}}
	transport := newRefLease(t, runner, remote)
	result, err := transport.CompareAndSwapBranch(context.Background(), pullrequest.BranchMutation{
		Locator:           pullrequest.Locator{Provider: pullrequest.ProviderGitHub, Repository: "acme/widget"},
		Ref:               source,
		TargetRef:         target,
		ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: true, SHA: objectID('a')},
		ExpectedTargetSHA: objectID('b'),
		NewSourceSHA:      objectID('c'),
		OperationKey:      operationKey('5'),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.HeadSHA != objectID('c') {
		t.Fatalf("CompareAndSwapBranch result = %+v", result)
	}
	if !pushed {
		t.Fatal("test did not exercise the post-write target check")
	}
}

func TestRefLeaseRepresentsExpectedAbsenceWithoutObservingALease(t *testing.T) {
	const (
		remote = "https://github.example/acme/widget.git"
		source = "refs/heads/agent/upgrade"
		target = "refs/heads/main"
	)
	pushed := false
	runner := &recordingRunner{run: func(command directgit.Command) directgit.CommandResult {
		switch command.Args[0] {
		case "cat-file":
			return directgit.CommandResult{}
		case "ls-remote":
			sourceLine := ""
			if pushed {
				sourceLine = objectID('c') + "\t" + source + "\n"
			}
			return directgit.CommandResult{
				Stdout: sourceLine + objectID('b') + "\t" + target + "\n",
			}
		case "push":
			pushed = true
			if !slices.Contains(command.Args, "--force-with-lease="+source+":") {
				t.Fatalf("absence push did not carry an exact empty lease: %#v", command.Args)
			}
			return directgit.CommandResult{}
		default:
			t.Fatalf("unexpected Git command: %#v", command.Args)
			return directgit.CommandResult{}
		}
	}}
	transport := newRefLease(t, runner, remote)
	result, err := transport.CompareAndSwapBranch(
		context.Background(),
		pullrequest.BranchMutation{
			Locator: pullrequest.Locator{
				Provider: pullrequest.ProviderGitHub, Repository: "acme/widget",
			},
			Ref: source, TargetRef: target,
			ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: false},
			ExpectedTargetSHA: objectID('b'),
			NewSourceSHA:      objectID('c'),
			OperationKey:      operationKey('4'),
		},
	)
	if err != nil || !result.Applied || result.HeadSHA != objectID('c') {
		t.Fatalf("absence CompareAndSwapBranch = (%+v, %v)", result, err)
	}
}

func TestRefLeaseDoesNotExposeCredentialFromRunnerFailures(t *testing.T) {
	const secret = "do-not-log-this"
	runner := &leakingRunner{secret: secret}
	transport := newRefLeaseWithToken(
		t, runner, "https://github.example/acme/widget.git", secret,
	)
	_, err := transport.CompareAndSwapBranch(
		context.Background(),
		pullrequest.BranchMutation{
			Locator: pullrequest.Locator{
				Provider: pullrequest.ProviderGitHub, Repository: "acme/widget",
			},
			Ref: "refs/heads/agent/upgrade", TargetRef: "refs/heads/main",
			ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: false},
			ExpectedTargetSHA: objectID('b'),
			NewSourceSHA:      objectID('c'),
			OperationKey:      operationKey('3'),
		},
	)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("runner error leaked credential: %v", err)
	}
}

func TestRefLeaseAuthenticationModeIsExplicitAndNeverInferredFromTokenText(t *testing.T) {
	const (
		remote = "https://github.example/acme/widget.git"
		source = "refs/heads/agent/upgrade"
		target = "refs/heads/main"
	)
	for _, test := range []struct {
		name      string
		token     string
		construct func(directgit.Runner, pullrequest.TokenSource, string) (*gittransport.RefLease, error)
		want      directgit.AuthenticationMode
	}{
		{
			name:  "default askpass compatibility with an opaque OAuth-looking token",
			token: "opaque-oauth-looking-token",
			construct: func(runner directgit.Runner, token pullrequest.TokenSource, repository string) (*gittransport.RefLease, error) {
				return gittransport.NewRefLease(runner, remote, repository, token, time.Second)
			},
			want: directgit.AuthenticationDefault,
		},
		{
			name:  "explicit askpass with an opaque OAuth-looking token",
			token: "opaque-oauth-looking-token",
			construct: func(runner directgit.Runner, token pullrequest.TokenSource, repository string) (*gittransport.RefLease, error) {
				return gittransport.NewRefLeaseWithAuthentication(
					runner, remote, repository, token,
					gittransport.AuthenticationAskpass, time.Second,
				)
			},
			want: directgit.AuthenticationAskpass,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var authenticated []directgit.Command
			runner := &recordingRunner{run: func(command directgit.Command) directgit.CommandResult {
				switch command.Args[0] {
				case "cat-file":
					return directgit.CommandResult{}
				case "ls-remote":
					if len(command.Credential) > 0 {
						authenticated = append(authenticated, command)
					}
					return directgit.CommandResult{
						Stdout: objectID('a') + "\t" + source + "\n" +
							objectID('d') + "\t" + target + "\n",
					}
				default:
					t.Fatalf("unexpected command: %#v", command)
					return directgit.CommandResult{}
				}
			}}
			transport, err := test.construct(runner, staticToken(test.token), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, err = transport.CompareAndSwapBranch(
				context.Background(),
				pullrequest.BranchMutation{
					Locator: pullrequest.Locator{
						Provider:   pullrequest.ProviderGitHub,
						Repository: "project/widget",
					},
					Ref: source, TargetRef: target,
					ExpectedSource: contracts.PullRequestHeadExpectation{
						Exists: true, SHA: objectID('a'),
					},
					ExpectedTargetSHA: objectID('b'),
					NewSourceSHA:      objectID('c'),
					OperationKey:      operationKey('8'),
				},
			)
			if !errors.Is(err, gittransport.ErrStaleTarget) {
				t.Fatalf("CompareAndSwapBranch error = %v, want stale target", err)
			}
			if len(authenticated) != 1 ||
				authenticated[0].Authentication != test.want ||
				string(authenticated[0].Credential) != test.token {
				t.Fatalf("authenticated commands = %#v, want mode %q", authenticated, test.want)
			}
		})
	}
}

func TestRefLeasePreservesCredentialResolutionDeadline(t *testing.T) {
	runner := &recordingRunner{run: func(command directgit.Command) directgit.CommandResult {
		if len(command.Args) == 0 || command.Args[0] != "cat-file" {
			t.Fatalf("unexpected Git command: %#v", command.Args)
		}
		return directgit.CommandResult{}
	}}
	transport, err := gittransport.NewRefLease(
		runner,
		"https://github.example/acme/widget.git",
		t.TempDir(),
		deadlineToken{},
		5*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.CompareAndSwapBranch(
		context.Background(),
		pullrequest.BranchMutation{
			Locator: pullrequest.Locator{
				Provider: pullrequest.ProviderGitHub, Repository: "acme/widget",
			},
			Ref:               "refs/heads/agent/upgrade",
			TargetRef:         "refs/heads/main",
			ExpectedSource:    contracts.PullRequestHeadExpectation{},
			ExpectedTargetSHA: objectID('b'),
			NewSourceSHA:      objectID('c'),
			OperationKey:      operationKey('9'),
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf(
			"CompareAndSwapBranch error = %v, want context deadline exceeded",
			err,
		)
	}
}

type recordingRunner struct {
	mu    sync.Mutex
	calls []directgit.Command
	run   func(directgit.Command) directgit.CommandResult
}

func (runner *recordingRunner) Run(_ context.Context, command directgit.Command) (directgit.CommandResult, error) {
	command.Args = append([]string(nil), command.Args...)
	command.Credential = append([]byte(nil), command.Credential...)
	runner.mu.Lock()
	runner.calls = append(runner.calls, command)
	runner.mu.Unlock()
	return runner.run(command), nil
}

func (runner *recordingRunner) commands() []directgit.Command {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]directgit.Command(nil), runner.calls...)
}

type leakingRunner struct {
	calls  int
	secret string
}

func (runner *leakingRunner) Run(
	_ context.Context,
	command directgit.Command,
) (directgit.CommandResult, error) {
	runner.calls++
	if command.Args[0] == "cat-file" {
		return directgit.CommandResult{}, nil
	}
	return directgit.CommandResult{}, fmt.Errorf("transport exposed %s", runner.secret)
}

type staticToken string

func (token staticToken) Token(context.Context) (string, error) {
	return string(token), nil
}

type deadlineToken struct{}

func (deadlineToken) Token(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func newRefLease(t *testing.T, runner directgit.Runner, remote string) *gittransport.RefLease {
	t.Helper()
	return newRefLeaseWithToken(t, runner, remote, "secret")
}

func newRefLeaseWithToken(t *testing.T, runner directgit.Runner, remote, token string) *gittransport.RefLease {
	t.Helper()
	transport, err := gittransport.NewRefLease(runner, remote, t.TempDir(), staticToken(token), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return transport
}

func objectID(character byte) string {
	value := make([]byte, 40)
	for index := range value {
		value[index] = character
	}
	return string(value)
}

func operationKey(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return "sha256:" + string(value)
}
