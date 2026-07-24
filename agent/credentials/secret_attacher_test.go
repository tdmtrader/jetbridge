package credentials_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAttachCreatesLabeledSecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "concourse-workers")

	cred := &credentials.Credential{UserID: 7, UserName: "alice", Kind: "anthropic_oauth", Token: "sk-tok"}
	name, err := attacher.Attach(context.Background(), 42, cred)
	if err != nil {
		t.Fatal(err)
	}
	if name != "agent-run-42" {
		t.Fatalf("secret name: %q", name)
	}

	secret, err := clientset.CoreV1().Secrets("concourse-workers").Get(context.Background(), "agent-run-42", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.StringData) != 1 || secret.StringData["anthropic-token"] != "sk-tok" {
		t.Fatalf("secret data: %v", secret.StringData)
	}
	if secret.Labels["concourse/agent-run"] != "42" {
		t.Fatalf("labels: %v", secret.Labels)
	}
}

func TestAttachIsIdempotentPerRun(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	cred := &credentials.Credential{Token: "tok-1"}

	if _, err := attacher.Attach(context.Background(), 7, cred); err != nil {
		t.Fatal(err)
	}
	cred.Token = "tok-2"
	name, err := attacher.Attach(context.Background(), 7, cred)
	if err != nil {
		t.Fatalf("second attach must update, not fail: %v", err)
	}
	if name != "agent-run-7" {
		t.Fatalf("name: %q", name)
	}
	secret, _ := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-7", metav1.GetOptions{})
	if secret.StringData["anthropic-token"] != "tok-2" {
		t.Fatalf("attach did not refresh token: %q", secret.StringData["anthropic-token"])
	}
	if len(secret.StringData) != 1 {
		t.Fatalf("secret data: %v", secret.StringData)
	}
}

func TestAttachReplacesExistingSecretUsingResourceVersionAndRetriesConflicts(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            credentials.RunSecretName(7),
			Namespace:       "ns",
			ResourceVersion: "1",
			Labels:          map[string]string{credentials.RunLabel: "7"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			credentials.SecretKeyAnthropicToken: []byte("stale-token"),
			"principal-token":                   []byte("retired-principal-token"),
		},
	})

	updates := 0
	clientset.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		secret := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		if len(secret.Data) != 0 {
			return true, nil, errors.New("update retained stored secret data")
		}
		if len(secret.StringData) != 1 || secret.StringData[credentials.SecretKeyAnthropicToken] != "fresh-token" {
			return true, nil, errors.New("update did not replace string data with the fresh model token")
		}
		if secret.ResourceVersion == "" {
			return true, nil, apierrors.NewInvalid(
				schema.GroupKind{Kind: "Secret"}, secret.Name,
				field.ErrorList{field.Required(field.NewPath("metadata", "resourceVersion"), "must be specified for an update")},
			)
		}
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, secret.Name, errors.New("concurrent reattachment"))
		}
		return false, nil, nil
	})

	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	name, err := attacher.Attach(context.Background(), 7, &credentials.Credential{Token: "fresh-token"})
	if err != nil {
		t.Fatalf("reattach existing secret: %v", err)
	}
	if name != credentials.RunSecretName(7) {
		t.Fatalf("secret name = %q", name)
	}
	if updates != 2 {
		t.Fatalf("update attempts = %d, want 2", updates)
	}

	secret, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), credentials.RunSecretName(7), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.Data) != 0 {
		t.Fatalf("secret data retained stale entries: %v", secret.Data)
	}
	if len(secret.StringData) != 1 || secret.StringData[credentials.SecretKeyAnthropicToken] != "fresh-token" {
		t.Fatalf("secret string data = %v", secret.StringData)
	}
}

func TestAttachValidatesInput(t *testing.T) {
	attacher := credentials.NewK8sSecretAttacher(fake.NewSimpleClientset(), "ns")
	if _, err := attacher.Attach(context.Background(), 1, nil); err == nil {
		t.Fatal("nil credential accepted")
	}
	if _, err := attacher.Attach(context.Background(), 1, &credentials.Credential{}); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestCleanupDeletesAndTolerangesMissing(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	cred := &credentials.Credential{Token: "tok"}

	if _, err := attacher.Attach(context.Background(), 5, cred); err != nil {
		t.Fatal(err)
	}
	if err := attacher.Cleanup(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-5", metav1.GetOptions{}); err == nil {
		t.Fatal("secret survived cleanup")
	}

	// abort/error paths call Cleanup unconditionally — not-found is fine
	if err := attacher.Cleanup(context.Background(), 5); err != nil {
		t.Fatalf("second cleanup must be a no-op: %v", err)
	}
}
