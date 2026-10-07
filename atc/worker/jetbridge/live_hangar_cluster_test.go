// hangar_live only, never live: this contract creates a namespace, cluster
// roles and bindings, a ValidatingAdmissionPolicy and its binding, impersonates
// a ServiceAccount and runs the artifact daemon, which serves the output plane,
// on a node's host port -- cluster-scope work a namespaced live-tier account cannot do and
// must not do against the deployed cluster. Its CI home is the
// hangar-cluster-contract job in deploy/k8s-e2e-pipeline.yml, which stands up a
// throwaway K3s cluster, loads the image this contract names into it, and hands
// it a cluster-admin kubeconfig; build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// liveClusterImageEnv names the image every JetBridge container in the chart
// runs: concourse (web, the bootstrap Jobs, its ENTRYPOINT), artifact-daemon,
// and hangar-store under /usr/local/concourse/bin, plus a shell. It must already be loaded into the
// cluster's container runtime (the chart is rendered with
// image.pullPolicy=Never) and carry a tag other than latest.
const liveClusterImageEnv = "HANGAR_CLUSTER_IMAGE"

const (
	liveClusterFieldManager = "argocd-controller"
	liveClusterSyncWait     = 12 * time.Minute
	liveClusterStoreID      = "contract-store-1"
	liveClusterTenant       = "contract-tenant"
	liveClusterEpoch        = 1
	liveClusterControlKeyID = "control-1"
	liveClusterBootstrapTag = "concourse-hangar-bootstrap"
)

// TestLiveHangarBootstrapHoldsAcrossSyncsAndEveryConsumerUsesIt is the
// hangar_secret_bootstrap contract on a real API server (its T11): the chart's
// own render, with the bootstrap and every Hangar consumer on, applied the way
// Argo applies it -- sync waves ascending, the bootstrap Job a Sync hook at its
// wave that is deleted and recreated on every sync (BeforeHookCreation), and
// and every consumer started once the runbook reaches it.
//
// It cannot be ONE first sync with everything on, and that is a property of the
// product rather than of this test. A fresh install with every consumer on
// never becomes Healthy: the artifact daemon proves its disk credential against
// the store at startup and exits while the store's initialize render holds it
// at zero replicas, and disk initialisation is its own explicit step (ADR-0005).
// So the contract syncs the runbook's order (hangar_stores_enabled_in_cluster
// S1-S6, then S10 and S13), and every sync after the first is a re-run of the
// bootstrap. There is no activation walk: in service is the hangar_enabled row
// web writes at startup from hangarOutput.webEnabled (S13).
//
// It proves:
//
//  1. the bootstrap identity cannot mint a service-account token Secret: before
//     the bootstrap's first run, impersonating its ServiceAccount, a create of
//     a kubernetes.io/service-account-token Secret under an inventory name it
//     has not created yet is refused by the ValidatingAdmissionPolicy, while a
//     labelled Opaque Secret under the same name would be admitted;
//  2. across six syncs, each recreating the bootstrap Job, every inventory
//     Secret keeps its UID and is byte-identical, and the policy and its
//     binding keep their UIDs;
//  3. the control ring holds exactly the public half of the control key, through
//     web's own ring loader, and no ring holds a symmetric key;
//  4. every consumer completes its first use of the generated Secrets: web
//     starts with capture on (its startup refuses a ring it cannot load); the
//     artifact daemon's output plane accepts web's control-plane client
//     certificate over mTLS on a route that requires one; the artifact daemon publishes into the
//     disk store as `input` and verifies a materialization warrant signed with
//     the generated warrant key; the disk store serves `publisher` a create
//     and a read; web puts the plane in service, its orphan sweep lists the
//     output namespace with the `inventory` token and counts -- and leaves --
//     the foreign-marked object that create left; and `reclaimer` stats and
//     deletes the object.
func TestLiveHangarBootstrapHoldsAcrossSyncsAndEveryConsumerUsesIt(t *testing.T) {
	cluster := newLiveCluster(t, "hc", 40*time.Minute)
	names := cluster.names

	policyChecked := false
	first := cluster.sync("S1-S2 bootstrap and disk init", cluster.through("S1", "S2"), liveSyncOptions{
		beforeWave: func(wave int) {
			if wave == -1 && !policyChecked {
				cluster.assertPolicyRefusesTokenSecret(names.warrant)
				policyChecked = true
			}
		},
	})
	if !policyChecked {
		t.Fatal("the render had no sync wave -1, so the bootstrap Job is not the Sync hook the policy and RBAC waves precede")
	}

	inventory := cluster.inventory()
	snapshot := cluster.snapshotSecrets(inventory)
	policies := cluster.policyUIDs()
	bootstrapJob := names.release + "-hangar-bootstrap"
	jobs := map[types.UID]string{}
	recordJob := func(label string, result liveSyncResult) {
		t.Helper()
		uid := result.hooks[bootstrapJob]
		if uid == "" {
			t.Fatalf("%s: the bootstrap Job did not run as a hook", label)
		}
		if previous, seen := jobs[uid]; seen {
			t.Fatalf("%s: the bootstrap Job has the UID it had at %s; a Sync hook is recreated each sync", label, previous)
		}
		jobs[uid] = label
	}
	recordJob("S1-S2", first)

	resync := func(label string, sets []string) liveSyncResult {
		t.Helper()
		result := cluster.sync(label, sets, liveSyncOptions{})
		recordJob(label, result)
		cluster.assertSecretsUnchanged(label, snapshot)
		cluster.assertPolicyUIDs(label, policies)
		return result
	}

	resync("S3 store up", cluster.through("S1", "S2", "S3"))
	resync("S4-S6 strict inputs, base workloads", cluster.through("S1", "S2", "S3", "S4", "S5", "S6"))

	// Before the output plane is on, so the web's FIRST sweep is what finds it.
	probe := cluster.storePublisherRoundTrip()

	everything := cluster.through("S1", "S2", "S3", "S4", "S5", "S6", "S10", "S13")
	resync("S10+S13 every consumer on", everything)

	cluster.assertRingsArePublicHalves(inventory)
	cluster.assertWebLoadedRings(inventory)
	cluster.assertOutputPlaneAcceptsWebClient()
	cluster.assertArtifactDaemonUsesWarrantKey()
	cluster.assertControllersSwept(probe)

	resync("re-sync 1", everything)
	resync("re-sync 2", everything)
	cluster.assertOutputPlaneAcceptsWebClient()
}

// liveClusterNames are the Secret and object names a release composes. The
// inventory Secrets are named here exactly as an operator names them in
// values; the bootstrap creates them.
type liveClusterNames struct {
	release, namespace string

	warrant, storeTLS, storeCredentials, control, capability string
	materialize, runInput                                    string

	daemonTLS, resolve, postgres, signingKey string
}

func newLiveClusterNames(release, namespace string) liveClusterNames {
	return liveClusterNames{
		release: release, namespace: namespace,
		warrant:          release + "-hangar-warrant-key",
		storeTLS:         release + "-hangar-store-tls",
		storeCredentials: release + "-hangar-store-credentials",
		control:          release + "-hangar-control-key-e1",
		capability:       release + "-hangar-capability-key",
		materialize:      release + "-hangar-materialize-key",
		runInput:         release + "-run-input-signing-key",
		// Operator-owned, outside the bootstrap inventory: the artifact
		// daemon's pinned TLS Secret and resolve key, the database password
		// the bundled PostgreSQL and web share, and web's session signing key.
		daemonTLS:  release + "-artifact-daemon-tls",
		resolve:    release + "-artifact-daemon-resolve",
		postgres:   release + "-postgresql-connection",
		signingKey: release + "-session-signing-key",
	}
}

