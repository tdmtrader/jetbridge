package steps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This composes the existing local PostgreSQL, envtest, GCS emulator and daemon
// fixtures. The daemon independently checks the lease before serving bytes, so
// the old one-minute admission fails even though this small tree reads quickly.
func TestRunManagedReadsProtectTheConfiguredOperationBudget(t *testing.T) {
	RegisterGomegaFailHandler()
	var resources []brine.ResourceDefinition
	for _, definition := range ResourceDefinitions() {
		switch definition.Name {
		case "postgres", "jetbridge-db":
			resources = append(resources, definition)
		case "real-cluster":
			// Start only the local API server; no kubelet or remote cluster.
			definition.Factory = func(map[string]any) (any, error) { return startRealCluster() }
			resources = append(resources, definition)
		}
	}
	registry, err := brine.NewResourceRegistry(resources)
	if err != nil {
		t.Fatal(err)
	}
	definitions := append(RunOutputRuntimeDefinitions(), RunOutputCandidateDefinitions()...)
	definitions = append(definitions, RunResultDefinitions()...)
	definitions = append(definitions, RunInputBindingDefinitions()...)
	definitions = append(definitions, brine.DefineCheckUsing[RunInputAdmission]("its input and result reads survive a fifteen minute operation budget", []string{"real-cluster"}, func(in RunInputAdmission, _ brine.Params, rec *brine.Recorder, res brine.Resources) error {
		return checkRunManagedReadBudget(in, rec, res)
	}))
	feature := brine.ParseFeatureText("managed-read-timeout.feature", `Feature: Run reads use their selected output plane budget
  Scenario: Input delivery and a retained result download admit sufficient leases
    Given a Run producer and a ready output node
    When its runtime producer publishes a successful review
    And its Run records the published source release
    And its published producer finishes as "succeeded"
    And its aggregate Run result is inspected
    And its other Run jobs finish as "succeeded"
    And its scheduler consumes all requested Run work
    And the aggregate Run completion is attempted
    And its result is admitted as a named input with "one source under two names"
    Then its input and result reads survive a fifteen minute operation budget
`)
	var events bytes.Buffer
	state := brine.NewResourceState(registry)
	t.Cleanup(func() {
		if failures := state.DisposeAll(); len(failures) != 0 {
			t.Errorf("dispose local resources: %v", failures)
		}
	})
	pipeline := brine.NewPipeline(brine.NewStepRegistry(definitions), brine.NewEmitter(WithDisposalVerdict(&events))).WithResources(state)
	result, code, err := pipeline.Run([]*brine.ParsedFeature{feature}, brine.TagFilter{})
	if err != nil || code != 0 || result.Scenarios != 1 || result.Passed != 1 {
		t.Fatalf("Run managed-read regression: %+v, %v; %s", result, err, events.String())
	}
}

