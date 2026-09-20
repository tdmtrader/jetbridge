package jetbridge

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/transport/spdy"
)

// client-go treats an empty error stream as success, including when the
// underlying socket disappears. Protocols v4/v5 require an explicit Status
// even for exit zero. Check the real stream, leaving decoding and nonzero
// exit handling to client-go. Older protocols legitimately use empty success.
type statusCheckingUpgrader struct{ spdy.Upgrader }

// errExecStatusMissing is the sentinel the error stream returns when it
// closed with nothing on it. client-go wraps it with %w ("error reading from
// error stream: %w", tools/remotecommand/errorstream.go) and so does our
// executor, so the chain survives to errors.Is. io.ErrUnexpectedEOF is kept
// alongside it because any truncated stream carries that, and callers that
// only know the standard sentinel still see the truncation.
var errExecStatusMissing = errors.New("exec connection closed without a remote exit status")

// isExecStatusMissing reports whether err is the transport saying the command
// never delivered an exit status: the stream broke, the pod went, or the
// kubelet's side closed before writing one. It is NOT a command exit.
func isExecStatusMissing(err error) bool {
	return errors.Is(err, errExecStatusMissing)
}

func (u statusCheckingUpgrader) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	conn, err := u.Upgrader.NewConnection(resp)
	if err != nil {
		return nil, err
	}
	switch resp.Header.Get(httpstream.HeaderProtocolVersion) {
	case remotecommand.StreamProtocolV4Name, remotecommand.StreamProtocolV5Name:
		return statusCheckingConnection{conn}, nil
	default:
		return conn, nil
	}
}

type statusCheckingConnection struct{ httpstream.Connection }

func (c statusCheckingConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	stream, err := c.Connection.CreateStream(headers)
	if err != nil {
		return nil, err
	}
	if headers.Get(corev1.StreamType) == corev1.StreamTypeError {
		return &execStatusStream{Stream: stream}, nil
	}
	return stream, nil
}

type execStatusStream struct {
	httpstream.Stream
	received bool
}

func (s *execStatusStream) Read(p []byte) (int, error) {
	n, err := s.Stream.Read(p)
	s.received = s.received || n > 0
	if err == io.EOF && !s.received {
		return n, fmt.Errorf("%w: %w", errExecStatusMissing, io.ErrUnexpectedEOF)
	}
	return n, err
}
