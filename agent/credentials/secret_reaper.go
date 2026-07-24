package credentials

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// RunChecker reports whether an agent run is still active. Narrow seam
// (F22): the production implementation is atc/db.NewAgentRunChecker over
// the pipeline_runs table (contracts §1.5, owned by pipeline-runs); an
// absent row — or an absent table, before that wave-mate merges — means
// the run cannot be active.
type RunChecker interface {
	RunActive(runID int) (bool, error)
}

// RunSecretReapGrace protects dispatch's CreateRun→Attach ordering from
// sweep races: secrets younger than this are never considered.
const RunSecretReapGrace = 5 * time.Minute

// RunSecretReaper is §8.2's "reaper safety-net GC" (final-review F22):
// dispatch's in-process Cleanup on abort/error paths is the first line of
// defense, this polling component is the guarantee. It lists worker-
// namespace secrets by the concourse/agent-run label, deletes any whose
// run is complete or absent.
type RunSecretReaper struct {
	logger    lager.Logger
	client    kubernetes.Interface
	namespace string
	runs      RunChecker
}

func NewRunSecretReaper(
	logger lager.Logger,
	client kubernetes.Interface,
	namespace string,
	runs RunChecker,
) *RunSecretReaper {
	return &RunSecretReaper{
		logger:    logger,
		client:    client,
		namespace: namespace,
		runs:      runs,
	}
}

// Run implements component.Runnable. One failing secret does not block
// the rest of the sweep; the first error is returned so the component
// retries on its next interval.
func (r *RunSecretReaper) Run(ctx context.Context) error {
	secrets, err := r.client.CoreV1().Secrets(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: RunLabel,
	})
	if err != nil {
		return fmt.Errorf("listing run secrets: %w", err)
	}

	var sweepErr error
	for i := range secrets.Items {
		secret := &secrets.Items[i]

		runID, err := strconv.Atoi(secret.Labels[RunLabel])
		if err != nil {
			r.logger.Info("skipping-unparseable-run-label", lager.Data{
				"secret": secret.Name, "label": secret.Labels[RunLabel],
			})
			continue
		}
		if time.Since(secret.CreationTimestamp.Time) < RunSecretReapGrace {
			continue // Attach may precede the run row becoming visible
		}

		active, err := r.runs.RunActive(runID)
		if err != nil {
			// Fail closed: keep the secret, surface the error, keep sweeping.
			r.logger.Error("failed-to-check-run", err, lager.Data{"run_id": runID})
			if sweepErr == nil {
				sweepErr = err
			}
			continue
		}
		if active {
			continue
		}

		err = r.client.CoreV1().Secrets(r.namespace).Delete(ctx, secret.Name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			r.logger.Error("failed-to-delete-run-secret", err, lager.Data{"secret": secret.Name})
			if sweepErr == nil {
				sweepErr = err
			}
			continue
		}
		r.logger.Info("reaped-run-secret", lager.Data{"secret": secret.Name, "run_id": runID})
	}
	return sweepErr
}
