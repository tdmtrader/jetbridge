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
)

const maxJSONDocumentBytes int64 = 1 << 20

func decodeStrictDocument(ctx context.Context, root *os.Root, name string, target any) error {
	contents, err := readRegularFile(ctx, root, name, maxJSONDocumentBytes)
	if err != nil {
		return err
	}
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
