package atccmd_test

import (
	"fmt"
	"github.com/concourse/concourse/hangar"
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
	"github.com/concourse/concourse/atc/runs"
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
	component := atccmd.NewPipelineRunReclaimerComponentForTest(db.NewPipelineRunReclaimLifecycle(nil), time.Now, gc.DefaultPipelineRunReclaimBatchSize)
	s.Equal(atc.ComponentReclaimerPipelineRuns, component.Component.Name)
	s.Equal(time.Minute, component.Interval)
	s.NotNil(component.Runnable)
}

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

// ADR-0002 as configuration: the fail-open cache, the strict inputs and the
// outputs are three different buckets or disk namespaces, and web refuses to
// start when any two coincide.
func (s *CommandSuite) TestK8sRuntimeRefusesSharedStorageNamespaces() {
	for _, buckets := range [][3]string{
		{"shared", "shared", ""},
		{"shared", "", "shared"},
		{"", "shared", "shared"},
	} {
		cmd := &atccmd.RunCommand{}
		cmd.Kubernetes.Namespace = "concourse"
		cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
		cmd.Kubernetes.CacheBucket, cmd.Kubernetes.InputBucket, cmd.Kubernetes.OutputBucket = buckets[0], buckets[1], buckets[2]

		err := atccmd.ValidateK8sRuntimeForTest(cmd)
		s.Error(err, "expected startup to refuse buckets %v", buckets)
		s.Contains(err.Error(), `"shared"`)
	}

	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	cmd.Kubernetes.CacheBucket, cmd.Kubernetes.InputBucket, cmd.Kubernetes.OutputBucket = "cache", "inputs", "outputs"
	s.NoError(atccmd.ValidateK8sRuntimeForTest(cmd))
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

func (s *CommandSuite) writeRBACConfig(body string) *atccmd.RunCommand {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "rbac.yml")
	s.NoError(os.WriteFile(path, []byte(body), 0644))

	cmd := &atccmd.RunCommand{}
	cmd.ConfigRBAC = flag.File(path)
	return cmd
}

func (s *CommandSuite) TestCustomRolesRefuseWeakerRunCreationThanSetPipeline() {
	cmd := s.writeRBACConfig("pipeline-operator:\n- CreatePipelineRunV2\n")

	err := atccmd.ValidateCustomRolesForTest(cmd)
	s.Error(err, "expected startup to refuse run creation weaker than set-pipeline")
	s.Contains(err.Error(), atc.CreatePipelineRunV2)
	s.Contains(err.Error(), atc.SaveConfig)
	s.Contains(err.Error(), "pipeline-operator")
	s.Contains(err.Error(), "member")
}

func (s *CommandSuite) TestCustomRolesRefuseRaisedSetPipelineWithDefaultRunCreation() {
	cmd := s.writeRBACConfig("owner:\n- SaveConfig\n")

	err := atccmd.ValidateCustomRolesForTest(cmd)
	s.Error(err, "raising set-pipeline alone leaves run creation weaker than it")
	s.Contains(err.Error(), atc.CreatePipelineRunV2)
	s.Contains(err.Error(), atc.SaveConfig)
}

func (s *CommandSuite) TestCustomRolesAcceptEqualOrStrongerRunCreation() {
	equal := s.writeRBACConfig("member:\n- CreatePipelineRunV2\n")
	s.NoError(atccmd.ValidateCustomRolesForTest(equal))

	stronger := s.writeRBACConfig("owner:\n- CreatePipelineRunV2\n")
	s.NoError(atccmd.ValidateCustomRolesForTest(stronger))

	together := s.writeRBACConfig("pipeline-operator:\n- CreatePipelineRunV2\n- SaveConfig\n")
	s.NoError(atccmd.ValidateCustomRolesForTest(together))
}

func (s *CommandSuite) TestCustomRolesReloadWhenThePathChangesAfterAnEmptyLoad() {
	cmd := &atccmd.RunCommand{}
	s.NoError(atccmd.ValidateCustomRolesForTest(cmd), "no RBAC file is a valid configuration")

	withFile := s.writeRBACConfig("viewer:\n- NotSaveConfig\n")
	cmd.ConfigRBAC = withFile.ConfigRBAC

	err := atccmd.ValidateCustomRolesForTest(cmd)
	s.Error(err, "a path set after an earlier empty load must be parsed, not served from cache")
	s.Contains(err.Error(), "unknown action NotSaveConfig")
}