func checkRunManagedReadBudget(in RunInputAdmission, rec *brine.Recorder, res brine.Resources) error {
	if in.Err != nil || in.Source.Candidate == nil {
		return fmt.Errorf("input admission failed: %v", in.Err)
	}
	const operationTimeout = 15 * time.Minute
	publication := in.Source
	publication.Start.Daemon.Output.cmd.Args = append(publication.Start.Daemon.Output.cmd.Args, "--output-timeout=15m")
	config := jetbridge.NewConfig("default", "")
	config.OutputOperationTimeout = operationTimeout
	config.PodStartupTimeout = 3 * time.Minute
	config.PodSchedulingTimeout = 5 * time.Minute
	source, signer, config, err := configureRunReadPlaneForClient(publication, rec, publication.Candidate.Runtime.Client, config)
	if err != nil {
		return err
	}
	reader := runs.ResultReader{Conn: publication.Start.DB.Conn, Minter: signer, Source: func(ctx context.Context, epoch executioncontrol.ActivationEpoch) (runs.ResultSource, error) {
		return source.ForResultRead(ctx, epoch)
	}}
	tree, err := reader.Read(context.Background(), publication.Start.Creation.Run.ID(), publication.Start.Plan.RunResult.Name)
	if err != nil {
		return fmt.Errorf("download result with the daemon's fifteen minute budget: %w", err)
	}
	defer tree.Close()
	if tree.Digest != publication.Candidate.Record.Ref.Digest {
		return fmt.Errorf("downloaded result differs from the retained exact tree")
	}
	if err := checkPersistedReadBudget(publication.Start.DB.Conn, "result", 1, 20*time.Minute); err != nil {
		return err
	}

	conn := publication.Start.DB.Conn
	factory := db.NewPipelineRunFactory(conn, publication.Start.DB.LockFactory)
	keys := hangaroutput.ControlKeyRing{ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Keys: []hangaroutput.ControlKeyEntry{{Epoch: executioncontrol.ActivationEpoch(hangarEpoch), PublicKey: base64.StdEncoding.EncodeToString(publication.Start.Daemon.ControlPublic)}}}
	starter := &runs.ExecutionStarter{Conn: conn, Factory: factory, Source: source, Epoch: executioncontrol.ActivationEpoch(hangarEpoch), Verifier: keys}
	starter.SetInputReadMinter(signer)
	definition, found, err := factory.Definition(in.Run.ID)
	if err != nil || !found {
		return fmt.Errorf("load consuming definition (found %t): %w", found, err)
	}
	task := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	plan := atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, Config: task.Config}
	var buildID, pipelineID int
	if err = conn.QueryRow(`SELECT id,pipeline_id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, in.Run.ID, definition.Materialized.Jobs[0].Name).Scan(&buildID, &pipelineID); err != nil {
		return err
	}
	spec := runtime.ContainerSpec{TeamID: in.Template.TeamID(), Dir: "/workspace", Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}}
	for _, route := range task.RunInputs {
		spec.Inputs = append(spec.Inputs, runtime.Input{RunInput: route.Name, DestinationPath: filepath.Join(spec.Dir, route.Input)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	spec, err = starter.PrepareTask(ctx, buildID, plan, spec)
	if err != nil {
		return err
	}
	row, err := publication.Start.DB.PersistNamedWorker("managed-read-budget")
	if err != nil {
		return err
	}
	config.OutputPlaneEnabled, config.HangarEnabled = true, true
	config.OutputActivationEpoch = int64(hangarEpoch)
	config.ArtifactDaemonHostPath = publication.Start.Daemon.Output.Root
	client := publication.Candidate.Runtime.Client
	worker := jetbridge.NewWorker(row, client, config)
	cluster, err := getRealCluster(res)
	if err != nil {
		return err
	}
	worker.SetExecutor(jetbridge.NewSPDYExecutor(client, cluster.env.Config))
	worker.SetOutputControls(jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(client), publication.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)))
	worker.SetExecutionPreparer(starter)
	metadata := db.ContainerMetadata{BuildID: buildID, PipelineID: pipelineID, Type: db.ContainerTypeTask}
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewBuildStepContainerOwner(buildID, "consume-input", spec.TeamID), metadata, spec, nil)
	if err != nil {
		return fmt.Errorf("admit task input read: %w", err)
	}
	// Both leases must cover five minutes of startup, two sequential sixteen
	// minute transfers, and the five minute lease margin: forty-two minutes.
	if err = checkPersistedReadBudget(conn, "input", len(spec.Inputs), 42*time.Minute); err != nil {
		return err
	}
	name := jetbridge.GeneratePodName(metadata, container.DBContainer().Handle())
	TrackDisposer(rec, "the managed-read budget pod "+name, func() error {
		zero := int64(0)
		return releasedIfGone(client.CoreV1().Pods(config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	})
	if _, err = container.Run(ctx, runtime.ProcessSpec{Path: "true"}, runtime.ProcessIO{}); err != nil {
		return err
	}
	pod, err := client.CoreV1().Pods(config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// Exercise the generated initializer's exact requests through the production
	// client; this host need not have the task image's wget executable.
	readerClient, err := source.ForResultRead(ctx, executioncontrol.ActivationEpoch(hangarEpoch))
	if err != nil {
		return err
	}
	verifier, err := output.NewReadWarrantVerifier(brineReadWarrantKey, output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		return err
	}
	initialized := 0
	for _, init := range pod.Spec.InitContainers {
		if !strings.HasPrefix(init.Name, "materialize-run-input-") {
			continue
		}
		encoded, found := scriptAssignment(init.Command[2], "REQUEST_B64")
		if !found {
			return fmt.Errorf("input initializer has no managed read request")
		}
		body, err := base64.StdEncoding.DecodeString(strings.Trim(encoded, "'"))
		if err != nil {
			return err
		}
		var request output.ManagedReadRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return err
		}
		if request.Ref != publication.Candidate.Record.Ref || request.Destination.Handle != container.DBContainer().Handle() {
			return fmt.Errorf("generated input request changed its admitted tree or container")
		}
		claims, err := verifier.Verify(request.Warrant, request.Ref, request.Destination)
		if err != nil {
			return err
		}
		if err := readerClient.MaterializeManagedOutput(ctx, request); err != nil {
			return fmt.Errorf("materialize generated input request: %w", err)
		}
		directory := filepath.Join(config.ArtifactDaemonHostPath, "steps", request.Destination.Handle, request.Destination.Volume)
		// The real materializer seals this fixture-owned directory read-only.
		// Restore deletion permission only during disposal, after the assertions.
		TrackDisposer(rec, "the sealed input directory "+request.Destination.Volume, func() error { return os.Chmod(directory, 0755) })
		body, err = os.ReadFile(filepath.Join(directory, ".hangar-materialized"))
		var sealed hangar.TreeRef
		if err != nil || json.Unmarshal(body, &sealed) != nil || sealed != request.Ref {
			return fmt.Errorf("input has no exact sealed receipt: %v", err)
		}
		var released bool
		if err := conn.QueryRow(`SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id=$1`, string(claims.ReadLeaseID)).Scan(&released); err != nil {
			return err
		}
		if !released {
			return fmt.Errorf("materialized input retained its read lease")
		}
		initialized++
	}
	if initialized != len(task.RunInputs) || initialized == 0 {
		return fmt.Errorf("not every admitted input was materialized")
	}
	return nil
}

func checkPersistedReadBudget(conn db.DbConn, kind string, expectedCount int, minimum time.Duration) error {
	rows, err := conn.Query(`SELECT lease_term_seconds, extract(epoch from expires_at-granted_at)
		FROM hangar_read_leases WHERE (destination_volume='result')=$1`, kind == "result")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var seconds int
		var protectedSeconds float64
		if err := rows.Scan(&seconds, &protectedSeconds); err != nil {
			return err
		}
		if time.Duration(seconds)*time.Second < minimum || protectedSeconds < minimum.Seconds() {
			return fmt.Errorf("%s read lease protects %ds; requires at least %s", kind, seconds, minimum)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != expectedCount || count == 0 {
		return fmt.Errorf("expected %d %s read admissions, found %d", expectedCount, kind, count)
	}
	return nil
}
