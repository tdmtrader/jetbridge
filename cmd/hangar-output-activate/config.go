package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/output"
)

// Mode is one of the four guarded transitions, the walk that composes them,
// or the integrity reconciliation.
//
// A FLAG and not a positional subcommand. The plan writes them as
// `hangar-output begin|attest|enable|drain`, and they are the same four
// transitions either way; a flag keeps this binary to one flag surface, which is
// what deploy/chart/tests/flag_drift_test.go can check against `--help`. A
// subcommand shape would need a help dialect per subcommand for the guard to see
// past the first one.
type Mode string

const (
	ModeBegin              Mode = "begin"
	ModeAttest             Mode = "attest"
	ModeEnable             Mode = "enable"
	ModeDrain              Mode = "drain"
	ModeReconcileIntegrity Mode = "reconcile-integrity"

	// ModeWalk moves the epoch's row toward --target, making only the
	// transitions the row shows are still needed. The chart runs it on every
	// sync, which a step mode could not survive.
	ModeWalk Mode = "walk"
)

// modeList is every mode, in the order the help and the refusals name them.
const modeList = "begin, attest, enable, drain, walk, reconcile-integrity"

// Config is what one activation Job was told.
type Config struct {
	DSN                string
	Epoch              int64
	Mode               Mode
	Facet              activation.Facet
	IntegrityViolation string
	IntegritySubject   string

	// All is --facet=all, which drain alone accepts.
	All bool

	// Finalize lets a drain take a facet to the terminal `disabled` state.
	//
	// Separate from the drain itself because they are different decisions.
	// Stopping new admission is reversible in the sense that matters -- a new
	// epoch can be enabled beside it -- and is the step an operator wants
	// immediately. `disabled` asserts the facet holds nothing, and that
	// assertion is checked against the live tables rather than taken.
	Finalize bool

	// Target is where a walk takes the epoch: off, base or output.
	Target activation.Target

	Namespace      string
	DaemonSelector string
	DaemonPort     int

	// DaemonSetName is the output DaemonSet a walk waits on before each
	// attestation. A name and not the selector, because the walk reads the
	// DaemonSet through a namespaced `daemonsets get`, which RBAC can confine
	// to that one name.
	DaemonSetName string
	// ReadinessTimeout bounds that wait.
	ReadinessTimeout time.Duration

	TLSCert   string
	TLSKey    string
	TLSCACert string
	// TLSServerName is the DNS name the daemon's server certificate carries.
	// The cohort is dialed by pod IP, which no certificate can name.
	TLSServerName string
}

func BindFlags(flags *flag.FlagSet, config *Config) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. It is the ACTIVATION role's, distinct from the web pod's: the architecture guard saying only this command writes hangar_output_activation_epochs is enforceable only while that is true of the credential as well as of the code.")
	flags.Int64Var(&config.Epoch, "epoch", 0,
		"The activation epoch to act on. Rotation creates a NEW epoch rather than replacing a key in place, so this is the one being brought up or taken down.")
	flags.Func("mode", "One of "+modeList+".", func(value string) error {
		switch Mode(strings.TrimSpace(value)) {
		case ModeBegin, ModeAttest, ModeEnable, ModeDrain, ModeWalk, ModeReconcileIntegrity:
			config.Mode = Mode(strings.TrimSpace(value))

			return nil
		}

		return fmt.Errorf("%w: --mode %q; choose: %s", output.ErrUnknownMember, value, modeList)
	})
	flags.Func("target", "For walk: off, base or output. The walk moves the epoch's row toward it, skipping every step already done; a lower target drains the facets above it.", func(value string) error {
		target, err := activation.ParseTarget(value)
		if err != nil {
			return err
		}
		config.Target = target

		return nil
	})
	flags.StringVar(&config.IntegrityViolation, "integrity-violation", "", "Runtime finding class to reconcile: out_of_band_absence or runtime_principal_denied. Repair the cause first; this acknowledges it and does not restore objects.")
	flags.StringVar(&config.IntegritySubject, "integrity-subject", "", "Exact subject of one open finding in the selected epoch. No wildcard or blanket reconciliation.")

	flags.Func("facet", "One of base, output, or (for drain) all.", func(value string) error {
		trimmed := strings.TrimSpace(value)
		if trimmed == "all" {
			config.All = true
			config.Facet = activation.FacetOutput

			return nil
		}
		facet, err := activation.ParseFacet(trimmed)
		if err != nil {
			return err
		}
		config.Facet = facet
		config.All = false

		return nil
	})
	flags.BoolVar(&config.Finalize, "finalize", false,
		"Let a drain, or a walk to a lower target, take a facet to the terminal `disabled` state. Without it a drain stops new admission and reports what is still live; unsafe removal is blocked rather than promised after a finite drain.")
	flags.StringVar(&config.Namespace, "namespace", "",
		"Kubernetes namespace the artifact daemon (which serves the output plane) runs in. Attestation enumerates the cohort from the API and never from node labels.")
	flags.StringVar(&config.DaemonSelector, "daemon-selector",
		"app.kubernetes.io/component=artifact-daemon",
		"Label selector for the artifact daemon's pods, which serve the output plane.")
	flags.IntVar(&config.DaemonPort, "daemon-port", 7780,
		"Port of the node-local artifact daemon, which serves the output plane.")
	flags.StringVar(&config.DaemonSetName, "daemonset-name", "",
		"For walk: the artifact daemon's DaemonSet, read through a namespaced get. Each attestation waits until its updated, ready and desired counts are equal and every member pod is Ready.")
	flags.DurationVar(&config.ReadinessTimeout, "readiness-timeout", 10*time.Minute,
		"For walk: how long an attestation waits for the output DaemonSet to settle before the walk fails.")
	flags.StringVar(&config.TLSCert, "tls-cert", "",
		"Client certificate for the daemon control API. The extension handshake names a bucket and a derived namespace, so it is behind the same certificate every other off-node call is.")
	flags.StringVar(&config.TLSKey, "tls-key", "", "Client private key.")
	flags.StringVar(&config.TLSCACert, "tls-ca-cert", "",
		"CA certificate the daemon's server certificate is verified against.")
	flags.StringVar(&config.TLSServerName, "tls-server-name", "",
		"DNS name the daemon's server certificate is verified against. The cohort is dialed by pod IP, which a certificate issued before the pod existed cannot name.")
}