func (s *CommandSuite) TestCustomRolesRefuseDuplicateRunCreationAssignments() {
	cmd := s.writeRBACConfig("owner:\n- CreatePipelineRunV2\npipeline-operator:\n- CreatePipelineRunV2\n")

	err := atccmd.ValidateCustomRolesForTest(cmd)
	s.Error(err, "expected startup to refuse an action assigned to multiple roles")
	s.Contains(err.Error(), atc.CreatePipelineRunV2)
	s.Contains(err.Error(), "assigned more than once")
}

func (s *CommandSuite) TestCustomRolesRefuseDuplicateSaveConfigAssignments() {
	cmd := s.writeRBACConfig("owner:\n- SaveConfig\nmember:\n- SaveConfig\n")

	err := atccmd.ValidateCustomRolesForTest(cmd)
	s.Error(err, "expected startup to refuse an action assigned to multiple roles")
	s.Contains(err.Error(), atc.SaveConfig)
	s.Contains(err.Error(), "assigned more than once")
}

func (s *CommandSuite) TestCustomRolesAcceptTheStockConfiguration() {
	cmd := atccmd.RunCommand{}

	s.NoError(atccmd.ValidateCustomRolesForTest(&cmd), "no --config-rbac must leave the defaults in force")
}

// The artifact daemon speaks mTLS or plaintext, never a mix. A cert without a
// key and CA used to set ArtifactDaemonTLSEnabled anyway, so the ATC dialled
// https at a plaintext daemon while presenting no client certificate, with
// nothing failing until the first artifact read.

func (s *CommandSuite) TestK8sRuntimeRefusesPartialDaemonTLSTriple() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	cmd.Kubernetes.ArtifactDaemonTLSCert = "/etc/tls/client.crt"
	// Key and CA cert intentionally left empty.

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.Error(err, "expected validation to fail when only part of the daemon TLS triple is set")
	s.Contains(err.Error(), "kubernetes-artifact-daemon-tls-key")
	s.Contains(err.Error(), "kubernetes-artifact-daemon-tls-ca-cert")
}

func (s *CommandSuite) TestK8sRuntimeAcceptsCompleteDaemonTLSTriple() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	cmd.Kubernetes.ArtifactDaemonTLSCert = "/etc/tls/client.crt"
	cmd.Kubernetes.ArtifactDaemonTLSKey = "/etc/tls/client.key"
	cmd.Kubernetes.ArtifactDaemonTLSCACert = "/etc/tls/ca.crt"

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.NoError(err, "expected a complete daemon TLS triple to be accepted")
}

func (s *CommandSuite) TestK8sRuntimeAcceptsNoDaemonTLSAtAll() {
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.Namespace = "concourse"
	cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"

	err := atccmd.ValidateK8sRuntimeForTest(cmd)
	s.NoError(err, "expected plaintext daemon traffic to remain a valid configuration")
}

// A flag with no reader is worse than no flag: --help advertises a default
// (--gc-interval 30s) that the binary does not honour, and an operator who
// tunes it sees no change and no error. GC components run at the hardcoded
// defaultComponentInterval; the idtoken signing-key lifecycler reads only its
// rotation and grace periods.
func (s *CommandSuite) TestOrphanedIntervalFlagsRemoved() {
	cmd := &atccmd.ATCCommand{}
	parser := flags.NewParser(cmd, flags.Default)
	parser.NamespaceDelimiter = "-"

	runCmd := parser.Find("run")
	s.NotNil(runCmd, "run subcommand should exist")

	for flag, why := range map[string]string{
		"gc-interval":                "GC components run at the hardcoded default component interval",
		"gc-check-recycle-period":    "nothing reads it; the checks collector takes no period",
		"signing-key-check-interval": "the signing key lifecycler reads only rotation and grace periods",
	} {
		s.Nil(runCmd.FindOptionByLongName(flag), "--%s should not exist: %s", flag, why)
	}
}

func TestSuite(t *testing.T) {
	suite.Run(t, &CommandSuite{
		Assertions: require.New(t),
	})
}

