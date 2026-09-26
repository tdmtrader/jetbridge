// hangar_live only, never live: this contract creates a namespace, a
// ClusterRole and ClusterRoleBinding (the artifact daemon's node-labelling
// grant) and schedules a step pod onto the node the artifact daemon labels --
// cluster-scope work a namespaced live-tier account cannot do and must not do
// against the deployed cluster. Its CI home is the hangar-disk-store-contract
// job in deploy/k8s-e2e-pipeline.yml, which stands up a throwaway K3s cluster,
// loads the image this contract names into it, and hands it a cluster-admin
// kubeconfig; build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc/db"
	atcruntime "github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
)

// liveDiskImageEnv names the image the chart runs the artifact daemon and
// hangar-store from. It must hold /usr/local/concourse/bin/artifact-daemon and
// /usr/local/concourse/bin/hangar-store, already be loaded into the cluster's
// container runtime (the chart is rendered with image.pullPolicy=Never), and
// carry a tag other than latest: the chart's initialize Job sets no pull
// policy, and Kubernetes pulls a :latest image Always.
const liveDiskImageEnv = "HANGAR_DISK_IMAGE"

const (
	liveDiskRelease   = "hl"
	liveDiskStoreID   = "hangar-live-store"
	liveDiskInputs    = "inputs"
	liveDiskScope     = "ci"
	liveDiskHostPath  = "/var/concourse/artifacts"
	liveDiskPort      = 7780
	liveDiskReadyWait = 500 * time.Millisecond
)

