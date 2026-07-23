package exec

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/runtime"
)

const (
	typedOutputMarkerOutputName = "__jetbridge_typed_output_markers_v1"
	typedOutputMarkerMountPath  = "/tmp/.jetbridge/typed-output-markers/v1"
	typedOutputMarkerEnvName    = "JETBRIDGE_OPTIONAL_OUTPUT_MARKERS"
)

func hasOptionalSnapshotOutputs(outputs map[string]atc.SnapshotOutputConfig) bool {
	for _, output := range outputs {
		if output.Optional {
			return true
		}
	}
	return false
}

// configureOptionalOutputMarkers exposes a control mount that is deliberately
// separate from every output mount. A producer marks an optional output as
// present by creating the empty file named in the JSON environment mapping.
// Keeping the marker outside the output prevents control bytes from becoming
// part of the immutable snapshot.
func configureOptionalOutputMarkers(spec *runtime.ContainerSpec, outputNames []string) error {
	if len(outputNames) == 0 {
		return nil
	}
	if spec.Outputs == nil {
		spec.Outputs = runtime.OutputPaths{}
	}
	if _, found := spec.Outputs[typedOutputMarkerOutputName]; found {
		return fmt.Errorf("reserved typed-output marker name %q conflicts with a declared output", typedOutputMarkerOutputName)
	}
	for name, outputPath := range spec.Outputs {
		if filePathsOverlap(outputPath, typedOutputMarkerMountPath) {
			return fmt.Errorf("declared output %q path conflicts with the reserved typed-output marker mount", name)
		}
	}
	for _, variable := range spec.Env {
		if strings.HasPrefix(variable, typedOutputMarkerEnvName+"=") {
			return fmt.Errorf("reserved environment variable %s is already defined", typedOutputMarkerEnvName)
		}
	}

	markers := make(map[string]string, len(outputNames))
	for _, name := range outputNames {
		marker := base64.RawURLEncoding.EncodeToString([]byte(name))
		markers[name] = filepath.Join(typedOutputMarkerMountPath, marker)
	}
	encoded, err := json.Marshal(markers)
	if err != nil {
		return fmt.Errorf("encode optional typed-output markers: %w", err)
	}
	spec.Env = append(spec.Env, typedOutputMarkerEnvName+"="+string(encoded))
	spec.Outputs[typedOutputMarkerOutputName] = ensureTrailingSlash(typedOutputMarkerMountPath)
	return nil
}

func filePathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if filepath.IsAbs(left) != filepath.IsAbs(right) {
		return false
	}
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func optionalOutputMarkerArtifact(worker runtime.Worker, volumeMounts []runtime.VolumeMount, required bool) (runtime.Artifact, error) {
	if !required {
		return nil, nil
	}
	var artifact runtime.Artifact
	matches := 0
	for _, mount := range volumeMounts {
		if filepath.Clean(mount.MountPath) != filepath.Clean(typedOutputMarkerMountPath) {
			continue
		}
		matches++
		if matches == 1 {
			artifact = worker.ArtifactFromVolume(mount.Volume)
		}
	}
	if matches == 0 {
		return nil, fmt.Errorf("optional typed-output marker mount is missing")
	}
	if matches > 1 {
		return nil, fmt.Errorf("optional typed-output marker mount is duplicated")
	}
	if artifact == nil {
		return nil, fmt.Errorf("optional typed-output marker artifact is unavailable")
	}
	return artifact, nil
}

func optionalOutputWasProduced(ctx context.Context, markerArtifact runtime.Artifact, outputName string) (bool, error) {
	markerName := base64.RawURLEncoding.EncodeToString([]byte(outputName))
	stream, err := markerArtifact.StreamOut(ctx, markerName, compression.NewGzipCompression())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read optional typed-output marker: %w", err)
	}
	defer stream.Close()

	uncompressed, err := gzip.NewReader(stream)
	if err != nil {
		return false, fmt.Errorf("decode optional typed-output marker: %w", err)
	}
	defer uncompressed.Close()

	reader := tar.NewReader(uncompressed)
	header, err := reader.Next()
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read optional typed-output marker archive: %w", err)
	}
	if filepath.Clean(header.Name) != filepath.Clean(markerName) ||
		header.Typeflag != tar.TypeReg || header.Size != 0 {
		return false, fmt.Errorf("optional typed-output marker for %q is malformed", outputName)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		if err == nil {
			return false, fmt.Errorf("optional typed-output marker for %q contains multiple entries", outputName)
		}
		return false, fmt.Errorf("read optional typed-output marker archive: %w", err)
	}
	return true, nil
}

func materializeSealedSnapshotArtifact(
	ctx context.Context,
	teamID int,
	ref snapshot.SnapshotRef,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
) (runtime.Artifact, error) {
	if metadata == nil || content == nil {
		return nil, fmt.Errorf("snapshot metadata and content stores are required")
	}
	manifest, found, err := metadata.GetAuthorized(ctx, teamID, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("load sealed snapshot metadata: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("sealed snapshot is unavailable")
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("sealed snapshot metadata is invalid")
	}
	if manifest.ID != ref.ID || manifest.Type != ref.Type || manifest.Digest != ref.Digest {
		return nil, fmt.Errorf("sealed snapshot metadata does not match the committed immutable reference")
	}
	artifact, err := runtime.NewSnapshotArtifact(manifest, content)
	if err != nil {
		return nil, fmt.Errorf("materialize sealed snapshot: %w", err)
	}
	return artifact, nil
}
