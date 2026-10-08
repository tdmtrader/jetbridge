package outputplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

type managedReads struct {
	verifier *output.ReadWarrantVerifier
	spent    *spentReads
	timeout  time.Duration
}

// configureReads arms the managed-read routes. They need the read-warrant key,
// which only an output-facet daemon holds.
//
// There is no control-plane client here: the daemon never asks the web whether
// the reader's claim is live, and never gives one back. The warrant IS the
// bound: it is minted from the committed claim's own instants, so its window
// is the claim's window (the read's timeout plus output.ReadClaimMargin), and
// it is single-use on this node (spentReads). The web gives the claim back
// when its read ends; an abandoned read's claim expires on the database clock.
func (server *Server) configureReads(config Config, store *controlStore) error {
	if strings.TrimSpace(config.MaterializationKeyFile) == "" {
		return nil
	}
	key, err := os.ReadFile(config.MaterializationKeyFile)
	if err != nil {
		return fmt.Errorf("read materialization key: %w", err)
	}
	verifier, err := output.NewReadWarrantVerifier(key, output.ClockFunc(nowUTC))
	if err != nil {
		return err
	}
	spent, err := openSpentReads(store, nowUTC)
	if err != nil {
		return err
	}
	server.reads = &managedReads{verifier: verifier, spent: spent, timeout: config.OperationTimeout}
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
	// The warrant names its node; on any other it opens nothing.
	if err != nil || claims.NodeUID == "" || claims.NodeUID != server.daemon.nodeUID {
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
	if err := server.reads.spent.begin(claims); err != nil {
		readRefusal(w, err)
		return
	}
	tree, attributes, err := server.stageRead(ctx, claims)
	// The private copy is complete and verified, or the read failed: either
	// way this warrant's read is over unless the failure is a retryable one.
	if spendErr := server.reads.spent.end(claims, err); spendErr != nil && err == nil {
		err = spendErr
		_ = tree.Close()
	}
	if err != nil {
		readRefusal(w, err)
		return
	}
	defer tree.Close()
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

// Stage under the warrant and verify the complete canonical tree. The returned
// private copy survives reclamation of the object.
//
// The warrant is verified -- binding and window -- by the route before this is
// called.
func (server *Server) stageRead(ctx context.Context, claims output.ReadWarrantClaims) (tree *hangar.CapturedTree, attributes hangar.TreeAttributes, err error) {
	defer func() {
		if err != nil && tree != nil {
			_ = tree.Close()
			tree = nil
		}
	}()
	archive, object, err := server.daemon.OpenRead(ctx, claims)
	if err != nil {
		return nil, attributes, err
	}
	attributes = object.Attributes
	tree, err = server.daemon.canonicalizer.Capture(ctx, archive)
	err = errors.Join(err, archive.Close())
	if err != nil {
		return tree, attributes, err
	}
	if tree.Digest != claims.Ref.Digest || tree.ByteSize != attributes.LogicalBytes {
		return tree, attributes, output.ErrCorrupt
	}
	return tree, attributes, nil
}