// TestLiveHangarDiskStoreRoundTripSurvivesRestart is the live coverage the
// persistent-disk Hangar store (hangarStorage.disk, cmd/hangar-store) ships
// without: its unit tests run the store over a real filesystem behind
// httptest and its chart tests only render. What neither can say is that the
// chart's hangar-store Deployment, PVC and initialize Job, and the artifact
// daemon it renders with artifactDaemon.hangar.store=disk, actually find each
// other on a cluster -- the Service DNS name, the TLS SAN, the projected
// per-role credential, the store identity -- and that what the store holds is
// on the volume rather than in the process.
//
// Everything below the chart is real: hangar-store on a local-path PVC,
// provisioned by the chart's own initialize Job; the artifact daemon from the
// chart's DaemonSet, with strict inputs pointed at that store; a step pod this
// runtime generates, whose init container asks the artifact daemon to
// materialize the tree. Nothing is stood in. The only things the contract
// holds that a deployment's web would are the daemon client certificate it
// publishes with and the warrant key it signs materialization warrants with.
//
// It proves, in order:
//
//  1. publish: a tar posted to the artifact daemon's strict publication route
//     comes back 201 with a tree ref, and again 200 with the SAME ref;
//  2. materialize: a generated step pod's init container materializes that
//     tree ref and the main container finds the exact tree, read-only;
//  3. restart: the hangar-store pod is deleted and its Deployment replaces it
//     on the same PVC;
//  4. after the restart, publishing the same tar is still 200 with the same
//     ref (the index survived), a fresh step pod materializes it again (the
//     blob survived), and a new tree gets a later generation (generations are
//     not reissued -- ADR-0005's restore rule, held here across a restart).
//
// Reaching the artifact daemon: the contract dials the node's InternalIP on
// the DaemonSet's hostPort. In the CI job that address is the K3s container's
// Docker bridge IP, which the task's own dockerd routes.
func TestLiveHangarDiskStoreRoundTripSurvivesRestart(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("real K3s execution of the disk store is CI-only on macOS")
	}
	image := os.Getenv(liveDiskImageEnv)
	colon := strings.LastIndex(image, ":")
	if colon <= 0 || colon == len(image)-1 || strings.Contains(image[colon:], "/") || strings.HasSuffix(image, ":latest") {
		t.Fatalf("%s must name a repository:tag image (not :latest) holding artifact-daemon and hangar-store, loaded into the cluster; got %q", liveDiskImageEnv, image)
	}
	repository, tag := image[:colon], image[colon+1:]

	kubeconfig := os.Getenv("KUBECONFIG")
	cfg := NewConfig("default", kubeconfig)
	client, err := NewClientset(cfg)
	if err != nil {
		t.Fatalf("create live Kubernetes client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) != 1 {
		t.Fatalf("the contract needs exactly one node (the step pod must land where the artifact daemon it dials runs): count=%d err=%v", len(nodes.Items), err)
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

	suffix := liveDiskRandomHex(t, 4)
	namespace := "hangar-disk-" + suffix
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace %s: %v", namespace, err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
	})

	// Render once to learn the names the chart composes. The certificates
	// must carry the Service DNS names the artifact daemon and diskclient
	// verify against, and those are the chart's to choose.
	secrets := liveDiskSecretNames{
		daemonTLS: "hl-daemon-tls", resolve: "hl-resolve",
		storeTLS: "hl-store-tls", storeCredentials: "hl-store-credentials",
	}
	provision := liveDiskRender(t, namespace, repository, tag, secrets, true)
	storeService := provision.only(t, "hangar-store Service", func(o k8sruntime.Object) bool {
		s, ok := o.(*corev1.Service)
		return ok && strings.HasSuffix(s.Name, "-hangar-store")
	}).(*corev1.Service).Name
	daemonService := provision.only(t, "artifact daemon Service", func(o k8sruntime.Object) bool {
		s, ok := o.(*corev1.Service)
		return ok && strings.HasSuffix(s.Name, "-artifact-daemon")
	}).(*corev1.Service).Name
	daemonDNS := daemonServerName(daemonService, namespace)

	ca := newLiveDiskCA(t)
	daemonCert, daemonKey := ca.issue(t, daemonDNS, []string{daemonDNS, "*." + daemonDNS}, []net.IP{net.ParseIP(nodeIP)},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	clientCert, clientKey := ca.issue(t, "hangar-disk-contract", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	storeName := storeService + "." + namespace + ".svc"
	storeCert, storeKey := ca.issue(t, storeName, []string{storeName}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})

	warrantKey := liveDiskRandomBytes(t, 32)
	tokens := map[string]string{}
	for _, role := range []string{"input", "publisher", "inventory", "reclaimer"} {
		tokens[role] = liveDiskRandomHex(t, 32)
	}
	serverJSON, err := json.Marshal(tokens)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]map[string][]byte{
		secrets.daemonTLS: {"tls.crt": daemonCert, "tls.key": daemonKey, "ca.crt": ca.certPEM, "hangar.key": warrantKey},
		secrets.resolve:   {"resolve.key": liveDiskRandomBytes(t, 32)},
		secrets.storeTLS:  {"tls.crt": storeCert, "tls.key": storeKey, "ca.crt": ca.certPEM},
		secrets.storeCredentials: {
			"server.json": serverJSON,
			"input":       []byte(tokens["input"]), "publisher": []byte(tokens["publisher"]),
			"inventory": []byte(tokens["inventory"]), "reclaimer": []byte(tokens["reclaimer"]),
		},
	} {
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: data}
		if _, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create Secret %s: %v", name, err)
		}
	}

	// Registered after the namespace's own cleanup, so it runs first: a
	// failure dumps the pods' logs before the namespace takes them away.
	t.Cleanup(func() {
		if t.Failed() {
			liveDiskDiagnostics(t, client, namespace, node.Name)
		}
	})

	// The operator's provisioning flow, as the chart renders it: with
	// initialize=true the Deployment has zero replicas and a one-shot Job
	// writes the store identity onto the empty PVC.
	store := provision.except(func(o k8sruntime.Object) bool { return liveDiskIsDaemonObject(o) })
	liveDiskApply(t, ctx, client, namespace, store)
	job := provision.only(t, "initialize Job", func(o k8sruntime.Object) bool { _, ok := o.(*batchv1.Job); return ok }).(*batchv1.Job)
	liveDiskWaitJob(t, ctx, client, namespace, job.Name)

	// Then the ordinary render: the same Deployment at one replica. This is
	// the flip an operator makes, applied as an update to the live object.
	serve := liveDiskRender(t, namespace, repository, tag, secrets, false)
	deployment := serve.only(t, "hangar-store Deployment", func(o k8sruntime.Object) bool { _, ok := o.(*appsv1.Deployment); return ok }).(*appsv1.Deployment)
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("the serving render's hangar-store Deployment has replicas %v, want 1", deployment.Spec.Replicas)
	}
	liveDiskUpdateDeployment(t, ctx, client, namespace, deployment)
	storeSelector := metav1.FormatLabelSelector(deployment.Spec.Selector)
	firstStorePod := liveDiskWaitOneReady(t, ctx, client, namespace, storeSelector, "")

	// The artifact daemon only after the store answers: it proves its disk
	// credential against the store's identity at startup and exits if it
	// cannot, and a crash-looping DaemonSet would only slow the contract down.
	liveDiskApply(t, ctx, client, namespace, serve.except(func(o k8sruntime.Object) bool { return !liveDiskIsDaemonObject(o) }))
	daemonSet := serve.only(t, "artifact daemon DaemonSet", func(o k8sruntime.Object) bool { _, ok := o.(*appsv1.DaemonSet); return ok }).(*appsv1.DaemonSet)
	liveDiskWaitOneReady(t, ctx, client, namespace, metav1.FormatLabelSelector(daemonSet.Spec.Selector), "")
	liveDiskWaitNodeLabel(t, ctx, client, node.Name, "concourse.dev/hangar-v1", "ready")

	dir := t.TempDir()
	clientCertPath, clientKeyPath, caPath := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"), filepath.Join(dir, "ca.crt")
	for path, data := range map[string][]byte{clientCertPath: clientCert, clientKeyPath: clientKey, caPath: ca.certPEM} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	daemon := newLiveDiskDaemon(t, fmt.Sprintf("https://%s:%d", nodeIP, liveDiskPort), daemonDNS, clientCertPath, clientKeyPath, ca.certPool())

	// 1. Publish, then publish again.
	tree := liveDiskTree(t, map[string]string{"literal [x]": "payload", "nested/run.sh": "run"}, []string{"empty", "nested"}, map[string]string{"latest": "nested/run.sh"})
	published := daemon.publish(t, ctx, tree, http.StatusCreated)
	if published.Ref.Scope != liveDiskScope || published.Ref.Generation <= 0 || published.LogicalBytes <= 0 {
		t.Fatalf("first publication attributes = %+v", published)
	}
	if again := daemon.publish(t, ctx, tree, http.StatusOK); again.Ref != published.Ref {
		t.Fatalf("republishing the same tree returned %+v, want the first ref %+v", again.Ref, published.Ref)
	}

	// 2. Materialize through a generated step pod.
	signer, err := hangar.NewWarrantSigner(warrantKey, 5*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Namespace = namespace
	cfg.ArtifactDaemonNamespace = namespace
	cfg.ArtifactDaemonService = daemonService
	cfg.ArtifactDaemonHostPath = liveDiskHostPath
	cfg.ArtifactDaemonPort = liveDiskPort
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCert, cfg.ArtifactDaemonTLSKey, cfg.ArtifactDaemonTLSCACert = clientCertPath, clientKeyPath, caPath
	// The chart's own kubernetes.artifactHelperImage, not BusyBox: the init
	// now speaks TLS to a real artifact daemon, and Alpine's wget does that
	// through ssl_client, as it does in every deployment.
	cfg.ArtifactHelperImage = "alpine:latest"
	cfg.HangarEnabled = true
	cfg.HangarWarrantSigner = signer
	liveDiskMaterialize(t, ctx, client, cfg, "before-restart-"+suffix, published.Ref)

	// 3. Restart the store: delete its pod, let the Deployment replace it.
	if err := client.CoreV1().Pods(namespace).Delete(ctx, firstStorePod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete hangar-store pod %s: %v", firstStorePod.Name, err)
	}
	liveDiskWaitGone(t, ctx, client, namespace, firstStorePod.Name)
	restarted := liveDiskWaitOneReady(t, ctx, client, namespace, storeSelector, firstStorePod.UID)
	t.Logf("hangar-store restarted: pod %s (%s) replaced %s (%s)", restarted.Name, restarted.UID, firstStorePod.Name, firstStorePod.UID)

	// 4. What the store held is still there, and still exact.
	if after := daemon.publish(t, ctx, tree, http.StatusOK); after.Ref != published.Ref {
		t.Fatalf("after the restart, republishing returned %+v, want the pre-restart ref %+v", after.Ref, published.Ref)
	}
	liveDiskMaterialize(t, ctx, client, cfg, "after-restart-"+suffix, published.Ref)
	other := liveDiskTree(t, map[string]string{"second": "a different tree"}, nil, nil)
	next := daemon.publish(t, ctx, other, http.StatusCreated)
	if next.Ref.Digest == published.Ref.Digest || next.Ref.Generation <= published.Ref.Generation {
		t.Fatalf("a new tree after the restart got %+v; want a different digest and a generation above %d", next.Ref, published.Ref.Generation)
	}
}

