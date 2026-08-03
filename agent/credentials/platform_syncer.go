package credentials

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PlatformSecretSyncer keeps the long-lived agent-platform-credential
// secret in sync with the platform user's vault row. It runs as a
// polling RunnableComponent (never notify-only, per the fork's
// notifications lesson), which also covers encryption-key rotation: the
// vault row decrypts with the current strategy on every pass.
type PlatformSecretSyncer struct {
	logger    lager.Logger
	backend   Backend
	client    kubernetes.Interface
	namespace string
}

func NewPlatformSecretSyncer(
	logger lager.Logger,
	backend Backend,
	client kubernetes.Interface,
	namespace string,
) *PlatformSecretSyncer {
	return &PlatformSecretSyncer{
		logger:    logger,
		backend:   backend,
		client:    client,
		namespace: namespace,
	}
}

// Run implements component.Runnable.
func (s *PlatformSecretSyncer) Run(ctx context.Context) error {
	userID, _, found, err := s.backend.UserBySub(PlatformUserSub)
	if err != nil {
		return fmt.Errorf("resolving platform user: %w", err)
	}
	if !found {
		s.logger.Info("platform-user-missing", lager.Data{"sub": PlatformUserSub})
		return nil
	}

	cred, found, err := ResolveUsableAnthropicCredential(s.backend, userID, time.Now())
	if err != nil {
		return fmt.Errorf("resolving platform credential: %w", err)
	}
	if !found {
		// Not an error: the platform credential is provisioned by an admin
		// running `fly agent auth --platform` (PutRequest.User = "platform").
		// Bidirectional sync: if the credential was unvaulted (admin ran
		// `fly agent auth --platform --delete`), the stale K8s secret MUST be
		// removed so no pod can mount a revoked token. NotFound is tolerated.
		err := s.client.CoreV1().Secrets(s.namespace).Delete(ctx, PlatformSecretName, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			s.logger.Info("platform-credential-not-vaulted")
			return nil
		}
		if err != nil {
			s.logger.Error("failed-to-delete-platform-secret", err)
			return err
		}
		s.logger.Info("platform-secret-deleted")
		return nil
	}

	// "kind" travels with the token: the agent pod's runner needs to know
	// whether it holds an OAuth token or a raw API key to pick the right
	// claude CLI credential env var, and only the vault row knows.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PlatformSecretName,
			Namespace: s.namespace,
			Labels:    map[string]string{"concourse/agent-platform-credential": "true"},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			SecretKeyAnthropicToken: cred.Token,
			SecretKeyModelTokenKind: cred.Kind,
		},
	}

	existing, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, PlatformSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := s.client.CoreV1().Secrets(s.namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			s.logger.Error("failed-to-create-platform-secret", err)
			return err
		}
		s.logger.Info("platform-secret-created")
		return nil
	}
	if err != nil {
		s.logger.Error("failed-to-get-platform-secret", err)
		return err
	}

	if string(existing.Data[SecretKeyAnthropicToken]) == cred.Token &&
		string(existing.Data[SecretKeyModelTokenKind]) == cred.Kind &&
		existing.StringData[SecretKeyAnthropicToken] == "" {
		return nil // already in sync (Data is the server-side representation)
	}
	if existing.StringData[SecretKeyAnthropicToken] == cred.Token &&
		existing.StringData[SecretKeyModelTokenKind] == cred.Kind {
		return nil // fake-clientset path: StringData not converted
	}
	updated := existing.DeepCopy()
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	updated.Labels["concourse/agent-platform-credential"] = "true"
	updated.Type = secret.Type
	updated.StringData = secret.StringData
	if _, err := s.client.CoreV1().Secrets(s.namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		s.logger.Error("failed-to-update-platform-secret", err)
		return err
	}
	s.logger.Info("platform-secret-updated")
	return nil
}
