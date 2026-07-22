package atccmd_test

import (
	"fmt"
	"testing"

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
}

func (s *CommandSuite) TestAgentSnapshotNumericBoundsAreAlwaysPositive() {
	for name, mutate := range map[string]func(*atccmd.RunCommand){
		"replication": func(command *atccmd.RunCommand) { command.AgentSnapshots.ReplicationFactor = 0 },
		"max bytes":   func(command *atccmd.RunCommand) { command.AgentSnapshots.MaxBytes = -1 },
		"max files":   func(command *atccmd.RunCommand) { command.AgentSnapshots.MaxFiles = 0 },
	} {
		s.Run(name, func() {
			command := &atccmd.RunCommand{}
			command.AgentSnapshots.ReplicationFactor = 2
			command.AgentSnapshots.MaxBytes = 10 << 30
			command.AgentSnapshots.MaxFiles = 100000
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

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.NoError(err, "expected validation to pass when DaemonSet host path is set")
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