// liveDiskMaterialize generates a step pod with the tree ref as its only
// strict input, runs it, and requires it to succeed. The init container is
// the runtime's own: it POSTs the batch to the artifact daemon and checks the
// materialization receipt. The main container then checks the tree itself.
func liveDiskMaterialize(t *testing.T, ctx context.Context, client kubernetes.Interface, cfg Config, handle string, ref hangar.TreeRef) {
	t.Helper()
	container := &Container{
		handle:   handle,
		podName:  "hangar-disk-" + handle,
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: atcruntime.ContainerSpec{
			Dir: "/work", Type: db.ContainerTypeTask,
			ImageSpec: atcruntime.ImageSpec{ImageURL: "busybox:latest"},
			Inputs:    []atcruntime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
		},
		config: cfg, storageBackend: NewDaemonSetBackend(cfg, nil, nil, nil), properties: map[string]string{},
	}
	receipt, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	mainScript := fmt.Sprintf(`set -eu
test "$(cat '/work/exact/literal [x]')" = 'payload'
test "$(cat /work/exact/nested/run.sh)" = 'run'
test -d /work/exact/empty
test ! -L /work/exact/empty
test "$(readlink /work/exact/latest)" = 'nested/run.sh'
test "$(stat -c '%%a' /work/exact)" = 555
test "$(stat -c '%%a' /work/exact/empty)" = 555
test "$(stat -c '%%a' '/work/exact/literal [x]')" = 444
test "$(stat -c '%%a' /work/exact/nested/run.sh)" = 444
test "$(stat -c '%%a' /work/exact/.hangar-materialized)" = 444
printf '%%s' '%s' | base64 -d | cmp - /work/exact/.hangar-materialized
if touch /work/exact/must-not-write 2>/dev/null; then exit 91; fi
`, base64.StdEncoding.EncodeToString(receipt))
	pod, err := container.buildPod(atcruntime.ProcessSpec{}, []string{"sh", "-c", mainScript}, nil)
	if err != nil {
		t.Fatalf("generate strict step pod: %v", err)
	}
	assertLivePodMountsResolve(t, pod)
	assertLiveHangarAffinity(t, pod)
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "materialize-hangar-inputs" || pod.Spec.InitContainers[0].Image != cfg.ArtifactHelperImage {
		t.Fatalf("generated strict init = %+v", pod.Spec.InitContainers)
	}
	createLivePod(t, ctx, client, pod)
	waitLivePodSucceeded(t, ctx, client, cfg.Namespace, pod.Name)
}

