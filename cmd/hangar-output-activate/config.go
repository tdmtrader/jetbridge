package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
	"github.com/concourse/concourse/hangar/output"
)

// Mode is one of the four guarded transitions.
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
)

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

	Namespace      string
	DaemonSelector string
	DaemonPort     int

	TLSCert   string
	TLSKey    string
	TLSCACert string

	ReceiptKeyLifetime time.Duration
}

func BindFlags(flags *flag.FlagSet, config *Config) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. It is the ACTIVATION role's, distinct from the web pod's: the architecture guard saying only this command writes hangar_output_activation_epochs is enforceable only while that is true of the credential as well as of the code.")
	flags.Int64Var(&config.Epoch, "epoch", 0,
		"The activation epoch to act on. Rotation creates a NEW epoch rather than replacing a key in place, so this is the one being brought up or taken down.")
	flags.Func("mode", "One of begin, attest, enable, drain, reconcile-integrity.", func(value string) error {
		switch Mode(strings.TrimSpace(value)) {
		case ModeBegin, ModeAttest, ModeEnable, ModeDrain, ModeReconcileIntegrity:
			config.Mode = Mode(strings.TrimSpace(value))

			return nil
		}

		return fmt.Errorf("%w: --mode %q; choose: begin, attest, enable, drain, reconcile-integrity",
			output.ErrUnknownMember, value)
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
		"Let a drain take a facet to the terminal `disabled` state. Without it a drain stops new admission and reports what is still live; unsafe removal is blocked rather than promised after a finite drain.")
	flags.StringVar(&config.Namespace, "namespace", "",
		"Kubernetes namespace the output daemon runs in. Attestation enumerates the cohort from the API and never from node labels.")
	flags.StringVar(&config.DaemonSelector, "daemon-selector",
		"app.kubernetes.io/component=hangar-output-daemon",
		"Label selector for the output daemon's pods.")
	flags.IntVar(&config.DaemonPort, "daemon-port", 7781,
		"Control port of the node-local output daemon.")
	flags.StringVar(&config.TLSCert, "tls-cert", "",
		"Client certificate for the daemon control API. The extension handshake names a bucket and a derived namespace, so it is behind the same certificate every other off-node call is.")
	flags.StringVar(&config.TLSKey, "tls-key", "", "Client private key.")
	flags.StringVar(&config.TLSCACert, "tls-ca-cert", "",
		"CA certificate the daemon's server certificate is verified against.")
	flags.DurationVar(&config.ReceiptKeyLifetime, "receipt-key-lifetime", 90*24*time.Hour,
		"How long this epoch's receipt key is valid for. It bounds the window in which a receipt signed under this epoch verifies; the key material itself is retained for as long as any durable state references the epoch.")
}

// TLSEnabled is all three or none.
func (config Config) TLSEnabled() bool {
	return config.TLSCert != "" && config.TLSKey != "" && config.TLSCACert != ""
}

// Validate refuses a Job that cannot do what it was asked.
func (config Config) Validate() error {
	if strings.TrimSpace(resolveDSN(config.DSN)) == "" {
		return fmt.Errorf("%w: --database is required, or %s in the environment",
			output.ErrIncomplete, dsnEnvironmentVariable)
	}
	if config.Epoch <= 0 {
		return fmt.Errorf("%w: --epoch is required and must be positive; zero is the absence "+
			"of an epoch", output.ErrIncomplete)
	}
	if config.Mode == "" {
		return fmt.Errorf("%w: --mode is required; choose: begin, attest, enable, drain, reconcile-integrity",
			output.ErrIncomplete)
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
	if config.Facet == "" {
		return fmt.Errorf("%w: --facet is required for --mode=%s", output.ErrIncomplete, config.Mode)
	}
	if config.All && config.Mode != ModeDrain {
		return fmt.Errorf("%w: --facet=all is only meaningful for --mode=drain. Attesting or "+
			"enabling \"both facets\" in one step would hide the ordering the whole protocol "+
			"is: output can never be ready without base", output.ErrIncomplete)
	}
	if config.Mode == ModeAttest {
		if strings.TrimSpace(config.Namespace) == "" {
			return fmt.Errorf("%w: --namespace is required for --mode=attest; the cohort is "+
				"enumerated from the Kubernetes API", output.ErrIncomplete)
		}
		if config.ReceiptKeyLifetime <= 0 {
			return fmt.Errorf("%w: --receipt-key-lifetime must be positive",
				output.ErrIncomplete)
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
	}

	return nil
}

// dsnEnvironmentVariable is where this command reads its PostgreSQL connection
// string when --database is not given, and is how the chart supplies it.
//
// Not `--database=$(HANGAR_OUTPUT_DSN)`: the kubelet expands $(VAR) in args, so
// the credential lands in /proc/<pid>/cmdline, which is world-readable inside
// the container. /proc/<pid>/environ is readable only by the process's own uid.
// The flag stays, because a developer running this by hand has no environment
// set up for it and a flag is the honest way to say so.
const dsnEnvironmentVariable = "HANGAR_OUTPUT_DSN"

// resolveDSN fills the connection string from the environment when the flag
// left it empty.
func resolveDSN(dsn string) string {
	if dsn != "" {
		return dsn
	}

	return os.Getenv(dsnEnvironmentVariable)
}
