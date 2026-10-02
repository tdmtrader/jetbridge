// hangar_live only, never live: this contract stands up a whole release of the
// chart on a disposable cluster, as live_hangar_cluster_test.go does, and moves
// its activation epoch -- one-way steps nobody may take against the deployed
// cluster from a test. Its CI home is the hangar-cluster-contract job in
// deploy/k8s-e2e-pipeline.yml; build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// TestLiveHangarActivationJobRecreatedAfterItsTransitionIsANoOp is
// hangar_stores_enabled_in_cluster's K3s contract (its T10, acceptance A5):
// the activation runbook driven through the chart's own activation Jobs, under
// the same Argo-shaped syncs as the bootstrap contract.
//
// The case it exists for is the one Argo makes ordinary: an activation Job
// whose transition has committed is deleted before it completes -- a node
// drain, a TTL, an operator -- and Argo, finding it missing from a render that
// still holds it, creates it again. The recreated Job must not repeat the
// transition. So the enable-base Job is created from its rendered manifest with
// its command wrapped in `&& sleep 60`, which holds it running after the
// transition commits; the contract waits for the committed row, deletes the
// Job, creates the rendered manifest again unmodified, and requires that the
// new Job exits 0 as a no-op, the row's revision and updated_at are those of
// the committed transition, and the output facet is still initial, with
// capture and Run results off. It then continues through output enable.
//
// Attest dials each output daemon by pod IP and verifies a certificate that
// names only the daemon's DNS name. The contract first runs the attest Job
// without --tls-server-name and requires the handshake to fail and the row to
// stay untouched, so the attestation that follows is known to have verified
// the certificate rather than skipped it.
func TestLiveHangarActivationJobRecreatedAfterItsTransitionIsANoOp(t *testing.T) {
	cluster := newLiveCluster(t, "ha", 45*time.Minute)
	release := cluster.names.release
	steps := []string{"S1", "S2"}
	next := func(label string, ids ...string) liveSyncResult {
		t.Helper()
		steps = append(steps, ids...)
		return cluster.sync(label, cluster.through(steps...), liveSyncOptions{})
	}

	// Web's init container migrates; the bootstrap writes the Secrets and,
	// as its PostSync step, the activation database role's credential.
	cluster.sync("S1-S2 migrate, bootstrap, disk init", cluster.through(steps...), liveSyncOptions{})
	next("S3 store up", "S3")
	next("S4-S6 strict inputs and the output daemons", "S4", "S5", "S6")

	begin := next("S7 begin", "S7")
	began := cluster.epochRow()
	if began.base != "initial" || began.output != "initial" {
		t.Fatalf("after begin the activation epoch row is %+v, want both facets initial", began)
	}
	cluster.assertJobLogged(begin, release+"-hangar-output-activation-begin-1", fmt.Sprintf("began activation epoch %d", liveClusterEpoch))

	cluster.assertAttestVerifiesTheDaemonCertificate(cluster.through(append(append([]string{}, steps...), "S8")...))
	attest := next("S8 attest base", "S8")
	attested := cluster.epochRow()
	if attested.base != "attested" || attested.output != "initial" || attested.revision <= began.revision {
		t.Fatalf("after attest base the row is %+v (begin left %+v)", attested, began)
	}
	cluster.assertJobLogged(attest, release+"-hangar-output-activation-attest-1", fmt.Sprintf("attested epoch %d's base facet over the cohort digest", liveClusterEpoch))

	// S9, interrupted after its transition commits.
	steps = append(steps, "S9")
	enableJob := release + "-hangar-output-activation-enable-1"
	interrupted := cluster.sync("S9 enable base, held after its transition", cluster.through(steps...), liveSyncOptions{
		mutate: func(object *unstructured.Unstructured) {
			if object.GetKind() == "Job" && object.GetName() == enableJob {
				liveWrapJobCommand(t, object, "sleep 60")
			}
		},
		unwatched:          map[string]bool{enableJob: true},
		stopBeforePostSync: true,
	})
	committed := cluster.waitEpochRow("the enable-base transition to commit", func(row liveEpochRow) bool { return row.base == "enabled" })
	if committed.output != "initial" || committed.revision <= attested.revision {
		t.Fatalf("the committed enable-base row is %+v (attest left %+v)", committed, attested)
	}
	// The row commits a moment before the command prints that it did.
	held := cluster.runningJob(enableJob)
	cluster.waitJobLogged(held, fmt.Sprintf("enabled epoch %d's base facet", liveClusterEpoch))
	held = cluster.runningJob(enableJob)
	cluster.deleteJob(enableJob, held)

	recreated := cluster.createRendered(interrupted.only(t, "Job", enableJob))
	cluster.waitJobComplete("S9 recreated", enableJob)
	if logs := cluster.jobLogsFor(recreated); !strings.Contains(logs, "already enabled; no transition made") {
		t.Fatalf("the recreated enable Job did not report a no-op:\n%s", logs)
	}
	replayed := cluster.epochRow()
	if replayed.revision != committed.revision || !replayed.updatedAt.Equal(committed.updatedAt) || replayed.base != "enabled" || replayed.output != "initial" {
		t.Fatalf("the recreated enable Job moved the row: committed %+v, now %+v", committed, replayed)
	}
	cluster.assertCaptureOff()

	// The sync resumes once the Job is Healthy, and is unchanged by it.
	cluster.sync("S9 enable base, resumed", cluster.through(steps...), liveSyncOptions{})
	if row := cluster.epochRow(); !row.same(replayed) {
		t.Fatalf("the resumed sync moved the row: %+v -> %+v", replayed, row)
	}

	next("S10 output workloads", "S10")
	outputAttest := next("S11 attest output", "S11")
	if row := cluster.epochRow(); row.base != "enabled" || row.output != "attested" {
		t.Fatalf("after attest output the row is %+v", row)
	}
	cluster.assertJobLogged(outputAttest, release+"-hangar-output-activation-attest-1", fmt.Sprintf("attested epoch %d's output facet over the cohort digest", liveClusterEpoch))
	outputEnable := next("S12 enable output", "S12")
	enabled := cluster.epochRow()
	if enabled.base != "enabled" || enabled.output != "enabled" {
		t.Fatalf("after enable output the row is %+v, want both facets enabled", enabled)
	}
	cluster.assertJobLogged(outputEnable, release+"-hangar-output-activation-enable-1", fmt.Sprintf("enabled epoch %d's output facet", liveClusterEpoch))
	for _, label := range []string{executioncontrol.ReadyLabel, output.ReadyLabel} {
		liveDiskWaitNodeLabel(t, cluster.ctx, cluster.client, cluster.node, label, "ready")
	}
}

