package exec

import (
	"context"
	"fmt"
	"io"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/runtime"
)

// loadSidecarConfigs reads sidecar definition files from the build's
// artifacts and returns the parsed SidecarConfig entries. Each path follows
// the same "SOURCE/FILE" convention as the task config file. Inline sidecar
// sources are validated and added directly.
//
// Shared by the task and agent steps.
func loadSidecarConfigs(ctx context.Context, logger lager.Logger, repo *build.Repository, streamer Streamer, sources []atc.SidecarSource) ([]atc.SidecarConfig, error) {
	if len(sources) == 0 {
		return nil, nil
	}

	var all []atc.SidecarConfig
	seen := make(map[string]bool)

	for _, src := range sources {
		if src.Config != nil {
			// Inline sidecar config — validate and add directly
			sc := *src.Config
			if err := sc.Validate(); err != nil {
				return nil, fmt.Errorf("inline sidecar: %w", err)
			}
			if atc.IsReservedContainerName(sc.Name) {
				return nil, fmt.Errorf("inline sidecar: reserved container name %q", sc.Name)
			}
			if seen[sc.Name] {
				return nil, fmt.Errorf("inline sidecar: duplicate sidecar name %q", sc.Name)
			}
			seen[sc.Name] = true
			all = append(all, sc)
			continue
		}

		sidecarPath := src.File
		segs := strings.SplitN(sidecarPath, "/", 2)
		if len(segs) != 2 {
			return nil, fmt.Errorf("sidecar path '%s' must be in the format SOURCE/FILE", sidecarPath)
		}

		sourceName := build.ArtifactName(segs[0])
		filePath := segs[1]

		artifact, _, found := repo.ArtifactFor(sourceName)
		if !found {
			return nil, fmt.Errorf("sidecar file '%s': unknown artifact source '%s'", sidecarPath, sourceName)
		}

		stream, err := streamer.StreamFile(lagerctx.NewContext(ctx, logger), artifact, filePath)
		if err != nil {
			return nil, fmt.Errorf("sidecar file '%s': %w", sidecarPath, err)
		}
		defer stream.Close()

		data, err := io.ReadAll(stream)
		if err != nil {
			return nil, fmt.Errorf("sidecar file '%s': %w", sidecarPath, err)
		}

		configs, err := atc.ParseSidecarConfigs(data)
		if err != nil {
			return nil, fmt.Errorf("sidecar file '%s': %w", sidecarPath, err)
		}

		for _, sc := range configs {
			if seen[sc.Name] {
				return nil, fmt.Errorf("sidecar file '%s': duplicate sidecar name %q", sidecarPath, sc.Name)
			}
			seen[sc.Name] = true
		}

		all = append(all, configs...)
	}

	return all, nil
}

// resolveSidecarImages resolves sidecar image artifact references from the
// build repository and pins registry image references to digests via OCI
// registry resolution (best-effort), operating on the slice in place.
//
// resolver may be nil, in which case digest pinning is skipped but the
// artifact-ref resolution (which needs only state) still runs.
//
// Shared by the task and agent steps.
func resolveSidecarImages(ctx context.Context, logger lager.Logger, state RunState, resolver imageresolver.Resolver, sidecars []atc.SidecarConfig) error {
	// Resolve sidecar image artifact references from the build repository
	for i, sc := range sidecars {
		if sc.ImageArtifact != "" {
			imageRef, found := state.ArtifactRepository().ImageRefFor(build.ArtifactName(sc.ImageArtifact))
			if !found {
				return fmt.Errorf("sidecar %q: image_artifact %q not found in artifact repository", sc.Name, sc.ImageArtifact)
			}
			sidecars[i].Image = imageRef
			sidecars[i].ImageArtifact = "" // resolved
		}
	}

	// Pin sidecar images to digests via OCI registry resolution (best-effort).
	// If resolution fails (e.g. auth not available for private registries),
	// fall through to the original tag-based reference and let the kubelet
	// pull it using pod-level imagePullSecrets.
	if resolver != nil {
		for i, sc := range sidecars {
			if sc.Image == "" || sc.ImageArtifact != "" {
				continue // skip artifact refs (already resolved) or empty
			}
			if strings.Contains(sc.Image, "@sha256:") {
				continue // already pinned
			}
			// Strip Concourse-internal prefixes before parsing so that
			// parseImageRef sees a bare image reference.
			bare := stripDockerPrefix(sc.Image)
			repo, tag := parseImageRef(bare)
			digest, err := resolver.Resolve(ctx, repo, tag, nil)
			if err != nil {
				logger.Info("sidecar-image-resolve-skipped", lager.Data{
					"sidecar": sc.Name,
					"image":   sc.Image,
					"reason":  err.Error(),
				})
				continue
			}
			sidecars[i].Image = repo + "@" + digest
		}
	}

	return nil
}

// sidecarProcessIO constructs a ProcessIO with per-sidecar writers when
// sidecars are present. Sidecar writers emit Log events with the sidecar's
// own plan ID as origin, enabling the UI to show per-sidecar log streams.
//
// Shared by the task and agent steps (the agent step reuses TaskDelegate).
func sidecarProcessIO(delegate TaskDelegate, sidecars []atc.SidecarConfig) runtime.ProcessIO {
	pio := runtime.ProcessIO{
		Stdout: delegate.Stdout(),
		Stderr: delegate.Stderr(),
	}
	if len(sidecars) > 0 {
		pio.SidecarWriters = make(map[string]io.Writer, len(sidecars))
		for _, sc := range sidecars {
			pio.SidecarWriters[sc.Name] = delegate.SidecarWriter(sc.Name)
		}
	}
	return pio
}
