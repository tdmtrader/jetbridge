package main

// Publishing a sealed source.
//
// The route hands over no bytes. That is the strongest form of "no caller
// chooses where an object goes": the daemon reads the sealed incarnation it is
// already holding, canonicalizes it with the foundation's own canonicalizer,
// and stores it at a key derived from the namespace it resolved from
// authenticated configuration and the active epoch. There is nothing in the
// request a caller could point somewhere else, and the one field that could
// name a location -- PublicationRequest.Namespace -- is refused with a message
// rather than ignored.
//
// The seal is a precondition and not a convention. SealedIncarnation refuses
// anything that is not `sealed`, so a canonical read cannot begin over bytes a
// writer may still be changing.

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// PublishSealedTree is the publish route's whole body.
func (server *Server) PublishSealedTree(ctx context.Context,
	request output.PublicationRequest) (output.PublicationResult, error) {
	if err := request.Validate(); err != nil {
		return output.PublicationResult{}, err
	}
	if request.ActivationEpoch != server.daemon.Namespace().ActivationEpoch() {
		return output.PublicationResult{}, fmt.Errorf(
			"%w: the publication names epoch %d and this daemon publishes under %d",
			output.ErrConflict, request.ActivationEpoch,
			server.daemon.Namespace().ActivationEpoch())
	}

	root, record, err := server.source.SealedIncarnation(
		request.HandoffID, request.Execution, request.ActivationEpoch)
	if err != nil {
		return output.PublicationResult{}, err
	}

	captured, err := server.daemon.CanonicalizeDirectory(ctx, root)
	if err != nil {
		return output.PublicationResult{}, err
	}
	defer captured.Close()

	archive, err := os.Open(captured.ArchivePath)
	if err != nil {
		return output.PublicationResult{}, fmt.Errorf(
			"%w: opening the canonical archive: %v", output.ErrInfrastructure, err)
	}
	defer archive.Close()

	namespace := server.daemon.Namespace()
	reservation := output.ResolvedReservation{
		ReservationID:   request.ReservationID,
		Execution:       request.Execution,
		ActivationEpoch: request.ActivationEpoch,
		HandoffID:       request.HandoffID,
		CaptureFence:    request.CaptureFence,
		// Server-derived, both of them. The scope comes from the namespace and
		// the digest from the bytes; neither is anywhere in the request.
		Scope:  namespace.Scope(),
		Digest: captured.Digest,
		Marker: namespace.MarkerFor(request.ReservationID, captured.Digest,
			output.NewTimestamp(nowUTC())),
	}

	object, err := server.daemon.Publish(ctx, PublishRequest{
		Namespace:   request.Namespace,
		Reservation: reservation,
	}, archive, captured.ByteSize)
	if err != nil {
		return output.PublicationResult{}, err
	}
	_ = record

	result := output.PublicationResult{
		ProtocolVersion: output.ProtocolVersion,
		Ref:             object.Attributes.Ref,
		Attributes:      output.AttributesFromFoundation(object.Attributes),
		MarkerVersion:   object.Marker.Version,
		ReservationID:   object.Marker.ReservationID,
		Deduplicated:    object.Deduplicated,
		Metageneration:  object.Metageneration,
	}

	return result, result.Validate()
}

// CanonicalizeSealedTree answers the logical identity of the sealed tree
// without creating anything.
//
// This is the first half of an ordering requirement 21 states and the publish
// route alone cannot express: the capture owner must durably resolve its
// reservation to the server-derived scope and logical digest AFTER
// canonicalization and BEFORE the first object create, so that every
// possibly-created object has a pre-existing reservation recovery and
// inventory can correlate. A control plane driving only `publish` learns the
// digest at the same moment the object exists.
//
// The seal is a precondition here for the same reason it is one for publish:
// `SealedIncarnation` refuses anything that is not `sealed`, so a canonical
// read cannot begin over bytes a writer may still be changing (Req 15).
//
// The tree is canonicalized twice across the pair, and deliberately. The
// canonical form is deterministic -- that is the property the whole plane is
// built on -- so the publish re-deriving it is a re-derivation and not a
// second opinion, and the control plane compares the two answers rather than
// carrying a digest between calls. Handing the publish a caller-supplied
// digest would be exactly the caller-chosen key Req 7 forbids.
func (server *Server) CanonicalizeSealedTree(ctx context.Context,
	request output.PublicationRequest) (output.CanonicalizationResult, error) {
	if err := request.Validate(); err != nil {
		return output.CanonicalizationResult{}, err
	}
	if request.ActivationEpoch != server.daemon.Namespace().ActivationEpoch() {
		return output.CanonicalizationResult{}, fmt.Errorf(
			"%w: the canonicalization names epoch %d and this daemon publishes under %d",
			output.ErrConflict, request.ActivationEpoch,
			server.daemon.Namespace().ActivationEpoch())
	}

	root, _, err := server.source.SealedIncarnation(
		request.HandoffID, request.Execution, request.ActivationEpoch)
	if err != nil {
		return output.CanonicalizationResult{}, err
	}

	captured, err := server.daemon.CanonicalizeDirectory(ctx, root)
	if err != nil {
		return output.CanonicalizationResult{}, err
	}
	defer captured.Close()

	result := output.CanonicalizationResult{
		ProtocolVersion: output.ProtocolVersion,
		HandoffID:       request.HandoffID,
		ReservationID:   request.ReservationID,
		CaptureFence:    request.CaptureFence,
		Scope:           server.daemon.Namespace().Scope(),
		Digest:          captured.Digest,
		LogicalBytes:    captured.ByteSize,
		ObservedAt:      output.NewTimestamp(nowUTC()),
	}

	return result, result.Validate()
}

