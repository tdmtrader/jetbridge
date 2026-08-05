package atccmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/snapshot"
	containername "github.com/google/go-containerregistry/pkg/name"
	"github.com/hashicorp/go-multierror"
)

const (
	defaultAgentPublisherCredentialRoot = "/run/concourse-publisher"
	incompletePRAuthoritySpineError     = "provider-native pull request production authority spine is incomplete; authoritative impact evaluation, action-bound initial publication composition, approved-baseline materialization and advancement, and lifecycle wiring must be composed before enablement"
)

type agentPublisherCredentialFiles map[string]string

func (files *agentPublisherCredentialFiles) UnmarshalFlag(value string) error {
	reference, filePath, found := strings.Cut(value, ":")
	if !found || reference == "" || filePath == "" {
		return fmt.Errorf("expected REFERENCE:ABSOLUTE-PATH")
	}
	if *files == nil {
		*files = make(agentPublisherCredentialFiles)
	}
	if _, exists := (*files)[reference]; exists {
		return fmt.Errorf("duplicate agent publisher credential reference %q", reference)
	}
	(*files)[reference] = filePath
	return nil
}

func (cmd *RunCommand) validateAgentPublisher() error {
	var errs *multierror.Error

	if cmd.AgentPublisher.RequestTimeout <= 0 || cmd.AgentPublisher.RequestTimeout > time.Hour {
		errs = multierror.Append(errs, errors.New("--agent-publisher-request-timeout must be positive and no greater than 1h"))
	}
	if cmd.AgentPublisher.LeaseDuration <= 0 || cmd.AgentPublisher.LeaseDuration > 24*time.Hour {
		errs = multierror.Append(errs, errors.New("--agent-publisher-lease-duration must be positive and no greater than 24h"))
	} else if cmd.AgentPublisher.RequestTimeout > 0 &&
		cmd.AgentPublisher.LeaseDuration <= cmd.AgentPublisher.RequestTimeout {
		errs = multierror.Append(errs, errors.New("--agent-publisher-lease-duration must be greater than --agent-publisher-request-timeout"))
	}

	if !cmd.AgentPublisher.Enabled {
		if cmd.AgentPublisher.PolicyFile != "" ||
			len(cmd.AgentPublisher.CredentialFiles) != 0 ||
			cmd.AgentPublisher.DirectGitEnabled ||
			cmd.AgentPublisher.PullRequestsEnabled ||
			cmd.AgentPublisher.PRResourceImage != "" ||
			(cmd.AgentPublisher.CredentialRoot != "" &&
				cmd.AgentPublisher.CredentialRoot != defaultAgentPublisherCredentialRoot) {
			errs = multierror.Append(errs, errors.New("--agent-publisher-enabled is required when publisher configuration is set"))
		}
		return errs.ErrorOrNil()
	}

	if !cmd.AgentSnapshots.Enabled {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-enabled is required when --agent-publisher-enabled is set"))
	}
	if !cmd.AgentPublisher.DirectGitEnabled && !cmd.AgentPublisher.PullRequestsEnabled {
		errs = multierror.Append(errs, errors.New("at least one of --agent-publisher-direct-git-enabled or --agent-publisher-pull-requests-enabled is required"))
	}
	if cmd.AgentPublisher.PullRequestsEnabled {
		if !exactDigestImage(cmd.AgentPublisher.PRResourceImage) {
			errs = multierror.Append(errs, errors.New("--agent-publisher-pr-resource-image must be an exact lowercase sha256 OCI digest reference"))
		}
		if cmd.AgentPublisher.PRPollInterval <= 0 {
			errs = multierror.Append(errs, errors.New("--agent-publisher-pr-poll-interval must be positive"))
		}
		if cmd.AgentPublisher.PRFreshnessInterval < cmd.AgentPublisher.PRPollInterval {
			errs = multierror.Append(errs, errors.New("--agent-publisher-pr-freshness-interval must be no shorter than --agent-publisher-pr-poll-interval"))
		}
		errs = multierror.Append(
			errs, errors.New(incompletePRAuthoritySpineError),
		)
	} else if cmd.AgentPublisher.PRResourceImage != "" {
		errs = multierror.Append(errs, errors.New("--agent-publisher-pull-requests-enabled is required when --agent-publisher-pr-resource-image is set"))
	}
	if !absoluteCleanPath(cmd.AgentPublisher.PolicyFile) {
		errs = multierror.Append(errs, errors.New("--agent-publisher-policy-file must be an absolute clean path"))
	}
	validCredentialRoot := absoluteCleanPath(cmd.AgentPublisher.CredentialRoot) &&
		cmd.AgentPublisher.CredentialRoot != string(filepath.Separator)
	if !validCredentialRoot {
		errs = multierror.Append(errs, errors.New("--agent-publisher-credential-root must be an absolute clean non-root path"))
	}
	if len(cmd.AgentPublisher.CredentialFiles) == 0 {
		errs = multierror.Append(errs, errors.New("--agent-publisher-credential-file must map every policy credential reference"))
	}
	for reference, filePath := range cmd.AgentPublisher.CredentialFiles {
		if strings.TrimSpace(reference) == "" || strings.ContainsAny(reference, "\r\n") {
			errs = multierror.Append(errs, errors.New("--agent-publisher-credential-file reference is invalid"))
			continue
		}
		if !absoluteCleanPath(filePath) {
			errs = multierror.Append(
				errs,
				fmt.Errorf("--agent-publisher-credential-file %q must use an absolute clean path", reference),
			)
			continue
		}
		if validCredentialRoot {
			relative, err := filepath.Rel(cmd.AgentPublisher.CredentialRoot, filePath)
			if err != nil || relative == "." || !filepath.IsLocal(relative) {
				errs = multierror.Append(
					errs,
					fmt.Errorf("--agent-publisher-credential-file %q must lie strictly beneath --agent-publisher-credential-root", reference),
				)
			}
		}
	}
	return errs.ErrorOrNil()
}

