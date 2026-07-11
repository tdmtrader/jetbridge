package credentials_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAttachCreatesLabeledSecret(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "concourse-workers")

	cred := &credentials.Credential{UserID: 7, UserName: "alice", Kind: "anthropic_oauth", Token: "sk-tok"}
	name, err := attacher.Attach(context.Background(), 42, cred, "cap1.9.secret")
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
	if secret.StringData["anthropic-token"] != "sk-tok" {
		t.Fatalf("anthropic-token: %q", secret.StringData["anthropic-token"])
	}
	if secret.StringData["principal-token"] != "cap1.9.secret" {
		t.Fatalf("principal-token: %q", secret.StringData["principal-token"])
	}
	if secret.Labels["concourse/agent-run"] != "42" {
		t.Fatalf("labels: %v", secret.Labels)
	}
}

func TestAttachIsIdempotentPerRun(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	cred := &credentials.Credential{Token: "tok-1"}

	if _, err := attacher.Attach(context.Background(), 7, cred, "p-1"); err != nil {
		t.Fatal(err)
	}
	cred.Token = "tok-2"
	name, err := attacher.Attach(context.Background(), 7, cred, "p-2")
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
}

func TestAttachValidatesInput(t *testing.T) {
	attacher := credentials.NewK8sSecretAttacher(fake.NewSimpleClientset(), "ns")
	if _, err := attacher.Attach(context.Background(), 1, nil, "p"); err == nil {
		t.Fatal("nil credential accepted")
	}
	if _, err := attacher.Attach(context.Background(), 1, &credentials.Credential{}, "p"); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestCleanupDeletesAndTolerangesMissing(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	attacher := credentials.NewK8sSecretAttacher(clientset, "ns")
	cred := &credentials.Credential{Token: "tok"}

	if _, err := attacher.Attach(context.Background(), 5, cred, "p"); err != nil {
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
