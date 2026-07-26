package publisher

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestGatewayStatusRetryableSplitsTerminalFromRetryable(t *testing.T) {
	terminal := []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
	}
	for _, status := range terminal {
		if gatewayStatusRetryable(status) {
			t.Errorf("status %d must be terminal: repeating identical request bytes cannot change the answer", status)
		}
	}
	retryable := []int{
		http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		// Deliberately unlisted statuses stay retryable: that is the behavior
		// every non-200 had before this taxonomy existed, and widening the
		// terminal set by accident would silently fail live publications.
		http.StatusTeapot, http.StatusMethodNotAllowed, http.StatusCreated,
	}
	for _, status := range retryable {
		if !gatewayStatusRetryable(status) {
			t.Errorf("status %d must stay retryable", status)
		}
	}
}

func TestGatewayErrorReportsClassAndUnwrapsTransportFailures(t *testing.T) {
	terminal := &GatewayError{Status: http.StatusForbidden, Path: "/v1/git/publish"}
	if got := terminal.Error(); got != "publisher gateway: /v1/git/publish responded with status 403 (terminal)" {
		t.Fatalf("terminal error text = %q", got)
	}
	cause := fmt.Errorf("dial tcp 127.0.0.1:1: connect: connection refused")
	transport := &GatewayError{Path: "/v1/publications/lookup", Retryable: true, Err: cause}
	if !errors.Is(transport, cause) {
		t.Fatal("transport GatewayError must unwrap to its cause")
	}
	var found *GatewayError
	if !errors.As(fmt.Errorf("publisher: reconcile: %w", transport), &found) || !found.Retryable {
		t.Fatal("a wrapped GatewayError must remain discoverable through errors.As")
	}
}

// stubActionsReader drives checkActionsAdmitted from inside the package so the
// suppression refusal under test is the one production builds, not a
// hand-assembled lookalike.
type stubActionsReader struct {
	mode  string
	found bool
	err   error
}

func (stub *stubActionsReader) GetActionsMode() (string, bool, error) {
	return stub.mode, stub.found, stub.err
}

// TestRetryableKeepsSuppressedActionsOutOfTheTerminalClass pins the boundary of
// the taxonomy. The gateway classification exists to end retry-forever loops by
// completing an operation as StatusFailed, and a terminal completion is
// PERMANENT: Acquire never reclaims a terminal row. ErrActionsSuppressed is
// retryable by construction — the switch refuses before any provider call
// precisely so the pending row survives and the identical semantic operation
// executes exactly once after an admin resumes actions. If a suppressed attempt
// were ever classified terminal, that operation key could never be resumed.
func TestRetryableKeepsSuppressedActionsOutOfTheTerminalClass(t *testing.T) {
	if !Retryable(ErrActionsSuppressed) {
		t.Fatal("ErrActionsSuppressed must be retryable: a terminal completion is permanent, so classifying suppression as terminal would poison the operation key forever")
	}
	// The production wrap, not a hand-built one: checkActionsAdmitted wraps the
	// sentinel with %w when the switch itself could not be read.
	wrapped := checkActionsAdmitted(&stubActionsReader{err: errors.New("connection refused")})
	if wrapped == nil || !errors.Is(wrapped, ErrActionsSuppressed) {
		t.Fatalf("unreadable switch = %v, want a wrapped ErrActionsSuppressed", wrapped)
	}
	// The taxonomy classifies ONLY the gateway HTTP boundary. Suppression is
	// refused before that boundary is reached, so it must carry no
	// classification at all.
	var gatewayErr *GatewayError
	if errors.As(wrapped, &gatewayErr) {
		t.Fatalf("a suppression refusal must never be a *GatewayError, got %+v", gatewayErr)
	}
	if errors.As(checkActionsAdmitted(&stubActionsReader{mode: ActionsModeSuppressed, found: true}), &gatewayErr) {
		t.Fatalf("a plain suppression refusal must never be a *GatewayError, got %+v", gatewayErr)
	}
	if !Retryable(wrapped) {
		t.Fatal("a suppression refusal raised by an unreadable switch must stay retryable")
	}
	if !Retryable(fmt.Errorf("publisher: reconcile repository publication: %w", ErrActionsSuppressed)) {
		t.Fatal("a service-wrapped suppression refusal must stay retryable")
	}
	// Defense in depth, and the reason the sentinel is checked BEFORE the
	// classification rather than after: a future change that wrongly routed
	// suppression through the gateway boundary would be unrecoverable in
	// production, so the sentinel must win even when it carries a terminal
	// classification. Reordering the two checks in Retryable fails here.
	if !Retryable(&GatewayError{Status: http.StatusForbidden, Path: "/v1/git/publish", Err: ErrActionsSuppressed}) {
		t.Fatal("suppression must outrank a terminal classification: completing a suppressed attempt as failed can never be undone")
	}

	// Terminal gateway responses are the only false answer.
	if Retryable(&GatewayError{Status: http.StatusForbidden, Path: "/v1/git/publish"}) {
		t.Fatal("a terminal GatewayError must not be retryable")
	}
	if !Retryable(&GatewayError{Status: http.StatusServiceUnavailable, Path: "/v1/git/publish", Retryable: true}) {
		t.Fatal("a retryable GatewayError must be retryable")
	}
	// Everything the taxonomy never classified — transport, decode, size-bound,
	// deadline, and no error at all — stays retryable.
	for name, err := range map[string]error{
		"transport": errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
		"decode":    errors.New("publisher gateway: decode response: multiple JSON values"),
		"none":      nil,
	} {
		if !Retryable(err) {
			t.Errorf("%s error must stay retryable: only a terminal *GatewayError may end an operation", name)
		}
	}
}
