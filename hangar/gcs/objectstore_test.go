package gcs

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/concourse/concourse/hangar/objectstore"
	"google.golang.org/api/googleapi"
)

func TestExactOperationsRejectUnpinnedGenerationsBeforeAccessingStorage(t *testing.T) {
	// A nil SDK client makes an accidental request fail immediately. Invalid
	// exact generations must never turn into reads of the current object.
	client := outputObjectClient{}
	for _, generation := range []int64{0, -1} {
		if _, err := client.StatExact(context.Background(), "bucket", "key", generation); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("StatExact(%d): %v", generation, err)
		}
		if _, err := client.OpenExact(context.Background(), "bucket", "key", generation); !errors.Is(err, objectstore.ErrPreconditionFailed) {
			t.Fatalf("OpenExact(%d): %v", generation, err)
		}
	}
}

func TestDownloadErrorsStayTypedAfterTheReaderHasOpened(t *testing.T) {
	cause := &googleapi.Error{Code: 403, Message: "permission revoked"}
	reader := translatedReader{ReadCloser: failingReader{err: cause}}
	_, err := reader.Read(make([]byte, 1))
	if !errors.Is(err, objectstore.ErrUnauthorized) || !errors.Is(err, cause) {
		t.Fatalf("read error lost classification or cause: %v", err)
	}
	if err := reader.Close(); !errors.Is(err, objectstore.ErrUnauthorized) {
		t.Fatalf("close error lost classification: %v", err)
	}
	eof := translatedReader{ReadCloser: failingReader{err: io.EOF}}
	if _, err := eof.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("EOF became a storage failure: %v", err)
	}
}

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }
func (reader failingReader) Close() error             { return reader.err }