type liveEpochRow struct {
	base, output string
	revision     int64
	updatedAt    time.Time
}

func (row liveEpochRow) same(other liveEpochRow) bool {
	return row.base == other.base && row.output == other.output && row.revision == other.revision && row.updatedAt.Equal(other.updatedAt)
}

func (cluster *liveCluster) epochRow() liveEpochRow {
	t := cluster.t
	t.Helper()
	db, stop := cluster.database()
	defer stop()
	var row liveEpochRow
	if err := db.QueryRowContext(cluster.ctx, `
		SELECT base_state, output_state, revision, updated_at
		  FROM hangar_output_activation_epochs WHERE epoch_id = $1`, liveClusterEpoch).
		Scan(&row.base, &row.output, &row.revision, &row.updatedAt); err != nil {
		t.Fatalf("read activation epoch %d: %v", liveClusterEpoch, err)
	}
	return row
}

func (cluster *liveCluster) waitEpochRow(what string, done func(liveEpochRow) bool) liveEpochRow {
	t := cluster.t
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		row := cluster.epochRow()
		if done(row) {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait for %s: the row is %+v", what, row)
		}
		liveDiskPause(t, cluster.ctx, what)
	}
}

// jobLogsFor is the logs of one incarnation of a Job, by its UID: a Job
// recreated under the same name is a different Job.
func (cluster *liveCluster) jobLogsFor(uid types.UID) string {
	return cluster.podLogs("batch.kubernetes.io/controller-uid=" + string(uid))
}

func (cluster *liveCluster) waitJobLogged(uid types.UID, line string) {
	t := cluster.t
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		logs := cluster.jobLogsFor(uid)
		if strings.Contains(logs, line) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Job did not log %q:\n%s", line, logs)
		}
		liveDiskPause(t, cluster.ctx, "the Job to log "+line)
	}
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

