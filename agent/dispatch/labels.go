package dispatch

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/credentials"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// RunSecretLabeler adds the operator-filtering concourse/ticket label to a
// run's credential secret (§2.8.2). Best-effort by contract: the caller
// logs failures and never fails a dispatched run over one — the
// secret-reaper's GC keys off concourse/agent-run alone.
type RunSecretLabeler interface {
	Label(ctx context.Context, runID, ticketID int) error
}

func NewK8sRunSecretLabeler(client kubernetes.Interface, namespace string) RunSecretLabeler {
	return &k8sRunSecretLabeler{client: client, namespace: namespace}
}

type k8sRunSecretLabeler struct {
	client    kubernetes.Interface
	namespace string
}

func (l *k8sRunSecretLabeler) Label(ctx context.Context, runID, ticketID int) error {
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{"concourse/ticket":"%d"}}}`, ticketID))
	_, err := l.client.CoreV1().Secrets(l.namespace).Patch(
		ctx, credentials.RunSecretName(runID), types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}
