//go:build live
// +build live

package jetbridge_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	credsk8s "github.com/concourse/concourse/atc/creds/kubernetes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestLiveCredentialManagerResolvesVars resolves ((vars)) through the
// Kubernetes credential manager against real Secrets, laid out under the
// namespace prefix the deployed web was started with.
//
// Unit tests run the lookup against a fake clientset, which cannot say whether
// the deployed prefix names a namespace that exists, or whether a Secret shaped
// the way the docs tell operators to shape it (a "value" key, pipeline-scoped
// as "<pipeline>.<var>") resolves. This does, for team main.
//
// What it cannot prove is the web's own ServiceAccount being allowed to read
// those Secrets: the lookup runs with this suite's credentials, and the suite
// has no permission to review another account's access.
func TestLiveCredentialManagerResolvesVars(t *testing.T) {
	clientset, _ := kubeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	prefix := liveFeatures["web.credentialManager"](deployed)
	if prefix == "off" {
		t.Fatal("the deployed web has no credential manager; the manifest should say so")
	}
	namespace := prefix + "main"
	if _, err := clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err != nil {
		t.Fatalf("team main's secret namespace %s: %v", namespace, err)
	}

	stamp := time.Now().UnixNano()
	name := fmt.Sprintf("live-credmgr-%d", stamp)
	pipeline := "live-credmgr-pipeline"
	put := func(secretName, value string) {
		t.Helper()
		_, err := clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName},
			Data:       map[string][]byte{"value": []byte(value)},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("creating secret %s/%s: %v", namespace, secretName, err)
		}
		t.Cleanup(func() {
			_ = clientset.CoreV1().Secrets(namespace).Delete(context.Background(), secretName, metav1.DeleteOptions{})
		})
	}
	teamValue := fmt.Sprintf("team-%d", stamp)
	pipelineValue := fmt.Sprintf("pipeline-%d", stamp)
	put(name, teamValue)
	put(pipeline+"."+name, pipelineValue)

	secrets := credsk8s.NewKubernetesFactory(lagertest.NewTestLogger("live-credmgr"), clientset, prefix).NewSecrets()
	resolve := func(params creds.SecretLookupParams) string {
		t.Helper()
		source, err := creds.NewSource(creds.NewVariables(secrets, params, false), atc.Source{"token": "((" + name + "))"}).Evaluate()
		if err != nil {
			t.Fatalf("evaluating ((%s)) for %+v: %v", name, params, err)
		}
		return fmt.Sprint(source["token"])
	}

	if got := resolve(creds.SecretLookupParams{Team: "main"}); got != teamValue {
		t.Errorf("team-scoped ((%s)) = %q, want %q", name, got, teamValue)
	}
	if got := resolve(creds.SecretLookupParams{Team: "main", Pipeline: pipeline}); got != pipelineValue {
		t.Errorf("pipeline-scoped ((%s)) = %q, want the <pipeline>.<var> secret's %q", name, got, pipelineValue)
	}
}