func exactDigestImage(image string) bool {
	if image == "" || image != strings.TrimSpace(image) {
		return false
	}
	parsed, err := containername.NewDigest(image, containername.StrictValidation)
	if err != nil {
		return false
	}
	_, err = snapshot.ParseDigest(parsed.DigestStr())
	return err == nil
}

func absoluteCleanPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func (cmd *RunCommand) agentPublisherCanonicalizer() snapshot.Canonicalizer {
	return snapshot.Canonicalizer{
		MaxContentBytes: cmd.AgentSnapshots.MaxBytes,
		MaxEntries:      cmd.AgentSnapshots.MaxFiles,
		TempDir:         cmd.AgentSnapshots.TempDir,
	}
}

func (cmd *RunCommand) buildAgentPublisher(
	store publisher.Store,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
) (publisher.Executor, error) {
	if !cmd.AgentPublisher.Enabled {
		return nil, fmt.Errorf("agent publisher is disabled")
	}
	if cmd.AgentPublisher.PullRequestsEnabled {
		return nil, errors.New(incompletePRAuthoritySpineError)
	}
	if isNilDependency(store) || isNilDependency(metadata) || isNilDependency(content) {
		return nil, fmt.Errorf("agent publisher requires publication and snapshot stores")
	}

	policy, err := publisher.LoadPolicy(cmd.AgentPublisher.PolicyFile)
	if err != nil {
		return nil, fmt.Errorf("load agent publisher policy: %w", err)
	}
	if err := cmd.validateAgentPublisherPolicy(policy); err != nil {
		return nil, err
	}
	credentials, err := publisher.NewFileCredentialProvider(
		policy,
		cmd.AgentPublisher.CredentialRoot,
		cmd.AgentPublisher.CredentialFiles,
	)
	if err != nil {
		return nil, fmt.Errorf("build agent publisher credentials: %w", err)
	}
	changes, err := publisher.NewSnapshotChangeInspector(
		metadata,
		content,
		cmd.agentPublisherCanonicalizer(),
	)
	if err != nil {
		return nil, fmt.Errorf("build agent publisher snapshot inspector: %w", err)
	}
	backend, err := directgit.New(cmd.AgentSnapshots.TempDir)
	if err != nil {
		return nil, fmt.Errorf("build agent publisher direct Git backend: %w", err)
	}
	git, err := publisher.NewGitService(
		store,
		credentials,
		changes,
		backend,
		cmd.AgentPublisher.RequestTimeout,
		cmd.AgentPublisher.LeaseDuration,
	)
	if err != nil {
		return nil, fmt.Errorf("build agent Git publisher: %w", err)
	}
	router, err := publisher.NewRouter(git, unavailablePublisherExecutor{})
	if err != nil {
		return nil, fmt.Errorf("build agent publisher router: %w", err)
	}
	return router, nil
}

func (cmd *RunCommand) validateAgentPublisherPolicy(policy publisher.Policy) error {
	for index, rule := range policy.Rules {
		switch rule.Mode {
		case publisher.ModeBranch, publisher.ModeMerge:
			if !cmd.AgentPublisher.DirectGitEnabled {
				return fmt.Errorf("agent publisher: direct Git adapter is disabled")
			}
			if rule.Adapter != publisher.AdapterDirectGit {
				return fmt.Errorf(
					"agent publisher: policy rule %d uses unsupported adapter %q",
					index,
					rule.Adapter,
				)
			}
			if rule.Publisher != publisher.GitPublisher {
				return fmt.Errorf(
					"agent publisher: policy rule %d uses unsupported publisher %q",
					index,
					rule.Publisher,
				)
			}
		case publisher.ModePullRequest:
			if !cmd.AgentPublisher.PullRequestsEnabled {
				return fmt.Errorf("agent publisher: pull request adapter is disabled")
			}
			if rule.Publisher != publisher.GitPublisher ||
				rule.Adapter != publisher.AdapterGitHub {
				return fmt.Errorf(
					"agent publisher: policy rule %d uses unsupported provider-native PR adapter %q",
					index,
					rule.Adapter,
				)
			}
		default:
			return fmt.Errorf(
				"agent publisher: policy rule %d uses unsupported mode %q",
				index,
				rule.Mode,
			)
		}
	}
	return nil
}

type unavailablePublisherExecutor struct{}

func (unavailablePublisherExecutor) Execute(
	ctx context.Context,
	request publisher.Request,
) (publisher.Publication, error) {
	if ctx == nil {
		return publisher.Publication{}, fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, err
	}
	return publisher.Publication{}, fmt.Errorf(
		"publisher: no in-process adapter is configured for %s",
		request.Publisher,
	)
}
