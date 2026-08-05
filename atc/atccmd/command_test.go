package atccmd_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/acme/autocert"
)

type CommandSuite struct {
	suite.Suite
	*require.Assertions
}

func (s *CommandSuite) TestLetsEncryptDefaultIsUpToDate() {
	cmd := &atccmd.ATCCommand{}

	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	opt := parser.Find("run").FindOptionByLongName("lets-encrypt-acme-url")
	s.NotNil(opt)

	s.Equal(opt.Default, []string{autocert.DefaultACMEDirectory})
}

func (s *CommandSuite) TestInvalidConcurrentRequestLimitAction() {
	cmd := &atccmd.RunCommand{}
	parser := flags.NewParser(cmd, flags.None)
	_, err := parser.ParseArgs([]string{
		"--client-secret",
		"client-secret",
		"--concurrent-request-limit",
		fmt.Sprintf("%s:2", atc.GetInfo),
	})

	s.Contains(
		err.Error(),
		fmt.Sprintf("action '%s' is not supported", atc.GetInfo),
	)
}

func (s *CommandSuite) TestKubernetesFlags() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	runCmd := parser.Find("run")
	s.NotNil(runCmd, "run subcommand should exist")

	nsOpt := runCmd.FindOptionByLongName("kubernetes-namespace")
	s.NotNil(nsOpt, "--kubernetes-namespace flag should exist")
	s.Contains(nsOpt.Description, "Kubernetes namespace")

	kubeconfigOpt := runCmd.FindOptionByLongName("kubernetes-kubeconfig")
	s.NotNil(kubeconfigOpt, "--kubernetes-kubeconfig flag should exist")
	s.Contains(kubeconfigOpt.Description, "kubeconfig")

	resolveKey := runCmd.FindOptionByLongName("kubernetes-artifact-daemon-resolve-capability-key")
	s.NotNil(resolveKey, "resolve capability key file flag should exist")
	resolveTTL := runCmd.FindOptionByLongName("kubernetes-artifact-daemon-resolve-capability-ttl")
	s.NotNil(resolveTTL, "resolve capability lifetime flag should exist")
	s.Equal([]string{"2h"}, resolveTTL.Default)
}

func (s *CommandSuite) TestAgentSnapshotFlagDefaults() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"
	runCmd := parser.Find("run")

	enabled := runCmd.FindOptionByLongName("agent-snapshot-enabled")
	s.NotNil(enabled)
	replication := runCmd.FindOptionByLongName("agent-snapshot-replication-factor")
	s.NotNil(replication)
	s.Equal([]string{"2"}, replication.Default)
	maxBytes := runCmd.FindOptionByLongName("agent-snapshot-max-bytes")
	s.NotNil(maxBytes)
	s.Equal([]string{"10737418240"}, maxBytes.Default)
	maxFiles := runCmd.FindOptionByLongName("agent-snapshot-max-files")
	s.NotNil(maxFiles)
	s.Equal([]string{"100000"}, maxFiles.Default)
	bindingRetention := runCmd.FindOptionByLongName("agent-snapshot-binding-retention")
	s.NotNil(bindingRetention)
	s.Equal([]string{"168h"}, bindingRetention.Default)
	orphanGrace := runCmd.FindOptionByLongName("agent-snapshot-orphan-grace-period")
	s.NotNil(orphanGrace)
	s.Equal([]string{"1h"}, orphanGrace.Default)
	gcInterval := runCmd.FindOptionByLongName("agent-snapshot-gc-interval")
	s.NotNil(gcInterval)
	s.Equal([]string{"5m"}, gcInterval.Default)
	repairInterval := runCmd.FindOptionByLongName("agent-snapshot-repair-interval")
	s.NotNil(repairInterval)
	s.Equal([]string{"10m"}, repairInterval.Default)
	tempDir := runCmd.FindOptionByLongName("agent-snapshot-temp-dir")
	s.NotNil(tempDir)
	s.Empty(tempDir.Default)
}

func (s *CommandSuite) TestAgentWorkflowRunReconcilerFlagDefaults() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"
	runCmd := parser.Find("run")

	interval := runCmd.FindOptionByLongName("agent-workflow-run-reconciler-interval")
	s.NotNil(interval)
	s.Equal([]string{"10s"}, interval.Default)
	timeout := runCmd.FindOptionByLongName("agent-workflow-run-admission-timeout")
	s.NotNil(timeout)
	s.Equal([]string{"15m"}, timeout.Default)
}

