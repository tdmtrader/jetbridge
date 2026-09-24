// Command hangar-store owns a single persistent disk and serves exact immutable
// objects. Provisioning is explicit; a replacement volume never initializes on
// an ordinary server restart.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/concourse/concourse/hangar/disk"
	"github.com/concourse/concourse/hangar/diskserver"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hangar-store:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("hangar-store", flag.ContinueOnError)
	root := flags.String("root", "/data", "Private persistent storage directory.")
	id := flags.String("store-id", "", "Stable storage identity, pinned by clients.")
	initialize := flags.Bool("initialize-only", false, "Provision an empty disk and exit; never use as an automatic restart action.")
	listen := flags.String("listen", ":7783", "HTTPS listener.")
	cert := flags.String("tls-cert", "", "Server certificate PEM.")
	key := flags.String("tls-key", "", "Server private key PEM.")
	credentials := flags.String("credentials-file", "", "JSON mapping input, publisher, inventory and reclaimer to distinct bearer credentials.")
	input := flags.String("input-namespace", "inputs", "Dedicated strict-input namespace.")
	output := flags.String("output-namespace", "outputs", "Dedicated output namespace.")
	maxBytes := flags.Int64("max-object-bytes", 16<<30, "Maximum stored bytes per object.")
	concurrency := flags.Int("max-concurrent", 4, "Maximum simultaneous object operations; excess requests fail for retry.")
	timeout := flags.Duration("operation-timeout", 5*time.Minute, "Maximum time to receive or serve an object.")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument")
	}
	if *initialize {
		return disk.Initialize(*root, *id)
	}
	if *cert == "" || *key == "" || *credentials == "" || *timeout <= 0 {
		return fmt.Errorf("TLS certificate/key, credentials file and positive timeout required")
	}
	f, err := os.Open(*credentials)
	if err != nil {
		return err
	}
	encoded, err := io.ReadAll(io.LimitReader(f, 32<<10))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	var tokens map[string]string
	if err := json.Unmarshal(encoded, &tokens); err != nil {
		return fmt.Errorf("invalid credentials file: %w", err)
	}
	store, err := disk.Open(*root, *id, *maxBytes)
	if err != nil {
		return err
	}
	defer store.Close()
	handler, err := diskserver.New(store, diskserver.Config{StoreID: *id, InputNamespace: *input, OutputNamespace: *output, Credentials: tokens, MaxConcurrent: *concurrency})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: *timeout, WriteTimeout: *timeout, IdleTimeout: time.Minute, MaxHeaderBytes: 128 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServeTLS(*cert, *key) }()
	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}