func (names liveClusterNames) storeService() string { return names.release + "-hangar-store" }
func (names liveClusterNames) storeDNS() string {
	return names.storeService() + "." + names.namespace + ".svc"
}

// outputPlaneServerName is the artifact daemon's server name: it serves the
// output plane.
func (names liveClusterNames) outputPlaneServerName() string {
	return daemonServerName(names.release+"-artifact-daemon", names.namespace)
}

// liveCluster is one release of the chart in its own namespace on a disposable
// cluster, and the Argo-shaped sync that converges it.
type liveCluster struct {
	t          *testing.T
	ctx        context.Context
	rest       *rest.Config
	client     kubernetes.Interface
	dynamic    dynamic.Interface
	mapper     meta.RESTMapper
	names      liveClusterNames
	repository string
	tag        string
	node       string
	nodeIP     string
	hostPath   string

	ca               liveDiskCA
	daemonClientCert []byte
	daemonClientKey  []byte
	postgresPassword string
	applied          map[string]liveArgoObject
}

func newLiveCluster(t *testing.T, release string, budget time.Duration) *liveCluster {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("real K3s execution of the chart is CI-only on macOS")
	}
	image := os.Getenv(liveClusterImageEnv)
	colon := strings.LastIndex(image, ":")
	if colon <= 0 || colon == len(image)-1 || strings.Contains(image[colon:], "/") || strings.HasSuffix(image, ":latest") {
		t.Fatalf("%s must name a repository:tag image (not :latest) holding every JetBridge binary, loaded into the cluster; got %q", liveClusterImageEnv, image)
	}

	restConfig, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("load the kubeconfig: %v", err)
	}
	restConfig.QPS, restConfig.Burst = 50, 100
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	disco, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	t.Cleanup(cancel)

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 1 {
		t.Fatalf("the contract needs exactly one node (the daemons it dials hold host ports there): count=%d err=%v", len(nodes.Items), err)
	}
	node := nodes.Items[0]
	nodeIP := ""
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			nodeIP = address.Address
		}
	}
	if nodeIP == "" {
		t.Fatalf("node %s has no InternalIP", node.Name)
	}

	namespace := release + "-" + liveDiskRandomHex(t, 3)
	cluster := &liveCluster{
		t: t, ctx: ctx, rest: restConfig, client: client, dynamic: dyn,
		mapper: restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disco)),
		names:  newLiveClusterNames(release, namespace), repository: image[:colon], tag: image[colon+1:],
		node: node.Name, nodeIP: nodeIP, hostPath: "/var/concourse/" + namespace,
		applied: map[string]liveArgoObject{},
	}

	// A previous contract on this cluster leaves its readiness labels behind if
	// its daemons were killed rather than stopped; a wait on a label must
	// see this release's daemons write it.
	cluster.clearNodeLabels()

	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace %s: %v", namespace, err)
	}
	t.Cleanup(cluster.teardown)
	// Registered after teardown, so it runs first: a failure dumps the pods'
	// logs before the namespace takes them away.
	t.Cleanup(func() {
		if t.Failed() {
			liveDiskDiagnostics(t, client, namespace, node.Name)
		}
	})

	cluster.createOperatorObjects()
	return cluster
}

// createOperatorObjects makes what an operator provides outside the bootstrap
// inventory, as concourse.home does: the artifact daemon's pinned TLS Secret
// and resolve key, the database password Secret and web's session signing key.
func (cluster *liveCluster) createOperatorObjects() {
	t, names := cluster.t, cluster.names
	cluster.ca = newLiveDiskCA(t)
	daemonDNS := daemonServerName(names.release+"-artifact-daemon", names.namespace)
	serverCert, serverKey := cluster.ca.issue(t, daemonDNS, []string{daemonDNS, "*." + daemonDNS}, []net.IP{net.ParseIP(cluster.nodeIP)},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	cluster.daemonClientCert, cluster.daemonClientKey = cluster.ca.issue(t, "concourse web", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	cluster.postgresPassword = liveDiskRandomHex(t, 16)
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate the session signing key: %v", err)
	}

	for name, data := range map[string]map[string][]byte{
		names.daemonTLS: {"tls.crt": serverCert, "tls.key": serverKey, "ca.crt": cluster.ca.certPEM,
			"client.crt": cluster.daemonClientCert, "client.key": cluster.daemonClientKey},
		names.resolve:  {"resolve.key": liveDiskRandomBytes(t, 32)},
		names.postgres: {"POSTGRES_PASSWORD": []byte(cluster.postgresPassword)},
		names.signingKey: {"session_signing_key": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(signingKey)})},
	} {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: names.namespace}, Data: data}
		if _, err := cluster.client.CoreV1().Secrets(names.namespace).Create(cluster.ctx, secret, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create operator Secret %s: %v", name, err)
		}
	}
}

func (cluster *liveCluster) clearNodeLabels() {
	current, err := cluster.client.CoreV1().Nodes().Get(cluster.ctx, cluster.node, metav1.GetOptions{})
	if err != nil {
		cluster.t.Fatalf("get node %s: %v", cluster.node, err)
	}
	changed := false
	for _, key := range []string{"concourse.dev/artifact-cache", "concourse.dev/hangar-v1", executioncontrol.ReadyLabel, output.ReadyLabel} {
		if _, found := current.Labels[key]; found {
			delete(current.Labels, key)
			changed = true
		}
	}
	if changed {
		if _, err := cluster.client.CoreV1().Nodes().Update(cluster.ctx, current, metav1.UpdateOptions{}); err != nil {
			cluster.t.Fatalf("clear node %s's readiness labels: %v", cluster.node, err)
		}
	}
}

