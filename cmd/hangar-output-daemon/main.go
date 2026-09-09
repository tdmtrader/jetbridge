// Command hangar-output-daemon is the output plane's node-local publisher and
// receipt signer.
//
// It is a separate binary and a separate DaemonSet from cmd/artifact-daemon
// because a Kubernetes service account is Pod-wide: giving the existing daemon
// an output-bucket role would give its cache and strict-input identity the same
// role, and no amount of care inside one process takes that back. This binary
// links a publisher client and nothing else -- no cache client, no strict-input
// client, no list or delete capability, no database handle.
//
// It does not serve yet. Requests, readiness and the source ledger are the next
// phase's; what exists here is the construction, the refusals that make the
// bucket isolation real, and the publish path itself.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func nowUTC() time.Time { return time.Now().UTC() }

func main() {
	config := Config{}
	BindFlags(flag.CommandLine, &config)
	flag.Parse()

	if err := run(context.Background(), config, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "hangar-output-daemon: %v\n", err)
		os.Exit(1)
	}
}

// run is main with its side effects named, so the construction can be driven by
// a test without a process.
func run(ctx context.Context, config Config, out *os.File) error {
	daemon, err := Build(ctx, config)
	if err != nil {
		return err
	}

	namespace := daemon.Namespace()
	fmt.Fprintf(out, "hangar-output-daemon configured\n")
	fmt.Fprintf(out, "  bucket:           %s\n", namespace.Bucket())
	fmt.Fprintf(out, "  key prefix:       %s\n", namespace.Prefix())
	fmt.Fprintf(out, "  derived scope:    %s\n", namespace.Scope())
	fmt.Fprintf(out, "  activation epoch: %d\n", namespace.ActivationEpoch())
	fmt.Fprintf(out, "  receipt key:      %s (public key %x)\n",
		config.ReceiptKeyID, daemon.ReceiptPublicKey()[:8])
	fmt.Fprintf(out, "\nThis binary does not serve requests yet: the control API, the source\n")
	fmt.Fprintf(out, "ledger and readiness are the next phase's. It exists now so the bucket\n")
	fmt.Fprintf(out, "isolation, the key material and the publish path are real code with a\n")
	fmt.Fprintf(out, "real principal rather than a plan.\n")

	return nil
}