type liveDiskSecretNames struct {
	daemonTLS, resolve, storeTLS, storeCredentials string
}

type liveDiskObjects []k8sruntime.Object

func (objects liveDiskObjects) only(t *testing.T, what string, match func(k8sruntime.Object) bool) k8sruntime.Object {
	t.Helper()
	var found []k8sruntime.Object
	for _, object := range objects {
		if match(object) {
			found = append(found, object)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the chart rendered %d %s, want exactly 1", len(found), what)
	}
	return found[0]
}

func (objects liveDiskObjects) except(drop func(k8sruntime.Object) bool) liveDiskObjects {
	var kept liveDiskObjects
	for _, object := range objects {
		if !drop(object) {
			kept = append(kept, object)
		}
	}
	return kept
}

func liveDiskIsDaemonObject(object k8sruntime.Object) bool {
	switch o := object.(type) {
	case *appsv1.DaemonSet, *corev1.ServiceAccount, *rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding:
		return true
	case *corev1.Service:
		return strings.HasSuffix(o.Name, "-artifact-daemon")
	}
	return false
}

// liveDiskRender runs `helm template` on the chart in this checkout, limited
// to the templates the disk store and the artifact daemon come from. The
// required-values file the chart tests use supplies the render-only
// references (an MCP client) nothing here deploys.
func liveDiskRender(t *testing.T, namespace, repository, tag string, secrets liveDiskSecretNames, initialize bool) liveDiskObjects {
	t.Helper()
	chart := filepath.Join("..", "..", "..", "deploy", "chart")
	args := []string{"template", liveDiskRelease, chart, "--namespace", namespace,
		"-f", filepath.Join(chart, "tests", "testdata", "required-values.yaml")}
	for _, set := range []string{
		"image.repository=" + repository,
		"image.tag=" + tag,
		"image.pullPolicy=Never",
		"artifactDaemon.tls.existingSecret=" + secrets.daemonTLS,
		"artifactDaemon.resolveCapability.existingSecret=" + secrets.resolve,
		"artifactDaemon.hostPath=" + liveDiskHostPath,
		fmt.Sprintf("artifactDaemon.port=%d", liveDiskPort),
		"artifactDaemon.hangar.enabled=true",
		"artifactDaemon.hangar.store=disk",
		"artifactDaemon.hangar.bucket=" + liveDiskInputs,
		"hangarStorage.disk.enabled=true",
		"hangarStorage.disk.storeID=" + liveDiskStoreID,
		"hangarStorage.disk.size=1Gi",
		"hangarStorage.disk.tls.existingSecret=" + secrets.storeTLS,
		"hangarStorage.disk.credentials.existingSecret=" + secrets.storeCredentials,
		fmt.Sprintf("hangarStorage.disk.initialize=%t", initialize),
	} {
		args = append(args, "--set", set)
	}
	for _, template := range []string{"hangar-store.yaml", "artifact-daemon-daemonset.yaml", "artifact-daemon-rbac.yaml", "artifact-daemon-service.yaml"} {
		args = append(args, "--show-only", "templates/"+template)
	}
	out, err := exec.Command("helm", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("helm template: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("helm template: %v", err)
	}
	var objects liveDiskObjects
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(out)))
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
		object, _, err := scheme.Codecs.UniversalDeserializer().Decode(doc, nil, nil)
		if err != nil {
			t.Fatalf("decode a rendered manifest: %v\n%s", err, doc)
		}
		objects = append(objects, object)
	}
	return objects
}

