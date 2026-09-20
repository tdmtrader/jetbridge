package steps

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"reflect"

	"github.com/concourse/concourse/atc/compression"
)

// Keep caller-side bytes separate from passive wire observations. File
// extraction alone ignores tar padding and cannot establish stream fidelity.
func (set VolumeSet) requireVolumeBytes(name, member, kind string, expected []byte) error {
	if set.execTrace == nil || set.execTrace.wire == nil {
		return fmt.Errorf("volume bytes require real wire observation")
	}
	binding, ok := set.execTrace.bindings[name]
	if !ok {
		return fmt.Errorf("no binding for volume %q", name)
	}
	actual, err := set.execTrace.wire.take([]volumeExecBinding{binding}, []string{member}, []string{kind})
	if err != nil {
		return err
	}
	return requireSameVolumeBytes(actual[0], expected)
}

func readVolumeBytes(set VolumeSet, name, member string) VolumeRead {
	fail := func(err error) VolumeRead { return VolumeRead{Err: err, Message: err.Error()} }
	volume, err := set.volume(name)
	if err != nil {
		return fail(err)
	}
	var files map[string]string
	for _, encoding := range []compression.Compression{nil, compression.NewGzipCompression()} {
		stream, err := volume.StreamOut(set.Ctx, member, encoding)
		if err != nil {
			return fail(err)
		}
		if stream == nil {
			return fail(fmt.Errorf("volume read returned no stream"))
		}
		data, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil {
			return fail(readErr)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
		if encoding != nil {
			reader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return fail(err)
			}
			data, readErr = io.ReadAll(reader)
			closeErr = reader.Close()
			if readErr != nil {
				return fail(readErr)
			}
			if closeErr != nil {
				return fail(closeErr)
			}
		}
		if err := set.requireVolumeBytes(name, member, "stdout", data); err != nil {
			return fail(err)
		}
		current, err := filesInTar(bytes.NewReader(data))
		if err != nil {
			return fail(err)
		}
		if files != nil && !reflect.DeepEqual(current, files) {
			return fail(fmt.Errorf("raw and gzip volume reads disagree"))
		}
		files = current
	}
	return VolumeRead{Files: files}
}

func moveVolumeBytes(set VolumeSet, source, destination string) error {
	src, err := set.volume(source)
	if err != nil {
		return err
	}
	dst, err := set.volume(destination)
	if err != nil {
		return err
	}
	if set.execTrace == nil || set.execTrace.wire == nil {
		return fmt.Errorf("volume handoff requires real wire observation")
	}
	sourceBinding, sourceFound := set.execTrace.bindings[source]
	destinationBinding, destinationFound := set.execTrace.bindings[destination]
	if !sourceFound || !destinationFound || sourceBinding.pod == destinationBinding.pod {
		return fmt.Errorf("handoff requires two distinct real pods")
	}
	for _, encoding := range []compression.Compression{nil, compression.NewGzipCompression()} {
		stream, err := src.StreamOut(set.Ctx, ".", encoding)
		if err != nil {
			return err
		}
		if stream == nil {
			return fmt.Errorf("source returned no stream")
		}
		writeErr := dst.StreamIn(set.Ctx, ".", encoding, 0, stream)
		closeErr := stream.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual, err := set.execTrace.wire.take([]volumeExecBinding{sourceBinding, destinationBinding}, []string{".", "."}, []string{"stdout", "stdin"})
		if err != nil {
			return err
		}
		if err := requireSameVolumeBytes(actual[1], actual[0]); err != nil {
			return fmt.Errorf("volume handoff: %w", err)
		}
	}
	return nil
}
