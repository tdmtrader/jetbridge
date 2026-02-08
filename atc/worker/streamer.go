package worker

import (
	"archive/tar"
	"context"
	"io"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/hashicorp/go-multierror"
)

type Streamer struct {
	compression compression.Compression
	limitInMB   float64

	resourceCacheFactory db.ResourceCacheFactory
}

func NewStreamer(cacheFactory db.ResourceCacheFactory, compression compression.Compression, limitInMB float64) Streamer {
	return Streamer{
		resourceCacheFactory: cacheFactory,
		compression:          compression,
		limitInMB:            limitInMB,
	}
}

func (s Streamer) Stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	loggerData := lager.Data{
		"to":          dst.DBVolume().WorkerName(),
		"to-handle":   dst.Handle(),
		"from":        src.Source(),
		"from-handle": src.Handle(),
	}
	logger := lagerctx.FromContext(ctx).Session("stream", loggerData)
	logger.Info("start")
	defer logger.Info("end")

	err := s.stream(ctx, src, dst)
	if err != nil {
		return err
	}

	srcVolume, isSrcVolume := src.(runtime.Volume)
	if !isSrcVolume {
		return nil
	}

	metric.Metrics.VolumesStreamed.Inc()

	resourceCacheID := srcVolume.DBVolume().GetResourceCacheID()
	if atc.EnableCacheStreamedVolumes && resourceCacheID != 0 {
		logger.Debug("initialize-streamed-resource-cache", lager.Data{"resource-cache-id": resourceCacheID})
		usedResourceCache, found, err := s.resourceCacheFactory.FindResourceCacheByID(resourceCacheID)
		if err != nil {
			logger.Error("stream-to-failed-to-find-resource-cache", err)
			return err
		}
		if !found {
			logger.Info("stream-resource-cache-not-found-should-not-happen", lager.Data{
				"resource-cache-id": resourceCacheID,
				"volume":            srcVolume.Handle(),
			})
			return StreamingResourceCacheNotFoundError{
				Handle:          srcVolume.Handle(),
				ResourceCacheID: resourceCacheID,
			}
		}

		_, err = dst.InitializeStreamedResourceCache(ctx,
			usedResourceCache,
			srcVolume.DBVolume().WorkerResourceCacheID())
		if err != nil {
			logger.Error("failed-to-init-resource-cache-on-dest-worker", err)
			return err
		}

		metric.Metrics.StreamedResourceCaches.Inc()
	}
	return nil
}

func (s Streamer) stream(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	return s.streamThroughATC(ctx, src, dst)
}

func (s Streamer) streamThroughATC(ctx context.Context, src runtime.Artifact, dst runtime.Volume) error {
	traceAttrs := tracing.Attrs{
		"dest-worker": dst.DBVolume().WorkerName(),
	}
	if srcVolume, ok := src.(runtime.Volume); ok {
		traceAttrs["origin-volume"] = srcVolume.Handle()
		traceAttrs["origin-worker"] = srcVolume.DBVolume().WorkerName()
	}
	out, err := src.StreamOut(ctx, ".", s.compression)

	if err != nil {
		return err
	}

	defer out.Close()

	return dst.StreamIn(ctx, ".", s.compression, s.limitInMB, out)
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
