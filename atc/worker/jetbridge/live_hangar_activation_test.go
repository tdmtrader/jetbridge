// hangar_live only, never live: this contract stands up a whole release of the
// chart on a disposable cluster, as live_hangar_cluster_test.go does, and walks
// its activation epoch -- one-way transitions nobody may make against the
// deployed cluster from a test. Its CI home is the hangar-cluster-contract job in
// deploy/k8s-e2e-pipeline.yml; build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

import (
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// TestLiveHangarActivationJobRecreatedAfterItsTransitionIsANoOp is
// hangar_walk_chart_wiring's K3s contract (acceptance A4): the activation walk
// driven through the chart's own hook, under the same Argo-shaped syncs as the
// bootstrap contract. The name is the one the k8s-e2e hangar-cluster-contract
// job runs by, and it still says what is proved: the activation Job is now the
// walk hook, which every sync recreates after its transitions have committed.
//
// It proves:
//
//  1. the walk to `off` that execution control renders from S6 writes no row;
//  2. one sync from no row, after the runbook's storage and base-workload
//     syncs (S1-S6), with the output workloads and the walk to `output` as its
//     only activation step (S10 plus S11), creates the walk Job only after the
//     database Job has completed and ends with base and output `enabled`. The
//     inventory and reclaimer controllers come up in that sync's waves, before
//     the walk has begun their epoch's row, and show no restarts: they wait as
//     non-owners rather than exit;
//  3. a second sync whose walk Job differs in its pod template, as an image
//     bump makes it differ, recreates the hook -- BeforeHookCreation, where a
//     plain apply would fail on the immutable template -- and that walk makes
//     no transition: the row's revision and updated_at are unchanged;
//  4. `target=base` then leaves output `draining`, and a later
//     `target=output` fails with logs naming output `draining`.
//
// Attest dials each output daemon by pod IP and verifies a certificate that
// names only the daemon's DNS name. Before the walk from no row, the contract
// runs the walk Job's rendered manifest without --tls-server-name and requires
// the handshake to fail, so the attestations that follow are known to have
// verified the certificate rather than skipped it. That walk runs at a probe
// epoch of its own, so the configured epoch still has no row when the real
// walk starts: it begins the probe epoch's row and leaves it `initial`.
func TestLiveHangarActivationJobRecreatedAfterItsTransitionIsANoOp(t *testing.T) {
	cluster := newLiveCluster(t, "ha", 45*time.Minute)
	release := cluster.names.release
	walkJob := release + "-hangar-output-walk"
	databaseJob := release + "-hangar-bootstrap-database"
	steps := []string{"S1", "S2"}

	// Web's init container migrates; the bootstrap writes the Secrets and,
	// as its PostSync step, the activation database role's credential.
	cluster.sync("S1-S2 migrate, bootstrap, disk init", cluster.through(steps...), liveSyncOptions{})
	steps = append(steps, "S3")
	cluster.sync("S3 store up", cluster.through(steps...), liveSyncOptions{})
	steps = append(steps, "S4", "S5", "S6")
	base := cluster.sync("S4-S6 strict inputs and the output daemons, walk to off", cluster.through(steps...), liveSyncOptions{})
	cluster.assertJobLogged(base, walkJob, "nothing written")
	if _, found := cluster.epochRowAt(liveClusterEpoch); found {
		t.Fatal("the walk to off wrote an activation epoch row")
	}

	cluster.assertWalkVerifiesTheDaemonCertificate(append(cluster.through(steps...), "hangarOutput.activation.target=base"))
	steps = append(steps, "S10", "S11")

	walked := cluster.sync("S10+S11 output workloads, walk to output from no row", cluster.through(steps...), liveSyncOptions{})
	enabled := cluster.epochRow()
	if enabled.base != "enabled" || enabled.output != "enabled" {
		t.Fatalf("after one walk to output the row is %+v, want both facets enabled", enabled)
	}
	cluster.assertJobLogged(walked, walkJob, fmt.Sprintf("began activation epoch %d", liveClusterEpoch))
	cluster.assertJobLogged(walked, walkJob, fmt.Sprintf("enabled epoch %d's output facet", liveClusterEpoch))
	cluster.assertCreatedAfterCompletion(walkJob, databaseJob)
	for _, component := range []string{"hangar-output-inventory", "hangar-output-reclaimer"} {
		cluster.assertNoRestarts(component)
	}
	for _, label := range []string{executioncontrol.ReadyLabel, output.ReadyLabel} {
		liveDiskWaitNodeLabel(t, cluster.ctx, cluster.client, cluster.node, label, "ready")
	}

	// An image bump changes the walk Job's pod template, which a Job never
	// takes in place. The annotation stands in for the image: the cluster has
	// only the one image loaded, and the template is what makes it matter.
	bumped := cluster.sync("S10+S11 again, the walk's pod template changed", cluster.through(steps...), liveSyncOptions{
		mutate: func(object *unstructured.Unstructured) {
			if object.GetKind() != "Job" || object.GetName() != walkJob {
				return
			}
			if err := unstructured.SetNestedField(object.Object, "2", "spec", "template", "metadata", "annotations", "contract.jetbridge.dev/image-bump"); err != nil {
				t.Fatal(err)
			}
		},
	})
	if bumped.hooks[walkJob] == walked.hooks[walkJob] {
		t.Fatal("the second sync did not recreate the walk Job")
	}
	cluster.assertJobLogged(bumped, walkJob, "already enabled; no transition made")
	if row := cluster.epochRow(); !row.same(enabled) {
		t.Fatalf("the repeated walk moved the row: %+v -> %+v", enabled, row)
	}

	lowered := cluster.sync("walk to base", append(cluster.through(steps...), "hangarOutput.activation.target=base"), liveSyncOptions{})
	drained := cluster.epochRow()
	if drained.base != "enabled" || drained.output != "draining" {
		t.Fatalf("after a walk to base the row is %+v, want base enabled and output draining", drained)
	}
	cluster.assertJobLogged(lowered, walkJob, fmt.Sprintf("epoch %d's output facet is draining", liveClusterEpoch))

	refused := cluster.sync("walk to output over a draining output facet", cluster.through(steps...), liveSyncOptions{
		failingHooks: map[string]bool{walkJob: true},
	})
	if logs := refused.failed[walkJob]; !strings.Contains(logs, "output facet is draining") {
		t.Fatalf("the walk to output over a draining facet failed without naming it:\n%s", logs)
	}
	if row := cluster.epochRow(); !row.same(drained) {
		t.Fatalf("the refused walk moved the row: %+v -> %+v", drained, row)
	}
}

// liveClusterProbeEpoch is the epoch the certificate probe walks, so that the
// configured epoch's row is still absent for the walk from no row.
const liveClusterProbeEpoch = 99

type liveEpochRow struct {
	base, output string
	revision     int64
	updatedAt    time.Time
}

func (row liveEpochRow) same(other liveEpochRow) bool {
	return row.base == other.base && row.output == other.output && row.revision == other.revision && row.updatedAt.Equal(other.updatedAt)
}

func (cluster *liveCluster) epochRow() liveEpochRow {
	cluster.t.Helper()
	row, found := cluster.epochRowAt(liveClusterEpoch)
	if !found {
		cluster.t.Fatalf("activation epoch %d has no row", liveClusterEpoch)
	}
	return row
}

func (cluster *liveCluster) epochRowAt(epoch int64) (liveEpochRow, bool) {
	t := cluster.t
	t.Helper()
	db, stop := cluster.database()
	defer stop()
	var row liveEpochRow
	err := db.QueryRowContext(cluster.ctx, `
		SELECT base_state, output_state, revision, updated_at
		  FROM hangar_output_activation_epochs WHERE epoch_id = $1`, epoch).
		Scan(&row.base, &row.output, &row.revision, &row.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return liveEpochRow{}, false
	}
	if err != nil {
		t.Fatalf("read activation epoch %d: %v", epoch, err)
	}
	return row, true
}

// assertCreatedAfterCompletion: one Job was created no earlier than another
// completed, which is what a later wave of PostSync hooks means.
func (cluster *liveCluster) assertCreatedAfterCompletion(later, earlier string) {
	t := cluster.t
	t.Helper()
	jobs := cluster.client.BatchV1().Jobs(cluster.names.namespace)
	after, err := jobs.Get(cluster.ctx, later, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job %s: %v", later, err)
	}
	before, err := jobs.Get(cluster.ctx, earlier, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job %s: %v", earlier, err)
	}
	if before.Status.CompletionTime == nil {
		t.Fatalf("Job %s has not completed", earlier)
	}
	if after.CreationTimestamp.Before(before.Status.CompletionTime) {
		t.Fatalf("Job %s was created at %s, before Job %s completed at %s", later,
			after.CreationTimestamp, earlier, before.Status.CompletionTime)
	}
}

// jobLogsFor is the logs of one incarnation of a Job, by its UID: a Job
// recreated under the same name is a different Job.
func (cluster *liveCluster) jobLogsFor(uid types.UID) string {
	return cluster.podLogs("batch.kubernetes.io/controller-uid=" + string(uid))
}

func (cluster *liveCluster) assertJobLogged(result liveSyncResult, name, line string) {
	t := cluster.t
	t.Helper()
	job, err := cluster.client.BatchV1().Jobs(cluster.names.namespace).Get(cluster.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job %s: %v", name, err)
	}
	result.only(t, "Job", name)
	if logs := cluster.jobLogsFor(job.UID); !strings.Contains(logs, line) {
		t.Fatalf("Job %s did not log %q:\n%s", name, line, logs)
	}
}

// deleteJob deletes one incarnation of a Job and waits for it and its pods to
// be gone, so nothing of it is left to finish.
func (cluster *liveCluster) deleteJob(name string, uid types.UID) {
	t := cluster.t
	t.Helper()
	background := metav1.DeletePropagationBackground
	if err := cluster.client.BatchV1().Jobs(cluster.names.namespace).Delete(cluster.ctx, name,
		metav1.DeleteOptions{PropagationPolicy: &background, Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		t.Fatalf("delete Job %s: %v", name, err)
	}
	for {
		_, err := cluster.client.BatchV1().Jobs(cluster.names.namespace).Get(cluster.ctx, name, metav1.GetOptions{})
		pods, listErr := cluster.client.CoreV1().Pods(cluster.names.namespace).List(cluster.ctx, metav1.ListOptions{LabelSelector: "batch.kubernetes.io/controller-uid=" + string(uid)})
		if listErr != nil {
			t.Fatalf("list Job %s's pods: %v", name, listErr)
		}
		if apierrors.IsNotFound(err) && len(pods.Items) == 0 {
			return
		}
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatalf("get Job %s: %v", name, err)
		}
		liveDiskPause(t, cluster.ctx, "Job "+name+" and its pods to be deleted")
	}
}

// createRendered creates a rendered object as it is, the way Argo replaces a
// resource that went missing from the live state.
func (cluster *liveCluster) createRendered(object *unstructured.Unstructured) types.UID {
	t := cluster.t
	t.Helper()
	classified := cluster.classify(object)
	created, err := cluster.resource(classified).Create(cluster.ctx, classified.obj, metav1.CreateOptions{FieldManager: liveClusterFieldManager})
	if err != nil {
		t.Fatalf("create %s %s: %v", object.GetKind(), object.GetName(), err)
	}
	return created.GetUID()
}

// assertWalkVerifiesTheDaemonCertificate runs the walk Job's rendered
// manifest without --tls-server-name, at the probe epoch. The daemon's
// certificate names only its DNS name and the walk dials pod IPs, so a
// handshake that verifies must fail -- and fail before anything is attested:
// the walk begins the probe epoch's row and leaves it `initial`, and the
// configured epoch still has none.
func (cluster *liveCluster) assertWalkVerifiesTheDaemonCertificate(sets []string) {
	t := cluster.t
	t.Helper()
	names := cluster.names
	// The artifact daemon serves the output plane. Its certificate names its
	// headless Service and the node IP; the walk dials pod IPs, which it
	// names nowhere.
	block, _ := pem.Decode(cluster.secret(names.daemonTLS).Data["tls.crt"])
	if block == nil {
		t.Fatalf("Secret %s holds no certificate", names.daemonTLS)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(certificate.DNSNames, names.outputPlaneServerName()) {
		t.Fatalf("the artifact daemon's certificate names %v; want %s among them", certificate.DNSNames, names.outputPlaneServerName())
	}
	for _, address := range certificate.IPAddresses {
		if address.String() != cluster.nodeIP {
			t.Fatalf("the artifact daemon's certificate names %v; the walk dials pod IPs, and only the node IP may be named", certificate.IPAddresses)
		}
	}

	var job *unstructured.Unstructured
	for _, object := range cluster.render(sets) {
		if object.GetKind() == "Job" && object.GetName() == names.release+"-hangar-output-walk" {
			job = object
		}
	}
	if job == nil {
		t.Fatal("the render holds no walk Job")
	}
	job.SetName(job.GetName() + "-no-server-name")
	job.SetAnnotations(nil)
	if err := unstructured.SetNestedField(job.Object, int64(0), "spec", "backoffLimit"); err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	command, _, _ := unstructured.NestedStringSlice(container, "command")
	var kept []any
	dropped, probed := false, false
	for _, argument := range command {
		switch {
		case strings.HasPrefix(argument, "--tls-server-name="):
			dropped = true
			continue
		case strings.HasPrefix(argument, "--epoch="):
			argument = fmt.Sprintf("--epoch=%d", liveClusterProbeEpoch)
			probed = true
		}
		kept = append(kept, argument)
	}
	if !dropped || !probed {
		t.Fatalf("the walk Job renders no --tls-server-name or no --epoch: %v", command)
	}
	container["command"] = kept
	if err := unstructured.SetNestedSlice(job.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	uid := cluster.createRendered(job)
	defer cluster.deleteJob(job.GetName(), uid)

	deadline := time.Now().Add(3 * time.Minute)
	for {
		live, err := cluster.client.BatchV1().Jobs(names.namespace).Get(cluster.ctx, job.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		complete, failed := liveJobState(live)
		if complete {
			t.Fatalf("a walk without --tls-server-name succeeded against a certificate that names no IP:\n%s", cluster.jobLogsFor(uid))
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a walk without --tls-server-name neither failed nor succeeded:\n%s", cluster.jobLogsFor(uid))
		}
		liveDiskPause(t, cluster.ctx, "a walk without --tls-server-name")
	}
	if logs := cluster.jobLogsFor(uid); !strings.Contains(logs, "x509:") {
		t.Fatalf("a walk without --tls-server-name failed for a reason other than certificate verification:\n%s", logs)
	}
	if row, found := cluster.epochRowAt(liveClusterProbeEpoch); !found || row.base != "initial" || row.output != "initial" {
		t.Fatalf("the failed walk left the probe epoch's row at %+v (found %t), want both facets initial", row, found)
	}
	if _, found := cluster.epochRowAt(liveClusterEpoch); found {
		t.Fatal("the probe walk wrote the configured epoch's row")
	}
}
