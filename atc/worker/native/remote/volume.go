package remote

import (
	"context"
	"fmt"
	"io"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/native/agentpb"
)

var _ runtime.Volume = (*Volume)(nil)

const streamInChunkSize = 32 * 1024 // 32KB per StreamIn data chunk

// Volume implements runtime.Volume for remote native workers. StreamIn proxies
// data to the remote agent via gRPC. StreamOut is not yet implemented (Phase 3).
type Volume struct {
	handle      string
	workerName  string
	dbVolume    db.CreatedVolume
	client      agentpb.NativeAgentClient // for StreamIn/StreamOut proxy
	containerID string                     // which container this volume belongs to
}

func NewVolume(handle, workerName string, dbVolume db.CreatedVolume) *Volume {
	return &Volume{
		handle:     handle,
		workerName: workerName,
		dbVolume:   dbVolume,
	}
}

// NewStreamableVolume creates a Volume with a gRPC client and container ID for
// StreamIn/StreamOut proxy support. Used for volumes that need to stream data
// to or from the remote agent (inputs and outputs).
func NewStreamableVolume(handle, workerName string, client agentpb.NativeAgentClient, containerID string) *Volume {
	return &Volume{
		handle:      handle,
		workerName:  workerName,
		client:      client,
		containerID: containerID,
	}
}

func (v *Volume) Handle() string {
	if v.dbVolume != nil {
		return v.dbVolume.Handle()
	}
	return v.handle
}

func (v *Volume) Source() string {
	if v.dbVolume != nil {
		return v.dbVolume.WorkerName()
	}
	return v.workerName
}

func (v *Volume) DBVolume() db.CreatedVolume {
	return v.dbVolume
}

func (v *Volume) StreamIn(ctx context.Context, path string, enc compression.Compression, limitInMB float64, reader io.Reader) error {
	if v.client == nil {
		return fmt.Errorf("remote volume StreamIn: no gRPC client configured")
	}

	// Note: limitInMB is not enforced remotely in Phase 2.
	stream, err := v.client.StreamIn(ctx)
	if err != nil {
		return fmt.Errorf("remote volume StreamIn: open stream: %w", err)
	}

	// Send meta first.
	encoding := ""
	if enc != nil {
		encoding = string(enc.Encoding())
	}
	err = stream.Send(&agentpb.StreamInMessage{
		Message: &agentpb.StreamInMessage_Meta{
			Meta: &agentpb.StreamInMeta{
				Path:        path,
				Encoding:    encoding,
				ContainerId: v.containerID,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("remote volume StreamIn: send meta: %w", err)
	}

	// Stream data in chunks.
	buf := make([]byte, streamInChunkSize)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := stream.Send(&agentpb.StreamInMessage{
				Message: &agentpb.StreamInMessage_Data{Data: chunk},
			}); err != nil {
				return fmt.Errorf("remote volume StreamIn: send data: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("remote volume StreamIn: read source: %w", readErr)
		}
	}

	_, err = stream.CloseAndRecv()
	if err != nil {
		return fmt.Errorf("remote volume StreamIn: close: %w", err)
	}

	return nil
}

func (v *Volume) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	if v.client == nil {
		return nil, fmt.Errorf("remote volume StreamOut: no gRPC client configured")
	}

	encoding := ""
	if enc != nil {
		encoding = string(enc.Encoding())
	}

	stream, err := v.client.StreamOut(ctx, &agentpb.StreamOutRequest{
		ContainerId: v.containerID,
		Path:        path,
		Encoding:    encoding,
	})
	if err != nil {
		return nil, fmt.Errorf("remote volume StreamOut: %w", err)
	}

	return &grpcChunkReader{stream: stream}, nil
}

// grpcChunkReader wraps a NativeAgent_StreamOutClient as an io.ReadCloser.
// Read() calls stream.Recv() for chunks, buffering leftover bytes.
type grpcChunkReader struct {
	stream agentpb.NativeAgent_StreamOutClient
	buf    []byte
}

func (r *grpcChunkReader) Read(p []byte) (int, error) {
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}

	chunk, err := r.stream.Recv()
	if err != nil {
		return 0, err // io.EOF when stream ends
	}

	n := copy(p, chunk.Data)
	if n < len(chunk.Data) {
		r.buf = chunk.Data[n:]
	}
	return n, nil
}

func (r *grpcChunkReader) Close() error {
	return nil
}

func (v *Volume) InitializeResourceCache(ctx context.Context, cache db.ResourceCache) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume != nil {
		return v.dbVolume.InitializeResourceCache(cache)
	}
	return nil, nil
}

func (v *Volume) InitializeStreamedResourceCache(ctx context.Context, cache db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume != nil {
		return v.dbVolume.InitializeStreamedResourceCache(cache, sourceWorkerResourceCacheID)
	}
	return nil, nil
}

func (v *Volume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error {
	if v.dbVolume != nil {
		return v.dbVolume.InitializeTaskCache(jobID, stepName, path)
	}
	return nil
}
