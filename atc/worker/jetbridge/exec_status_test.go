package jetbridge

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/httpstream"
)

// A stream that hands out its scripted reads, then EOF. Literal bytes are the
// whole input to the policy under test; no transport is simulated.
type scriptedStream struct {
	httpstream.Stream
	reads [][]byte
}

func (s *scriptedStream) Read(p []byte) (int, error) {
	if len(s.reads) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.reads[0])
	s.reads = s.reads[1:]
	return n, nil
}

func TestExecStatusStreamDistinguishesSilenceFromExitZero(t *testing.T) {
	t.Run("EOF with nothing received is not a status", func(t *testing.T) {
		s := &execStatusStream{Stream: &scriptedStream{}}
		n, err := s.Read(make([]byte, 16))
		if n != 0 || err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("Read = (%d, %v), want (0, ...%v)", n, err, io.ErrUnexpectedEOF)
		}
		// client-go's own wrapping (tools/remotecommand/errorstream.go),
		// verbatim, then the executor's. Both are %w, so the sentinel
		// survives to errors.Is; the text is not the contract.
		wrapped := fmt.Errorf("exec stream: %w", fmt.Errorf("error reading from error stream: %w", err))
		if !isExecStatusMissing(wrapped) {
			t.Fatalf("%q not recognised as a missing exit status", wrapped)
		}
		if isExecStatusMissing(errors.New(errExecStatusMissing.Error())) {
			t.Fatal("an unrelated error with the same text must not be recognised")
		}
	})
	t.Run("EOF after a status is the ordinary end of the stream", func(t *testing.T) {
		s := &execStatusStream{Stream: &scriptedStream{reads: [][]byte{[]byte(`{"status":"Success"}`)}}}
		buf := make([]byte, 64)
		n, err := s.Read(buf)
		if err != nil || !strings.Contains(string(buf[:n]), "Success") {
			t.Fatalf("first Read = (%d, %v)", n, err)
		}
		if n, err := s.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("Read after status = (%d, %v), want (0, EOF)", n, err)
		}
	})
	t.Run("other errors are not a missing status", func(t *testing.T) {
		for _, err := range []error{nil, io.EOF, errors.New("unable to upgrade connection"), &ExecExitError{ExitCode: 2}} {
			if isExecStatusMissing(err) {
				t.Errorf("%v misread as a missing exit status", err)
			}
		}
	})
}

func TestStatusCheckingConnectionWrapsOnlyTheErrorStream(t *testing.T) {
	conn := statusCheckingConnection{recordingConnection{}}
	for _, tc := range []struct {
		streamType string
		wrapped    bool
	}{{"error", true}, {"stdout", false}, {"stdin", false}, {"stderr", false}} {
		headers := http.Header{}
		headers.Set("streamType", tc.streamType)
		stream, err := conn.CreateStream(headers)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := stream.(*execStatusStream); ok != tc.wrapped {
			t.Errorf("%s stream wrapped=%t, want %t", tc.streamType, ok, tc.wrapped)
		}
	}
}

type recordingConnection struct{ httpstream.Connection }

func (recordingConnection) CreateStream(http.Header) (httpstream.Stream, error) {
	return &scriptedStream{}, nil
}
