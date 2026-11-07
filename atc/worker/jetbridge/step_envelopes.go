package jetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// StepEnvelope is what a step pod's launcher fetches before it runs the
// command: the bytes the command reads on stdin. For a check, get or put
// that is the resource request, with its interpolated source and params. It
// lives in the web's memory and in the pod's, never in a Kubernetes object.
type StepEnvelope struct {
	Stdin []byte
}

// StepEnvelopes holds the envelopes of the step Jobs this web has started,
// by Job name. Each is handed out once, to the one pod the Job owns. A web
// restart loses them; a step resumed by a new web registers its envelope
// again when its Run is replayed, and a pod that asks before that is told to
// retry.
type StepEnvelopes struct {
	mu        sync.Mutex
	envelopes map[string]*registeredEnvelope
}

type registeredEnvelope struct {
	envelope StepEnvelope
	takenBy  string
}

func NewStepEnvelopes() *StepEnvelopes {
	return &StepEnvelopes{envelopes: map[string]*registeredEnvelope{}}
}

// Register records a step Job's envelope, replacing any earlier one.
func (s *StepEnvelopes) Register(jobName string, envelope StepEnvelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envelopes[jobName] = &registeredEnvelope{envelope: envelope}
}

// Forget drops a step Job's envelope once the step is over.
func (s *StepEnvelopes) Forget(jobName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.envelopes, jobName)
}

var (
	errEnvelopeUnknown = errors.New("no step envelope registered for this job")
	errEnvelopeTaken   = errors.New("this job's step envelope was already delivered to another pod")
)

// Take hands a Job's envelope to the pod named by UID. The same pod may
// take it again (a launcher that lost the response retries); another pod of
// the same Job may not.
func (s *StepEnvelopes) Take(jobName, podUID string) (StepEnvelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	registered, ok := s.envelopes[jobName]
	if !ok {
		return StepEnvelope{}, errEnvelopeUnknown
	}
	if registered.takenBy != "" && registered.takenBy != podUID {
		return StepEnvelope{}, errEnvelopeTaken
	}
	registered.takenBy = podUID
	return registered.envelope, nil
}

// StepEnvelopeHandler serves POST <StepEnvelopeURL>. The caller is a step
// pod's launcher presenting the pod-bound projected ServiceAccount token the
// Job's pod template mounts. The token is verified with a TokenReview for
// the StepTokenAudience; the pod it names is read and must be the one pod
// of a step Job this web registered an envelope for.
type StepEnvelopeHandler struct {
	Logger    lager.Logger
	Clientset kubernetes.Interface
	Namespace string
	Envelopes *StepEnvelopes
}

// stepEnvelopeResponse is the body the launcher reads.
type stepEnvelopeResponse struct {
	Stdin []byte `json:"stdin,omitempty"`
}

// StepEnvelopePath is the path the web serves step envelopes on. The
// chart's --kubernetes-step-envelope-url names it on the web's Service.
const StepEnvelopePath = "/runtime/v1/step-envelope"

const (
	podNameExtraKey = "authentication.kubernetes.io/pod-name"
	podUIDExtraKey  = "authentication.kubernetes.io/pod-uid"
)

func (h *StepEnvelopeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	pod, err := h.reviewToken(ctx, strings.TrimSpace(token))
	if err != nil {
		h.Logger.Info("step-envelope-refused", lager.Data{"reason": err.Error()})
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	job := owningStepJob(pod.ObjectMeta)
	if job == "" || pod.Labels[runtimeLabelKey] != StepRuntimeJob {
		h.Logger.Info("step-envelope-refused", lager.Data{"pod": pod.Name, "reason": "not a step job's pod"})
		w.WriteHeader(http.StatusForbidden)
		return
	}
	envelope, err := h.Envelopes.Take(job, string(pod.UID))
	switch {
	case errors.Is(err, errEnvelopeUnknown):
		// The web that registered it is gone, or the step has not been
		// resumed yet. The launcher retries until its own deadline.
		h.Logger.Info("step-envelope-not-registered", lager.Data{"job": job, "pod": pod.Name})
		http.Error(w, "step envelope not registered yet", http.StatusServiceUnavailable)
		return
	case err != nil:
		h.Logger.Error("step-envelope-refused", err, lager.Data{"job": job, "pod": pod.Name})
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stepEnvelopeResponse{Stdin: envelope.Stdin})
}

// reviewToken verifies the launcher's token and returns the pod it is bound
// to, read from the API so its labels and owner are the cluster's word.
func (h *StepEnvelopeHandler) reviewToken(ctx context.Context, token string) (*corev1.Pod, error) {
	review, err := h.Clientset.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{StepTokenAudience}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token review: %w", err)
	}
	if !review.Status.Authenticated {
		return nil, fmt.Errorf("token not authenticated: %s", review.Status.Error)
	}
	audienceOK := false
	for _, audience := range review.Status.Audiences {
		if audience == StepTokenAudience {
			audienceOK = true
		}
	}
	if !audienceOK {
		return nil, errors.New("token audience is not the step audience")
	}
	podName := firstExtra(review.Status.User.Extra, podNameExtraKey)
	podUID := firstExtra(review.Status.User.Extra, podUIDExtraKey)
	if podName == "" || podUID == "" {
		return nil, errors.New("token is not bound to a pod")
	}
	pod, err := h.Clientset.CoreV1().Pods(h.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading the token's pod: %w", err)
	}
	if string(pod.UID) != podUID {
		return nil, errors.New("the token's pod UID does not match the pod")
	}
	if pod.DeletionTimestamp != nil {
		return nil, errors.New("the token's pod is terminating")
	}
	return pod, nil
}

func firstExtra(extra map[string]authenticationv1.ExtraValue, key string) string {
	values := extra[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
