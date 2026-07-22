package worker

import (
	"archive/tar"
	"context"
	"io"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/hashicorp/go-multierror"
)

type Streamer struct {
	compression compression.Compression
}

func NewStreamer(compression compression.Compression) Streamer {
	return Streamer{
		compression: compression,
	}
}

func (s Streamer) StreamFile(ctx context.Context, artifact runtime.Artifact, path string) (io.ReadCloser, error) {
	out, err := artifact.StreamOut(ctx, path, s.compression)
	if err != nil {
		return nil, err
	}

	compressionReader, err := s.compression.NewReader(out)
	if err != nil {
		_ = out.Close()
		return nil, err
	}
	tarReader := tar.NewReader(compressionReader)

	_, err = tarReader.Next()
	if err != nil {
		if err == io.EOF {
			if terminalErr := drainTarTerminal(tarReader, compressionReader, out); terminalErr != nil {
				err = terminalErr
			}
		}
		_ = compressionReader.Close()
		_ = out.Close()
		return nil, err
	}

	return fileReadMultiCloser{
		Reader: &terminalTarEntryReader{
			tar:     tarReader,
			decoded: compressionReader,
			encoded: out,
		},
		closers: []io.Closer{
			out,
			compressionReader,
		},
	}, nil
}

type terminalErrorStream interface {
	TerminalError() error
}

type terminalTarEntryReader struct {
	tar      *tar.Reader
	decoded  io.Reader
	encoded  io.Reader
	finished bool
}

func (reader *terminalTarEntryReader) Read(buffer []byte) (int, error) {
	if reader.finished {
		return 0, io.EOF
	}
	n, err := reader.tar.Read(buffer)
	if err != io.EOF {
		return n, err
	}
	reader.finished = true
	if terminalErr := drainTarTerminal(reader.tar, reader.decoded, reader.encoded); terminalErr != nil {
		return n, terminalErr
	}
	return n, io.EOF
}

func drainTarTerminal(tarReader *tar.Reader, decoded io.Reader, encoded io.Reader) error {
	for {
		_, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tarReader); err != nil {
			return err
		}
	}
	if _, err := io.Copy(io.Discard, decoded); err != nil {
		return err
	}
	if stream, ok := encoded.(terminalErrorStream); ok {
		return stream.TerminalError()
	}
	return nil
}

type fileReadMultiCloser struct {
	io.Reader
	closers []io.Closer
}

func (frc fileReadMultiCloser) Close() error {
	var closeErrors error

	for _, closer := range frc.closers {
		err := closer.Close()
		if err != nil {
			closeErrors = multierror.Append(closeErrors, err)
		}
	}

	return closeErrors
}
