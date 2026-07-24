package credentials

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// §8.2 secret naming and keys — the injection contract every consumer
// (dispatch, gateway, agent-step exec) reads.
const (
	SecretKeyAnthropicToken = "anthropic-token"
	RunLabel                = "concourse/agent-run"

	// PlatformSecretName is the long-lived platform credential secret
	// (§8.2/§1.13), maintained by PlatformSecretSyncer — never per-run.
	PlatformSecretName = "agent-platform-credential"
)

// RunSecretName returns the §8.2 per-run secret name.
func RunSecretName(runID int) string {
	return fmt.Sprintf("agent-run-%d", runID)
}

// K8sSecretAttacher implements SecretAttacher against a worker namespace.
type K8sSecretAttacher struct {
	client    kubernetes.Interface
	namespace string
}

func NewK8sSecretAttacher(client kubernetes.Interface, namespace string) *K8sSecretAttacher {
	return &K8sSecretAttacher{client: client, namespace: namespace}
}

func (a *K8sSecretAttacher) Attach(ctx context.Context, runID int, cred *Credential) (string, error) {
	if cred == nil || cred.Token == "" {
		return "", fmt.Errorf("attach run %d: credential with a decrypted token is required", runID)
	}
	name := RunSecretName(runID)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: a.namespace,
			Labels: map[string]string{
				RunLabel: strconv.Itoa(runID),
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			SecretKeyAnthropicToken: cred.Token,
		},
	}

	secrets := a.client.CoreV1().Secrets(a.namespace)
	_, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Reattachment replaces the full desired shape. Fetching before each
		// update supplies the API-server resource version and lets concurrent
		// reattachments retry safely.
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}

			desired := secret.DeepCopy()
			desired.ResourceVersion = existing.ResourceVersion
			_, err = secrets.Update(ctx, desired, metav1.UpdateOptions{})
			return err
		})
	}
	if err != nil {
		return "", fmt.Errorf("attach run %d: %w", runID, err)
	}
	return name, nil
}

func (a *K8sSecretAttacher) Cleanup(ctx context.Context, runID int) error {
	err := a.client.CoreV1().Secrets(a.namespace).Delete(ctx, RunSecretName(runID), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

var _ SecretAttacher = (*K8sSecretAttacher)(nil)
