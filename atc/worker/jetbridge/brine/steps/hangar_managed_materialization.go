package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/output"
)

func exerciseManagedMaterialization(ctx context.Context, in BoundOutput, mode string, node *jetbridge.OutputControlClient, admission *hangaroutput.ReadAdmission, warrant hangaroutput.ReadWarrant) error {
	daemon := in.Tree.Outcome.Source.Draft.Daemon
	if mode == "unauthenticated" || mode == "node-local warrant" {
		transport := daemon.HTTP.Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.Certificates = nil
		defer transport.CloseIdleConnections()
		node = jetbridge.NewOutputControlClient(daemon.Output.URL, &http.Client{Transport: transport, Timeout: 10 * time.Second}, daemon.Minter, warrant.Lease.ActivationEpoch)
	}
	materializer, ok := any(node).(interface {
		MaterializeManagedOutput(context.Context, output.ManagedReadRequest) error
	})
	if !ok {
		return fmt.Errorf("the node client has no managed-output materialization operation")
	}
	request := output.ManagedReadRequest{Ref: in.Tree.Ref, Destination: warrant.Record.Destination, Warrant: warrant.Token}
	root := filepath.Join(daemon.Output.Root, "steps", request.Destination.Handle, request.Destination.Volume)
	switch mode {
	case "unauthenticated":
		request.Warrant = "invalid"
	case "wrong destination":
		request.Destination.Volume = "input-1"
	case "wrong generation":
		request.Ref.Generation++
	case "conflicting destination":
		if err := os.MkdirAll(root, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "keep-me"), []byte("existing consumer data"), 0644); err != nil {
			return err
		}
	case "symlink destination":
		if err := os.MkdirAll(filepath.Dir(root), 0755); err != nil {
			return err
		}
		if err := os.Symlink(in.Tree.Outcome.Source.incarnationRoot(), root); err != nil {
			return err
		}
	}
	err := materializer.MaterializeManagedOutput(ctx, request)
	if mode != "live" && mode != "fresh retry" && mode != "node-local warrant" {
		want := output.ErrUnauthorized
		if mode == "conflicting destination" || mode == "symlink destination" {
			want = output.ErrConflict
		}
		if !errors.Is(err, want) {
			return fmt.Errorf("%s materialization was not refused as %v: %v", mode, want, err)
		}
		if mode == "conflicting destination" {
			data, err := os.ReadFile(filepath.Join(root, "keep-me"))
			if err != nil || string(data) != "existing consumer data" {
				return fmt.Errorf("refused materialization changed existing data: %v", err)
			}
		} else if mode == "symlink destination" {
			if _, err := os.Readlink(root); err != nil {
				return fmt.Errorf("refused materialization replaced the symlink: %v", err)
			}
		} else if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("unauthorized materialization created a destination: %v", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := verifyManagedMaterialization(in, root); err != nil {
		return err
	}
	if err := materializer.MaterializeManagedOutput(ctx, request); !errors.Is(err, output.ErrUnauthorized) {
		return fmt.Errorf("completed materialization reused its released lease: %v", err)
	}
	if mode == "fresh retry" {
		// A lost response is recovered with fresh authority, including after the
		// daemon restarts. Existing identical sealed bytes are reused safely.
		if err := daemon.Output.crash(); err != nil {
			return err
		}
		if err := daemon.Output.restart(ctx, daemon.HTTP); err != nil {
			return err
		}
		nonce, err := output.NewReadWarrantNonce(freshReader())
		if err != nil {
			return err
		}
		retry, err := admission.Admit(ctx, hangaroutput.ReadRequest{ReadLeaseID: output.ReadLeaseID(freshUUID()), WarrantNonce: nonce, ClaimID: in.Acquisition.ClaimID, Ref: in.Tree.Ref, Destination: request.Destination, ActivationEpoch: warrant.Lease.ActivationEpoch, MaterializationTimeout: time.Minute})
		if err != nil {
			return err
		}
		request.Warrant = retry.Token
		if err := materializer.MaterializeManagedOutput(ctx, request); err != nil {
			return err
		}
		return verifyManagedMaterialization(in, root)
	}
	return nil
}

func verifyManagedMaterialization(in BoundOutput, root string) error {
	want, err := json.Marshal(in.Tree.Ref)
	if err != nil {
		return err
	}
	receipt, err := os.ReadFile(filepath.Join(root, ".hangar-materialized"))
	if err != nil || !bytes.Equal(receipt, want) {
		return fmt.Errorf("materialized input lacks its exact receipt: %v", err)
	}
	source := in.Tree.Outcome.Source.incarnationRoot()
	entries := 0
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, rel)
		info, err := os.Lstat(target)
		if err != nil {
			return err
		}
		if info.Mode().Type() != entry.Type() || (info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0222 != 0) {
			return fmt.Errorf("input %s changed type or remains writable", rel)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			a, err := os.Readlink(path)
			if err != nil {
				return err
			}
			b, err := os.Readlink(target)
			if err != nil || a != b {
				return fmt.Errorf("input symlink changed: %v", err)
			}
		} else if !entry.IsDir() {
			entries++
			a, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(a, b) {
				return fmt.Errorf("input contents changed: %v", err)
			}
		}
		return nil
	})
	if err == nil && entries == 0 {
		return fmt.Errorf("materialization assertion compared no payload files")
	}
	return err
}