func stripYAMLComments(doc []byte) []byte {
	var kept [][]byte
	for _, line := range bytes.Split(doc, []byte("\n")) {
		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("#")) {
			kept = append(kept, line)
		}
	}
	return bytes.Join(kept, []byte("\n"))
}

// liveDiskApply creates each rendered object in the contract's namespace. It
// accepts only the kinds these four templates render: a NetworkPolicy or
// anything else appearing would change what the contract is proving, so it
// fails rather than skipping it.
func liveDiskApply(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string, objects liveDiskObjects) {
	t.Helper()
	for _, object := range objects {
		var err error
		switch o := object.(type) {
		case *corev1.PersistentVolumeClaim:
			o.Namespace = namespace
			_, err = client.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, o, metav1.CreateOptions{})
		case *corev1.Service:
			o.Namespace = namespace
			_, err = client.CoreV1().Services(namespace).Create(ctx, o, metav1.CreateOptions{})
		case *corev1.ServiceAccount:
			o.Namespace = namespace
			_, err = client.CoreV1().ServiceAccounts(namespace).Create(ctx, o, metav1.CreateOptions{})
		case *appsv1.Deployment:
			o.Namespace = namespace
			_, err = client.AppsV1().Deployments(namespace).Create(ctx, o, metav1.CreateOptions{})
		case *appsv1.DaemonSet:
			o.Namespace = namespace
			_, err = client.AppsV1().DaemonSets(namespace).Create(ctx, o, metav1.CreateOptions{})
		case *batchv1.Job:
			o.Namespace = namespace
			_, err = client.BatchV1().Jobs(namespace).Create(ctx, o, metav1.CreateOptions{})
		case *rbacv1.ClusterRole:
			// Cluster-scoped and named for the release, not the namespace;
			// suffix it so a second run on the same cluster cannot collide.
			name := o.Name + "-" + namespace
			o.Name = name
			_, err = client.RbacV1().ClusterRoles().Create(ctx, o, metav1.CreateOptions{})
			t.Cleanup(func() { _ = client.RbacV1().ClusterRoles().Delete(context.Background(), name, metav1.DeleteOptions{}) })
		case *rbacv1.ClusterRoleBinding:
			name := o.Name + "-" + namespace
			o.Name = name
			o.RoleRef.Name += "-" + namespace
			_, err = client.RbacV1().ClusterRoleBindings().Create(ctx, o, metav1.CreateOptions{})
			t.Cleanup(func() {
				_ = client.RbacV1().ClusterRoleBindings().Delete(context.Background(), name, metav1.DeleteOptions{})
			})
		default:
			t.Fatalf("the chart rendered an unexpected %T; the contract applies only what it knows it is proving", object)
		}
		if err != nil {
			t.Fatalf("apply %T: %v", object, err)
		}
	}
}

