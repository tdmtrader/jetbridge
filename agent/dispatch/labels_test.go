package dispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestK8sRunSecretLabelerPatchesTicketLabel(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "agent-run-555", Namespace: "concourse",
			Labels: map[string]string{"concourse/agent-run": "555"},
		},
	})
	l := dispatch.NewK8sRunSecretLabeler(client, "concourse")
	if err := l.Label(context.Background(), 555, 42); err != nil {
		t.Fatalf("Label: %v", err)
	}
	got, err := client.CoreV1().Secrets("concourse").Get(context.Background(), "agent-run-555", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Labels["concourse/ticket"] != "42" {
		t.Errorf("want concourse/ticket=42, labels=%v", got.Labels)
	}
	if got.Labels["concourse/agent-run"] != "555" {
		t.Errorf("existing labels must survive the merge patch, labels=%v", got.Labels)
	}
}

func TestK8sRunSecretLabelerMissingSecretErrors(t *testing.T) {
	l := dispatch.NewK8sRunSecretLabeler(fake.NewSimpleClientset(), "concourse")
	if err := l.Label(context.Background(), 999, 42); err == nil {
		t.Fatal("labeling a missing secret must error (caller logs, never fatal)")
	}
}

// erroringLabeler always fails, standing in for a cluster that refuses
// the patch.
type erroringLabeler struct{ calls int }

func (l *erroringLabeler) Label(ctx context.Context, runID, ticketID int) error {
	l.calls++
	return errors.New("patch refused")
}

func TestDispatchOneLabelerFailureDoesNotFailDispatch(t *testing.T) {
	deps, store, _, _ := dispatchDeps(t)
	labeler := &erroringLabeler{}
	deps.SecretLabels = labeler
	id := queuedTicket(t, store, "smoke")

	// Best-effort by contract (§2.8.2): a labeling failure is logged,
	// never fatal — GC keys off concourse/agent-run alone.
	res, err := dispatch.DispatchOne(context.Background(), deps, id, "admin")
	if err != nil {
		t.Fatalf("DispatchOne must succeed despite a labeler failure: %v", err)
	}
	if labeler.calls != 1 {
		t.Errorf("Label calls = %d, want 1", labeler.calls)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning || got.PipelineRunID == nil || *got.PipelineRunID != res.RunID {
		t.Errorf("ticket after dispatch = %+v", got)
	}
}
