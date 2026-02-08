package jetbridge

import (
	"errors"
	"net"
	"net/url"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// TransientError wraps an error that represents a transient K8s API failure
// which should be retried at the step level. It implements runtime.RetryableError.
type TransientError struct {
	Cause error
}

func (e *TransientError) Error() string {
	return e.Cause.Error()
}

func (e *TransientError) Unwrap() error {
	return e.Cause
}

func (e *TransientError) IsRetryable() bool {
	return true
}

// wrapIfTransient wraps err as a TransientError if it represents a transient
// K8s API failure that should be retried. Non-transient errors are returned
// unchanged.
func wrapIfTransient(err error) error {
	if err == nil {
		return nil
	}
	if isTransientK8sError(err) {
		return &TransientError{Cause: err}
	}
	return err
}

// isTransientK8sError returns true if the error represents a transient K8s
// API failure that is likely to succeed on retry. This includes server-side
// errors (429, 500, 503, 504) and network-level errors.
func isTransientK8sError(err error) bool {
	// K8s API server-side errors that are typically transient.
	if apierrors.IsServerTimeout(err) ||
		apierrors.IsServiceUnavailable(err) ||
		apierrors.IsTooManyRequests(err) ||
		apierrors.IsInternalError(err) {
		return true
	}

	// Network-level errors (connection refused, timeout, etc.).
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	return false
}
