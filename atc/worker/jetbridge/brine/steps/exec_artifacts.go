package steps

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Existing artifacts are consumed through the real daemon and production
// tar/gzip reader. This does not stand in for producer publication tests.
func serveExecArtifact(ctx context.Context, rec *brine.Recorder, key string, files map[string]string) (runtime.Artifact, error) {
	daemon, err := startExecArtifactDaemon(rec)
	if err != nil {
		return nil, err
	}
	if err := writeExecArtifact(ctx, daemon, key, files); err != nil {
		return nil, err
	}
	host, port, err := hostPortOfURL(daemon.URL)
	if err != nil {
		return nil, err
	}
	config := jetbridge.NewConfig("exec-artifact", "")
	config.ArtifactDaemonPort = port
	return jetbridge.NewDaemonSetVolumeFromIP(key, key, "artifact-source", host, config), nil
}

func startExecArtifactDaemon(rec *brine.Recorder) (*realDaemon, error) {
	daemon, err := startRealDaemon()
	if err != nil {
		return nil, err
	}
	fmt.Printf("started exec artifact daemon %s\n", daemon.Root)
	rec.RegisterDisposer(func() {
		if err := daemon.stop(); err != nil {
			panic(err)
		}
		fmt.Printf("stopped exec artifact daemon %s\n", daemon.Root)
	})
	return daemon, nil
}

func writeExecArtifact(ctx context.Context, daemon *realDaemon, key string, files map[string]string) error {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || len(files) == 0 {
		return fmt.Errorf("artifact fixture needs a simple key and nonempty files")
	}
	for name := range files {
		clean := filepath.Clean(name)
		if filepath.IsAbs(name) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("artifact path escapes its owned root: %q", name)
		}
	}
	dir := filepath.Join(daemon.Root, "steps", key)
	for name, body := range files {
		if err := writeArtifactFile(filepath.Join(dir, name), body); err != nil {
			return err
		}
	}
	return registerDaemonArtifact(ctx, http.DefaultClient, daemon.URL, key, dir)
}

const execTaskWorkerName = "task-validation-worker"

// Local envtest supplies real API and database-backed workers, not executing pods.
func realExecTaskPool(ctx context.Context, rec *brine.Recorder, database JetbridgeDB, res brine.Resources) (atcworker.Pool, error) {
	return realExecPool(ctx, rec, database, res, nil)
}

func realExecPool(ctx context.Context, rec *brine.Recorder, database JetbridgeDB, res brine.Resources, routed *rest.Config) (atcworker.Pool, error) {
	fixture, err := newExecPoolFixture(ctx, rec, database, res)
	if err != nil {
		return atcworker.Pool{}, err
	}
	return fixture.pool(routed, nil)
}

type execPoolFixture struct {
	DB      atcworker.DB
	Cluster *realCluster
	Config  jetbridge.Config
}

func newExecPoolFixture(ctx context.Context, rec *brine.Recorder, database JetbridgeDB, res brine.Resources) (execPoolFixture, error) {
	cluster, ok := res.Get("real-cluster").(*realCluster)
	if !ok {
		return execPoolFixture{}, fmt.Errorf("missing real Kubernetes API")
	}
	ns, err := cluster.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "exec-step-"}}, metav1.CreateOptions{})
	if err != nil {
		return execPoolFixture{}, err
	}
	registerNamespacePodCleanup(rec, cluster.Clientset, ns.Name)
	if _, err := database.PersistNamedWorker(execTaskWorkerName); err != nil {
		return execPoolFixture{}, err
	}
	workerDB := newExecWorkerDB(database)
	return execPoolFixture{DB: workerDB, Cluster: cluster, Config: jetbridge.NewConfig(ns.Name, "")}, nil
}

// Retry faults may route this client. Lifecycle cleanup always uses the original API.
func (f execPoolFixture) pool(routed *rest.Config, daemon *jetbridge.DaemonClient) (atcworker.Pool, error) {
	var client kubernetes.Interface = f.Cluster.Clientset
	apiConfig := f.Cluster.RESTConfig
	if routed != nil {
		apiConfig = routed
		var err error
		client, err = kubernetes.NewForConfig(apiConfig)
		if err != nil {
			return atcworker.Pool{}, err
		}
	}
	factory := atcworker.DefaultFactory{DB: f.DB, K8sClientset: client, K8sConfig: &f.Config,
		K8sExecutor: jetbridge.NewSPDYExecutor(client, apiConfig), K8sDaemonClient: daemon}
	return atcworker.NewPool(factory, f.DB), nil
}

func newExecWorkerDB(database JetbridgeDB) atcworker.DB {
	return atcworker.NewDB(database.WorkerFactory, database.TeamFactory, database.VolumeRepository,
		db.NewTaskCacheFactory(database.Conn), db.NewWorkerTaskCacheFactory(database.Conn),
		db.NewResourceCacheFactory(database.Conn, database.LockFactory), db.NewWorkerBaseResourceTypeFactory(database.Conn), database.LockFactory)
}