func (s *CommandSuite) TestAgentWorkflowRunReconcilerDurationsAreValid() {
	tests := map[string]struct {
		interval time.Duration
		timeout  time.Duration
		want     string
	}{
		"zero interval":     {interval: 0, timeout: time.Minute, want: "reconciler-interval must be positive"},
		"negative interval": {interval: -time.Second, timeout: time.Minute, want: "reconciler-interval must be positive"},
		"zero timeout":      {interval: time.Second, timeout: 0, want: "admission-timeout must be positive"},
		"negative timeout":  {interval: time.Second, timeout: -time.Second, want: "admission-timeout must be positive"},
		"timeout too short": {interval: 10 * time.Second, timeout: 19 * time.Second, want: "at least twice"},
	}
	for name, test := range tests {
		s.Run(name, func() {
			command := &atccmd.RunCommand{}
			command.AgentWorkflowRuns.ReconcilerInterval = test.interval
			command.AgentWorkflowRuns.AdmissionTimeout = test.timeout
			err := atccmd.ValidateAgentWorkflowRunsForTest(command)
			s.Error(err)
			s.Contains(err.Error(), test.want)
		})
	}

	command := &atccmd.RunCommand{}
	command.AgentWorkflowRuns.ReconcilerInterval = 10 * time.Second
	command.AgentWorkflowRuns.AdmissionTimeout = 20 * time.Second
	s.NoError(atccmd.ValidateAgentWorkflowRunsForTest(command))
}

func (s *CommandSuite) TestWorkflowRunTemplateGracePeriodFlagDefault() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"
	runCmd := parser.Find("run")

	grace := runCmd.FindOptionByLongName("gc-workflow-run-template-grace-period")
	s.NotNil(grace)
	s.Equal([]string{"24h"}, grace.Default)
}

func (s *CommandSuite) TestWorkflowRunTemplateGracePeriodOutlastsAdmission() {
	tests := map[string]struct {
		grace   time.Duration
		timeout time.Duration
		want    string
	}{
		"zero grace":      {grace: 0, timeout: 15 * time.Minute, want: "must be positive"},
		"negative grace":  {grace: -time.Second, timeout: 15 * time.Minute, want: "must be positive"},
		"grace too short": {grace: 10 * time.Minute, timeout: 15 * time.Minute, want: "must be greater than --agent-workflow-run-admission-timeout"},
		"grace equal": {grace: 15 * time.Minute, timeout: 15 * time.Minute,
			want: "must be greater than --agent-workflow-run-admission-timeout"},
	}
	for name, test := range tests {
		s.Run(name, func() {
			command := &atccmd.RunCommand{}
			command.GC.WorkflowRunTemplateGracePeriod = test.grace
			command.AgentWorkflowRuns.AdmissionTimeout = test.timeout
			err := atccmd.ValidateGarbageCollectionForTest(command)
			s.Error(err)
			s.Contains(err.Error(), test.want)
		})
	}

	command := &atccmd.RunCommand{}
	command.GC.WorkflowRunTemplateGracePeriod = 24 * time.Hour
	command.AgentWorkflowRuns.AdmissionTimeout = 15 * time.Minute
	s.NoError(atccmd.ValidateGarbageCollectionForTest(command))
}

func (s *CommandSuite) TestWorkflowRunTemplateRetirementPeriodFlagDefault() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"
	runCmd := parser.Find("run")

	retirement := runCmd.FindOptionByLongName("gc-workflow-run-template-retirement-period")
	s.NotNil(retirement)
	s.Equal([]string{"720h"}, retirement.Default)
}

func (s *CommandSuite) TestWorkflowRunTemplateRetirementPeriodValidation() {
	command := &atccmd.RunCommand{}
	command.GC.WorkflowRunTemplateGracePeriod = 24 * time.Hour
	command.AgentWorkflowRuns.AdmissionTimeout = 15 * time.Minute

	command.GC.WorkflowRunTemplateRetirementPeriod = -time.Second
	err := atccmd.ValidateGarbageCollectionForTest(command)
	s.Error(err)
	s.Contains(err.Error(), "--gc-workflow-run-template-retirement-period must not be negative")

	// zero disables retirement rather than misconfiguring it
	command.GC.WorkflowRunTemplateRetirementPeriod = 0
	s.NoError(atccmd.ValidateGarbageCollectionForTest(command))

	command.GC.WorkflowRunTemplateRetirementPeriod = 720 * time.Hour
	s.NoError(atccmd.ValidateGarbageCollectionForTest(command))
}

