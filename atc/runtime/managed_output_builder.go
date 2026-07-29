package runtime

import (
	"fmt"
	"path/filepath"

	"github.com/concourse/concourse/atc"
)

// ValidateManagedOutputBuilder checks the intentionally small runtime
// contract before the worker allocates a container. It is not a general
// sidecar API: the name, authority mount, and one-file projection are fixed.
func ValidateManagedOutputBuilder(spec ContainerSpec) error {
	builder := spec.ManagedOutputBuilder
	for _, sidecar := range spec.Sidecars {
		if sidecar.Name == ManagedOutputBuilderName && builder == nil {
			return fmt.Errorf("managed output builder sidecar %q is reserved", ManagedOutputBuilderName)
		}
	}
	if builder == nil {
		return nil
	}
	if builder.Authority.MountPath != ManagedOutputBuilderAuthorityMountRoot ||
		len(builder.Authority.Files) != 1 || builder.Authority.Files[ManagedOutputBuilderAuthorityFile] == nil ||
		len(builder.Authority.Files[ManagedOutputBuilderAuthorityFile]) == 0 {
		return fmt.Errorf("managed output builder requires one authority.json file at %q", ManagedOutputBuilderAuthorityMountRoot)
	}
	found := 0
	for _, sidecar := range spec.Sidecars {
		if sidecar.Name != ManagedOutputBuilderName {
			continue
		}
		found++
		if sidecar.Image == "" || sidecar.Image != spec.ImageSpec.ImageURL || sidecar.WorkingDir != "/" || len(sidecar.Command) != 2 ||
			sidecar.Command[0] != "/usr/local/bin/agent-output" || sidecar.Command[1] != "serve" || len(sidecar.Args) != 0 ||
			len(sidecar.Ports) != 1 || sidecar.Ports[0] != (atc.SidecarPort{ContainerPort: 7783, Protocol: "TCP"}) {
			return fmt.Errorf("managed output builder sidecar has an invalid fixed runtime shape")
		}
		if len(sidecar.Env) != 0 || len(spec.SidecarEnv[sidecar.Name]) != 0 {
			return fmt.Errorf("managed output builder must not receive authored or injected environment")
		}
	}
	if found != 1 {
		return fmt.Errorf("managed output builder sidecar must occur exactly once (found %d)", found)
	}
	seen := map[string]struct{}{}
	for _, paths := range [][]string{builder.InputMountPaths, builder.OutputMountPaths} {
		for _, path := range paths {
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return fmt.Errorf("managed output builder projection %q must be an absolute clean path", path)
			}
			if _, duplicate := seen[path]; duplicate {
				return fmt.Errorf("managed output builder projection %q is duplicated", path)
			}
			seen[path] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("managed output builder has no typed port projections")
	}
	return nil
}
