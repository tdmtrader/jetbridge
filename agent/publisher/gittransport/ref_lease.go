// Package gittransport provides the caller-sealed Git transport used by
// provider-native pull-request branch publication.
package gittransport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/pullrequest"
)

var (
	ErrStaleSource = errors.New("git transport: source head is stale")
	ErrStaleTarget = errors.New("git transport: target head is stale")

	operationKeyPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type AuthenticationMode = directgit.AuthenticationMode

const (
	AuthenticationDefault = directgit.AuthenticationDefault
	AuthenticationAskpass = directgit.AuthenticationAskpass
)

// RefLease binds the exact policy-authorized remote, materialized candidate
// repository, and credential source. Unlike the direct-Git branch publisher,
// it never derives a lease from an observation made inside the transport.
type RefLease struct {
	runner         directgit.Runner
	remoteURL      string
	repository     string
	token          pullrequest.TokenSource
	authentication AuthenticationMode
	timeout        time.Duration
}

func NewRefLease(
	runner directgit.Runner,
	remoteURL string,
	repository string,
	token pullrequest.TokenSource,
	timeout time.Duration,
) (*RefLease, error) {
	return newRefLease(
		runner,
		remoteURL,
		repository,
		token,
		AuthenticationDefault,
		timeout,
	)
}

func NewRefLeaseWithAuthentication(
	runner directgit.Runner,
	remoteURL string,
	repository string,
	token pullrequest.TokenSource,
	authentication AuthenticationMode,
	timeout time.Duration,
) (*RefLease, error) {
	return newRefLease(
		runner,
		remoteURL,
		repository,
		token,
		authentication,
		timeout,
	)
}

func newRefLease(
	runner directgit.Runner,
	remoteURL string,
	repository string,
	token pullrequest.TokenSource,
	authentication AuthenticationMode,
	timeout time.Duration,
) (*RefLease, error) {
	if nilInterface(runner) || nilInterface(token) {
		return nil, fmt.Errorf("git transport: runner and credential source are required")
	}
	switch authentication {
	case AuthenticationDefault, AuthenticationAskpass:
	default:
		return nil, fmt.Errorf("git transport: authentication mode is invalid")
	}
	if err := validateRemoteURL(remoteURL); err != nil {
		return nil, err
	}
	if err := validateRepositoryPath(repository); err != nil {
		return nil, err
	}
	if err := validateTimeout(timeout); err != nil {
		return nil, err
	}
	return &RefLease{
		runner: runner, remoteURL: remoteURL, repository: repository,
		token: token, authentication: authentication, timeout: timeout,
	}, nil
}

func validateRemoteURL(remoteURL string) error {
	parsed, err := url.Parse(remoteURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("git transport: remote must be an absolute http or https URL without credentials, query, or fragment")
	}
	return nil
}

func validateRepositoryPath(repository string) error {
	if repository == "" || !filepath.IsAbs(repository) ||
		filepath.Clean(repository) != repository ||
		repository == string(filepath.Separator) {
		return fmt.Errorf("git transport: materialized repository must be an absolute clean non-root path")
	}
	return nil
}

func validateTimeout(timeout time.Duration) error {
	if timeout <= 0 || timeout > 5*time.Minute {
		return fmt.Errorf("git transport: timeout is invalid")
	}
	return nil
}

func (transport *RefLease) CompareAndSwapBranch(
	ctx context.Context,
	mutation pullrequest.BranchMutation,
) (pullrequest.BranchResult, error) {
	if ctx == nil {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: context is required")
	}
	if err := ctx.Err(); err != nil {
		return pullrequest.BranchResult{}, err
	}
	if transport == nil {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: ref lease is required")
	}
	if err := validateMutation(mutation); err != nil {
		return pullrequest.BranchResult{}, err
	}

	bounded, cancel := context.WithTimeout(ctx, transport.timeout)
	defer cancel()
	local, err := transport.runner.Run(bounded, directgit.Command{
		Dir: transport.repository,
		Args: []string{
			"cat-file", "-e", mutation.NewSourceSHA + "^{commit}",
		},
	})
	if err != nil {
		return pullrequest.BranchResult{}, transportError(bounded, "verify candidate commit", err)
	}
	if local.ExitCode != 0 {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: candidate commit is unavailable")
	}

	rawToken, err := transport.token.Token(bounded)
	if err != nil {
		if contextErr := bounded.Err(); contextErr != nil {
			return pullrequest.BranchResult{}, contextErr
		}
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: credential unavailable")
	}
	if !validCredential(rawToken) {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: credential unavailable")
	}
	credential := []byte(rawToken)
	defer wipe(credential)

	sourceHead, targetHead, err := transport.observeHeads(bounded, credential, mutation)
	if err != nil {
		return pullrequest.BranchResult{}, err
	}
	if targetHead != mutation.ExpectedTargetSHA {
		return pullrequest.BranchResult{}, ErrStaleTarget
	}
	if sourceHead == mutation.NewSourceSHA {
		return pullrequest.BranchResult{HeadSHA: sourceHead, Applied: false}, nil
	}
	if mutation.ExpectedSource.Exists {
		if sourceHead != mutation.ExpectedSource.SHA {
			return pullrequest.BranchResult{}, ErrStaleSource
		}
	} else if sourceHead != "" {
		return pullrequest.BranchResult{}, ErrStaleSource
	}

	expectedSource := mutation.ExpectedSource.SHA
	pushed, err := transport.runner.Run(bounded, directgit.Command{
		Dir:            transport.repository,
		Credential:     credential,
		Authentication: transport.authentication,
		Args: []string{
			"push",
			"--porcelain",
			"--force-with-lease=" + mutation.Ref + ":" + expectedSource,
			transport.remoteURL,
			mutation.NewSourceSHA + ":" + mutation.Ref,
		},
	})
	if err != nil {
		if contextErr := bounded.Err(); contextErr != nil {
			return pullrequest.BranchResult{}, contextErr
		}
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: push exact source head failed")
	}
	if pushed.ExitCode != 0 {
		// A provider may accept the write and lose the response. Recovery reads
		// the exact ref, but never turns that observation into a new lease.
		recoveredSource, recoveredTarget, observeErr := transport.observeHeads(bounded, credential, mutation)
		if observeErr != nil {
			return pullrequest.BranchResult{}, fmt.Errorf("git transport: push outcome is unknown")
		}
		if recoveredSource == mutation.NewSourceSHA {
			return pullrequest.BranchResult{HeadSHA: recoveredSource, Applied: false}, nil
		}
		if recoveredTarget != mutation.ExpectedTargetSHA {
			return pullrequest.BranchResult{}, ErrStaleTarget
		}
		if mutation.ExpectedSource.Exists && recoveredSource != mutation.ExpectedSource.SHA ||
			!mutation.ExpectedSource.Exists && recoveredSource != "" {
			return pullrequest.BranchResult{}, ErrStaleSource
		}
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: exact source push failed")
	}

	publishedSource, _, err := transport.observeHeads(bounded, credential, mutation)
	if err != nil {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: verify source publication: %w", err)
	}
	if publishedSource != mutation.NewSourceSHA {
		return pullrequest.BranchResult{}, fmt.Errorf("git transport: provider did not publish the exact source head")
	}
	return pullrequest.BranchResult{HeadSHA: publishedSource, Applied: true}, nil
}

func (transport *RefLease) observeHeads(
	ctx context.Context,
	credential []byte,
	mutation pullrequest.BranchMutation,
) (string, string, error) {
	result, err := transport.runner.Run(ctx, directgit.Command{
		Args:           []string{"ls-remote", "--refs", transport.remoteURL, mutation.Ref, mutation.TargetRef},
		Credential:     credential,
		Authentication: transport.authentication,
		NoRepository:   true,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", "", contextErr
		}
		return "", "", fmt.Errorf("git transport: read exact remote heads failed")
	}
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("git transport: read exact remote heads failed")
	}
	heads := make(map[string]string, 2)
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 2 ||
			(fields[1] != mutation.Ref && fields[1] != mutation.TargetRef) ||
			!validObjectID(fields[0]) || len(fields[0]) != len(mutation.NewSourceSHA) {
			return "", "", fmt.Errorf("git transport: remote head response is invalid")
		}
		if _, duplicate := heads[fields[1]]; duplicate {
			return "", "", fmt.Errorf("git transport: remote head response is ambiguous")
		}
		heads[fields[1]] = fields[0]
	}
	targetHead, found := heads[mutation.TargetRef]
	if !found {
		return "", "", fmt.Errorf("git transport: exact target head is unavailable")
	}
	return heads[mutation.Ref], targetHead, nil
}

