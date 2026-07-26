package publisher

import (
	"errors"
	"fmt"
)

// Cluster-wide action-suppression modes. These are the exact strings stored in
// agent_settings.actions_mode (migration 1773106128) and carried on the
// GET/PUT /api/v1/agent/actions wire.
//
//	active     — external side effects execute normally.
//	suppressed — every external side effect refuses BEFORE any provider call.
//
// Suppression bounds EXTERNAL EFFECTS ONLY: dispatch, agent execution, and
// sealing are deliberately NOT gated, which is what makes the switch a
// shadow-mode enabler rather than a stop-the-world button.
const (
	ActionsModeActive     = "active"
	ActionsModeSuppressed = "suppressed"
)

// ErrActionsSuppressed is the typed refusal every publisher service returns
// while the switch is engaged. It is RETRYABLE: the durable operation row is
// left pending, so the identical semantic operation — same OperationKey, same
// Idempotency-Key — executes exactly once after an admin resumes actions.
var ErrActionsSuppressed = errors.New(
	`publisher: external actions are suppressed by the platform action switch; ` +
		`resume with "fly agent actions resume" (PUT /api/v1/agent/actions {"mode":"active"}) and retry the build`)

// ActionsModeReader is the hot read of the cluster-wide switch.
// db.AgentSettingsFactory satisfies it. found=false means no admin has ever
// engaged the brake.
type ActionsModeReader interface {
	GetActionsMode() (mode string, found bool, err error)
}

// ValidActionsMode reports whether s is a recognized mode. The API layer uses
// it to reject a bad PUT before touching the database.
func ValidActionsMode(s string) bool {
	return s == ActionsModeActive || s == ActionsModeSuppressed
}

// EffectiveActionsMode applies the fail-safe policy to one settings read.
//
//   - readErr != nil ⇒ suppressed. A DB fault must never be the reason an
//     external side effect escapes an engaged brake, and a node that cannot
//     read agent_settings could not have completed the durable publish
//     protocol anyway. Same direction as dispatch.EffectiveModeFromRead, which
//     pauses on read error.
//   - found == false ⇒ active. Absence is meaningful: the switch is an
//     emergency brake and an untouched brake is not engaged.
//   - any stored value other than exactly "active" ⇒ suppressed. The CHECK
//     constraint makes an unknown value unreachable through the API; if one
//     ever appears, fail closed rather than guess.
func EffectiveActionsMode(mode string, found bool, readErr error) string {
	if readErr != nil {
		return ActionsModeSuppressed
	}
	if !found {
		return ActionsModeActive
	}
	if mode == ActionsModeActive {
		return ActionsModeActive
	}
	return ActionsModeSuppressed
}

// ServiceOption configures optional publisher-service dependencies without
// changing the existing positional constructors.
type ServiceOption func(*serviceOptions)

type serviceOptions struct {
	actions ActionsModeReader
}

// WithActionsGate makes the service consult the cluster-wide
// action-suppression switch before every external interaction.
func WithActionsGate(reader ActionsModeReader) ServiceOption {
	return func(options *serviceOptions) { options.actions = reader }
}

func buildServiceOptions(options []ServiceOption) serviceOptions {
	var resolved serviceOptions
	for _, option := range options {
		option(&resolved)
	}
	return resolved
}

// checkActionsAdmitted is the choke point every external side effect calls
// before touching a provider — including the recovery Lookup, so a suppressed
// publisher makes no network call at all.
//
// A nil reader means the deployment composed a service without the switch.
// NewGatewayExecutor refuses that at startup, so nil here can only come from a
// direct in-test construction and is admitted.
func checkActionsAdmitted(reader ActionsModeReader) error {
	if nilInterface(reader) {
		return nil
	}
	mode, found, err := reader.GetActionsMode()
	if EffectiveActionsMode(mode, found, err) == ActionsModeSuppressed {
		if err != nil {
			return fmt.Errorf("%w (the switch could not be read: %v)", ErrActionsSuppressed, err)
		}
		return ErrActionsSuppressed
	}
	return nil
}
