package jetbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// tokenReviewDouble answers TokenReviews the way the API server does for a
// projected token: the token string names the pod it is bound to.
func tokenReviewDouble(clientset *fake.Clientset, pods map[string]string) {
	clientset.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		podName, ok := pods[review.Spec.Token]
		if !ok || len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != StepTokenAudience {
			review.Status = authenticationv1.TokenReviewStatus{Authenticated: false, Error: "invalid"}
			return true, review, nil
		}
		review.Status = authenticationv1.TokenReviewStatus{
			Authenticated: true,
			Audiences:     []string{StepTokenAudience},
			User: authenticationv1.UserInfo{
				Username: "system:serviceaccount:test-ns:default",
				Extra: map[string]authenticationv1.ExtraValue{
					podNameExtraKey: {podName},
					podUIDExtraKey:  {"uid-" + podName},
				},
			},
		}
		return true, review, nil
	})
}

func stepJobPod(name, job string) *corev1.Pod {
	controller := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "test-ns", UID: types.UID("uid-" + name),
		Labels:          map[string]string{jobNameLabelKey: job, runtimeLabelKey: StepRuntimeJob},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job, Controller: &controller}},
	}}
}

func TestStepEnvelopeHandler(t *testing.T) {
	clientset := fake.NewSimpleClientset(stepJobPod("job-a-xyz", "job-a"), stepJobPod("job-a-second", "job-a"),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pause", Namespace: "test-ns", UID: "uid-pause"}})
	tokenReviewDouble(clientset, map[string]string{"tok-a": "job-a-xyz", "tok-second": "job-a-second", "tok-pause": "pause"})
	envelopes := NewStepEnvelopes()
	handler := &StepEnvelopeHandler{Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: "test-ns", Envelopes: envelopes}

	post := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, StepEnvelopePath, strings.NewReader("{}"))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	if got := post("").Code; got != http.StatusUnauthorized {
		t.Errorf("no token: %d", got)
	}
	if got := post("forged").Code; got != http.StatusUnauthorized {
		t.Errorf("a token the cluster rejects: %d", got)
	}
	if got := post("tok-pause").Code; got != http.StatusForbidden {
		t.Errorf("a pod no Job owns: %d", got)
	}
	if got := post("tok-a").Code; got != http.StatusServiceUnavailable {
		t.Errorf("before the envelope is registered: %d, want 503 so the launcher retries", got)
	}

	envelopes.Register("job-a", StepEnvelope{Stdin: []byte(`{"source":{"uri":"x"}}`)})
	recorder := post("tok-a")
	if recorder.Code != http.StatusOK {
		t.Fatalf("registered: %d %s", recorder.Code, recorder.Body.String())
	}
	var response stepEnvelopeResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if string(response.Stdin) != `{"source":{"uri":"x"}}` {
		t.Errorf("stdin = %q", response.Stdin)
	}
	if got := post("tok-a").Code; got != http.StatusOK {
		t.Errorf("the same pod asking again: %d", got)
	}
	if got := post("tok-second").Code; got != http.StatusConflict {
		t.Errorf("a second pod of the same Job: %d, want 409", got)
	}

	deleted, _ := clientset.CoreV1().Pods("test-ns").Get(context.Background(), "job-a-xyz", metav1.GetOptions{})
	now := metav1.Now()
	deleted.DeletionTimestamp = &now
	_, _ = clientset.CoreV1().Pods("test-ns").Update(context.Background(), deleted, metav1.UpdateOptions{})
	if got := post("tok-a").Code; got != http.StatusUnauthorized {
		t.Errorf("a terminating pod: %d", got)
	}
}