func liveDiskUpdateDeployment(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace string, rendered *appsv1.Deployment) {
	t.Helper()
	current, err := client.AppsV1().Deployments(namespace).Get(ctx, rendered.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get hangar-store Deployment: %v", err)
	}
	rendered.Namespace = namespace
	rendered.ResourceVersion = current.ResourceVersion
	if _, err := client.AppsV1().Deployments(namespace).Update(ctx, rendered, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update hangar-store Deployment to the serving render: %v", err)
	}
}

func liveDiskWaitJob(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace, name string) {
	t.Helper()
	for {
		job, err := client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get Job %s: %v", name, err)
		}
		if job.Status.Succeeded > 0 {
			return
		}
		if job.Status.Failed > 0 {
			t.Fatalf("the hangar-store initialize Job %s failed", name)
		}
		liveDiskPause(t, ctx, "the hangar-store initialize Job "+name)
	}
}

// liveDiskWaitOneReady waits for exactly one Ready, non-terminating pod under
// selector whose UID is not previous ("" for none), and returns it.
func liveDiskWaitOneReady(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace, selector string, previous types.UID) corev1.Pod {
	t.Helper()
	for {
		pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			t.Fatalf("list pods %s: %v", selector, err)
		}
		var ready []corev1.Pod
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil || (previous != "" && pod.UID == previous) {
				continue
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					ready = append(ready, pod)
				}
			}
		}
		if len(ready) == 1 {
			return ready[0]
		}
		liveDiskPause(t, ctx, "one Ready pod for "+selector)
	}
}

func liveDiskWaitGone(t *testing.T, ctx context.Context, client kubernetes.Interface, namespace, name string) {
	t.Helper()
	for {
		_, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil {
			t.Fatalf("get pod %s: %v", name, err)
		}
		liveDiskPause(t, ctx, "pod "+name+" to be gone")
	}
}

func liveDiskWaitNodeLabel(t *testing.T, ctx context.Context, client kubernetes.Interface, node, key, value string) {
	t.Helper()
	for {
		current, err := client.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get node %s: %v", node, err)
		}
		if current.Labels[key] == value {
			return
		}
		liveDiskPause(t, ctx, fmt.Sprintf("node label %s=%s", key, value))
	}
}

func liveDiskPause(t *testing.T, ctx context.Context, what string) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("wait for %s: %v", what, ctx.Err())
	case <-time.After(liveDiskReadyWait):
	}
}

type liveDiskDaemon struct {
	url    string
	client *http.Client
}

func newLiveDiskDaemon(t *testing.T, url, serverName, certPath, keyPath string, roots *x509.CertPool) liveDiskDaemon {
	t.Helper()
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName, Certificates: []tls.Certificate{certificate}}
	return liveDiskDaemon{url: url, client: &http.Client{Transport: transport, Timeout: time.Minute}}
}