func (s *CommandSuite) TestAgentSnapshotNumericBoundsAreAlwaysPositive() {
	for name, mutate := range map[string]func(*atccmd.RunCommand){
		"replication":       func(command *atccmd.RunCommand) { command.AgentSnapshots.ReplicationFactor = 0 },
		"max bytes":         func(command *atccmd.RunCommand) { command.AgentSnapshots.MaxBytes = -1 },
		"max files":         func(command *atccmd.RunCommand) { command.AgentSnapshots.MaxFiles = 0 },
		"binding retention": func(command *atccmd.RunCommand) { command.AgentSnapshots.BindingRetention = 0 },
		"orphan grace":      func(command *atccmd.RunCommand) { command.AgentSnapshots.OrphanGracePeriod = -time.Second },
		"gc interval":       func(command *atccmd.RunCommand) { command.AgentSnapshots.GCInterval = 0 },
		"repair interval":   func(command *atccmd.RunCommand) { command.AgentSnapshots.RepairInterval = -time.Second },
	} {
		s.Run(name, func() {
			command := &atccmd.RunCommand{}
			command.AgentSnapshots.ReplicationFactor = 2
			command.AgentSnapshots.MaxBytes = 10 << 30
			command.AgentSnapshots.MaxFiles = 100000
			command.AgentSnapshots.BindingRetention = 7 * 24 * time.Hour
			command.AgentSnapshots.OrphanGracePeriod = time.Hour
			command.AgentSnapshots.GCInterval = 5 * time.Minute
			command.AgentSnapshots.RepairInterval = 10 * time.Minute
			mutate(command)
			err := atccmd.ValidateAgentSnapshotsForTest(command)
			s.Error(err)
			s.Contains(err.Error(), "must be positive")
		})
	}
}

func (s *CommandSuite) TestAgentSnapshotLimitsMayOnlyLowerCanonicalizerDefaults() {
	for name, mutate := range map[string]func(*atccmd.RunCommand){
		"content bytes": func(command *atccmd.RunCommand) {
			command.AgentSnapshots.MaxBytes = snapshot.DefaultMaxSnapshotContentBytes + 1
		},
		"entries": func(command *atccmd.RunCommand) {
			command.AgentSnapshots.MaxFiles = snapshot.DefaultMaxSnapshotEntries + 1
		},
	} {
		s.Run(name, func() {
			command := &atccmd.RunCommand{}
			command.AgentSnapshots.ReplicationFactor = 2
			command.AgentSnapshots.MaxBytes = snapshot.DefaultMaxSnapshotContentBytes
			command.AgentSnapshots.MaxFiles = snapshot.DefaultMaxSnapshotEntries
			mutate(command)
			err := atccmd.ValidateAgentSnapshotsForTest(command)
			s.Error(err)
			s.Contains(err.Error(), "must not exceed")
		})
	}
}

func (s *CommandSuite) TestEnabledAgentSnapshotsRequireK8sDaemonAndCompleteMTLS() {
	command := &atccmd.RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.ReplicationFactor = 2
	command.AgentSnapshots.MaxBytes = 10 << 30
	command.AgentSnapshots.MaxFiles = 100000
	command.AgentSnapshots.BindingRetention = 7 * 24 * time.Hour
	command.AgentSnapshots.OrphanGracePeriod = time.Hour
	command.AgentSnapshots.GCInterval = 5 * time.Minute
	command.AgentSnapshots.RepairInterval = 10 * time.Minute
	command.AgentSnapshots.TempDir = s.T().TempDir()

	err := atccmd.ValidateAgentSnapshotsForTest(command)
	s.Error(err)
	s.Contains(err.Error(), "kubernetes-namespace")
	s.Contains(err.Error(), "artifact-daemon-host-path")
	s.Contains(err.Error(), "artifact-daemon-service")
	s.Contains(err.Error(), "mTLS")

	command.Kubernetes.Namespace = "cicd"
	command.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	command.Kubernetes.ArtifactDaemonService = "artifact-daemon"
	command.Kubernetes.ArtifactDaemonPort = 7780
	command.Kubernetes.ArtifactDaemonTLSCert = "/cert"
	command.Kubernetes.ArtifactDaemonTLSKey = "/key"
	command.Kubernetes.ArtifactDaemonTLSCACert = "/ca"
	command.Kubernetes.ArtifactDaemonResolveCapabilityKey = "/resolve-capability/key"
	s.NoError(atccmd.ValidateAgentSnapshotsForTest(command))
}

func (s *CommandSuite) TestEnabledAgentSnapshotsRequireDedicatedAbsoluteTempDir() {
	command := &atccmd.RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.ReplicationFactor = 2
	command.AgentSnapshots.MaxBytes = 10 << 30
	command.AgentSnapshots.MaxFiles = 100000
	command.AgentSnapshots.BindingRetention = 7 * 24 * time.Hour
	command.AgentSnapshots.OrphanGracePeriod = time.Hour
	command.AgentSnapshots.GCInterval = 5 * time.Minute
	command.AgentSnapshots.RepairInterval = 10 * time.Minute
	command.Kubernetes.Namespace = "cicd"
	command.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	command.Kubernetes.ArtifactDaemonService = "artifact-daemon"
	command.Kubernetes.ArtifactDaemonPort = 7780
	command.Kubernetes.ArtifactDaemonTLSCert = "/cert"
	command.Kubernetes.ArtifactDaemonTLSKey = "/key"
	command.Kubernetes.ArtifactDaemonTLSCACert = "/ca"

	err := atccmd.ValidateAgentSnapshotsForTest(command)
	s.Error(err)
	s.Contains(err.Error(), "agent-snapshot-temp-dir")

	command.AgentSnapshots.TempDir = "relative/scratch"
	err = atccmd.ValidateAgentSnapshotsForTest(command)
	s.Error(err)
	s.Contains(err.Error(), "absolute")

	command.AgentSnapshots.TempDir = s.T().TempDir()
	s.NoError(atccmd.ValidateAgentSnapshotsForTest(command))
}

