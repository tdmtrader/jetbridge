package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"unicode/utf8"
)

// maxJSONDocumentBytes bounds every strict JSON document a snapshot tree can
// carry. It is a var rather than a const only so tests can drive its boundary
// with a candidate small enough to build; production never assigns it.
var maxJSONDocumentBytes int64 = 1 << 20

func decodeStrictDocument(ctx context.Context, root *os.Root, name string, target any) error {
	contents, err := readRegularFile(ctx, root, name, maxJSONDocumentBytes)
	if err != nil {
		return err
	}
	return decodeStrictJSONBytes(name, contents, target)
}

// admitStrictDocument is decodeStrictDocument plus the SEAL-ONLY text-encoding
// gate. Only seal-time entry points call it.
func admitStrictDocument(ctx context.Context, root *os.Root, name string, target any) error {
	contents, err := readRegularFile(ctx, root, name, maxJSONDocumentBytes)
	if err != nil {
		return err
	}
	if err := admitRecordTextEncoding(name, contents); err != nil {
		return err
	}
	return decodeStrictJSONBytes(name, contents, target)
}

// decodeStrictJSONBytes is the decode half of decodeStrictDocument, split out so
// the seal path can see the bytes before decoding. It holds the strict-decode
// body verbatim: DisallowUnknownFields, the trailing-JSON check, and the same two
// error strings. It is named decodeStrictJSONBytes rather than decodeStrictJSON
// because schema_document_load.go already owns a decodeStrictJSON with a
// different signature.
func decodeStrictJSONBytes(name string, contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("snapshot contracts: decode %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("snapshot contracts: %s contains trailing JSON", name)
		}
		return fmt.Errorf("snapshot contracts: decode trailing data in %s: %w", name, err)
	}
	return nil
}

// decodeExactJSONDocument decodes bytes into exactly one closed shape: unknown
// fields and trailing JSON are both refused. Callers that try more than one
// shape rely on that strictness — it is what keeps a multi-shape read from
// degenerating into accepting anything.
func decodeExactJSONDocument[T any](raw []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		var zero T
		return zero, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var zero T
		if err == nil {
			return zero, fmt.Errorf("document contains trailing JSON")
		}
		return zero, fmt.Errorf("decode trailing data: %w", err)
	}
	return value, nil
}

// admitRecordTextEncoding is the SEAL-ONLY text-encoding gate over the exact
// bytes of a candidate document.
//
// It applies the two predicates canonical JSON already applies to a schema
// document: the bytes must be valid UTF-8, and they must carry no unpaired
// surrogate escape. Both are byte-level on purpose. Go's encoding/json
// SANITIZES rather than refuses — a raw 0xff inside a string literal and a lone
// \ud800 escape both decode to U+FFFD with a nil error — so a utf8.ValidString
// check on a decoded body field could never fire, and the only door the rule can
// stand at is this one, on the bytes.
//
// THE TWO-GATE ARGUMENT, which is why this tightening needs no descriptor bump,
// no history entry and no data migration:
//
//   - It is the same predicate canonical JSON already enforces, for the reason
//     stated there: replacing a bad byte with U+FFFD maps two distinct inputs to
//     one canonical form. This moves it to the remaining door a producer writes
//     through.
//   - It runs at ADMISSION only. RevalidateSealed and every stored-record reader
//     are untouched, so every record already in the corpus reads exactly as it
//     read before — including a hypothetical record whose bytes are not valid
//     UTF-8. A gate that rejected stored bytes on read would be the
//     descriptor-digest data-loss class of change, and it is forbidden.
//   - A candidate refused here was never a sealed record, so no digest moves and
//     no schema document changes. This is a Go gate rule, not a declared field
//     rule: writing it into a schema document would change that document's
//     canonical bytes, and a rev2 file's bytes are a frozen descriptor.
func admitRecordTextEncoding(name string, contents []byte) error {
	if !utf8.Valid(contents) {
		return fmt.Errorf(
			"snapshot contracts: %s is not valid UTF-8; replacing a bad byte with U+FFFD would map two distinct inputs to one record",
			name,
		)
	}
	if err := rejectUnpairedSurrogateEscapes(contents); err != nil {
		return fmt.Errorf("snapshot contracts: %s: %w", name, err)
	}
	return nil
}

func readRegularFile(ctx context.Context, root *os.Root, name string, limit int64) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("snapshot contracts: snapshot root is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("snapshot contracts: required regular file %q is missing", name)
		}
		return nil, fmt.Errorf("snapshot contracts: inspect %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("snapshot contracts: %q must be a regular file", name)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("snapshot contracts: %q exceeds size limit of %d bytes", name, limit)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("snapshot contracts: open %q: %w", name, err)
	}
	openedInfo, statErr := file.Stat()
	if statErr == nil && (!openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo)) {
		statErr = fmt.Errorf("snapshot contracts: %q changed while opening or is not a regular file", name)
	}
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("snapshot contracts: read %q: %w", name, err)
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("snapshot contracts: %q exceeds size limit of %d bytes", name, limit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return contents, nil
}
