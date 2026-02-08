package worker

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"k8s.io/client-go/kubernetes"
)

type Factory interface {
	NewWorker(lager.Logger, db.Worker) runtime.Worker
}

type DefaultFactory struct {
	DB DB

	Streamer Streamer

	// K8sClientset is the Kubernetes client used to create K8s-backed workers.
	// If nil, K8s workers cannot be created.
	K8sClientset kubernetes.Interface
	// K8sConfig holds the Kubernetes runtime configuration. If nil, K8s
	// workers cannot be created.
	K8sConfig *jetbridge.Config
	// K8sExecutor is the PodExecutor used for exec-mode I/O (resource
	// get/put/check steps). If nil, exec-mode I/O is disabled.
	K8sExecutor jetbridge.PodExecutor
}

func (f DefaultFactory) NewWorker(logger lager.Logger, dbWorker db.Worker) runtime.Worker {
	return f.newK8sWorker(dbWorker)
}

func (f DefaultFactory) newK8sWorker(dbWorker db.Worker) *jetbridge.Worker {
	w := jetbridge.NewWorker(dbWorker, f.K8sClientset, *f.K8sConfig)
	if f.K8sExecutor != nil {
		w.SetExecutor(f.K8sExecutor)
	}
	if f.K8sConfig.CacheVolumeClaim != "" || f.K8sConfig.ArtifactStoreClaim != "" {
		w.SetVolumeRepo(f.DB.VolumeRepo)
	}
	return w
}
