package atccmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
)

func TestAgentPublisherGatewayFlagDefaults(t *testing.T) {
	command := &atccmd.ATCCommand{}
	parser := flags.NewParser(command, flags.Default)
	parser.NamespaceDelimiter = "-"
	run := parser.Find("run")

	for name, defaultValue := range map[string][]string{
		"agent-publisher-gateway-enabled":             nil,
		"agent-publisher-gateway-endpoint":            nil,
		"agent-publisher-gateway-policy-file":         nil,
		"agent-publisher-gateway-token-file":          nil,
		"agent-publisher-gateway-ca-certificate-file": nil,
		"agent-publisher-gateway-request-timeout":     {"30s"},
		"agent-publisher-gateway-lease-duration":      {"5m"},
		"agent-publisher-gateway-max-response-bytes":  {"1048576"},
	} {
		option := run.FindOptionByLongName(name)
		if option == nil {
			t.Errorf("--%s is missing", name)
			continue
		}
		if defaultValue != nil && strings.Join(option.Default, ",") != strings.Join(defaultValue, ",") {
			t.Errorf("--%s default = %v, want %v", name, option.Default, defaultValue)
		}
	}
}

func TestAgentPublisherGatewayValidationIsDefaultDisabledAndFailClosed(t *testing.T) {
	command := &atccmd.RunCommand{}
	command.AgentPublisherGateway.RequestTimeout = 30 * time.Second
	command.AgentPublisherGateway.LeaseDuration = 5 * time.Minute
	command.AgentPublisherGateway.MaxResponseBytes = 1 << 20
	if err := atccmd.ValidateAgentPublisherGatewayForTest(command); err != nil {
		t.Fatalf("default-disabled validation: %v", err)
	}

	command.AgentPublisherGateway.Endpoint = "https://gateway.example"
	if err := atccmd.ValidateAgentPublisherGatewayForTest(command); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("disabled configuration error = %v", err)
	}

	command.AgentPublisherGateway.Enabled = true
	if err := atccmd.ValidateAgentPublisherGatewayForTest(command); err == nil ||
		!strings.Contains(err.Error(), "snapshot-enabled") ||
		!strings.Contains(err.Error(), "policy-file") ||
		!strings.Contains(err.Error(), "token-file") {
		t.Fatalf("incomplete enabled configuration error = %v", err)
	}

	command.AgentSnapshots.Enabled = true
	command.AgentPublisherGateway.PolicyFile = "/etc/concourse/publisher/policy.json"
	command.AgentPublisherGateway.TokenFile = "/etc/concourse/publisher/token"
	if err := atccmd.ValidateAgentPublisherGatewayForTest(command); err != nil {
		t.Fatalf("valid enabled configuration: %v", err)
	}

	command.AgentPublisherGateway.Endpoint = "http://gateway.example"
	if err := atccmd.ValidateAgentPublisherGatewayForTest(command); err == nil || !strings.Contains(err.Error(), "HTTPS origin") {
		t.Fatalf("insecure endpoint error = %v", err)
	}
}
