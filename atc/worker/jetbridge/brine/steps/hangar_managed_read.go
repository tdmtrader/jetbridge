package steps

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

func HangarManagedReadDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		CheckThat[BoundOutput]("the consumer can inspect only the exact output through an authenticated daemon", func(in BoundOutput) error {
			daemon := in.Tree.Outcome.Source.Draft.Daemon
			control := jetbridge.NewOutputControlClient(daemon.Output.URL, daemon.HTTP, daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
			stat, ok := any(control).(interface {
				StatExactObject(context.Context, hangar.TreeRef) (output.PublishedObject, error)
			})
			if !ok {
				return fmt.Errorf("the node client has no managed-read stat operation")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			got, err := stat.StatExactObject(ctx, in.Tree.Ref)
			if err != nil {
				return err
			}
			if err = got.Validate(); err != nil {
				return err
			}
			if got.Attributes.Ref != in.Tree.Ref {
				return fmt.Errorf("stat lost the exact generation")
			}
			foreign := in.Tree.Ref
			foreign.Scope = "foreign"
			if _, err = stat.StatExactObject(ctx, foreign); !errors.Is(err, output.ErrUnauthorized) {
				return fmt.Errorf("foreign namespace was not refused: %v", err)
			}
			absent := in.Tree.Ref
			absent.Generation += 100000
			if _, err = stat.StatExactObject(ctx, absent); !errors.Is(err, output.ErrNotFound) {
				return fmt.Errorf("missing generation was not refused: %v", err)
			}
			// Real TLS without a client certificate, under the same trusted CA.
			transport := daemon.HTTP.Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.Certificates = nil
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			body, _ := json.Marshal(map[string]any{"ref": in.Tree.Ref})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, daemon.Output.URL+"/read/v1/stat", bytes.NewReader(body))
			if err != nil {
				return err
			}
			response, err := client.Do(req)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				return fmt.Errorf("unauthenticated stat returned HTTP %d", response.StatusCode)
			}
			return nil
		}),
		brine.DefineMap[BoundOutput, BoundOutput]("the consumer downloads with a {string} read lease", func(in BoundOutput, p brine.Params, rec *brine.Recorder) (BoundOutput, error) {
			mode, _ := p.GetString(0)
			return exerciseManagedRead(in, mode, false, rec)
		}),
		brine.DefineMap[BoundOutput, BoundOutput]("the consumer materializes with a {string} read lease", func(in BoundOutput, p brine.Params, rec *brine.Recorder) (BoundOutput, error) {
			mode, _ := p.GetString(0)
			return exerciseManagedRead(in, mode, true, rec)
		}),
		brine.DefineMap[BoundOutput, BoundOutput]("the consumer input initializes with {string}", func(in BoundOutput, p brine.Params, rec *brine.Recorder) (BoundOutput, error) {
			mode, _ := p.GetString(0)
			return exerciseManagedRead(in, "init:"+mode, true, rec)
		}),
	}
}

