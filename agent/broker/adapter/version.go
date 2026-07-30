package adapter

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/concourse/concourse/agent/broker"
)

var (
	ErrHarnessUnavailable  = errors.New("broker adapter: harness unavailable")
	ErrHarnessIncompatible = errors.New("broker adapter: harness incompatible")
	harnessVersionPattern  = regexp.MustCompile(`(?:^|[^0-9A-Za-z.+-])v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)(?:$|[^0-9A-Za-z.+-])`)
)

// VersionProbe separates binary discovery and version execution so the
// preflight stays deterministic under test and never needs a live provider.
type VersionProbe interface {
	LookPath(string) (string, error)
	Output(context.Context, string, []string, []string) ([]byte, error)
}

// SystemVersionProbe performs the local, argv-only harness inspection used by
// broker-worker startup.
type SystemVersionProbe struct{}

func (SystemVersionProbe) LookPath(binary string) (string, error) {
	return exec.LookPath(binary)
}

func (SystemVersionProbe) Output(ctx context.Context, binary string, args []string, env []string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append([]string(nil), env...)
	return command.Output()
}

func preflightEnvironment() []string {
	return []string{"LC_ALL=C"}
}

type Identity struct {
	Name    broker.AdapterName
	Binary  string
	Version string
}

// Capabilities distinguishes the effective execution boundary from controls
// that the native CLI enforces itself.
type Capabilities struct {
	ReadOnlyWorkspace bool
	NoBrokerRecursion bool
	TestsUnavailable  bool

	NativeReadOnlyWorkspace bool
	NativeTerminalToolDeny  bool
	NativeOutputSchema      bool
	IgnoresUserConfig       bool
}

var supportedCapabilities = map[broker.AdapterName]map[string]Capabilities{
	broker.AdapterCodex: {
		"0.146.0": {
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeReadOnlyWorkspace: true, NativeOutputSchema: true, IgnoresUserConfig: true,
		},
	},
	broker.AdapterClaude: {
		"2.1.212": {
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeReadOnlyWorkspace: true, NativeTerminalToolDeny: true, NativeOutputSchema: true,
		},
	},
}

// CapabilitiesFor validates the declared frozen controls against the one
// deployment-pinned CLI contract. It intentionally has no fallback: adding a
// new harness version requires an explicit compatibility entry and review.
func CapabilitiesFor(profile broker.Profile) (Capabilities, error) {
	versions, found := supportedCapabilities[profile.Adapter.Name]
	if !found {
		return Capabilities{}, fmt.Errorf("%w: unsupported adapter %q", ErrHarnessIncompatible, profile.Adapter.Name)
	}
	capabilities, found := versions[profile.Adapter.Version]
	if !found {
		return Capabilities{}, fmt.Errorf(
			"%w: adapter %q version %q has no pinned compatibility contract",
			ErrHarnessIncompatible, profile.Adapter.Name, profile.Adapter.Version,
		)
	}
	if !profile.Controls.ReadOnlyWorkspace || !capabilities.ReadOnlyWorkspace {
		return Capabilities{}, fmt.Errorf("%w: adapter %q cannot provide read-only workspace", ErrHarnessIncompatible, profile.Adapter.Name)
	}
	if !profile.Controls.NoBrokerRecursion || !capabilities.NoBrokerRecursion {
		return Capabilities{}, fmt.Errorf("%w: adapter %q cannot provide no broker recursion", ErrHarnessIncompatible, profile.Adapter.Name)
	}
	if !profile.Controls.TestsUnavailable || !capabilities.TestsUnavailable {
		return Capabilities{}, fmt.Errorf("%w: adapter %q cannot provide unavailable tests", ErrHarnessIncompatible, profile.Adapter.Name)
	}
	if profile.Adapter.Name == broker.AdapterClaude && !profile.Controls.NativeOutputSchema {
		return Capabilities{}, fmt.Errorf("%w: adapter %q requires native output schema", ErrHarnessIncompatible, profile.Adapter.Name)
	}
	if profile.Controls.NativeOutputSchema && !capabilities.NativeOutputSchema {
		return Capabilities{}, fmt.Errorf("%w: adapter %q version %q cannot provide native output schema", ErrHarnessIncompatible, profile.Adapter.Name, profile.Adapter.Version)
	}
	if profile.Controls.IgnoresUserConfig && !capabilities.IgnoresUserConfig {
		return Capabilities{}, fmt.Errorf("%w: adapter %q version %q cannot ignore user configuration", ErrHarnessIncompatible, profile.Adapter.Name, profile.Adapter.Version)
	}
	return capabilities, nil
}

// Preflight verifies the discovered binary is the profile's named harness,
// reports its exact detected version, and rejects any version/control mismatch
// before a credential or prompt reaches the process.
func Preflight(ctx context.Context, profile broker.Profile, probe VersionProbe) (Identity, error) {
	if probe == nil {
		return Identity{}, fmt.Errorf("%w: version probe is required", ErrHarnessUnavailable)
	}
	if _, err := CapabilitiesFor(profile); err != nil {
		return Identity{}, err
	}
	binary, err := binaryName(profile.Adapter.Name)
	if err != nil {
		return Identity{}, err
	}
	path, err := probe.LookPath(binary)
	if err != nil || strings.TrimSpace(path) == "" {
		return Identity{}, fmt.Errorf("%w: %s: %v", ErrHarnessUnavailable, binary, err)
	}
	output, err := probe.Output(ctx, path, []string{"--version"}, preflightEnvironment())
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %s version probe: %v", ErrHarnessUnavailable, binary, err)
	}
	version, err := parseHarnessVersion(output)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %s: %v", ErrHarnessIncompatible, binary, err)
	}
	if version != profile.Adapter.Version {
		return Identity{}, fmt.Errorf("%w: %s reports %q, profile requires %q", ErrHarnessIncompatible, binary, version, profile.Adapter.Version)
	}
	return Identity{Name: profile.Adapter.Name, Binary: path, Version: version}, nil
}

func binaryName(name broker.AdapterName) (string, error) {
	switch name {
	case broker.AdapterCodex:
		return "codex", nil
	case broker.AdapterClaude:
		return "claude", nil
	case broker.AdapterCursor:
		return "cursor-agent", nil
	default:
		return "", fmt.Errorf("%w: unsupported adapter %q", ErrHarnessIncompatible, name)
	}
}

func parseHarnessVersion(output []byte) (string, error) {
	matches := harnessVersionPattern.FindAllStringSubmatch(string(output), -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one semver version in harness output")
	}
	return matches[0][1], nil
}
