package k8sruntime

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Compile-time check that Worker satisfies runtime.Worker.
var _ runtime.Worker = (*Worker)(nil)

// Worker implements runtime.Worker using Kubernetes Pods as the execution
// backend instead of Garden containers.
type Worker struct {
	dbWorker  db.Worker
	clientset kubernetes.Interface
	config    Config
	executor  PodExecutor
}

// NewWorker creates a new Worker backed by the given Kubernetes clientset.
func NewWorker(dbWorker db.Worker, clientset kubernetes.Interface, config Config) *Worker {
	return &Worker{
		dbWorker:  dbWorker,
		clientset: clientset,
		config:    config,
	}
}

// SetExecutor sets the PodExecutor used for exec-mode I/O in containers.
// When set, containers that receive ProcessIO with Stdin will use the
// exec API to pipe stdin/stdout/stderr instead of baking the command
// into the Pod spec.
func (w *Worker) SetExecutor(executor PodExecutor) {
	w.executor = executor
}

func (w *Worker) Name() string {
	return w.dbWorker.Name()
}

func (w *Worker) DBWorker() db.Worker {
	return w.dbWorker
}

func (w *Worker) FindOrCreateContainer(
	ctx context.Context,
	owner db.ContainerOwner,
	metadata db.ContainerMetadata,
	containerSpec runtime.ContainerSpec,
	delegate runtime.BuildStepDelegate,
) (runtime.Container, []runtime.VolumeMount, error) {
	creatingContainer, createdContainer, err := w.dbWorker.FindContainer(owner)
	if err != nil {
		return nil, nil, fmt.Errorf("find container in db: %w", err)
	}

	var containerHandle string

	if creatingContainer != nil {
		containerHandle = creatingContainer.Handle()
	} else if createdContainer != nil {
		containerHandle = createdContainer.Handle()
	} else {
		creatingContainer, err = w.dbWorker.CreateContainer(owner, metadata)
		if err != nil {
			return nil, nil, fmt.Errorf("create container in db: %w", err)
		}
		containerHandle = creatingContainer.Handle()
	}

	// If we already have a created container in the DB, return it directly.
	// The Pod may or may not exist yet (it gets created in Container.Run).
	if createdContainer != nil {
		return newContainer(containerHandle, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor), nil, nil
	}

	// Transition the creating container to created state in the DB.
	// Pod creation is deferred to Container.Run() since the command isn't
	// known until then.
	createdContainer, err = creatingContainer.Created()
	if err != nil {
		return nil, nil, fmt.Errorf("mark container as created: %w", err)
	}

	return newContainer(containerHandle, containerSpec, createdContainer, w.clientset, w.config, w.Name(), w.executor), nil, nil
}

func (w *Worker) CreateVolumeForArtifact(ctx context.Context, teamID int) (runtime.Volume, db.WorkerArtifact, error) {
	return nil, nil, fmt.Errorf("k8sruntime: CreateVolumeForArtifact not yet implemented")
}

func (w *Worker) LookupContainer(ctx context.Context, handle string) (runtime.Container, bool, error) {
	_, err := w.clientset.CoreV1().Pods(w.config.Namespace).Get(ctx, handle, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lookup pod %q: %w", handle, err)
	}
	return newContainer(handle, runtime.ContainerSpec{}, nil, w.clientset, w.config, w.Name(), w.executor), true, nil
}

func (w *Worker) LookupVolume(ctx context.Context, handle string) (runtime.Volume, bool, error) {
	return nil, false, nil
}

func markContainerAsFailed(container db.CreatingContainer) {
	if container != nil {
		_, _ = container.Failed()
	}
}