func exerciseManagedRead(in BoundOutput, mode string, materialize bool, rec *brine.Recorder) (BoundOutput, error) {
	initialize := strings.HasPrefix(mode, "init:")
	mode = strings.TrimPrefix(mode, "init:")
	daemon := in.Tree.Outcome.Source.Draft.Daemon
	plane := in.Tree.Outcome.Plane
	clock := output.ClockFunc(func() time.Time { return time.Now().UTC() })
	verifier, err := output.NewReadWarrantVerifier(brineReadWarrantKey, clock)
	if err != nil {
		return in, err
	}
	signer, err := output.NewReadWarrantSigner(brineReadWarrantKey)
	if err != nil {
		return in, err
	}
	control := &hangaroutput.LeaseControl{Transactor: brineTransactor{conn: plane.DB.Conn}, Leases: plane.Repository, Warrants: verifier, Minter: signer, Clock: clock,
		Keys: hangaroutput.NodeKeysFunc(func(node executioncontrol.NodeUID, key string) (ed25519.PublicKey, error) {
			if string(node) != daemon.NodeUID || key != hangarControlKeyID {
				return nil, output.ErrUnauthorized
			}
			return daemon.ControlPublic, nil
		})}
	server := httptest.NewUnstartedServer(control.Handler())
	cert, err := tls.LoadX509KeyPair(filepath.Join(daemon.CertDir, "server.crt"), filepath.Join(daemon.CertDir, "server.key"))
	if err != nil {
		return in, err
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	TrackDisposer(rec, "the managed-read TLS server", func() error { server.Close(); return nil })
	if err = daemon.Output.crash(); err != nil {
		return in, err
	}
	daemon.Output.cmd.Args = append(daemon.Output.cmd.Args, "--read-control-url", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = daemon.Output.restart(ctx, daemon.HTTP); err != nil {
		return in, err
	}
	node := jetbridge.NewOutputControlClient(daemon.Output.URL, daemon.HTTP, daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	stat, ok := any(node).(hangaroutput.ExactStat)
	if !ok {
		return in, fmt.Errorf("managed-read stat is unavailable")
	}
	admission := &hangaroutput.ReadAdmission{Transactor: brineTransactor{conn: plane.DB.Conn}, Leases: plane.Repository, Stat: stat, Minter: signer, Clock: clock}
	nonce, err := output.NewReadWarrantNonce(freshReader())
	if err != nil {
		return in, err
	}
	warrant, err := admission.Admit(ctx, hangaroutput.ReadRequest{ReadLeaseID: output.ReadLeaseID(freshUUID()), WarrantNonce: nonce, ClaimID: in.Acquisition.ClaimID, Ref: in.Tree.Ref, Destination: output.ReadDestination{Handle: "consumer", Volume: "input-0"}, ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), MaterializationTimeout: time.Minute})
	if err != nil {
		return in, err
	}
	if mode == "released" {
		tx, err := plane.DB.Conn.Begin()
		if err != nil {
			return in, err
		}
		defer tx.Rollback()
		if err = plane.Repository.ReleaseReadLease(ctx, tx, warrant.Lease); err != nil {
			return in, err
		}
		if err = tx.Commit(); err != nil {
			return in, err
		}
	}
	if mode == "missing" {
		// A valid signature is insufficient: this lease was never stored.
		absent := warrant.Lease
		absent.ReadLeaseID = output.ReadLeaseID(freshUUID())
		warrant.Token, err = signer.Sign(absent, warrant.Record.Destination, warrant.Record.WarrantNonce)
		if err != nil {
			return in, err
		}
	}
	if mode == "forged" {
		warrant.Token += "x"
	}
	if initialize {
		return in, exerciseManagedInputInit(ctx, in, mode, warrant)
	}
	if materialize {
		return in, exerciseManagedMaterialization(ctx, in, mode, node, admission, warrant)
	}
	limit := int64(16 << 20)
	if mode == "limited" {
		limit = 1
	}
	archive, attributes, err := node.OpenManagedOutput(ctx, output.ManagedReadRequest{Ref: in.Tree.Ref, Destination: warrant.Record.Destination, Warrant: warrant.Token}, limit)
	if mode != "live" {
		want := output.ErrUnauthorized
		if mode == "limited" {
			want = output.ErrLimitExceeded
		}
		if !errors.Is(err, want) || archive != nil {
			return in, fmt.Errorf("%s read was not refused as %v: %v", mode, want, err)
		}
		return in, nil
	}
	if err != nil {
		return in, err
	}
	defer archive.Close()
	if attributes.Ref != in.Tree.Ref {
		return in, fmt.Errorf("client changed the archive identity")
	}

	scratch, err := AttributedTempDir("brine-managed-read-")
	if err != nil {
		return in, err
	}
	defer os.RemoveAll(scratch)
	tree, err := (hangar.Canonicalizer{TempDir: scratch}).Capture(ctx, io.LimitReader(archive, 16<<20))
	if err != nil {
		return in, err
	}
	defer tree.Close()
	if tree.Digest != in.Tree.Ref.Digest {
		return in, fmt.Errorf("download does not match the committed exact tree")
	}
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	if _, err = plane.Repository.LoadReadLease(ctx, tx, warrant.Lease.ReadLeaseID); !errors.Is(err, output.ErrConflict) {
		return in, fmt.Errorf("verified download did not release its lease: %v", err)
	}
	return in, nil
}