// teardown deletes the namespace and every cluster-scoped object the syncs
// applied, and waits for the namespace to go: the next contract on this
// cluster binds the same host ports.
func (cluster *liveCluster) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	for _, object := range cluster.applied {
		if !object.namespaced {
			_ = cluster.dynamic.Resource(object.gvr).Delete(ctx, object.obj.GetName(), metav1.DeleteOptions{})
		}
	}
	_ = cluster.client.CoreV1().Namespaces().Delete(ctx, cluster.names.namespace, metav1.DeleteOptions{})
	for {
		_, err := cluster.client.CoreV1().Namespaces().Get(ctx, cluster.names.namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		select {
		case <-ctx.Done():
			cluster.t.Logf("namespace %s was still terminating when teardown gave up", cluster.names.namespace)
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// runbookStep returns one step's values from the hangar_stores_enabled_in_cluster
// rollout runbook, as deploy/chart/tests/hangar_cluster_rollout_test.go renders
// them, named for this release.
func (cluster *liveCluster) runbookStep(id string) []string {
	names := cluster.names
	switch id {
	case "S1":
		return []string{
			"hangarBootstrap.enabled=true",
			fmt.Sprintf("hangarOutput.activationEpoch=%d", liveClusterEpoch),
			"hangarOutput.executionControl.keySecret=" + names.control,
			"hangarOutput.executionControl.keyID=" + liveClusterControlKeyID,
			"hangarOutput.capabilityKeySecret=" + names.capability,
			"hangarOutput.materializationKeySecret=" + names.materialize,
			"hangarStorage.disk.tls.existingSecret=" + names.storeTLS,
			"hangarStorage.disk.credentials.existingSecret=" + names.storeCredentials,
			"artifactDaemon.hangar.keySecret=" + names.warrant,
		}
	case "S2":
		return []string{"hangarStorage.disk.enabled=true", "hangarStorage.disk.storeID=" + liveClusterStoreID,
			"hangarStorage.disk.storageClass=local-path", "hangarStorage.disk.size=1Gi", "hangarStorage.disk.initialize=true"}
	case "S3":
		return []string{"hangarStorage.disk.initialize=false"}
	case "S4":
		return []string{"artifactDaemon.hangar.enabled=true", "artifactDaemon.hangar.store=disk", "artifactDaemon.hangar.bucket=inputs"}
	case "S5":
		return []string{"artifactDaemon.hangar.webEnabled=true"}
	case "S6":
		return []string{"hangarOutput.executionControl.enabled=true", "artifactDaemon.outputScratch.sizeLimit=32Gi"}
	case "S10":
		return []string{
			"hangarOutput.enabled=true", "hangarOutput.store=disk", "hangarOutput.bucket=outputs", "hangarOutput.tenant=" + liveClusterTenant}
	case "S13":
		return []string{
			"hangarOutput.webEnabled=true", "web.runInputSigningKeySecret=" + names.runInput}
	}
	cluster.t.Fatalf("no runbook step %q", id)
	return nil
}

// through accumulates runbook steps, in order, over the release's own values.
func (cluster *liveCluster) through(ids ...string) []string {
	names := cluster.names
	sets := []string{
		"fullnameOverride=" + names.release,
		"image.repository=" + cluster.repository, "image.tag=" + cluster.tag, "image.pullPolicy=Never",
		"artifactDaemon.tls.existingSecret=" + names.daemonTLS,
		"artifactDaemon.resolveCapability.existingSecret=" + names.resolve,
		"secrets.signingKeySecret=" + names.signingKey,
		// Per release, so a second contract on this node never opens the
		// first one's output control ledger: it lives in the storage root.
		"artifactDaemon.hostPath=" + cluster.hostPath,
		"postgresql.existingSecret=" + names.postgres, "postgresql.passwordSecretKey=POSTGRES_PASSWORD",
		// The chart's PostgreSQL runs as uid 999 on a subPath, and a
		// local-path volume's subPath is created root-owned, so initdb cannot
		// take it. Nothing here restarts the database; its own layer will do.
		"postgresql.persistence.enabled=false",
		// Nothing reaches web from outside the cluster here, and a
		// LoadBalancer would have K3s bind web's ports on the node.
		"service.type=ClusterIP",
	}
	for _, id := range ids {
		sets = append(sets, cluster.runbookStep(id)...)
	}
	return sets
}

// render runs `helm template` over the whole chart in this checkout.
func (cluster *liveCluster) render(sets []string) []*unstructured.Unstructured {
	t := cluster.t
	t.Helper()
	chart := filepath.Join("..", "..", "..", "deploy", "chart")
	args := []string{"template", cluster.names.release, chart, "--namespace", cluster.names.namespace,
		"-f", filepath.Join(chart, "tests", "testdata", "required-values.yaml")}
	for _, set := range sets {
		args = append(args, "--set", set)
	}
	out, err := exec.Command("helm", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("helm template: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("helm template: %v", err)
	}
	return liveClusterDecode(t, out)
}

func liveClusterDecode(t *testing.T, rendered []byte) []*unstructured.Unstructured {
	t.Helper()
	var objects []*unstructured.Unstructured
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(rendered)))
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("split the chart render: %v", err)
		}
		if len(bytes.TrimSpace(stripYAMLComments(doc))) == 0 {
			continue
		}
		object := &unstructured.Unstructured{}
		if err := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096).Decode(&object.Object); err != nil {
			t.Fatalf("decode a rendered manifest: %v\n%s", err, doc)
		}
		if object.GetKind() == "" || object.GetName() == "" {
			t.Fatalf("a rendered manifest has no kind or name:\n%s", doc)
		}
		objects = append(objects, object)
	}
	return objects
}

// liveArgoObject is one rendered object as Argo classifies it.
type liveArgoObject struct {
	obj        *unstructured.Unstructured
	gvr        schema.GroupVersionResource
	namespaced bool
	hook       string
	wave       int
}

func (object liveArgoObject) key() string {
	return object.gvr.String() + "/" + object.obj.GetNamespace() + "/" + object.obj.GetName()
}

type liveSyncOptions struct {
	// beforeWave runs before a wave's objects are applied.
	beforeWave func(wave int)
	// mutate may change an object before it is applied.
	mutate func(object *unstructured.Unstructured)
	// unwatched names objects whose health the sync does not wait for, and
	// stopBeforePostSync leaves the sync where an interrupted one stops.
	unwatched          map[string]bool
	stopBeforePostSync bool
	// failingHooks names hooks the sync expects to fail. Their logs land in
	// the result's failed instead of failing the test, and one that completes
	// fails it.
	failingHooks map[string]bool
}

type liveSyncResult struct {
	rendered []*unstructured.Unstructured
	hooks    map[string]types.UID
	// failed holds the logs of each hook in failingHooks, which failed.
	failed map[string]string
}

func (result liveSyncResult) only(t *testing.T, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, object := range result.rendered {
		if object.GetKind() == kind && object.GetName() == name {
			return object.DeepCopy()
		}
	}
	t.Fatalf("the render holds no %s %s", kind, name)
	return nil
}

var liveArgoKindOrder = []string{
	"Namespace", "NetworkPolicy", "ResourceQuota", "LimitRange", "PodDisruptionBudget", "ServiceAccount",
	"Secret", "ConfigMap", "StorageClass", "PersistentVolume", "PersistentVolumeClaim",
	"CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding",
	"Service", "DaemonSet", "Pod", "ReplicaSet", "Deployment", "StatefulSet", "Job", "CronJob", "Ingress",
}

func liveArgoKindRank(kind string) int {
	for index, known := range liveArgoKindOrder {
		if known == kind {
			return index
		}
	}
	return len(liveArgoKindOrder)
}

// sync converges the release on one render the way an automated Argo sync
// with pruning does: PreSync hooks; then each sync wave in ascending order,
// its resources applied (kind order within a wave) and its Sync hooks
// recreated, waiting for all of them to be healthy before the next wave;
// pruning what the render no longer holds; and PostSync hooks once
// everything is healthy, in ascending wave order and in render order within a
// wave, as Argo orders them. A hook with BeforeHookCreation -- Argo's default --
// is deleted and created again, so it runs on every sync.
func (cluster *liveCluster) sync(label string, sets []string, options liveSyncOptions) liveSyncResult {
	t := cluster.t
	t.Helper()
	started := time.Now()
	t.Logf("sync %q: begin", label)
	rendered := cluster.render(sets)
	result := liveSyncResult{rendered: rendered, hooks: map[string]types.UID{}, failed: map[string]string{}}
	run := func(hook liveArgoObject) {
		t.Helper()
		name := hook.obj.GetName()
		uid, logs := cluster.runHook(label, hook, options.failingHooks[name])
		result.hooks[name] = uid
		if options.failingHooks[name] {
			result.failed[name] = logs
		}
	}

	var resources, preSync, postSync []liveArgoObject
	syncHooks := map[int][]liveArgoObject{}
	waves := map[int]bool{}
	for _, raw := range rendered {
		object := raw.DeepCopy()
		if options.mutate != nil {
			options.mutate(object)
		}
		classified := cluster.classify(object)
		switch classified.hook {
		case "":
			resources = append(resources, classified)
			waves[classified.wave] = true
		case "PreSync":
			preSync = append(preSync, classified)
		case "Sync":
			syncHooks[classified.wave] = append(syncHooks[classified.wave], classified)
			waves[classified.wave] = true
		case "PostSync":
			postSync = append(postSync, classified)
		default:
			t.Fatalf("%s %s carries hook %q, which this sync does not model", object.GetKind(), object.GetName(), classified.hook)
		}
	}

	for _, hook := range preSync {
		run(hook)
	}

	ordered := make([]int, 0, len(waves))
	for wave := range waves {
		ordered = append(ordered, wave)
	}
	sort.Ints(ordered)
	current := map[string]liveArgoObject{}
	for _, wave := range ordered {
		if options.beforeWave != nil {
			options.beforeWave(wave)
		}
		var inWave []liveArgoObject
		for _, resource := range resources {
			if resource.wave == wave {
				inWave = append(inWave, resource)
			}
		}
		sort.SliceStable(inWave, func(i, j int) bool {
			return liveArgoKindRank(inWave[i].obj.GetKind()) < liveArgoKindRank(inWave[j].obj.GetKind())
		})
		for _, resource := range inWave {
			cluster.apply(resource)
			current[resource.key()] = resource
		}
		for _, hook := range syncHooks[wave] {
			run(hook)
		}
		var watched []liveArgoObject
		for _, resource := range inWave {
			if !options.unwatched[resource.obj.GetName()] {
				watched = append(watched, resource)
			}
		}
		cluster.waitHealthy(fmt.Sprintf("%s, wave %d", label, wave), watched)
	}

	for key, previous := range cluster.applied {
		if _, kept := current[key]; !kept {
			cluster.prune(previous)
		}
	}
	cluster.applied = current

	if options.stopBeforePostSync {
		t.Logf("sync %q: stopped before PostSync after %s", label, time.Since(started).Round(time.Second))
		return result
	}
	sort.SliceStable(postSync, func(i, j int) bool { return postSync[i].wave < postSync[j].wave })
	for _, hook := range postSync {
		run(hook)
	}
	t.Logf("sync %q: Synced and Healthy after %s", label, time.Since(started).Round(time.Second))
	return result
}

