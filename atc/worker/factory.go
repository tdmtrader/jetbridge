package worker

import (
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/atc/worker/native"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
	"github.com/concourse/concourse/atc/worker/native/remote"
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

	// K8sArtifactLocator tracks artifact → node mapping for DaemonSet mode
	// scheduling affinity. Shared across all workers and the reaper.
	K8sArtifactLocator *jetbridge.ArtifactLocator

	// NativeConfig holds the native worker configuration. When non-nil,
	// workers with a matching platform (e.g. "darwin") are created as
	// native workers that execute tasks as local OS processes.
	NativeConfig *native.Config

	// RemoteNativeClients maps worker names to gRPC clients for remote
	// native agents. When a db.Worker's name matches a key, it's created
	// as a remote worker that proxies execution to the corresponding agent.
	RemoteNativeClients map[string]agentpb.NativeAgentClient

	// Compression is the compression algorithm for artifact streaming.
	// Shared by both K8s and native workers.
	Compression compression.Compression
}

func (f DefaultFactory) NewWorker(logger lager.Logger, dbWorker db.Worker) runtime.Worker {
	if client, ok := f.RemoteNativeClients[dbWorker.Name()]; ok {
		return f.newRemoteNativeWorker(dbWorker, client)
	}
	if f.NativeConfig != nil && dbWorker.Platform() == f.NativeConfig.Platform {
		return f.newNativeWorker(dbWorker)
	}
	return f.newK8sWorker(dbWorker)
}

func (f DefaultFactory) newK8sWorker(dbWorker db.Worker) *jetbridge.Worker {
	w := jetbridge.NewWorker(dbWorker, f.K8sClientset, *f.K8sConfig)
	if f.K8sExecutor != nil {
		w.SetExecutor(f.K8sExecutor)
	}
	w.SetVolumeRepo(f.DB.VolumeRepo)
	if f.K8sArtifactLocator != nil {
		w.SetArtifactLocator(f.K8sArtifactLocator)
	}
	return w
}

func (f DefaultFactory) newNativeWorker(dbWorker db.Worker) *native.Worker {
	return native.NewWorker(dbWorker, *f.NativeConfig, f.DB.VolumeRepo, f.Compression)
}

func (f DefaultFactory) newRemoteNativeWorker(dbWorker db.Worker, client agentpb.NativeAgentClient) *remote.Worker {
	return remote.NewWorker(dbWorker, client, f.DB.VolumeRepo, f.Compression)
}
