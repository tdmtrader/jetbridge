package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Chart templates render CLI flags into container args. When a flag is removed
// from a binary but left in the chart, the process exits at startup on the
// unrecognized flag -- stdlib flag is ExitOnError and go-flags errors likewise
// -- and every pod CrashLoopBackOffs the moment the chart syncs ahead of, or
// behind, the image.
//
// This has now happened twice. aad91a9911 fixed it for the hangar inventory
// flags after it took the fleet down on 2026-08-03, and the checkpoint removal
// reintroduced it for --preemption-watch/--preemption-budget. Neither was
// caught by anything: helm lint and every rendering test are satisfied by a
// well-formed argument, and no Go test reads the chart. The drift is only
// visible by asking the binary itself what it accepts.
//
// So this test builds the real binaries and reads their --help. That is the
// only source that accounts for go-flags namespacing (namespace:"postgres" +
// long:"host" is --postgres-host, which appears in no single source tag),
// embedded flag structs from other packages (skymarshal/skycmd), and
// subcommand scoping.
//
// Flags are collected by scanning the template TEXT rather than a rendered
// manifest, deliberately: a flag behind {{- if .Values.x.enabled }} is exactly
// the kind that drifts unnoticed, and rendering with default values would not
// emit it. --preemption-watch was gated that way.

// flagSurface is one template and the binary its flags are handed to.
type flagSurface struct {
	template string // path under deploy/chart/templates
	pkg      string // Go package to build, relative to the repo root
}

var flagSurfaces = []flagSurface{
	{template: "web-deployment.yaml", pkg: "./cmd/concourse"},
	{template: "artifact-daemon-daemonset.yaml", pkg: "./cmd/artifact-daemon"},
}

var (
	// "- --flag", "- --flag=value", "- --flag={{ .Values.x }}"
	renderedFlag = regexp.MustCompile(`^\s*-\s+--([a-z0-9][a-z0-9-]*)`)
	// A bare argument that is not a flag: the subcommand, e.g. "- web".
	renderedArg = regexp.MustCompile(`^\s*-\s+([a-z][a-z0-9-]*)\s*$`)
	// Both help dialects: "  -name type" (stdlib) and "  -x, --name" / "      --name" (go-flags).
	helpFlag = regexp.MustCompile(`(?m)^\s+(?:-\w,\s+)?--?([a-z0-9][a-z0-9-]*)`)
)

// flagsBySubcommand attributes every rendered flag to the subcommand of the
// container that renders it. A template with no bare argument before its flags
// (the artifact daemon) attributes them to the empty subcommand, meaning the
// binary itself.
func flagsBySubcommand(templateBody string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	subcommand := ""
	for _, line := range strings.Split(templateBody, "\n") {
		// A new args:/command: block resets which binary invocation we are in.
		trimmed := strings.TrimSpace(line)
		if trimmed == "args:" || trimmed == "command:" {
			subcommand = ""
			continue
		}
		if m := renderedFlag.FindStringSubmatch(line); m != nil {
			if out[subcommand] == nil {
				out[subcommand] = map[string]bool{}
			}
			out[subcommand][m[1]] = true
			continue
		}
		if m := renderedArg.FindStringSubmatch(line); m != nil && subcommand == "" {
			// First bare argument after args:/command: is the subcommand.
			// Paths (/usr/local/...) do not match, so an explicit command:
			// entrypoint is correctly read as "no subcommand".
			subcommand = m[1]
		}
	}
	return out
}

func binaryFlags(t *testing.T, binary string, subcommand string) map[string]bool {
	t.Helper()
	args := []string{}
	if subcommand != "" {
		args = append(args, subcommand)
	}
	args = append(args, "--help")

	// Both dialects write help and exit non-zero in at least one variant, so
	// the exit status is deliberately ignored; empty output is the real error.
	out, _ := exec.Command(binary, args...).CombinedOutput()
	if len(out) == 0 {
		t.Fatalf("%s %v produced no help output", filepath.Base(binary), args)
	}

	flags := map[string]bool{}
	for _, m := range helpFlag.FindAllStringSubmatch(string(out), -1) {
		flags[m[1]] = true
	}
	if len(flags) == 0 {
		t.Fatalf("parsed no flags from %s %v help:\n%s", filepath.Base(binary), args, out)
	}
	return flags
}

func TestChartRendersOnlyFlagsTheBinaryAccepts(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	binDir := t.TempDir()
	if len(flagSurfaces) == 0 {
		t.Fatal("flag drift test has no chart surfaces to inspect")
	}

	for _, surface := range flagSurfaces {
		t.Run(surface.template, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(repoRoot, "deploy/chart/templates", surface.template))
			if err != nil {
				t.Fatalf("read template: %v", err)
			}

			binary := filepath.Join(binDir, strings.TrimPrefix(filepath.Base(surface.pkg), "./"))
			build := exec.Command("go", "build", "-o", binary, surface.pkg)
			build.Dir = repoRoot
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %s: %v\n%s", surface.pkg, err, out)
			}

			bySubcommand := flagsBySubcommand(string(body))
			if len(bySubcommand) == 0 {
				t.Fatalf("%s renders no flags; this test is watching the wrong file", surface.template)
			}

			for subcommand, rendered := range bySubcommand {
				if len(rendered) == 0 {
					t.Fatalf("%s matched a command surface but no flags", surface.template)
				}
				accepted := binaryFlags(t, binary, subcommand)

				var missing []string
				for flag := range rendered {
					if !accepted[flag] {
						missing = append(missing, "--"+flag)
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					invocation := filepath.Base(surface.pkg)
					if subcommand != "" {
						invocation += " " + subcommand
					}
					t.Errorf(
						"%s renders %d flag(s) that %q does not accept: %s\n"+
							"Every pod running that container will exit at startup on the unrecognized flag.\n"+
							"Either restore the flag in %s or stop rendering it in the chart.",
						surface.template, len(missing), invocation, strings.Join(missing, " "), surface.pkg,
					)
				}
			}
		})
	}
}