func (s *CommandSuite) TestHangarRuntimeRequiresCompleteDaemonTLSAndExactCapabilityKey() {
	validKey := filepath.Join(s.T().TempDir(), "hangar.key")
	s.Require().NoError(os.WriteFile(validKey, []byte("0123456789abcdef0123456789abcdef"), 0600))

	tests := map[string]struct {
		configure func(*atccmd.RunCommand)
		want      string
	}{
		"daemon hostPath": {
			configure: func(cmd *atccmd.RunCommand) { cmd.Kubernetes.ArtifactDaemonHostPath = "" },
			want:      "kubernetes-artifact-daemon-host-path",
		},
		// A partial triple is refused before Hangar's own check runs, by the
		// one daemon-TLS predicate every site shares; Hangar's "complete TLS"
		// refusal is reached only when no TLS is configured at all.
		"TLS certificate": {
			configure: func(cmd *atccmd.RunCommand) { cmd.Kubernetes.ArtifactDaemonTLSCert = "" },
			want:      "partially configured",
		},
		"TLS private key": {
			configure: func(cmd *atccmd.RunCommand) { cmd.Kubernetes.ArtifactDaemonTLSKey = "" },
			want:      "partially configured",
		},
		"TLS CA": {
			configure: func(cmd *atccmd.RunCommand) { cmd.Kubernetes.ArtifactDaemonTLSCACert = "" },
			want:      "partially configured",
		},
		"no TLS at all": {
			configure: func(cmd *atccmd.RunCommand) {
				cmd.Kubernetes.ArtifactDaemonTLSCert = ""
				cmd.Kubernetes.ArtifactDaemonTLSKey = ""
				cmd.Kubernetes.ArtifactDaemonTLSCACert = ""
			},
			want: "complete artifact daemon TLS",
		},
		"capability key path": {
			configure: func(cmd *atccmd.RunCommand) { cmd.Kubernetes.HangarWarrantKey = "" },
			want:      "kubernetes-hangar-warrant-key",
		},
		"exact raw key": {
			configure: func(cmd *atccmd.RunCommand) {
				shortKey := filepath.Join(s.T().TempDir(), "short.key")
				s.Require().NoError(os.WriteFile(shortKey, []byte("too-short"), 0600))
				cmd.Kubernetes.HangarWarrantKey = shortKey
			},
			want: "exactly 32 raw bytes",
		},
	}

	for name, test := range tests {
		s.Run(name, func() {
			cmd := &atccmd.RunCommand{}
			cmd.Kubernetes.Namespace = "concourse"
			cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
			cmd.Kubernetes.ArtifactDaemonTLSCert = "/tls/client.crt"
			cmd.Kubernetes.ArtifactDaemonTLSKey = "/tls/client.key"
			cmd.Kubernetes.ArtifactDaemonTLSCACert = "/tls/ca.crt"
			cmd.Kubernetes.HangarEnabled = true
			cmd.Kubernetes.HangarWarrantKey = validKey
			cmd.Kubernetes.HangarWarrantTTL = 15 * time.Minute
			test.configure(cmd)

			err := atccmd.ValidateK8sRuntimeForTest(cmd)
			s.Error(err)
			s.Contains(err.Error(), test.want)
		})
	}
}

func (s *CommandSuite) TestHangarRuntimeAcceptsCompleteConfigurationAndDisabledCompatibility() {
	validKey := filepath.Join(s.T().TempDir(), "hangar.key")
	s.Require().NoError(os.WriteFile(validKey, []byte("0123456789abcdef0123456789abcdef"), 0600))

	enabled := &atccmd.RunCommand{}
	enabled.Kubernetes.Namespace = "concourse"
	enabled.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	enabled.Kubernetes.ArtifactDaemonTLSCert = "/tls/client.crt"
	enabled.Kubernetes.ArtifactDaemonTLSKey = "/tls/client.key"
	enabled.Kubernetes.ArtifactDaemonTLSCACert = "/tls/ca.crt"
	enabled.Kubernetes.HangarEnabled = true
	enabled.Kubernetes.HangarWarrantKey = validKey
	enabled.Kubernetes.HangarWarrantTTL = 15 * time.Minute
	s.NoError(atccmd.ValidateK8sRuntimeForTest(enabled))

	disabled := &atccmd.RunCommand{}
	disabled.Kubernetes.Namespace = "concourse"
	disabled.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
	disabled.Kubernetes.HangarWarrantKey = "/does/not/exist"
	s.NoError(atccmd.ValidateK8sRuntimeForTest(disabled), "disabled Hangar must not load or require its key")
}