// runningJob returns the UID of a Job whose pod is still running, which is
// what makes deleting it an interruption rather than a cleanup.
func (cluster *liveCluster) runningJob(name string) types.UID {
	t := cluster.t
	t.Helper()
	job, err := cluster.client.BatchV1().Jobs(cluster.names.namespace).Get(cluster.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job %s: %v", name, err)
	}
	if complete, failed := liveJobState(job); complete || failed || job.Status.Active != 1 {
		t.Fatalf("Job %s is no longer running (complete %t, failed %t, active %d); the interruption must land before it finishes", name, complete, failed, job.Status.Active)
	}
	return job.UID
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

// liveWrapJobCommand runs a Job's command, then the suffix, under sh.
func liveWrapJobCommand(t *testing.T, job *unstructured.Unstructured, suffix string) {
	t.Helper()
	containers, found, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("Job %s has containers %v (%v)", job.GetName(), containers, err)
	}
	container := containers[0].(map[string]any)
	command, found, err := unstructured.NestedStringSlice(container, "command")
	if err != nil || !found || len(command) == 0 {
		t.Fatalf("Job %s's container has no command (%v)", job.GetName(), err)
	}
	quoted := make([]string, 0, len(command))
	for _, argument := range command {
		if strings.Contains(argument, "'") {
			t.Fatalf("argument %q cannot be single-quoted", argument)
		}
		quoted = append(quoted, "'"+argument+"'")
	}
	container["command"] = []any{"sh", "-c", strings.Join(quoted, " ") + " && " + suffix}
	if err := unstructured.SetNestedSlice(job.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
}

// assertAttestVerifiesTheDaemonCertificate runs the attest-base Job's
// rendered manifest without --tls-server-name. The daemon's certificate names
// only its DNS name and the Job dials pod IPs, so a handshake that verifies
// must fail -- and fail before the row is touched.
func (cluster *liveCluster) assertAttestVerifiesTheDaemonCertificate(sets []string) {
	t := cluster.t
	t.Helper()
	names := cluster.names
	block, _ := pem.Decode(cluster.secret(names.outputTLS).Data["tls.crt"])
	if block == nil {
		t.Fatalf("Secret %s holds no certificate", names.outputTLS)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(certificate.IPAddresses) != 0 || len(certificate.DNSNames) != 1 || certificate.DNSNames[0] != names.outputDaemonServerName() {
		t.Fatalf("the output daemon's certificate names %v and %v; want only %s", certificate.DNSNames, certificate.IPAddresses, names.outputDaemonServerName())
	}

	before := cluster.epochRow()
	var job *unstructured.Unstructured
	for _, object := range cluster.render(sets) {
		if object.GetKind() == "Job" && object.GetName() == names.release+"-hangar-output-activation-attest-1" {
			job = object
		}
	}
	if job == nil {
		t.Fatal("the attest-base render holds no attest Job")
	}
	job.SetName(job.GetName() + "-no-server-name")
	if err := unstructured.SetNestedField(job.Object, int64(0), "spec", "backoffLimit"); err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	container := containers[0].(map[string]any)
	command, _, _ := unstructured.NestedStringSlice(container, "command")
	var kept []any
	dropped := false
	for _, argument := range command {
		if strings.HasPrefix(argument, "--tls-server-name=") {
			dropped = true
			continue
		}
		kept = append(kept, argument)
	}
	if !dropped {
		t.Fatalf("the attest Job renders no --tls-server-name: %v", command)
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
			t.Fatalf("attest without --tls-server-name succeeded against a certificate that names no IP:\n%s", cluster.jobLogsFor(uid))
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attest without --tls-server-name neither failed nor succeeded:\n%s", cluster.jobLogsFor(uid))
		}
		liveDiskPause(t, cluster.ctx, "attest without --tls-server-name")
	}
	if logs := cluster.jobLogsFor(uid); !strings.Contains(logs, "x509:") {
		t.Fatalf("attest without --tls-server-name failed for a reason other than certificate verification:\n%s", logs)
	}
	if after := cluster.epochRow(); !after.same(before) {
		t.Fatalf("a failed attestation moved the row: %+v -> %+v", before, after)
	}
}

// assertCaptureOff: until both facets are enabled, web runs with neither
// capture nor Run results.
func (cluster *liveCluster) assertCaptureOff() {
	t := cluster.t
	t.Helper()
	deployment, err := cluster.client.AppsV1().Deployments(cluster.names.namespace).Get(cluster.ctx, cluster.names.release+"-web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var web corev1.Container
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == "concourse-web" {
			web = container
		}
	}
	args := strings.Join(web.Args, " ")
	if web.Name == "" || strings.Contains(args, "--kubernetes-hangar-output-capture-enabled") || strings.Contains(args, "--run-result-scratch-dir") {
		t.Fatalf("web runs with capture or Run results on before the output facet is enabled: %s", args)
	}
}