// CanonicalizeDirectory turns a sealed incarnation into canonical bytes.
//
// It tars the directory and hands the tar to the foundation's Canonicalizer,
// rather than canonicalizing here: the canonical form -- ordering, headers,
// modes, padding -- is the foundation's, and a second implementation of it
// would be a second answer to "what are these bytes". The intermediate tar is
// not the artifact; it is input to the one canonicalizer this repository has.
func (daemon *Daemon) CanonicalizeDirectory(ctx context.Context, root string) (*hangar.CapturedTree, error) {
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(tarDirectory(root, writer)) }()

	captured, err := daemon.canonicalizer.Capture(ctx, reader)
	if err != nil {
		_ = reader.CloseWithError(err)

		return nil, fmt.Errorf("%w: canonicalizing the sealed source: %v", output.ErrCorrupt, err)
	}

	return captured, nil
}

// tarDirectory writes a deterministic tar of a directory tree.
//
// Deterministic because the canonicalizer's digest is over what comes out of
// it, and a walk whose order depended on the filesystem would give one tree two
// digests on two nodes. Entries are sorted; times, uids and names are
// normalized. Anything that is not a regular file, a directory or a symlink is
// refused rather than skipped: silently dropping a device node or a socket
// would publish a tree that is not the one the producer wrote.
func tarDirectory(root string, out io.Writer) error {
	writer := tar.NewWriter(out)

	var walk func(string) error
	walk = func(relative string) error {
		entries, err := os.ReadDir(filepath.Join(root, relative))
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, entry := range entries {
			name := tarPath(relative, entry.Name())
			info, err := entry.Info()
			if err != nil {
				return err
			}

			switch {
			case info.IsDir():
				if err := writeHeader(writer, &tar.Header{
					Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755,
				}); err != nil {
					return err
				}
				if err := walk(name); err != nil {
					return err
				}
			case info.Mode()&fs.ModeSymlink != 0:
				target, err := os.Readlink(filepath.Join(root, name))
				if err != nil {
					return err
				}
				if err := writeHeader(writer, &tar.Header{
					Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777,
				}); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				mode := int64(0o644)
				if info.Mode()&0o111 != 0 {
					mode = 0o755
				}
				if err := writeHeader(writer, &tar.Header{
					Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: info.Size(),
				}); err != nil {
					return err
				}
				file, err := os.Open(filepath.Join(root, name))
				if err != nil {
					return err
				}
				_, err = io.Copy(writer, file)
				_ = file.Close()
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: %s is a %s; a sealed source holds regular files, "+
					"directories and symlinks", output.ErrCorrupt, name, info.Mode().Type())
			}
		}

		return nil
	}

	if err := walk(""); err != nil {
		_ = writer.Close()

		return err
	}

	return writer.Close()
}

// tarEpoch is the one modification time every entry carries.
var tarEpoch = time.Unix(0, 0).UTC()

func writeHeader(writer *tar.Writer, header *tar.Header) error {
	// Every varying field is pinned. The digest is over these bytes, so a
	// modification time or an owner name would make the same tree hash
	// differently on two nodes.
	//
	// AccessTime and ChangeTime are left ZERO rather than pinned: a non-zero
	// value makes the PAX writer emit atime/ctime records, and the foundation's
	// canonicalizer refuses those by name. They carry nothing a tree's identity
	// depends on, so the right pin for them is absence.
	header.ModTime = tarEpoch
	header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}
	header.Uid, header.Gid = 0, 0
	header.Uname, header.Gname = "", ""
	header.Format = tar.FormatPAX

	return writer.WriteHeader(header)
}

func tarPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return strings.TrimSuffix(prefix, "/") + "/" + name
}