func (cluster *liveCluster) classify(object *unstructured.Unstructured) liveArgoObject {
	t := cluster.t
	t.Helper()
	gvk := object.GroupVersionKind()
	mapping, err := cluster.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		t.Fatalf("map %s: %v", gvk, err)
	}
	classified := liveArgoObject{obj: object, gvr: mapping.Resource, namespaced: mapping.Scope.Name() == meta.RESTScopeNameNamespace}
	if classified.namespaced {
		object.SetNamespace(cluster.names.namespace)
	} else {
		object.SetNamespace("")
	}
	annotations := object.GetAnnotations()
	classified.hook = annotations["argocd.argoproj.io/hook"]
	if raw, found := annotations["argocd.argoproj.io/sync-wave"]; found {
		wave, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s %s has sync-wave %q", object.GetKind(), object.GetName(), raw)
		}
		classified.wave = wave
	}
	if policy := annotations["argocd.argoproj.io/hook-delete-policy"]; classified.hook != "" && policy != "" && policy != "BeforeHookCreation" {
		t.Fatalf("%s %s has hook-delete-policy %q; only BeforeHookCreation is modelled", object.GetKind(), object.GetName(), policy)
	}
	return classified
}

func (cluster *liveCluster) resource(object liveArgoObject) dynamic.ResourceInterface {
	if object.namespaced {
		return cluster.dynamic.Resource(object.gvr).Namespace(cluster.names.namespace)
	}
	return cluster.dynamic.Resource(object.gvr)
}

func (cluster *liveCluster) apply(object liveArgoObject) {
	t := cluster.t
	t.Helper()
	if _, err := cluster.resource(object).Apply(cluster.ctx, object.obj.GetName(), object.obj,
		metav1.ApplyOptions{FieldManager: liveClusterFieldManager, Force: true}); err != nil {
		t.Fatalf("apply %s %s: %v", object.obj.GetKind(), object.obj.GetName(), err)
	}
}

func (cluster *liveCluster) prune(object liveArgoObject) {
	t := cluster.t
	t.Helper()
	background := metav1.DeletePropagationBackground
	err := cluster.resource(object).Delete(cluster.ctx, object.obj.GetName(), metav1.DeleteOptions{PropagationPolicy: &background})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("prune %s %s: %v", object.obj.GetKind(), object.obj.GetName(), err)
	}
	cluster.waitGone(object)
}

func (cluster *liveCluster) waitGone(object liveArgoObject) {
	t := cluster.t
	t.Helper()
	for {
		_, err := cluster.resource(object).Get(cluster.ctx, object.obj.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil {
			t.Fatalf("get %s %s: %v", object.obj.GetKind(), object.obj.GetName(), err)
		}
		liveDiskPause(t, cluster.ctx, object.obj.GetKind()+" "+object.obj.GetName()+" to be deleted")
	}
}

// runHook deletes the hook's previous incarnation, creates it again and waits
// for it to complete, returning the new object's UID. A hook expected to fail
// returns its logs once it has failed, and fails the test if it completes.
func (cluster *liveCluster) runHook(label string, hook liveArgoObject, expectFailure bool) (types.UID, string) {
	t := cluster.t
	t.Helper()
	if hook.obj.GetKind() != "Job" {
		t.Fatalf("hook %s %s is not a Job", hook.obj.GetKind(), hook.obj.GetName())
	}
	background := metav1.DeletePropagationBackground
	err := cluster.resource(hook).Delete(cluster.ctx, hook.obj.GetName(), metav1.DeleteOptions{PropagationPolicy: &background})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete hook %s: %v", hook.obj.GetName(), err)
	}
	cluster.waitGone(hook)
	created, err := cluster.resource(hook).Create(cluster.ctx, hook.obj, metav1.CreateOptions{FieldManager: liveClusterFieldManager})
	if err != nil {
		t.Fatalf("%s: create hook %s: %v", label, hook.obj.GetName(), err)
	}
	if !expectFailure {
		cluster.waitJobComplete(label, hook.obj.GetName())
		return created.GetUID(), ""
	}
	if !cluster.waitJobDone(label, hook.obj.GetName()) {
		return created.GetUID(), cluster.jobLogs(hook.obj.GetName())
	}
	t.Fatalf("%s: hook %s was expected to fail and completed:\n%s", label, hook.obj.GetName(), cluster.jobLogs(hook.obj.GetName()))
	return "", ""
}

func (cluster *liveCluster) waitJobComplete(label, name string) {
	t := cluster.t
	t.Helper()
	if !cluster.waitJobDone(label, name) {
		t.Fatalf("%s: Job %s failed:\n%s", label, name, cluster.jobLogs(name))
	}
}

// waitJobDone waits for a Job to finish and reports whether it completed
// rather than failed.
func (cluster *liveCluster) waitJobDone(label, name string) bool {
	t := cluster.t
	t.Helper()
	for {
		job, err := cluster.client.BatchV1().Jobs(cluster.names.namespace).Get(cluster.ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%s: get Job %s: %v", label, name, err)
		}
		done, failed := liveJobState(job)
		if done {
			return true
		}
		if failed {
			return false
		}
		liveDiskPause(t, cluster.ctx, label+": Job "+name)
	}
}

func liveJobState(job *batchv1.Job) (complete, failed bool) {
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case batchv1.JobComplete:
			complete = true
		case batchv1.JobFailed:
			failed = true
		}
	}
	return complete, failed
}

// jobLogs is every log of every pod a Job made. These Jobs log names, kinds,
// public fingerprints and transitions only.
func (cluster *liveCluster) jobLogs(name string) string {
	return cluster.podLogs("job-name=" + name)
}

func (cluster *liveCluster) podLogs(selector string) string {
	pods, err := cluster.client.CoreV1().Pods(cluster.names.namespace).List(cluster.ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err.Error()
	}
	var logs strings.Builder
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			raw, err := cluster.client.CoreV1().Pods(cluster.names.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container.Name}).DoRaw(cluster.ctx)
			if err != nil {
				raw = []byte(err.Error())
			}
			fmt.Fprintf(&logs, "--- %s/%s (%s):\n%s\n", pod.Name, container.Name, pod.Status.Phase, raw)
		}
	}
	return logs.String()
}

