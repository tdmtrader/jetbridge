package credentials_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type fakeRunChecker struct {
	active map[int]bool
	failOn map[int]bool
	calls  []int
}

func (f *fakeRunChecker) RunActive(runID int) (bool, error) {
	f.calls = append(f.calls, runID)
	if f.failOn[runID] {
		return false, fmt.Errorf("checker exploded for run %d", runID)
	}
	return f.active[runID], nil
}

type fakeRevoker struct {
	names []string
	err   error
}

func (f *fakeRevoker) RevokeByName(name string) error {
	f.names = append(f.names, name)
	return f.err
}

func seedRunSecret(t *testing.T, clientset *fake.Clientset, ns string, runID int, age time.Duration) {
	t.Helper()
	_, err := clientset.CoreV1().Secrets(ns).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              credentials.RunSecretName(runID),
			Namespace:         ns,
			Labels:            map[string]string{credentials.RunLabel: strconv.Itoa(runID)},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			credentials.SecretKeyAnthropicToken: "tok",
			credentials.SecretKeyPrincipalToken: "cap1.1.x",
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReaperDeletesFinishedRunSecretAndRevokesPrincipal(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 42, time.Hour) // finished
	seedRunSecret(t, clientset, "ns", 43, time.Hour) // still running
	checker := &fakeRunChecker{active: map[int]bool{43: true}}
	revoker := &fakeRevoker{}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, revoker)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-42", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("finished run's secret must be reaped, got err=%v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-43", metav1.GetOptions{}); err != nil {
		t.Fatalf("active run's secret must survive: %v", err)
	}
	if len(revoker.names) != 1 || revoker.names[0] != "agent-run-42" {
		t.Fatalf("revoked principals: %v", revoker.names)
	}
}

func TestReaperDeletesSecretWhoseRunRowIsAbsent(t *testing.T) {
	// The F22 crash window: Attach succeeded but the run row was never
	// created (or was deleted). Absent = inactive = reap.
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 7, time.Hour)
	reaper := credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-7", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("orphaned secret must be reaped, got err=%v", err)
	}
}

func TestReaperGraceWindowProtectsFreshSecrets(t *testing.T) {
	// Protects the dispatch CreateRun→Attach ordering: a just-created
	// secret is never reaped even when its run is not (yet) visible.
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 9, 0)
	checker := &fakeRunChecker{}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-9", metav1.GetOptions{}); err != nil {
		t.Fatalf("fresh secret must survive the grace window: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Fatalf("fresh secret must not even be checked: %v", checker.calls)
	}
}

func TestReaperIgnoresSecretsWithoutRunLabel(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              credentials.PlatformSecretName,
			Namespace:         "ns",
			Labels:            map[string]string{"concourse/agent-platform-credential": "true"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	})
	checker := &fakeRunChecker{}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), credentials.PlatformSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("platform secret must never be touched: %v", err)
	}
	if len(checker.calls) != 0 {
		t.Fatalf("unlabeled secrets must not be checked: %v", checker.calls)
	}
}

func TestReaperRevokeIsBestEffort(t *testing.T) {
	// nil revoker (wave-1 wiring, before agent-identity binds it)
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 11, time.Hour)
	reaper := credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, nil)
	if err := reaper.Run(context.Background()); err != nil {
		t.Fatalf("nil revoker must be tolerated: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-11", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("secret must be reaped with nil revoker, got err=%v", err)
	}

	// failing revoker: revocation is attempted, its error logged, never fatal
	seedRunSecret(t, clientset, "ns", 12, time.Hour)
	revoker := &fakeRevoker{err: fmt.Errorf("store down")}
	reaper = credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, revoker)
	if err := reaper.Run(context.Background()); err != nil {
		t.Fatalf("revoker error must not fail the sweep: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-12", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("secret must be reaped despite revoke failure, got err=%v", err)
	}
	if len(revoker.names) != 1 || revoker.names[0] != "agent-run-12" {
		t.Fatalf("revocation must be attempted: %v", revoker.names)
	}
}

func TestReaperSkipsUnparseableRunLabel(t *testing.T) {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "agent-run-mystery",
			Namespace:         "ns",
			Labels:            map[string]string{credentials.RunLabel: "not-a-number"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
	})
	reaper := credentials.NewRunSecretReaper(
		lagertest.NewTestLogger("reaper"), clientset, "ns", &fakeRunChecker{}, nil)

	if err := reaper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-mystery", metav1.GetOptions{}); err != nil {
		t.Fatalf("unparseable label must be skipped (logged), not deleted: %v", err)
	}
}

func TestReaperContinuesSweepWhenCheckerErrors(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	seedRunSecret(t, clientset, "ns", 21, time.Hour) // checker errors
	seedRunSecret(t, clientset, "ns", 22, time.Hour) // reapable
	checker := &fakeRunChecker{failOn: map[int]bool{21: true}}
	reaper := credentials.NewRunSecretReaper(lagertest.NewTestLogger("reaper"), clientset, "ns", checker, nil)

	err := reaper.Run(context.Background())
	if err == nil {
		t.Fatal("sweep must surface the checker error (component retries next interval)")
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-21", metav1.GetOptions{}); err != nil {
		t.Fatalf("run 21's secret must be kept on checker error (fail closed): %v", err)
	}
	if _, err := clientset.CoreV1().Secrets("ns").Get(context.Background(), "agent-run-22", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("one bad run must not block the rest of the sweep, got err=%v", err)
	}
}