func (s *CommandSuite) TestHangarRuntimeRejectsCapabilityTTLOutsideCoreBound() {
	validKey := filepath.Join(s.T().TempDir(), "hangar.key")
	s.Require().NoError(os.WriteFile(validKey, []byte("0123456789abcdef0123456789abcdef"), 0600))
	for _, ttl := range []time.Duration{0, -time.Second, hangar.MaxWarrantTTL + time.Nanosecond} {
		cmd := &atccmd.RunCommand{}
		cmd.Kubernetes.Namespace = "concourse"
		cmd.Kubernetes.ArtifactDaemonHostPath = "/var/concourse/artifacts"
		cmd.Kubernetes.ArtifactDaemonTLSCert = "/tls/client.crt"
		cmd.Kubernetes.ArtifactDaemonTLSKey = "/tls/client.key"
		cmd.Kubernetes.ArtifactDaemonTLSCACert = "/tls/ca.crt"
		cmd.Kubernetes.HangarEnabled = true
		cmd.Kubernetes.HangarWarrantKey = validKey
		cmd.Kubernetes.HangarWarrantTTL = ttl
		err := atccmd.ValidateK8sRuntimeForTest(cmd)
		s.Error(err)
		s.Contains(err.Error(), "kubernetes-hangar-warrant-ttl")
	}
}

// TWO NEW COMPONENTS RAN ON EVERY DEPLOYMENT, INCLUDING NON-KUBERNETES ONES.
//
// The capture advancer and the read-lease cleanup were appended to the
// component table outside both the Kubernetes block and any output-plane
// check. Requirement 59 asks for a deployment with capture disabled to behave
// as it did before, and two `components` rows, two advisory locks and two
// queries a minute is not that -- however cheap each pass is.
func (s *CommandSuite) TestTheOutputPlanesComponentsRunOnlyWhereThePlaneIsEnabled() {
	names := func(components []atccmd.RunnableComponent) []string {
		var named []string
		for _, component := range components {
			named = append(named, component.Component.Name)
		}

		return named
	}

	off := &atccmd.RunCommand{}
	s.Empty(names(atccmd.HangarOutputComponentsForTest(off, nil)),
		"a deployment that never opted into the output plane registers one of its components")

	// The plane, with no activation epoch: the two that do the plane's work,
	// and not the status surface, which has a plane to describe only once one
	// has been activated.
	on := &atccmd.RunCommand{}
	on.Kubernetes.OutputPlaneEnabled = true
	s.ElementsMatch([]string{
		atc.ComponentHangarOutputCapture,
		atc.ComponentHangarOutputReadLeaseCleanup,
	}, names(atccmd.HangarOutputComponentsForTest(on, nil)))

	// And with one, all three of the plane's own -- capture, read-lease
	// cleanup and status -- without which the assertions above would pass
	// against a plane that registers nothing at all. Run cancellation is not
	// among them; every web node registers it (runComponents).
	activated := &atccmd.RunCommand{}
	activated.Kubernetes.OutputPlaneEnabled = true
	activated.Kubernetes.OutputActivationEpoch = 7
	activated.Kubernetes.OutputBucket = "output-bucket"
	activated.Kubernetes.OutputTenant = "tenant-a"
	s.ElementsMatch([]string{
		atc.ComponentHangarOutputCapture,
		atc.ComponentHangarOutputReadLeaseCleanup,
		atc.ComponentHangarOutputStatus,
	}, names(atccmd.HangarOutputComponentsForTest(activated, nil)))
}

