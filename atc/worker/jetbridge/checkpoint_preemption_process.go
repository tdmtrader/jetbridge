package jetbridge

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

var _ runtime.CheckpointPreemptionProcess = (*execProcess)(nil)

// WaitForCheckpointPreemption binds the advisory notice to the exact pod's
// scheduled node and its already-authenticated DaemonSet client. It neither
// manufactures a provider safe boundary nor retries ambiguous transport.
func (p *execProcess) WaitForCheckpointPreemption(ctx context.Context) (time.Time, error) {
	if ctx == nil || p == nil || p.container == nil || p.clientset == nil ||
		p.container.metadata.Type != db.ContainerTypeAgent ||
		!p.container.containerSpec.CheckpointCapture ||
		!p.container.containerSpec.Hermetic {
		return time.Time{}, errors.New("checkpoint preemption source is not configured")
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	pod, err := p.exactCheckpointPod(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if _, _, err := checkpointPodIdentity(pod, p.container.handle, p.podName); err != nil {
		return time.Time{}, err
	}
	backend, ok := p.container.storageBackend.(*DaemonSetBackend)
	if !ok || backend == nil || backend.daemonClient == nil {
		return time.Time{}, errors.New("checkpoint preemption daemon client is unavailable")
	}
	source, err := NewCheckpointPreemptionNoticeSource(backend.daemonClient, pod.Spec.NodeName)
	if err != nil {
		return time.Time{}, err
	}
	return source.WaitForNodePreemption(ctx)
}
