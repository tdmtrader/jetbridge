package jetbridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The launcher retries a 5xx and gives up on anything else, so a transient
// API failure while verifying its token must not read as a refusal: an
// ordinary step's pod would exit without running its command. Only an
// actual authentication refusal is a 401.
func TestStepEnvelopeHandlerRetriesTransientAPIFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(*fake.Clientset)
		want  int
	}{
		{"the TokenReview fails", func(clientset *fake.Clientset) {
			clientset.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, errors.New("connection refused")
			})
		}, http.StatusServiceUnavailable},
		{"reading the pod fails", func(clientset *fake.Clientset) {
			clientset.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("etcd is sad")
			})
		}, http.StatusServiceUnavailable},
		{"the token's pod is gone", func(clientset *fake.Clientset) {
			clientset.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "job-a-xyz")
			})
		}, http.StatusUnauthorized},
		{"no fault", func(*fake.Clientset) {}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset(stepJobPod("job-a-xyz", "job-a"))
			tokenReviewDouble(clientset, map[string]string{"tok-a": "job-a-xyz"})
			tc.fault(clientset)
			envelopes := NewStepEnvelopes()
			envelopes.Register("job-a", StepEnvelope{Stdin: []byte(`{}`)})
			handler := &StepEnvelopeHandler{Logger: lagertest.NewTestLogger("envelope"), Clientset: clientset, Namespace: "test-ns", Envelopes: envelopes}
			request := httptest.NewRequest(http.MethodPost, StepEnvelopePath, strings.NewReader("{}"))
			request.Header.Set("Authorization", "Bearer tok-a")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}