// publish posts a raw tar to the strict publication route and requires the
// wanted status. A 503 is the artifact daemon's answer for every Hangar
// infrastructure refusal -- the same status the materialization init retries
// -- so it is retried for a bounded minute; any other status is final.
func (daemon liveDiskDaemon) publish(t *testing.T, ctx context.Context, archive []byte, want int) hangar.TreeAttributes {
	t.Helper()
	url := daemon.url + strings.Replace(artifactwire.HangarPublish.Path, "{scope}", liveDiskScope, 1)
	deadline := time.Now().Add(time.Minute)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(archive))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := daemon.client.Do(request)
		if err != nil {
			t.Fatalf("publish to the artifact daemon at %s: %v", url, err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatalf("read the publication response: %v", readErr)
		}
		if response.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			t.Logf("publication refused 503, retrying: %s", strings.TrimSpace(string(body)))
			liveDiskPause(t, ctx, "the artifact daemon to accept a publication")
			continue
		}
		if response.StatusCode != want {
			t.Fatalf("publication returned %d, want %d: %s", response.StatusCode, want, strings.TrimSpace(string(body)))
		}
		var attributes hangar.TreeAttributes
		if err := json.Unmarshal(body, &attributes); err != nil {
			t.Fatalf("decode publication attributes %q: %v", body, err)
		}
		return attributes
	}
}

// liveDiskTree builds the raw tar a producer would upload. Modes and owners
// are deliberately not canonical: canonicalization is the artifact daemon's
// job, and the step pod checks the canonical result.
func liveDiskTree(t *testing.T, files map[string]string, dirs []string, symlinks map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	write := func(header *tar.Header, content string) {
		header.Uid, header.Gid = 1234, 5678
		header.ModTime = time.Unix(123456789, 0)
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if content != "" {
			if _, err := io.WriteString(writer, content); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, dir := range dirs {
		write(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0700}, "")
	}
	for name, content := range files {
		write(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0640, Size: int64(len(content))}, content)
	}
	for name, target := range symlinks {
		write(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: target}, "")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

type liveDiskCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newLiveDiskCA(t *testing.T) liveDiskCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hangar-disk-contract-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return liveDiskCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca liveDiskCA) certPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

func (ca liveDiskCA) issue(t *testing.T, commonName string, dnsNames []string, ips []net.IP, usages []x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames: dnsNames, IPAddresses: ips, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func liveDiskRandomBytes(t *testing.T, n int) []byte {
	t.Helper()
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func liveDiskRandomHex(t *testing.T, n int) string {
	t.Helper()
	return hex.EncodeToString(liveDiskRandomBytes(t, n))
}

// liveDiskDiagnostics dumps what a failed run needs and the namespace is about
// to delete: every pod's phase and each container's logs, previous ones
// included (a crash-looping hangar-store or artifact daemon logs its reason
// there), the namespace's events, and the node's labels.
func liveDiskDiagnostics(t *testing.T, client kubernetes.Interface, namespace, node string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("diagnostics: list pods: %v", err)
		return
	}
	for _, pod := range pods.Items {
		t.Logf("diagnostics: pod %s phase=%s uid=%s", pod.Name, pod.Status.Phase, pod.UID)
		for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
			for _, previous := range []bool{false, true} {
				if previous && status.RestartCount == 0 {
					continue
				}
				logs, err := client.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: status.Name, Previous: previous}).DoRaw(ctx)
				if err != nil {
					logs = []byte(err.Error())
				}
				t.Logf("diagnostics: %s/%s previous=%t restarts=%d:\n%s", pod.Name, status.Name, previous, status.RestartCount, logs)
			}
		}
	}
	if events, err := client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{}); err == nil {
		for _, event := range events.Items {
			t.Logf("diagnostics: event %s %s/%s: %s", event.Reason, event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Message)
		}
	}
	if current, err := client.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{}); err == nil {
		t.Logf("diagnostics: node %s labels %v", node, current.Labels)
	}
}
