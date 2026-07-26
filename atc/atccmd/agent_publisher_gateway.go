package atccmd

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/hashicorp/go-multierror"
)

func (cmd *RunCommand) validateAgentPublisherGateway() error {
	var errs *multierror.Error
	if !cmd.AgentPublisherGateway.Enabled {
		if cmd.AgentPublisherGateway.Endpoint != "" || cmd.AgentPublisherGateway.PolicyFile != "" ||
			cmd.AgentPublisherGateway.TokenFile != "" || cmd.AgentPublisherGateway.CACertificateFile != "" {
			errs = multierror.Append(errs, errors.New("--agent-publisher-gateway-enabled is required when publisher gateway configuration is set"))
		}
		if cmd.AgentPublisherGateway.RequestTimeout <= 0 {
			errs = multierror.Append(errs, errors.New("--agent-publisher-gateway-request-timeout must be positive"))
		}
		if cmd.AgentPublisherGateway.LeaseDuration <= 0 {
			errs = multierror.Append(errs, errors.New("--agent-publisher-gateway-lease-duration must be positive"))
		}
		if cmd.AgentPublisherGateway.MaxResponseBytes <= 0 {
			errs = multierror.Append(errs, errors.New("--agent-publisher-gateway-max-response-bytes must be positive"))
		}
		return errs.ErrorOrNil()
	}
	if !cmd.AgentSnapshots.Enabled {
		errs = multierror.Append(errs, errors.New("--agent-snapshot-enabled is required when --agent-publisher-gateway-enabled is set"))
	}
	missingRequired := false
	for flagName, value := range map[string]string{
		"--agent-publisher-gateway-endpoint":    cmd.AgentPublisherGateway.Endpoint,
		"--agent-publisher-gateway-policy-file": cmd.AgentPublisherGateway.PolicyFile,
		"--agent-publisher-gateway-token-file":  cmd.AgentPublisherGateway.TokenFile,
	} {
		if value == "" {
			missingRequired = true
			errs = multierror.Append(errs, fmt.Errorf("%s is required when --agent-publisher-gateway-enabled is set", flagName))
		}
	}
	if !missingRequired {
		if err := publisher.ValidateGatewayConfig(cmd.agentPublisherGatewayConfig()); err != nil {
			errs = multierror.Append(errs, err)
		}
	}
	return errs.ErrorOrNil()
}

func (cmd *RunCommand) agentPublisherGatewayConfig() publisher.GatewayConfig {
	return publisher.GatewayConfig{
		Endpoint:          cmd.AgentPublisherGateway.Endpoint,
		PolicyFile:        cmd.AgentPublisherGateway.PolicyFile,
		TokenFile:         cmd.AgentPublisherGateway.TokenFile,
		CACertificateFile: cmd.AgentPublisherGateway.CACertificateFile,
		RequestTimeout:    cmd.AgentPublisherGateway.RequestTimeout,
		LeaseDuration:     cmd.AgentPublisherGateway.LeaseDuration,
		MaxResponseBytes:  cmd.AgentPublisherGateway.MaxResponseBytes,
		SnapshotCanonicalizer: snapshot.Canonicalizer{
			MaxContentBytes: cmd.AgentSnapshots.MaxBytes,
			MaxEntries:      cmd.AgentSnapshots.MaxFiles,
			TempDir:         cmd.AgentSnapshots.TempDir,
		},
	}
}

func (cmd *RunCommand) buildAgentPublisherGateway(
	store publisher.Store,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	actions publisher.ActionsModeReader,
) (publisher.Executor, error) {
	if !cmd.AgentPublisherGateway.Enabled {
		return nil, fmt.Errorf("publisher gateway is disabled")
	}
	config := cmd.agentPublisherGatewayConfig()
	config.ActionsMode = actions
	executor, err := publisher.NewGatewayExecutor(store, metadata, content, config)
	if err != nil {
		return nil, fmt.Errorf("build publisher gateway: %w", err)
	}
	return executor, nil
}