// waitHealthy waits for Argo's built-in health of each object: a Deployment or
// DaemonSet fully rolled out and available, a Job complete, a PVC bound.
// Anything else is healthy once applied.
func (cluster *liveCluster) waitHealthy(label string, objects []liveArgoObject) {
	t := cluster.t
	t.Helper()
	deadline := time.Now().Add(liveClusterSyncWait)
	lastReport := time.Now()
	for {
		var pending []string
		for _, object := range objects {
			if reason := cluster.unhealthy(label, object); reason != "" {
				pending = append(pending, object.obj.GetKind()+" "+object.obj.GetName()+": "+reason)
			}
		}
		if len(pending) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not Healthy after %s:\n  %s", label, liveClusterSyncWait, strings.Join(pending, "\n  "))
		}
		if time.Since(lastReport) > time.Minute {
			t.Logf("%s: waiting on %s", label, strings.Join(pending, "; "))
			lastReport = time.Now()
		}
		liveDiskPause(t, cluster.ctx, label+" to be Healthy")
	}
}

func (cluster *liveCluster) unhealthy(label string, object liveArgoObject) string {
	t := cluster.t
	t.Helper()
	live, err := cluster.resource(object).Get(cluster.ctx, object.obj.GetName(), metav1.GetOptions{})
	if err != nil {
		return err.Error()
	}
	switch object.obj.GetKind() {
	case "Deployment":
		var deployment appsv1.Deployment
		liveClusterFromUnstructured(t, live, &deployment)
		want := int32(1)
		if deployment.Spec.Replicas != nil {
			want = *deployment.Spec.Replicas
		}
		status := deployment.Status
		if status.ObservedGeneration < deployment.Generation || status.UpdatedReplicas != want ||
			status.Replicas != want || status.AvailableReplicas != want {
			return fmt.Sprintf("updated %d, available %d, total %d of %d", status.UpdatedReplicas, status.AvailableReplicas, status.Replicas, want)
		}
	case "DaemonSet":
		var daemonSet appsv1.DaemonSet
		liveClusterFromUnstructured(t, live, &daemonSet)
		status := daemonSet.Status
		if status.ObservedGeneration < daemonSet.Generation || status.DesiredNumberScheduled == 0 ||
			status.UpdatedNumberScheduled != status.DesiredNumberScheduled || status.NumberAvailable != status.DesiredNumberScheduled {
			return fmt.Sprintf("updated %d, available %d of %d", status.UpdatedNumberScheduled, status.NumberAvailable, status.DesiredNumberScheduled)
		}
	case "Job":
		var job batchv1.Job
		liveClusterFromUnstructured(t, live, &job)
		complete, failed := liveJobState(&job)
		if failed {
			t.Fatalf("%s: Job %s failed:\n%s", label, job.Name, cluster.jobLogs(job.Name))
		}
		if !complete {
			return "running"
		}
	case "PersistentVolumeClaim":
		var claim corev1.PersistentVolumeClaim
		liveClusterFromUnstructured(t, live, &claim)
		if claim.Status.Phase != corev1.ClaimBound {
			return string(claim.Status.Phase)
		}
	}
	return ""
}

func liveClusterFromUnstructured(t *testing.T, object *unstructured.Unstructured, into any) {
	t.Helper()
	if err := k8sruntime.DefaultUnstructuredConverter.FromUnstructured(object.Object, into); err != nil {
		t.Fatalf("convert %s %s: %v", object.GetKind(), object.GetName(), err)
	}
}

// liveInventoryEntry is the slice of a bootstrap inventory entry this
// contract reads back from the chart's inventory ConfigMap.
type liveInventoryEntry struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Ring  string `json:"ring"`
	Epoch int64  `json:"epoch"`
	KeyID string `json:"keyID"`
}

func (cluster *liveCluster) inventory() []liveInventoryEntry {
	t := cluster.t
	t.Helper()
	configMap, err := cluster.client.CoreV1().ConfigMaps(cluster.names.namespace).Get(cluster.ctx, cluster.names.release+"-hangar-bootstrap-inventory", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get the bootstrap inventory ConfigMap: %v", err)
	}
	var inventory struct {
		Entries []liveInventoryEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(configMap.Data["inventory.json"]), &inventory); err != nil {
		t.Fatalf("decode the bootstrap inventory: %v", err)
	}
	kinds := map[string]int{}
	for _, entry := range inventory.Entries {
		kinds[entry.Kind]++
	}
	// Every kind the inventory can declare, with every consumer on.
	for kind, want := range map[string]int{"random32": 4, "ed25519": 2, "store-tokens": 1, "ca": 1, "tls-server": 1, "tls-client": 1, "tls-bundle": 1, "ring": 1, "dsn": 1} {
		if kinds[kind] != want {
			t.Fatalf("the bootstrap inventory declares %d %s entries, want %d (all: %v)", kinds[kind], kind, want, kinds)
		}
	}
	return inventory.Entries
}

func liveInventoryNamed(t *testing.T, inventory []liveInventoryEntry, kind string) liveInventoryEntry {
	t.Helper()
	for _, entry := range inventory {
		if entry.Kind == kind {
			return entry
		}
	}
	t.Fatalf("the bootstrap inventory has no %s entry", kind)
	return liveInventoryEntry{}
}

func (cluster *liveCluster) secret(name string) *corev1.Secret {
	cluster.t.Helper()
	secret, err := cluster.client.CoreV1().Secrets(cluster.names.namespace).Get(cluster.ctx, name, metav1.GetOptions{})
	if err != nil {
		cluster.t.Fatalf("get Secret %s: %v", name, err)
	}
	return secret
}

type liveSecretSnapshot struct {
	uid  types.UID
	kind corev1.SecretType
	data map[string][]byte
}

func (cluster *liveCluster) snapshotSecrets(inventory []liveInventoryEntry) map[string]liveSecretSnapshot {
	snapshot := map[string]liveSecretSnapshot{}
	for _, entry := range inventory {
		secret := cluster.secret(entry.Name)
		if secret.Labels["app.kubernetes.io/managed-by"] != liveClusterBootstrapTag {
			cluster.t.Fatalf("inventory Secret %s does not carry the bootstrap label", entry.Name)
		}
		snapshot[entry.Name] = liveSecretSnapshot{uid: secret.UID, kind: secret.Type, data: secret.Data}
	}
	return snapshot
}

// assertSecretsUnchanged names the Secret and data key that moved, never a
// value.
func (cluster *liveCluster) assertSecretsUnchanged(label string, snapshot map[string]liveSecretSnapshot) {
	t := cluster.t
	t.Helper()
	for name, before := range snapshot {
		after := cluster.secret(name)
		if after.UID != before.uid || after.Type != before.kind {
			t.Errorf("%s: inventory Secret %s was replaced (UID %s -> %s, type %s -> %s)", label, name, before.uid, after.UID, before.kind, after.Type)
			continue
		}
		keys := map[string]bool{}
		for key := range before.data {
			keys[key] = true
		}
		for key := range after.Data {
			keys[key] = true
		}
		for key := range keys {
			if !bytes.Equal(before.data[key], after.Data[key]) {
				t.Errorf("%s: inventory Secret %s key %s is not byte-identical to the first sync's", label, name, key)
			}
		}
	}
}

func (cluster *liveCluster) policyUIDs() map[string]types.UID {
	t := cluster.t
	t.Helper()
	name := cluster.names.release + "-hangar-bootstrap"
	policy, err := cluster.client.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(cluster.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get the bootstrap ValidatingAdmissionPolicy: %v", err)
	}
	binding, err := cluster.client.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(cluster.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get the bootstrap ValidatingAdmissionPolicyBinding: %v", err)
	}
	return map[string]types.UID{"policy": policy.UID, "binding": binding.UID}
}

