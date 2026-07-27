package credentials_test

import (
	"context"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/credentials/credentialstest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newSyncerFixture(withCred bool) (*credentials.PlatformSecretSyncer, *credentialstest.MemoryBackend, *fake.Clientset) {
	backend := credentialstest.NewMemoryBackend()
	backend.AddUser(credentials.PlatformUserSub, 99, "platform")
	if withCred {
		_ = backend.Put(99, "platform", credentials.KindAnthropicOAuth, "sk-platform", time.Now().Add(time.Hour))
	}
	clientset := fake.NewSimpleClientset()
	syncer := credentials.NewPlatformSecretSyncer(
		lagertest.NewTestLogger("syncer"), backend, clientset, "concourse-workers",
	)
	return syncer, backend, clientset
}

func TestSyncerCreatesPlatformSecret(t *testing.T) {
	syncer, _, clientset := newSyncerFixture(true)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret, err := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.StringData["anthropic-token"] != "sk-platform" {
		t.Fatalf("token: %q", secret.StringData["anthropic-token"])
	}
	if secret.StringData[credentials.SecretKeyModelTokenKind] != credentials.KindAnthropicOAuth {
		t.Fatalf("kind: %q", secret.StringData[credentials.SecretKeyModelTokenKind])
	}
}

// The runner picks the claude CLI credential env var from the "kind" key, so a
// vault row that switched from OAuth to a raw API key must rewrite it — a
// token-only comparison would leave the pod exporting the wrong variable.
func TestSyncerRefreshesChangedKind(t *testing.T) {
	backend := credentialstest.NewMemoryBackend()
	backend.AddUser(credentials.PlatformUserSub, 99, "platform")
	_ = backend.Put(99, "platform", credentials.KindAnthropicOAuth, "sk-same", time.Now().Add(time.Hour))
	clientset := fake.NewSimpleClientset()
	syncer := credentials.NewPlatformSecretSyncer(
		lagertest.NewTestLogger("syncer"), backend, clientset, "concourse-workers",
	)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := backend.Delete(99, credentials.KindAnthropicOAuth); err != nil {
		t.Fatal(err)
	}
	_ = backend.Put(99, "platform", credentials.KindAnthropicAPIKey, "sk-same", time.Now().Add(time.Hour))
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	secret, _ := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{})
	if secret.StringData[credentials.SecretKeyModelTokenKind] != credentials.KindAnthropicAPIKey {
		t.Fatalf("kind not refreshed: %q", secret.StringData[credentials.SecretKeyModelTokenKind])
	}
}

func TestSyncerRefreshesChangedToken(t *testing.T) {
	syncer, backend, clientset := newSyncerFixture(true)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = backend.Put(99, "platform", credentials.KindAnthropicOAuth, "sk-rotated", time.Now().Add(time.Hour))
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret, _ := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{})
	if secret.StringData["anthropic-token"] != "sk-rotated" {
		t.Fatalf("token not refreshed: %q", secret.StringData["anthropic-token"])
	}
}

func TestSyncerNoopsWithoutPlatformCredential(t *testing.T) {
	syncer, _, clientset := newSyncerFixture(false)
	if err := syncer.Run(context.Background()); err != nil {
		t.Fatalf("missing credential must not error the component: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("secret must not exist when the vault has no platform credential")
	}
}

func TestSyncerDeletesSecretWhenCredentialUnvaulted(t *testing.T) {
	// Seed the platform secret as if a prior sync (with a vaulted credential)
	// had created it, then unvault the credential. Bidirectional sync (§8.2)
	// requires the syncer to DELETE the now-stale secret so no pod can mount a
	// revoked token.
	syncer, _, clientset := newSyncerFixture(false)
	seed := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentials.PlatformSecretName,
			Namespace: "concourse-workers",
			Labels:    map[string]string{"concourse/agent-platform-credential": "true"},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"anthropic-token": "sk-stale"},
	}
	if _, err := clientset.CoreV1().Secrets("concourse-workers").
		Create(context.Background(), seed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := syncer.Run(context.Background()); err != nil {
		t.Fatalf("unvaulted credential must not error the component: %v", err)
	}

	if _, err := clientset.CoreV1().Secrets("concourse-workers").
		Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("stale platform secret must be deleted after the credential is unvaulted, got err=%v", err)
	}
}