func validateMutation(mutation pullrequest.BranchMutation) error {
	switch mutation.Locator.Provider {
	case pullrequest.ProviderGitHub:
	default:
		return fmt.Errorf("git transport: provider is invalid")
	}
	if !boundedText(mutation.Locator.Repository, 512) ||
		mutation.Locator.ExternalID != "" && !boundedText(mutation.Locator.ExternalID, 256) {
		return fmt.Errorf("git transport: locator is invalid")
	}
	if !validRef(mutation.Ref) || !validRef(mutation.TargetRef) ||
		mutation.Ref == mutation.TargetRef {
		return fmt.Errorf("git transport: source and target refs are invalid")
	}
	if err := mutation.ExpectedSource.Validate(); err != nil {
		return fmt.Errorf("git transport: expected source is invalid: %w", err)
	}
	if !validObjectID(mutation.ExpectedTargetSHA) ||
		!validObjectID(mutation.NewSourceSHA) ||
		len(mutation.ExpectedTargetSHA) != len(mutation.NewSourceSHA) ||
		mutation.ExpectedSource.Exists &&
			len(mutation.ExpectedSource.SHA) != len(mutation.NewSourceSHA) {
		return fmt.Errorf("git transport: object identities are invalid")
	}
	if !operationKeyPattern.MatchString(mutation.OperationKey) {
		return fmt.Errorf("git transport: operation key is invalid")
	}
	return nil
}

func validRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") || len(value) > 512 ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "//") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.ContainsAny(value, "\\~^:?*[ \t\r\n\x00") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == "@" ||
			strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func boundedText(value string, maximum int) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) ||
		len(value) > maximum || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	return !strings.ContainsAny(value, "\r\n")
}

func validCredential(value string) bool {
	return value != "" && len(value) <= 64<<10 &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func transportError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return fmt.Errorf("git transport: %s: %w", operation, err)
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ interface {
	CompareAndSwapBranch(context.Context, pullrequest.BranchMutation) (pullrequest.BranchResult, error)
} = (*RefLease)(nil)