func (cluster *liveCluster) assertPolicyUIDs(label string, before map[string]types.UID) {
	cluster.t.Helper()
	after := cluster.policyUIDs()
	for which, uid := range before {
		if after[which] != uid {
			cluster.t.Errorf("%s: the bootstrap admission %s was replaced (UID %s -> %s); it is a plain resource and must never be absent while the Role grants create", label, which, uid, after[which])
		}
	}
}

// assertPolicyRefusesTokenSecret runs before the bootstrap's first run, so the
// name is in the policy's list and the Role's, and no Secret holds it yet: a
// refusal here is the policy's and cannot be an AlreadyExists.
func (cluster *liveCluster) assertPolicyRefusesTokenSecret(name string) {
	t := cluster.t
	t.Helper()
	names := cluster.names
	policyName := names.release + "-hangar-bootstrap"
	if _, err := cluster.client.CoreV1().Secrets(names.namespace).Get(cluster.ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("inventory name %s is already in use before the bootstrap ran (err %v)", name, err)
	}

	impersonated := rest.CopyConfig(cluster.rest)
	impersonated.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + names.namespace + ":" + policyName}
	asBootstrap, err := kubernetes.NewForConfig(impersonated)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"app.kubernetes.io/managed-by": liveClusterBootstrapTag}
	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: names.namespace, Labels: labels,
			Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"}},
		Type: corev1.SecretTypeServiceAccountToken,
	}
	refusedByPolicy := func(err error) bool {
		return err != nil && (apierrors.IsForbidden(err) || apierrors.IsInvalid(err)) &&
			strings.Contains(err.Error(), "ValidatingAdmissionPolicy") && strings.Contains(err.Error(), policyName) &&
			strings.Contains(err.Error(), "only Opaque or kubernetes.io/tls")
	}

	// A new policy reaches the admission chain a moment after it is stored,
	// and a new RoleBinding reaches the authorizer a moment after that. Wait
	// with dry runs, which pass through admission and persist nothing, so an
	// early create can never mint the token this is about.
	deadline := time.Now().Add(time.Minute)
	for {
		_, err := asBootstrap.CoreV1().Secrets(names.namespace).Create(cluster.ctx, token, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if refusedByPolicy(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("as the bootstrap identity, a dry-run service-account token Secret %s was not refused by the admission policy; last answer: %v", name, err)
		}
		liveDiskPause(t, cluster.ctx, "the bootstrap admission policy to be enforced")
	}

	_, err = asBootstrap.CoreV1().Secrets(names.namespace).Create(cluster.ctx, token, metav1.CreateOptions{})
	if !refusedByPolicy(err) {
		t.Fatalf("as the bootstrap identity, creating a service-account token Secret under the unused inventory name %s was not refused by the admission policy: %v", name, err)
	}
	t.Logf("the bootstrap identity's service-account token Secret was refused: %v", err)
	if _, err := cluster.client.CoreV1().Secrets(names.namespace).Get(cluster.ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("after the refusal, Secret %s exists (err %v)", name, err)
	}

	// The control: the same identity, name and label, as an Opaque Secret,
	// is admitted. So the refusal was the type rule and not RBAC or the name.
	opaque := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: names.namespace, Labels: labels},
		Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"probe": []byte("dry run")}}
	if _, err := asBootstrap.CoreV1().Secrets(names.namespace).Create(cluster.ctx, opaque, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		t.Fatalf("as the bootstrap identity, a dry-run labelled Opaque Secret %s was refused, so the token refusal proves nothing about the policy's type rule: %v", name, err)
	}
}

func liveEd25519Private(t *testing.T, name string, raw []byte) ed25519.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("Secret %s holds no PEM key", name)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("Secret %s's key is not PKCS#8: %v", name, err)
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("Secret %s's key is a %T, not Ed25519", name, parsed)
	}
	return private
}

