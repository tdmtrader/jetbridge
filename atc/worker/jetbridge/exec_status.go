package jetbridge

import (
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
		return n, fmt.Errorf("exec connection closed without a remote exit status: %w", io.ErrUnexpectedEOF)
	}
	return n, err
}
