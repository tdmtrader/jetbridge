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
		return nil, err
	}
	tarReader := tar.NewReader(compressionReader)

	_, err = tarReader.Next()
	if err != nil {
		return nil, err
	}

	return fileReadMultiCloser{
		Reader: tarReader,
		closers: []io.Closer{
			out,
			compressionReader,
		},
	}, nil
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