// ringFiles writes the ring Secret web mounts to disk, the way the kubelet
// projects it, and returns the directory and the keys it held. The control
// ring is the only ring there is.
func (cluster *liveCluster) ringFiles(inventory []liveInventoryEntry) (string, []string) {
	t := cluster.t
	t.Helper()
	ring := cluster.secret(liveInventoryNamed(t, inventory, "ring").Name)
	if _, found := ring.Data["control-keys.json"]; !found {
		keys := make([]string, 0, len(ring.Data))
		for key := range ring.Data {
			keys = append(keys, key)
		}
		t.Fatalf("the ring Secret holds %v and no control-keys.json", keys)
	}
	dir := t.TempDir()
	keys := make([]string, 0, len(ring.Data))
	for key, value := range ring.Data {
		if err := os.WriteFile(filepath.Join(dir, key), value, 0o600); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	return dir, keys
}

// assertRingsArePublicHalves: each ring holds exactly the public half of its
// purpose's key for this activation epoch, and no symmetric key appears in
// either ring in any encoding.
func (cluster *liveCluster) assertRingsArePublicHalves(inventory []liveInventoryEntry) {
	t := cluster.t
	t.Helper()
	dir, ringKeys := cluster.ringFiles(inventory)
	controls, err := hangaroutput.LoadControlKeyRing(filepath.Join(dir, "control-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range inventory {
		if entry.Kind != "ed25519" {
			continue
		}
		public := base64.StdEncoding.EncodeToString(liveEd25519Private(t, entry.Name, cluster.secret(entry.Name).Data[entry.Key]).Public().(ed25519.PublicKey))
		switch entry.Ring {
		case "control":
			if len(controls.Keys) != 1 || int64(controls.Keys[0].Epoch) != entry.Epoch || controls.Keys[0].PublicKey != public {
				t.Errorf("the control ring %+v is not exactly the public half of %s (epoch %d)", controls, entry.Name, entry.Epoch)
			}
		default:
			t.Errorf("Ed25519 entry %s names ring %q", entry.Name, entry.Ring)
		}
	}
	var rings []byte
	for _, key := range ringKeys {
		ring, err := os.ReadFile(filepath.Join(dir, key))
		if err != nil {
			t.Fatal(err)
		}
		rings = append(rings, ring...)
	}
	for _, entry := range inventory {
		if entry.Kind != "random32" {
			continue
		}
		key := cluster.secret(entry.Name).Data[entry.Key]
		if len(key) != 32 {
			t.Fatalf("symmetric key %s is %d bytes", entry.Name, len(key))
		}
		for _, encoded := range [][]byte{key, []byte(base64.StdEncoding.EncodeToString(key)), []byte(base64.RawStdEncoding.EncodeToString(key)), []byte(hex.EncodeToString(key))} {
			if bytes.Contains(rings, encoded) {
				t.Errorf("symmetric key %s appears in a ring", entry.Name)
			}
		}
	}
}

// assertWebLoadedRings: web's startup reads both rings and refuses to run on
// one it cannot load or that names another activation epoch, so a Ready web
// whose live spec mounts the ring Secret and turns capture on is a web that
// loaded both.
func (cluster *liveCluster) assertWebLoadedRings(inventory []liveInventoryEntry) {
	t := cluster.t
	t.Helper()
	name := cluster.names.release + "-web"
	deployment, err := cluster.client.AppsV1().Deployments(cluster.names.namespace).Get(cluster.ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, " ")
	for _, flag := range []string{"--kubernetes-hangar-output-capture-enabled", "--kubernetes-hangar-output-control-keys=", "--kubernetes-hangar-warrant-key=", "--run-input-signing-key="} {
		if !strings.Contains(args, flag) {
			t.Fatalf("web runs without %s", flag)
		}
	}
	ring := liveInventoryNamed(t, inventory, "ring").Name
	mounted := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName == ring {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("web does not mount the ring Secret %s", ring)
	}
	if deployment.Status.AvailableReplicas < 1 || deployment.Status.UpdatedReplicas != deployment.Status.Replicas {
		t.Fatalf("web is not available on its current spec: %+v", deployment.Status)
	}
}

// outputPlaneClient presents web's client certificate for the artifact
// daemon, from the Secret web mounts, and verifies the daemon's server
// certificate against its headless Service name. The artifact daemon serves the
// output plane.
func (cluster *liveCluster) outputPlaneClient(withCertificate bool) *http.Client {
	t := cluster.t
	t.Helper()
	secret := cluster.secret(cluster.names.daemonTLS)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(secret.Data["ca.crt"]) {
		t.Fatalf("Secret %s's ca.crt holds no certificate", cluster.names.daemonTLS)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: cluster.names.outputPlaneServerName()}
	if withCertificate {
		certificate, err := tls.X509KeyPair(secret.Data["client.crt"], secret.Data["client.key"])
		if err != nil {
			t.Fatalf("Secret %s is not a key pair: %v", cluster.names.daemonTLS, err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = config
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

// assertOutputPlaneAcceptsWebClient dials the artifact daemon's output plane
// where web does, the node's IP on the daemon's host port. Web itself makes this call only for an
// execution under the base protocol, which is on from S6,
// but this contract runs no execution, so the contract presents web's mounted
// client Secret itself.
func (cluster *liveCluster) assertOutputPlaneAcceptsWebClient() {
	t := cluster.t
	t.Helper()
	handshakeURL := fmt.Sprintf("https://%s:7780/capture/v1/handshake", cluster.nodeIP)
	body, status := liveGet(t, cluster.ctx, cluster.outputPlaneClient(true), handshakeURL)
	if status != http.StatusOK {
		t.Fatalf("the artifact daemon's output plane refused web's client certificate on the capture handshake: %d %s", status, body)
	}
	var handshake output.ExtensionHandshake
	if err := json.Unmarshal(body, &handshake); err != nil {
		t.Fatalf("decode the capture handshake: %v", err)
	}
	if err := handshake.Validate(); err != nil {
		t.Fatalf("the capture handshake does not validate: %v", err)
	}
	if handshake.Base.ControlKeyID != liveClusterControlKeyID || handshake.Base.ActivationEpoch != liveClusterEpoch {
		t.Fatalf("the artifact daemon's output plane reports %+v, want control key %s, epoch %d", handshake, liveClusterControlKeyID, liveClusterEpoch)
	}
	if body, status := liveGet(t, cluster.ctx, cluster.outputPlaneClient(false), handshakeURL); status != http.StatusUnauthorized {
		t.Fatalf("without a client certificate the capture handshake answered %d %s; it requires one, so the 200 above proves nothing about web's", status, body)
	}
}

func liveGet(t *testing.T, ctx context.Context, client *http.Client, address string) ([]byte, int) {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", address, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return body, response.StatusCode
}

// assertArtifactDaemonUsesWarrantKey publishes a tree through the artifact
// daemon -- which writes it into the disk store's input namespace as `input`
// -- and has a generated step pod materialize it under a warrant signed with
// the generated warrant key, which the daemon verifies.
func (cluster *liveCluster) assertArtifactDaemonUsesWarrantKey() {
	t := cluster.t
	t.Helper()
	names := cluster.names
	liveDiskWaitNodeLabel(t, cluster.ctx, cluster.client, cluster.node, "concourse.dev/hangar-v1", "ready")
	dir := t.TempDir()
	certPath, keyPath, caPath := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"), filepath.Join(dir, "ca.crt")
	for path, data := range map[string][]byte{certPath: cluster.daemonClientCert, keyPath: cluster.daemonClientKey, caPath: cluster.ca.certPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := names.release + "-artifact-daemon"
	daemon := newLiveDiskDaemon(t, fmt.Sprintf("https://%s:%d", cluster.nodeIP, liveDiskPort), daemonServerName(service, names.namespace), certPath, keyPath, cluster.ca.certPool())
	tree := liveDiskTree(t, map[string]string{"literal [x]": "payload", "nested/run.sh": "run"}, []string{"empty", "nested"}, map[string]string{"latest": "nested/run.sh"})
	published := daemon.publish(t, cluster.ctx, tree, http.StatusCreated)

	signer, err := hangar.NewWarrantSigner(cluster.secret(names.warrant).Data["hangar.key"], 5*time.Minute, nil)
	if err != nil {
		t.Fatalf("the generated warrant key does not make a signer: %v", err)
	}
	cfg := NewConfig(names.namespace, os.Getenv("KUBECONFIG"))
	cfg.ArtifactDaemonNamespace = names.namespace
	cfg.ArtifactDaemonService = service
	cfg.ArtifactDaemonHostPath = cluster.hostPath
	cfg.ArtifactDaemonPort = liveDiskPort
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCert, cfg.ArtifactDaemonTLSKey, cfg.ArtifactDaemonTLSCACert = certPath, keyPath, caPath
	cfg.ArtifactHelperImage = "alpine:latest"
	cfg.HangarEnabled = true
	cfg.HangarWarrantSigner = signer
	liveDiskMaterialize(t, cluster.ctx, cluster.client, cfg, "bootstrap-warrant-"+liveDiskRandomHex(t, 3), published.Ref)
}

// liveStoreProbe is the object the publisher round trip leaves in the output
// namespace for web's orphan sweep to find. It carries a marker of a version
// this store does not write, which the sweep counts as unmarked and never
// deletes.
type liveStoreProbe struct {
	key        string
	generation int64
}

// store is a client of the disk store through a port-forward to its pod,
// verifying its certificate against the Service name the bootstrap issued it
// for and presenting one generated role token.
type liveStore struct {
	t        *testing.T
	ctx      context.Context
	base     string
	client   *http.Client
	storeID  string
	tokens   map[string]string
	shutdown func()
}

func (cluster *liveCluster) store() *liveStore {
	t := cluster.t
	t.Helper()
	names := cluster.names
	port, stop := cluster.forward("app.kubernetes.io/component=hangar-store", 7783)
	tlsSecret := cluster.secret(names.storeTLS)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(tlsSecret.Data["ca.crt"]) {
		t.Fatalf("Secret %s's ca.crt holds no certificate", names.storeTLS)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: names.storeDNS()}
	credentials := cluster.secret(names.storeCredentials)
	tokens := map[string]string{}
	for _, role := range []string{"input", "publisher", "inventory", "reclaimer"} {
		tokens[role] = strings.TrimSpace(string(credentials.Data[role]))
	}
	return &liveStore{t: t, ctx: cluster.ctx, base: fmt.Sprintf("https://127.0.0.1:%d", port),
		client: &http.Client{Transport: transport, Timeout: time.Minute}, storeID: liveClusterStoreID, tokens: tokens, shutdown: stop}
}

func (store *liveStore) do(role, method, operation string, query url.Values, body []byte, headers ...string) (int, []byte) {
	t := store.t
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(store.ctx, method, store.base+"/v1/"+operation+"?"+query.Encode(), reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+store.tokens[role])
	request.Header.Set("X-Hangar-Store-ID", store.storeID)
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response, err := store.client.Do(request)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, operation, role, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		t.Fatalf("read %s %s as %s: %v", method, operation, role, err)
	}
	if got := response.Header.Get("X-Hangar-Store-ID"); got != store.storeID {
		t.Fatalf("%s as %s: the store answered as %q, want %q", operation, role, got, store.storeID)
	}
	return response.StatusCode, answer
}

// storePublisherRoundTrip creates the probe under the prefix web's orphan
// sweep lists and reads it back, as `publisher`.
func (cluster *liveCluster) storePublisherRoundTrip() liveStoreProbe {
	t := cluster.t
	t.Helper()
	store := cluster.store()
	defer store.shutdown()
	namespace := cluster.outputNamespace()
	key := namespace.ListPrefix() + "hangar-cluster-contract/" + liveDiskRandomHex(t, 4)
	content := []byte("an object under a foreign marker: the orphan sweep counts it and leaves it")
	metadata, err := json.Marshal(map[string]string{output.MarkerKeyVersion: "hangar-output-v0"})
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"bucket": {"outputs"}, "key": {key}}
	status, body := store.do("publisher", http.MethodPost, "create", query, content,
		"X-Hangar-Metadata", base64.StdEncoding.EncodeToString(metadata))
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("create as publisher: %d %s", status, body)
	}
	var attrs struct{ Generation int64 }
	if err := json.Unmarshal(body, &attrs); err != nil || attrs.Generation <= 0 {
		t.Fatalf("create as publisher answered %s (%v)", body, err)
	}
	query.Set("generation", strconv.FormatInt(attrs.Generation, 10))
	status, read := store.do("publisher", http.MethodGet, "read", query, nil)
	if status != http.StatusOK || !bytes.Equal(read, content) {
		t.Fatalf("read back as publisher: %d, %d bytes, want %d", status, len(read), len(content))
	}
	// Distinct principals: the inventory role lists and stats, and may not write.
	if status, body := store.do("inventory", http.MethodPost, "create", url.Values{"bucket": {"outputs"}, "key": {key + "-inventory"}}, content); status != http.StatusForbidden {
		t.Fatalf("a create as inventory answered %d %s; the store must refuse it", status, body)
	}
	return liveStoreProbe{key: key, generation: attrs.Generation}
}

func (cluster *liveCluster) outputNamespace() output.OutputNamespace {
	cluster.t.Helper()
	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store: output.StoreDisk, StoreID: liveClusterStoreID, Bucket: "outputs",
		TenantID: liveClusterTenant, ActivationEpoch: liveClusterEpoch,
	})
	if err != nil {
		cluster.t.Fatal(err)
	}
	return namespace
}

// assertControllersSwept: web put the plane in service, and its orphan sweep
// listed the output namespace with the `inventory` token and counted the
// foreign-marked probe as unmarked -- and left it. The probe is then stat'd and
// deleted as `reclaimer`, the token the web's reclaim pass deletes with.
func (cluster *liveCluster) assertControllersSwept(probe liveStoreProbe) {
	t := cluster.t
	t.Helper()
	db, stop := cluster.database()
	defer stop()

	var enabled bool
	if err := db.QueryRowContext(cluster.ctx, `SELECT enabled FROM hangar_enabled`).Scan(&enabled); err != nil {
		t.Fatalf("read the in-service row: %v", err)
	}
	if !enabled {
		t.Fatal("web runs with hangarOutput.webEnabled and the in-service row says false")
	}

	deadline := time.Now().Add(4 * time.Minute)
	for !liveClusterSweepCounted(cluster.deploymentLogs("web"), "unmarked") {
		if time.Now().After(deadline) {
			t.Fatalf("web's orphan sweep never counted the foreign-marked probe\n%s", cluster.deploymentLogs("web"))
		}
		liveDiskPause(t, cluster.ctx, "web's first orphan sweep")
	}
	cluster.assertNoRestarts("web")

	store := cluster.store()
	defer store.shutdown()
	query := url.Values{"bucket": {"outputs"}, "key": {probe.key}, "generation": {strconv.FormatInt(probe.generation, 10)}}
	if status, body := store.do("reclaimer", http.MethodGet, "stat", query, nil); status != http.StatusOK {
		t.Fatalf("the sweep removed an object it does not own, or stat as reclaimer failed: %d %s", status, body)
	}
	if status, body := store.do("reclaimer", http.MethodDelete, "delete", query, nil); status != http.StatusNoContent {
		t.Fatalf("delete as reclaimer: %d %s", status, body)
	}
	if status, _ := store.do("publisher", http.MethodGet, "stat", query, nil); status != http.StatusNotFound {
		t.Fatalf("after the reclaimer's delete, a stat answered %d, want 404", status)
	}
}

// liveClusterSweepCounted finds web's `hangar-output-orphan-sweep` line with a
// nonzero count for class. The line is a lager JSON record.
func liveClusterSweepCounted(logs, class string) bool {
	for _, line := range strings.Split(logs, "\n") {
		var record struct {
			Message string         `json:"message"`
			Data    map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &record) != nil {
			continue
		}
		if !strings.HasSuffix(record.Message, "hangar-output-orphan-sweep") {
			continue
		}
		if count, ok := record.Data[class].(float64); ok && count > 0 {
			return true
		}
	}
	return false
}

func (cluster *liveCluster) componentPods(component string) []corev1.Pod {
	cluster.t.Helper()
	pods, err := cluster.client.CoreV1().Pods(cluster.names.namespace).List(cluster.ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=" + component})
	if err != nil {
		cluster.t.Fatalf("list %s pods: %v", component, err)
	}
	return pods.Items
}

func (cluster *liveCluster) deploymentLogs(component string) string {
	var logs strings.Builder
	for _, pod := range cluster.componentPods(component) {
		if pod.DeletionTimestamp != nil {
			continue
		}
		raw, err := cluster.client.CoreV1().Pods(cluster.names.namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(cluster.ctx)
		if err != nil {
			raw = []byte(err.Error())
		}
		logs.Write(raw)
	}
	return logs.String()
}

func (cluster *liveCluster) assertNoRestarts(component string) {
	cluster.t.Helper()
	for _, pod := range cluster.componentPods(component) {
		if pod.DeletionTimestamp != nil {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.RestartCount != 0 {
				cluster.t.Fatalf("%s pod %s restarted %d times; its first start must have succeeded", component, pod.Name, status.RestartCount)
			}
		}
	}
}

// forward opens a port-forward to the one Ready pod under selector and
// returns the local port.
func (cluster *liveCluster) forward(selector string, port int) (int, func()) {
	t := cluster.t
	t.Helper()
	pod := liveDiskWaitOneReady(t, cluster.ctx, cluster.client, cluster.names.namespace, selector, "")
	transport, upgrader, err := spdy.RoundTripperFor(cluster.rest)
	if err != nil {
		t.Fatal(err)
	}
	target := cluster.client.CoreV1().RESTClient().Post().Resource("pods").Namespace(cluster.names.namespace).Name(pod.Name).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, target)
	stop, ready := make(chan struct{}), make(chan struct{})
	forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", port)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	go func() { failed <- forwarder.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-failed:
		t.Fatalf("port-forward to %s:%d: %v", pod.Name, port, err)
	case <-time.After(30 * time.Second):
		t.Fatalf("port-forward to %s:%d never became ready", pod.Name, port)
	}
	ports, err := forwarder.GetPorts()
	if err != nil || len(ports) != 1 {
		t.Fatalf("port-forward ports %v: %v", ports, err)
	}
	return int(ports[0].Local), func() { close(stop) }
}

// database is a connection to the bundled PostgreSQL as web's user.
func (cluster *liveCluster) database() (*sql.DB, func()) {
	t := cluster.t
	t.Helper()
	port, stop := cluster.forward("app.kubernetes.io/component=database", 5432)
	db, err := sql.Open("pgx", fmt.Sprintf("host=127.0.0.1 port=%d user=concourse dbname=concourse sslmode=disable password=%s", port, cluster.postgresPassword))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db, func() { _ = db.Close(); stop() }
}
