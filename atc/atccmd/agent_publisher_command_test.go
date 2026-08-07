package atccmd_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
)

func TestAgentPublisherFlagSurface(t *testing.T) {
	command := &atccmd.ATCCommand{}
	parser := flags.NewParser(command, flags.Default)
	parser.NamespaceDelimiter = "-"
	run := parser.Find("run")

	for name, defaultValue := range map[string][]string{
		"agent-publisher-enabled":               nil,
		"agent-publisher-credential-root":       {"/run/concourse-publisher"},
		"agent-publisher-policy-file":           nil,
		"agent-publisher-credential-file":       nil,
		"agent-publisher-direct-git-enabled":    nil,
		"agent-publisher-request-timeout":       {"30s"},
		"agent-publisher-lease-duration":        {"5m"},
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

	oldFlags := []string{
		"agent-publisher-pull-requests-enabled",
		"agent-publisher-pr-resource-image",
		"agent-publisher-pr-poll-interval",
		"agent-publisher-pr-freshness-interval",
		"agent-publisher-gateway-enabled",
		"agent-publisher-gateway-endpoint",
		"agent-publisher-gateway-credential-root",
		"agent-publisher-gateway-policy-file",
		"agent-publisher-gateway-token-file",
		"agent-publisher-gateway-ca-certificate-file",
		"agent-publisher-gateway-request-timeout",
		"agent-publisher-gateway-lease-duration",
		"agent-publisher-gateway-max-response-bytes",
	}
	for _, name := range oldFlags {
		if option := run.FindOptionByLongName(name); option != nil {
			t.Errorf("legacy --%s is still accepted", name)
		}
		legacyParser := flags.NewParser(&atccmd.ATCCommand{}, flags.Default)
		legacyParser.NamespaceDelimiter = "-"
		_, err := legacyParser.ParseArgs([]string{"run", "--" + name})
		var flagError *flags.Error
		if !errors.As(err, &flagError) || flagError.Type != flags.ErrUnknownFlag {
			t.Errorf("legacy --%s parse error = %v, want unknown flag", name, err)
		}
	}
}

func TestAgentPublisherCredentialFileFlagParsesReferenceToAbsolutePath(t *testing.T) {
	command := &atccmd.ATCCommand{}
	parser := flags.NewParser(command, flags.Default)
	parser.NamespaceDelimiter = "-"
	option := parser.Find("run").FindOptionByLongName("agent-publisher-credential-file")
	if option == nil {
		t.Fatal("--agent-publisher-credential-file is missing")
	}

	value := "widget-git:/run/concourse-publisher/widget-git"
	if err := option.Set(&value); err != nil {
		t.Fatalf("parse credential file mapping: %v", err)
	}
	if got := command.RunCommand.AgentPublisher.CredentialFiles["widget-git"]; got != "/run/concourse-publisher/widget-git" {
		t.Fatalf("credential file mapping = %q, want mounted absolute path", got)
	}
}

func TestAgentPublisherCredentialFileFlagRejectsDuplicateReferences(t *testing.T) {
	command := &atccmd.ATCCommand{}
	parser := flags.NewParser(command, flags.Default)
	parser.NamespaceDelimiter = "-"
	option := parser.Find("run").FindOptionByLongName("agent-publisher-credential-file")
	if option == nil {
		t.Fatal("--agent-publisher-credential-file is missing")
	}

	first := "widget-git:/run/concourse-publisher/widget-git"
	if err := option.Set(&first); err != nil {
		t.Fatalf("parse first credential file mapping: %v", err)
	}
	duplicate := "widget-git:/run/concourse-publisher/replacement"
	if err := option.Set(&duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate credential mapping error = %v", err)
	}
	if got := command.RunCommand.AgentPublisher.CredentialFiles["widget-git"]; got != "/run/concourse-publisher/widget-git" {
		t.Fatalf("duplicate mapping changed credential path to %q", got)
	}
}

func TestAgentPublisherValidationIsDefaultDisabledAndFailClosed(t *testing.T) {
	command := &atccmd.RunCommand{}
	command.AgentPublisher.RequestTimeout = 30 * time.Second
	command.AgentPublisher.LeaseDuration = 5 * time.Minute
	command.AgentPublisher.CredentialRoot = "/run/concourse-publisher"
	if err := atccmd.ValidateAgentPublisherForTest(command); err != nil {
		t.Fatalf("default-disabled validation: %v", err)
	}

	command.AgentPublisher.PolicyFile = "/run/concourse-publisher-policy/policy.json"
	if err := atccmd.ValidateAgentPublisherForTest(command); err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("disabled configuration error = %v", err)
	}

	command.AgentPublisher.Enabled = true
	if err := atccmd.ValidateAgentPublisherForTest(command); err == nil ||
		!strings.Contains(err.Error(), "snapshot-enabled") ||
		!strings.Contains(err.Error(), "credential-file") ||
		!strings.Contains(err.Error(), "direct-git-enabled") {
		t.Fatalf("incomplete enabled configuration error = %v", err)
	}

	command.AgentSnapshots.Enabled = true
	command.AgentPublisher.CredentialFiles = map[string]string{
		"widget-git": "/run/concourse-publisher/widget-git",
	}
	command.AgentPublisher.DirectGitEnabled = true
	if err := atccmd.ValidateAgentPublisherForTest(command); err != nil {
		t.Fatalf("valid enabled configuration: %v", err)
	}
}

func TestAgentPublisherValidationBoundsPathsRequestsAndLeases(t *testing.T) {
	valid := func() *atccmd.RunCommand {
		command := &atccmd.RunCommand{}
		command.AgentSnapshots.Enabled = true
		command.AgentPublisher.Enabled = true
		command.AgentPublisher.PolicyFile = "/run/concourse-publisher-policy/policy.json"
		command.AgentPublisher.CredentialRoot = "/run/concourse-publisher"
		command.AgentPublisher.CredentialFiles = map[string]string{
			"widget-git": "/run/concourse-publisher/widget-git",
		}
		command.AgentPublisher.DirectGitEnabled = true
		command.AgentPublisher.RequestTimeout = 30 * time.Second
		command.AgentPublisher.LeaseDuration = 5 * time.Minute
		return command
	}

	tests := map[string]struct {
		mutate func(*atccmd.RunCommand)
		want   string
	}{
		"relative policy": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.PolicyFile = "policy.json"
			},
			want: "policy-file must be an absolute clean path",
		},
		"filesystem root credential boundary": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.CredentialRoot = "/"
			},
			want: "credential-root must be an absolute clean non-root path",
		},
		"relative credential": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.CredentialFiles["widget-git"] = "widget-git"
			},
			want: "credential-file",
		},
		"credential outside root": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.CredentialFiles["widget-git"] = "/run/other/widget-git"
			},
			want: "strictly beneath",
		},
		"zero request": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.RequestTimeout = 0
			},
			want: "request-timeout",
		},
		"request over one hour": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.RequestTimeout = time.Hour + time.Nanosecond
			},
			want: "request-timeout",
		},
		"lease not longer than request": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.LeaseDuration = command.AgentPublisher.RequestTimeout
			},
			want: "lease-duration",
		},
		"lease over one day": {
			mutate: func(command *atccmd.RunCommand) {
				command.AgentPublisher.LeaseDuration = 24*time.Hour + time.Nanosecond
			},
			want: "lease-duration",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			command := valid()
			test.mutate(command)
			err := atccmd.ValidateAgentPublisherForTest(command)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}
