package jetbridge

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestTransientErrorWrapsAndUnwraps(t *testing.T) {
	cause := fmt.Errorf("pod creation timeout")
	te := &TransientError{Cause: cause}

	if te.Error() != "pod creation timeout" {
		t.Errorf("expected error message %q, got %q", "pod creation timeout", te.Error())
	}

	unwrapped := errors.Unwrap(te)
	if unwrapped != cause {
		t.Errorf("Unwrap returned %v, expected %v", unwrapped, cause)
	}

	// Verify errors.Is works through the wrapper.
	if !errors.Is(te, cause) {
		t.Error("errors.Is should find the cause through TransientError")
	}
}

func TestTransientErrorIsRetryable(t *testing.T) {
	te := &TransientError{Cause: fmt.Errorf("transient")}
	if !te.IsRetryable() {
		t.Error("IsRetryable should return true")
	}
}

func TestWrapIfTransientNil(t *testing.T) {
	if result := wrapIfTransient(nil); result != nil {
		t.Errorf("wrapIfTransient(nil) should return nil, got %v", result)
	}
}

func TestWrapIfTransientK8sServerErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"TooManyRequests (429)", apierrors.NewTooManyRequests("rate limited", 5)},
		{"InternalError (500)", apierrors.NewInternalError(fmt.Errorf("internal"))},
		{"ServiceUnavailable (503)", apierrors.NewServiceUnavailable("unavailable")},
		{"ServerTimeout (504)", apierrors.NewServerTimeout(schema.GroupResource{Group: "", Resource: "pods"}, "get", 30)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := wrapIfTransient(tc.err)
			var te *TransientError
			if !errors.As(wrapped, &te) {
				t.Fatalf("expected TransientError wrapper, got %T", wrapped)
			}
			if !te.IsRetryable() {
				t.Error("wrapped error should be retryable")
			}
			if !errors.Is(wrapped, tc.err) {
				t.Error("original error should be accessible via errors.Is")
			}
		})
	}
}

func TestWrapIfTransientNetworkErrors(t *testing.T) {
	urlErr := &url.Error{Op: "Get", URL: "https://k8s-api:6443", Err: fmt.Errorf("connection refused")}
	wrapped := wrapIfTransient(urlErr)
	var te *TransientError
	if !errors.As(wrapped, &te) {
		t.Fatalf("url.Error should be wrapped as TransientError, got %T", wrapped)
	}

	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("connection refused")}
	wrapped = wrapIfTransient(netErr)
	if !errors.As(wrapped, &te) {
		t.Fatalf("net.Error should be wrapped as TransientError, got %T", wrapped)
	}
}

func TestWrapIfTransientPassthroughNonTransient(t *testing.T) {
	nonTransient := fmt.Errorf("container image not found")
	result := wrapIfTransient(nonTransient)

	// Should return the error unchanged.
	if result != nonTransient {
		t.Errorf("non-transient error should pass through unchanged, got %T", result)
	}

	var te *TransientError
	if errors.As(result, &te) {
		t.Error("non-transient error should not be wrapped as TransientError")
	}
}

func TestWrapIfTransientK8sNotFoundPassthrough(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, "my-pod")
	result := wrapIfTransient(notFound)

	var te *TransientError
	if errors.As(result, &te) {
		t.Error("NotFound error should not be wrapped as TransientError")
	}
}