// A REQUIRED SECRET THAT NOTHING EVER OPENED.
//
// --kubernetes-hangar-output-warrant-key was refused when empty, compared
// with two other flags for distinctness, and never read: the message said "a
// control plane that cannot mint one can make no call at all" while nothing
// established that the file contained a key at all, let alone one a minter
// would accept. That is the failure class this track spent three review rounds
// removing -- a refusal claiming more than the code checks.
func (s *CommandSuite) TestTheOutputCapabilityKeyIsReadAtStartupAndNotMerelyNamed() {
	dir := s.T().TempDir()
	valid := filepath.Join(dir, "capability.key")
	s.Require().NoError(os.WriteFile(valid, []byte("0123456789abcdef0123456789abcdef"), 0600))
	short := filepath.Join(dir, "short.key")
	s.Require().NoError(os.WriteFile(short, []byte("too short"), 0600))

	plane := func(key string) *atccmd.RunCommand {
		cmd := &atccmd.RunCommand{}
		cmd.Kubernetes.OutputPlaneEnabled = true
		cmd.Kubernetes.OutputWarrantKey = key
		cmd.Kubernetes.OutputActivationEpoch = 7
		cmd.Kubernetes.ArtifactDaemonTLSCert = filepath.Join(dir, "tls.crt")
		cmd.Kubernetes.ArtifactDaemonTLSKey = filepath.Join(dir, "tls.key")
		cmd.Kubernetes.ArtifactDaemonTLSCACert = filepath.Join(dir, "ca.crt")
		cmd.Kubernetes.OutputOperationTimeout = 15 * time.Minute
		cmd.Kubernetes.OutputSealDeadline = 30 * time.Minute
		cmd.Kubernetes.OutputCaptureDeadline = 2 * time.Hour
		cmd.Kubernetes.OutputLeaseTerm = 15 * time.Minute
		cmd.Kubernetes.OutputLeaseRenewInterval = 30 * time.Second

		return cmd
	}

	err := atccmd.ValidateHangarOutputPlaneForTest(plane(filepath.Join(dir, "absent.key")))
	s.Require().Error(err, "a capability key that is not there was accepted, so the refusal for "+
		"an EMPTY flag is the only thing that was ever checked about it")
	s.Contains(err.Error(), "kubernetes-hangar-output-warrant-key")

	err = atccmd.ValidateHangarOutputPlaneForTest(plane(short))
	s.Require().Error(err, "a file too short to be a capability key was accepted at startup; the "+
		"first control call would be the thing that discovered it")
	s.Contains(err.Error(), "kubernetes-hangar-output-warrant-key")

	// The control: a real key passes, so the two refusals above are about the
	// key rather than about the rest of the configuration.
	s.NoError(atccmd.ValidateHangarOutputPlaneForTest(plane(valid)))

	for _, timeout := range []time.Duration{0, -time.Second, 24 * time.Hour} {
		invalid := plane(valid)
		invalid.Kubernetes.OutputOperationTimeout = timeout
		err = atccmd.ValidateHangarOutputPlaneForTest(invalid)
		s.Require().Error(err)
		s.Contains(err.Error(), "kubernetes-hangar-output-operation-timeout")
	}

	// And the epoch, which every minted capability names and which this gate
	// asked for only under capture. The chart has always refused it here.
	noEpoch := plane(valid)
	noEpoch.Kubernetes.OutputActivationEpoch = 0
	err = atccmd.ValidateHangarOutputPlaneForTest(noEpoch)
	s.Require().Error(err)
	s.Contains(err.Error(), "kubernetes-hangar-output-activation-epoch")

	// A deployment with no output plane neither requires nor opens a key.
	off := &atccmd.RunCommand{}
	off.Kubernetes.OutputWarrantKey = "/does/not/exist"
	s.NoError(atccmd.ValidateHangarOutputPlaneForTest(off))
}

// AN ACCEPTED FENCE THAT NOTHING WOULD EVER CONVERGE.
//
// The cancellation worker was registered only inside the output plane's
// components, while the cancel route answers on every web node and Run
// activation zero stops admission but leaves running Runs running. On a node
// with no output plane, or one that has turned admission off, an accepted
// cancellation was a durable fence with no worker to converge it: the Run
// stayed running forever. The worker's own operations (scheduler debt, build
// abort, candidate settlement, terminalization) need no output plane, and a
// source operation it cannot reach stays typed debt with the Run running.
func (s *CommandSuite) TestTheRunCancellationWorkerRunsWithOrWithoutAnOutputPlane() {
	// runComponents reads neither the activation epoch nor the plane flag, so
	// one configuration stands for all of them; what this pins is that the
	// worker lives there and not among the plane's components. That
	// backendComponents appends runComponents is not asserted here: building
	// the full backend list needs a live database and runtime.
	cmd := &atccmd.RunCommand{}
	cmd.Kubernetes.OutputPlaneEnabled = true

	var found []atccmd.RunnableComponent
	for _, component := range atccmd.RunComponentsForTest(cmd, nil) {
		if component.Component.Name == atc.ComponentRunCancellation {
			found = append(found, component)
		}
	}
	s.Require().Len(found, 1, "the cancellation worker must be registered exactly once")
	s.True(found[0].Interval > 0 && found[0].Interval <= 30*time.Second, "cancellation must have a periodic fallback no slower than 30 seconds")
	worker, ok := found[0].Runnable.(*runs.CancellationWorker)
	s.Require().True(ok, "the component must run the cancellation worker")
	s.NotEmpty(worker.OwnerID)
	s.NotNil(worker.Actions)

	for _, component := range atccmd.HangarOutputComponentsForTest(cmd, nil) {
		s.NotEqual(atc.ComponentRunCancellation, component.Component.Name,
			"the output plane registers the cancellation worker a second time")
	}
}
