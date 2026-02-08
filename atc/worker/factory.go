package worker

import (
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/gardenruntime"
	"github.com/concourse/concourse/atc/worker/gardenruntime/gclient"
	"github.com/concourse/concourse/atc/worker/gardenruntime/transport"
	"github.com/concourse/concourse/atc/worker/k8sruntime"
	bclient "github.com/concourse/concourse/worker/baggageclaim/client"
	"github.com/concourse/retryhttp"
	"k8s.io/client-go/kubernetes"
)

type Factory interface {
	NewWorker(lager.Logger, db.Worker) runtime.Worker
}

type DefaultFactory struct {
	DB DB

	Streamer Streamer

	GardenRequestTimeout              time.Duration
	BaggageclaimResponseHeaderTimeout time.Duration
	HTTPRetryTimeout                  time.Duration

	// K8sClientset is the Kubernetes client used to create K8s-backed workers.
	// If nil, K8s workers cannot be created.
	K8sClientset kubernetes.Interface
	// K8sConfig holds the Kubernetes runtime configuration. If nil, K8s
	// workers cannot be created.
	K8sConfig *k8sruntime.Config
	// K8sExecutor is the PodExecutor used for exec-mode I/O (resource
	// get/put/check steps). If nil, exec-mode I/O is disabled.
	K8sExecutor k8sruntime.PodExecutor
}

func (f DefaultFactory) NewWorker(logger lager.Logger, dbWorker db.Worker) runtime.Worker {
	if f.isK8sWorker(dbWorker) {
		return f.newK8sWorker(dbWorker)
	}
	return f.newGardenWorker(logger, dbWorker)
}

// isK8sWorker returns true if the worker should use the Kubernetes runtime.
// A worker is considered K8s-backed if it has no Garden address (nil or empty)
// and the factory has K8s configuration available.
func (f DefaultFactory) isK8sWorker(dbWorker db.Worker) bool {
	if f.K8sClientset == nil || f.K8sConfig == nil {
		return false
	}
	addr := dbWorker.GardenAddr()
	return addr == nil || *addr == ""
}

func (f DefaultFactory) newK8sWorker(dbWorker db.Worker) *k8sruntime.Worker {
	w := k8sruntime.NewWorker(dbWorker, f.K8sClientset, *f.K8sConfig)
	if f.K8sExecutor != nil {
		w.SetExecutor(f.K8sExecutor)
	}
	return w
}

func (f DefaultFactory) newGardenWorker(logger lager.Logger, dbWorker db.Worker) *gardenruntime.Worker {
	gcf := gclient.NewGardenClientFactory(
		f.DB.WorkerFactory,
		logger.Session("garden-connection"),
		dbWorker.Name(),
		dbWorker.GardenAddr(),
		retryhttp.NewExponentialBackOffFactory(f.HTTPRetryTimeout),
		f.GardenRequestTimeout,
	)
	gClient := gcf.NewClient()
	bcClient := bclient.New("", transport.NewBaggageclaimRoundTripper(
		dbWorker.Name(),
		dbWorker.BaggageclaimURL(),
		f.DB.WorkerFactory,
		&http.Transport{
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: f.BaggageclaimResponseHeaderTimeout,
		},
	))

	return gardenruntime.NewWorker(
		dbWorker,
		gClient,
		bcClient,
		f.DB.ToGardenRuntimeDB(),
		f.Streamer,
	)
}
