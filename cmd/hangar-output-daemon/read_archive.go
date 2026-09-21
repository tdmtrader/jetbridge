package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type managedReads struct {
	control  *output.LeaseControlClient
	verifier *output.ReadWarrantVerifier
	timeout  time.Duration
}

func (server *Server) configureReads(config Config) error {
	if config.ReadControlURL == "" {
		return nil
	}
	key, err := os.ReadFile(config.MaterializationKeyFile)
	if err != nil {
		return fmt.Errorf("read materialization key: %w", err)
	}
	clock := output.ClockFunc(nowUTC)
	verifier, err := output.NewReadWarrantVerifier(key, clock)
	if err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(config.TLSCACert)
	if err != nil {
		return err
	}
	if !roots.AppendCertsFromPEM(ca) {
		return fmt.Errorf("read control CA contains no certificate")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	server.reads = &managedReads{verifier: verifier, timeout: config.OperationTimeout, control: &output.LeaseControlClient{
		BaseURL: config.ReadControlURL, HTTP: client, NodeUID: executioncontrol.NodeUID(config.NodeUID),
		KeyID: config.ControlKeyID, Signer: server.daemon.CaptureSigner(), Clock: clock,
	}}
	return nil
}

func (server *Server) readArchive(w http.ResponseWriter, request *http.Request) {
	if !server.authorizeRead(w, request) {
		return
	}
	if server.reads == nil {
		readRefusal(w, output.ErrCaptureDisabled)
		return
	}
	var input output.ManagedReadRequest
	if err := decodeRead(request, &input); err != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	if err := input.Validate(); err != nil {
		readRefusal(w, output.ErrIncomplete)
		return
	}
	claims, err := server.reads.verifier.Verify(input.Warrant, input.Ref, input.Destination)
	if err != nil || claims.ActivationEpoch != server.daemon.ActivationEpoch() {
		readRefusal(w, output.ErrUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.reads.timeout)
	defer cancel()
	release, err := server.spooling(ctx)
	if err != nil {
		readRefusal(w, err)
		return
	}
	defer release()
	tree, attributes, releaseLease, err := server.stageRead(ctx, input)
	if err != nil {
		readRefusal(w, err)
		return
	}
	defer tree.Close()
	// The private copy is complete and verified: the object needs no more
	// protection for this download.
	if err := releaseLease(nil); err != nil {
		readRefusal(w, err)
		return
	}
	file, err := os.Open(tree.ArchivePath)
	if err != nil {
		readRefusal(w, output.ErrInfrastructure)
		return
	}
	defer file.Close()
	// A stalled consumer must not retain this daemon's scratch slot forever.
	deadline, _ := ctx.Deadline()
	if err = http.NewResponseController(w).SetWriteDeadline(deadline); err != nil {
		readRefusal(w, output.ErrInfrastructure)
		return
	}
	metadata, _ := json.Marshal(output.AttributesFromFoundation(attributes))
	w.Header().Set(output.ReadAttributesHeader, string(metadata))
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprint(tree.ByteSize))
	if _, err = io.Copy(w, file); err != nil {
		return
	}
}

// Stage under a live lease and verify the complete canonical tree. The
// returned private copy survives reclamation of the object.
//
// The lease is given back through the returned release, which the caller
// calls with its read's final error once it no longer needs the protection. A
// failure staging here is released here. See leaseRelease for when a failed
// read keeps its lease.
func (server *Server) stageRead(ctx context.Context, input output.ManagedReadRequest) (tree *hangar.CapturedTree, attributes hangar.TreeAttributes, release func(error) error, err error) {
	profile, err := output.NewLeaseReadProfile(server.reads.control, input.Warrant, server.reads.timeout)
	if err != nil {
		return nil, attributes, nil, err
	}
	lease, err := profile.AdmitLease(ctx, input.Ref, input.Destination.Handle, input.Destination.Volume)
	if err != nil {
		return nil, attributes, nil, err
	}
	release = leaseRelease(ctx, profile)
	defer func() {
		if err != nil {
			err = errors.Join(err, release(err))
			release = nil
			if tree != nil {
				_ = tree.Close()
				tree = nil
			}
		}
	}()
	archive, object, err := server.daemon.publisher.OpenExactObject(ctx, input.Ref, lease)
	if err != nil {
		return nil, attributes, nil, err
	}
	attributes = object.Attributes
	tree, err = server.daemon.canonicalizer.Capture(ctx, archive)
	err = errors.Join(err, archive.Close())
	if err != nil {
		return tree, attributes, nil, err
	}
	if tree.Digest != input.Ref.Digest || tree.ByteSize != attributes.LogicalBytes {
		return tree, attributes, nil, output.ErrCorrupt
	}
	return tree, attributes, release, nil
}

// leaseRelease gives a read's lease back once the read is over -- on success,
// and on every failure a retry cannot change: a refusal, a conflict, a corrupt
// or missing object, a malformed request.
//
// A failure this daemon answers as unavailable (503) is the one the reader is
// told to retry, and the managed-input init does, with the same warrant: the
// lease was minted for exactly one destination before its Pod existed, so a
// released lease is one that init can never use again, and one transient
// failure would fail the step for good. Such a failure keeps the lease. It
// stays bounded -- it closes by database-clock expiry at the end of its term,
// swept like any lease nobody released -- and a replay under any other
// destination is still refused, because the warrant binds it.
func leaseRelease(ctx context.Context, profile *output.LeaseReadProfile) func(error) error {
	return func(readErr error) error {
		if readErr != nil && readRefusalClass(readErr) == output.ErrInfrastructure &&
			!errors.Is(readErr, output.ErrCorrupt) && !errors.Is(readErr, hangar.ErrCorrupt) {
			return nil
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return profile.Release(cleanup, readErr)
	}
}
