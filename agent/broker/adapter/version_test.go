package adapter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
)

func TestPreflightRequiresTheExactAvailableHarnessVersion(t *testing.T) {
	profile := profile(broker.AdapterCodex)
	probe := &fakeVersionProbe{
		path: "/opt/bin/codex", version: "codex-cli 0.146.0\n",
	}
	identity, err := adapter.Preflight(context.Background(), profile, probe)
	if err != nil {
		t.Fatalf("Preflight(): %v", err)
	}
	if identity.Name != broker.AdapterCodex || identity.Binary != "/opt/bin/codex" || identity.Version != "0.146.0" {
		t.Fatalf("identity = %#v", identity)
	}
	if strings.Join(probe.args, " ") != "--version" || strings.Join(probe.env, " ") != "LC_ALL=C" {
		t.Fatalf("version probe argv/env = %q / %q", probe.args, probe.env)
	}
}

func TestPreflightAcceptsOnlyThePackagedReleaseVersionFixtures(t *testing.T) {
	for _, test := range []struct {
		name    string
		adapter broker.AdapterName
		output  string
		version string
	}{
		{name: "codex", adapter: broker.AdapterCodex, output: "codex-cli 0.146.0\n", version: "0.146.0"},
		{name: "claude", adapter: broker.AdapterClaude, output: "2.1.212 (Claude Code)\n", version: "2.1.212"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := adapter.Preflight(context.Background(), profile(test.adapter), &fakeVersionProbe{
				path: "/opt/pinned/" + test.name, version: test.output,
			})
			if err != nil {
				t.Fatalf("Preflight(): %v", err)
			}
			if identity.Version != test.version {
				t.Fatalf("version = %q, want %q", identity.Version, test.version)
			}
		})
	}
}

func TestPreflightFailsClosedWhenHarnessIsUnavailableOrIncompatible(t *testing.T) {
	profile := profile(broker.AdapterCodex)
	for _, tc := range []struct {
		name  string
		probe *fakeVersionProbe
		want  error
	}{
		{
			name: "unavailable", probe: &fakeVersionProbe{lookupErr: errors.New("not found")},
			want: adapter.ErrHarnessUnavailable,
		},
		{
			name: "wrong version", probe: &fakeVersionProbe{path: "/opt/bin/codex", version: "codex-cli 0.146.1\n"},
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

func TestPreflightRejectsVersionTokenContinuations(t *testing.T) {
	profile := profile(broker.AdapterCodex)
	for _, version := range []string{"codex 0.146.0-rc.1\n", "codex 0.146.0+build\n", "codex 0.146.0.4\n"} {
		t.Run(version, func(t *testing.T) {
			_, err := adapter.Preflight(context.Background(), profile, &fakeVersionProbe{
				path: "/opt/bin/codex", version: version,
			})
			if !errors.Is(err, adapter.ErrHarnessIncompatible) {
				t.Fatalf("Preflight() error = %v, want incompatible version", err)
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
			name: "cursor clean context is not verified",
			profile: func() broker.Profile {
				p := profile(broker.AdapterCursor)
				return p
			}(),
			version: "cursor-agent 2026.07.23-e383d2b\n", want: "unsupported adapter",
		},
		{
			name: "claude requires native output schema",
			profile: func() broker.Profile {
				p := profile(broker.AdapterClaude)
				p.Controls.NativeOutputSchema = false
				return p
			}(),
			version: "2.1.212 (Claude Code)\n", want: "native output schema",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapter.Preflight(context.Background(), tc.profile, &fakeVersionProbe{
				path: "/opt/bin/harness", version: tc.version,
			})
			if !errors.Is(err, adapter.ErrHarnessIncompatible) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Preflight() error = %v, want incompatible %q", err, tc.want)
			}
		})
	}
}

func TestClaudeBuildNegotiatesStructuredOutputForPinnedSupportedVersion(t *testing.T) {
	schema := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(schema, []byte(`{"type":"object"}`), 0600); err != nil {
		t.Fatal(err)
	}
	paths := adapter.Paths{WorkDir: "/work", ScratchDir: "/scratch", OutputSchema: schema}
	invocation, err := (adapter.Claude{}).Build(profile(broker.AdapterClaude), paths, "secret")
	if err != nil {
		t.Fatalf("Build(): %v", err)
	}
	if !contains(invocation.Args, "--json-schema") || !contains(invocation.Args, `{"type":"object"}`) {
		t.Fatalf("Claude args = %q, want pinned structured-output schema", invocation.Args)
	}
}

func TestCapabilitiesQuarantineCursorWithoutVerifiedCleanContext(t *testing.T) {
	_, err := adapter.CapabilitiesFor(profile(broker.AdapterCursor))
	if !errors.Is(err, adapter.ErrHarnessIncompatible) || !strings.Contains(err.Error(), "unsupported adapter") {
		t.Fatalf("CapabilitiesFor(Cursor) error = %v, want unsupported adapter", err)
	}
}

type fakeVersionProbe struct {
	path       string
	version    string
	lookupErr  error
	versionErr error
	args       []string
	env        []string
}

func (probe fakeVersionProbe) LookPath(string) (string, error) {
	if probe.lookupErr != nil {
		return "", probe.lookupErr
	}
	return probe.path, nil
}

func (probe *fakeVersionProbe) Output(_ context.Context, _ string, args []string, env []string) ([]byte, error) {
	probe.args = append([]string(nil), args...)
	probe.env = append([]string(nil), env...)
	if probe.versionErr != nil {
		return nil, probe.versionErr
	}
	return []byte(probe.version), nil
}
