package atccmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atccmd"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/flag/v2"
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

func (s *CommandSuite) TestBuildTrackerIntervalFlagRemoved() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	runCmd := parser.Find("run")
	s.NotNil(runCmd, "run subcommand should exist")

	opt := runCmd.FindOptionByLongName("build-tracker-interval")
	s.Nil(opt, "--build-tracker-interval should not exist; build tracker is notification-only")
}

func (s *CommandSuite) TestPipelineRunReclaimerComponentIsBoundedAndPeriodic() {
	component := atccmd.NewPipelineRunReclaimerComponentForTest(commandRunReclaimLifecycle{}, time.Now, gc.DefaultPipelineRunReclaimBatchSize)
	s.Equal(atc.ComponentReclaimerPipelineRuns, component.Component.Name)
	s.Equal(time.Minute, component.Interval)
	s.NotNil(component.Runnable)
}

type commandRunReclaimLifecycle struct{}

func (commandRunReclaimLifecycle) ReclaimCandidateRunIDs(int) ([]int, error) { return nil, nil }
func (commandRunReclaimLifecycle) ReclaimBacklog() (int, error)              { return 0, nil }
func (commandRunReclaimLifecycle) DestroyReclaimableRun(int) (bool, error)   { return false, nil }
func (commandRunReclaimLifecycle) DeferRunReclaim(int, time.Time) error      { return nil }

var _ db.PipelineRunReclaimLifecycle = commandRunReclaimLifecycle{}

// The batch size is operator-tunable because the backlog metric can show the
// reclaimer failing to keep up with its one-minute interval, and there is no
// other lever. A default that drifted from the code's own would make that
// diagnosis wrong.
func (s *CommandSuite) TestPipelineRunReclaimBatchFlagDefaultsToTheCodeDefault() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	runCmd := parser.Find("run")
	s.NotNil(runCmd, "run subcommand should exist")

	opt := runCmd.FindOptionByLongName("pipeline-run-reclaim-batch")
	s.NotNil(opt, "--pipeline-run-reclaim-batch should exist")
	s.Equal([]string{strconv.Itoa(gc.DefaultPipelineRunReclaimBatchSize)}, opt.Default)
}

// The default is pinned above and the reclaimer honours whatever batch it is
// constructed with, but until this spec nothing joined the two: gcComponents
// could hand the constructor the package default and every test in the tree
// stayed green, which is exactly the "the flag exists but is dead" failure the
// flag was added to avoid.
func (s *CommandSuite) TestPipelineRunReclaimBatchFlagReachesTheComponent() {
	const configured = 5
	s.NotEqual(gc.DefaultPipelineRunReclaimBatchSize, configured, "the fixture has to differ from the default it is guarding against")

	cmd := &atccmd.RunCommand{}
	cmd.PipelineRunReclaimBatch = configured

	components, err := atccmd.GCComponentsForTest(cmd, lagertest.NewTestLogger("test"), nil, nil)
	s.NoError(err)

	var reclaimer atccmd.RunnableComponent
	var found bool
	for _, component := range components {
		if component.Component.Name == atc.ComponentReclaimerPipelineRuns {
			reclaimer, found = component, true
		}
	}
	s.True(found, "gc components should include the pipeline run reclaimer")

	sized, ok := reclaimer.Runnable.(interface{ BatchSize() int })
	s.True(ok, "the reclaimer should report the batch it was built with")
	s.Equal(configured, sized.BatchSize(), "the reclaimer must run on the configured batch, not the package default")
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

// A run parameter value is interpolated verbatim into the materialized payload
// config, so creating a run carries the same trust as setting a pipeline. An
// RBAC config that grants run creation to a weaker role than SaveConfig turns
// that equivalence into a credential-read escalation, so startup must refuse it.

func (s *CommandSuite) writeRBACConfig(body string) atccmd.RunCommand {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "rbac.yml")
	s.NoError(os.WriteFile(path, []byte(body), 0644))

	cmd := atccmd.RunCommand{}
	cmd.ConfigRBAC = flag.File(path)
	return cmd
}

func (s *CommandSuite) TestCustomRolesRefuseWeakerRunCreationThanSetPipeline() {
	cmd := s.writeRBACConfig("pipeline-operator:\n- CreatePipelineRun\n")

	err := atccmd.ValidateCustomRolesForTest(&cmd)
	s.Error(err, "expected startup to refuse run creation weaker than set-pipeline")
	s.Contains(err.Error(), atc.CreatePipelineRun)
	s.Contains(err.Error(), atc.SaveConfig)
	s.Contains(err.Error(), "pipeline-operator")
	s.Contains(err.Error(), "member")
}

func (s *CommandSuite) TestCustomRolesRefuseRaisedSetPipelineWithDefaultRunCreation() {
	cmd := s.writeRBACConfig("owner:\n- SaveConfig\n")

	err := atccmd.ValidateCustomRolesForTest(&cmd)
	s.Error(err, "raising set-pipeline alone leaves run creation weaker than it")
	s.Contains(err.Error(), atc.CreatePipelineRun)
	s.Contains(err.Error(), atc.SaveConfig)
}

func (s *CommandSuite) TestCustomRolesAcceptEqualOrStrongerRunCreation() {
	equal := s.writeRBACConfig("member:\n- CreatePipelineRun\n")
	s.NoError(atccmd.ValidateCustomRolesForTest(&equal))

	stronger := s.writeRBACConfig("owner:\n- CreatePipelineRun\n")
	s.NoError(atccmd.ValidateCustomRolesForTest(&stronger))

	together := s.writeRBACConfig("pipeline-operator:\n- CreatePipelineRun\n- SaveConfig\n")
	s.NoError(atccmd.ValidateCustomRolesForTest(&together))
}

func (s *CommandSuite) TestCustomRolesAcceptTheStockConfiguration() {
	cmd := atccmd.RunCommand{}

	s.NoError(atccmd.ValidateCustomRolesForTest(&cmd), "no --config-rbac must leave the defaults in force")
}

func TestSuite(t *testing.T) {
	suite.Run(t, &CommandSuite{
		Assertions: require.New(t),
	})
}