// TLSEnabled is all three or none.
func (config Config) TLSEnabled() bool {
	return config.TLSCert != "" && config.TLSKey != "" && config.TLSCACert != ""
}

// Validate refuses a Job that cannot do what it was asked.
func (config Config) Validate() error {
	if strings.TrimSpace(controller.ResolveDSN(config.DSN)) == "" {
		return fmt.Errorf("%w: --database is required, or %s in the environment",
			output.ErrIncomplete, controller.DSNEnvironmentVariable)
	}
	if config.Epoch <= 0 {
		return fmt.Errorf("%w: --epoch is required and must be positive; zero is the absence "+
			"of an epoch", output.ErrIncomplete)
	}
	if config.Mode == "" {
		return fmt.Errorf("%w: --mode is required; choose: %s", output.ErrIncomplete, modeList)
	}
	if config.Mode == ModeReconcileIntegrity {
		if _, err := output.ParsePolicyViolation(config.IntegrityViolation); err != nil {
			return err
		}
		if strings.TrimSpace(config.IntegritySubject) == "" {
			return fmt.Errorf("%w: --integrity-subject is required", output.ErrIncomplete)
		}
		return nil
	}
	if config.IntegrityViolation != "" || config.IntegritySubject != "" {
		return fmt.Errorf("%w: integrity flags require --mode=reconcile-integrity", output.ErrIncomplete)
	}

	if config.Mode == ModeBegin {
		// begin creates the row; both facets start in `initial` and neither is
		// named. A --facet here would read as "begin this facet", which is not
		// a thing: there is one row.
		return nil
	}
	if config.Mode == ModeWalk {
		return config.validateWalk()
	}
	if config.Facet == "" {
		return fmt.Errorf("%w: --facet is required for --mode=%s", output.ErrIncomplete, config.Mode)
	}
	if config.All && config.Mode != ModeDrain {
		return fmt.Errorf("%w: --facet=all is only meaningful for --mode=drain. Attesting or "+
			"enabling \"both facets\" in one step would hide the ordering the whole protocol "+
			"is: output can never be ready without base", output.ErrIncomplete)
	}
	if config.Mode == ModeAttest {
		return config.validateCohortAccess()
	}

	return nil
}

// validateWalk refuses a walk that could not reach any target.
//
// It names no facet: the target says how far, and the walk takes base before
// output itself. A --facet beside it would read as "walk this facet", which
// would skip the ordering the walk exists to keep.
func (config Config) validateWalk() error {
	if config.Facet != "" || config.All {
		return fmt.Errorf("%w: --facet is not meaningful for --mode=walk; --target says how "+
			"far, and the walk takes base before output itself", output.ErrIncomplete)
	}
	if config.Target == "" {
		return fmt.Errorf("%w: --target is required for --mode=walk; choose: off, base, output",
			output.ErrIncomplete)
	}
	if strings.TrimSpace(config.DaemonSetName) == "" {
		return fmt.Errorf("%w: --daemonset-name is required for --mode=walk; each "+
			"attestation waits on the output DaemonSet's rollout", output.ErrIncomplete)
	}
	if config.ReadinessTimeout <= 0 {
		return fmt.Errorf("%w: --readiness-timeout must be positive", output.ErrIncomplete)
	}

	return config.validateCohortAccess()
}

// validateCohortAccess is what attesting needs, whether one step attests or
// the walk does: the namespace the cohort is enumerated in and the client TLS
// whole or not at all.
func (config Config) validateCohortAccess() error {
	if strings.TrimSpace(config.Namespace) == "" {
		return fmt.Errorf("%w: --namespace is required for --mode=%s; the cohort is "+
			"enumerated from the Kubernetes API", output.ErrIncomplete, config.Mode)
	}
	var missing []string
	for _, one := range []struct{ name, value string }{
		{"--tls-cert", config.TLSCert},
		{"--tls-key", config.TLSKey},
		{"--tls-ca-cert", config.TLSCACert},
	} {
		if strings.TrimSpace(one.value) == "" {
			missing = append(missing, one.name)
		}
	}
	if len(missing) != 0 && len(missing) != 3 {
		return fmt.Errorf("%w: the daemon control API's client TLS is partially "+
			"configured; %s must also be set", output.ErrIncomplete,
			strings.Join(missing, " and "))
	}

	return nil
}