func (s *CommandSuite) TestBuildTrackerIntervalFlagRemoved() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	runCmd := parser.Find("run")
	s.NotNil(runCmd, "run subcommand should exist")

	opt := runCmd.FindOptionByLongName("build-tracker-interval")
	s.Nil(opt, "--build-tracker-interval should not exist; build tracker is notification-only")
}

func (s *CommandSuite) TestKubernetesFieldsExistOnRunCommand() {
	cmd := &atccmd.RunCommand{}
	s.Equal("", cmd.Kubernetes.Namespace, "namespace should default to empty string")
	s.Equal("", cmd.Kubernetes.Kubeconfig, "kubeconfig should default to empty string")

	cmd.Kubernetes.Namespace = "ci-workers"
	cmd.Kubernetes.Kubeconfig = "/etc/k8s/config"

	s.Equal("ci-workers", cmd.Kubernetes.Namespace)
	s.Equal("/etc/k8s/config", cmd.Kubernetes.Kubeconfig)
}

// K8s runtime startup requires the DaemonSet artifact cache. Without it,
// every step-produced artifact reads via exec into the producer pod, which
// fails once the reaper deletes the pod. The web must refuse to start in that
// configuration rather than silently fall back to the broken exec path.
// See track
// route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418.

func (s *CommandSuite) TestK8sRuntimeRequiresArtifactDaemonHostPath() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	// ArtifactDaemonHostPath intentionally left empty.

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.Error(err, "expected validation to fail when K8s runtime is on and DaemonSet host path is unset")
	s.Contains(err.Error(), "kubernetes-artifact-daemon-host-path is required")
}

func (s *CommandSuite) TestK8sRuntimeAcceptsConfiguredDaemonHostPath() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	cmd.Kubernetes.ArtifactHelperImage = "registry.example/concourse/artifact-helper@sha256:" + strings.Repeat("a", 64)
	cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey = "/resolve-capability/key"
	cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL = 2 * time.Hour

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.NoError(err, "expected validation to pass when DaemonSet host path is set")
}

func (s *CommandSuite) TestK8sRuntimeRejectsCapabilityTTLShorterThanAdmissionAndRetryBound() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	cmd.Kubernetes.ArtifactHelperImage = "registry.example/concourse/artifact-helper@sha256:" + strings.Repeat("a", 64)
	cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey = "/resolve-capability/key"
	cmd.Kubernetes.PodSchedulingTimeout = 2 * time.Hour
	cmd.Kubernetes.PodStartupTimeout = time.Hour
	cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL = 3 * time.Hour

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.Error(err)
	s.Contains(err.Error(), "resolve-capability-ttl")
}

func (s *CommandSuite) TestK8sRuntimeRequiresImmutableArtifactHelperImage() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	cmd.Kubernetes.ArtifactDaemonResolveCapabilityKey = "/resolve-capability/key"
	cmd.Kubernetes.ArtifactDaemonResolveCapabilityTTL = 2 * time.Hour

	for _, image := range []string{"", "alpine:latest", "registry.example/concourse/artifact-helper:1.0"} {
		cmd.Kubernetes.ArtifactHelperImage = image
		err := atccmd.ValidateK8sRuntimeForTest(cmd)
		s.Error(err)
		s.Contains(err.Error(), "kubernetes-artifact-helper-image")
		s.Contains(err.Error(), "exact sha256 digest")
	}

	cmd.Kubernetes.ArtifactHelperImage = "registry.example/concourse/artifact-helper@sha256:" + strings.Repeat("b", 64)
	s.NoError(atccmd.ValidateK8sRuntimeForTest(cmd))
}

func (s *CommandSuite) TestK8sRuntimeRequiresResolveCapabilityKey() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.Error(err)
	s.Contains(err.Error(), "kubernetes-artifact-daemon-resolve-capability-key")
}

func (s *CommandSuite) TestK8sRuntimeValidationSkippedWhenK8sDisabled() {
	cmd := &atccmd.RunCommand{}
	// Namespace empty — K8s runtime not enabled.

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.NoError(err, "expected validation to be a no-op when --kubernetes-namespace is empty")
}

func TestSuite(t *testing.T) {
	suite.Run(t, &CommandSuite{
		Assertions: require.New(t),
	})
}
