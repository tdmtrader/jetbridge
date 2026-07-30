package adapter_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
)

func TestPreflightRequiresTheExactAvailableHarnessVersion(t *testing.T) {
	profile := profile(broker.AdapterCodex)
	identity, err := adapter.Preflight(context.Background(), profile, fakeVersionProbe{
		path: "/opt/bin/codex", version: "codex 1.2.3\n",
	})
	if err != nil {
		t.Fatalf("Preflight(): %v", err)
	}
	if identity.Name != broker.AdapterCodex || identity.Binary != "/opt/bin/codex" || identity.Version != "1.2.3" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestPreflightFailsClosedWhenHarnessIsUnavailableOrIncompatible(t *testing.T) {
	profile := profile(broker.AdapterCodex)
	for _, tc := range []struct {
		name  string
		probe fakeVersionProbe
		want  error
	}{
		{
			name: "unavailable", probe: fakeVersionProbe{lookupErr: errors.New("not found")},
			want: adapter.ErrHarnessUnavailable,
		},
		{
			name: "wrong version", probe: fakeVersionProbe{path: "/opt/bin/codex", version: "codex 1.2.4\n"},
			want: adapter.ErrHarnessIncompatible,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Preflight(context.Background(), profile, tc.probe)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Preflight() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestPreflightRejectsControlsThePinnedHarnessCannotProvide(t *testing.T) {
	tests := []struct {
		name    string
		profile broker.Profile
		version string
		want    string
	}{
		{
			name: "claude schema before its pinned supported version",
			profile: func() broker.Profile {
				p := profile(broker.AdapterClaude)
				p.Adapter.Version = "1.2.2"
				return p
			}(),
			version: "claude 1.2.2\n", want: "native output schema",
		},
		{
			name: "cursor user configuration isolation",
			profile: func() broker.Profile {
				p := profile(broker.AdapterCursor)
				p.Controls.IgnoresUserConfig = true
				return p
			}(),
			version: "cursor-agent 1.2.3\n", want: "ignore user configuration",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Preflight(context.Background(), tc.profile, fakeVersionProbe{
				path: "/opt/bin/harness", version: tc.version,
			})
			if !errors.Is(err, adapter.ErrHarnessIncompatible) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Preflight() error = %v, want incompatible %q", err, tc.want)
			}
		})
	}
}

func TestClaudeBuildNegotiatesStructuredOutputForPinnedSupportedVersion(t *testing.T) {
	paths := adapter.Paths{WorkDir: "/work", ScratchDir: "/scratch", OutputSchema: "/schema/result.json"}
	invocation, err := (adapter.Claude{}).Build(profile(broker.AdapterClaude), paths, "secret")
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if !contains(invocation.Args, "--json-schema") || !contains(invocation.Args, paths.OutputSchema) {
		t.Fatalf("Claude args = %q, want pinned structured-output schema", invocation.Args)
	}
}

func TestCapabilitiesReportCursorsWeakerNativeEnforcement(t *testing.T) {
	capabilities, err := adapter.CapabilitiesFor(profile(broker.AdapterCursor))
	if err != nil {
		t.Fatalf("CapabilitiesFor(): %v", err)
	}
	if capabilities.NativeReadOnlyWorkspace || capabilities.NativeOutputSchema || capabilities.NativeTerminalToolDeny {
		t.Fatalf("Cursor native capabilities overclaim enforcement: %#v", capabilities)
	}
}

type fakeVersionProbe struct {
	path       string
	version    string
	lookupErr  error
	versionErr error
}

func (probe fakeVersionProbe) LookPath(string) (string, error) {
	if probe.lookupErr != nil {
		return "", probe.lookupErr
	}
	return probe.path, nil
}

func (probe fakeVersionProbe) Output(context.Context, string, ...string) ([]byte, error) {
	if probe.versionErr != nil {
		return nil, probe.versionErr
	}
	return []byte(probe.version), nil
}
