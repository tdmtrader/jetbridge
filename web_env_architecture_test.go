// An external test package, unlike the rest of the root rules: atc/atccmd
// imports the root package for its version strings, so `package concourse`
// cannot import it back.
package concourse_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/atccmd"
	flags "github.com/jessevdk/go-flags"
	"github.com/vito/twentythousandtonnesofcrudeoil"
	"sigs.k8s.io/yaml"
)

// concourseHomeFixture is the chart test's copy of concourse.home's values. Its
// web.env is the one place a deployment hands the web flags by environment
// name rather than through a typed chart value.
const concourseHomeFixture = "deploy/chart/tests/testdata/concourse-home-rollout.yaml"

// go-flags ignores an environment variable no option claims, silently. So when
// a flag is removed, the CONCOURSE_* name a deployment sets for it keeps
// rendering, keeps reaching the pod, and does nothing: concourse.home carried
// six such names (CONCOURSE_ENABLE_GLOBAL_RESOURCES, CONCOURSE_GC_INTERVAL,
// CONCOURSE_BUILD_TRACKER_INTERVAL, ...) for months after their flags went.
//
// The rule: every CONCOURSE_* name in the fixture's web.env is the environment
// name of a `concourse web` option. Removing a flag now fails here until the
// fixture -- and the home-infra values it mirrors -- drop the name too.
func TestEveryConcourseEnvVarTheFixtureSetsIsAWebFlag(t *testing.T) {
	declared := webFlagEnvNames(t)

	// A parser that registered nothing would make every name unknown and the
	// failure below unreadable; one that registered only a handful would mean
	// the struct below stopped describing the web command.
	if len(declared) < 100 {
		t.Fatalf("the web command declares only %d environment names; it has well over 100. "+
			"The parser below no longer describes `concourse web`.", len(declared))
	}
	for _, known := range []string{"CONCOURSE_PEER_ADDRESS", "CONCOURSE_ENCRYPTION_KEY", "CONCOURSE_RESOURCE_CHECKING_INTERVAL"} {
		if !declared[known] {
			t.Errorf("%s is not among the derived names; the derivation is wrong, not the fixture", known)
		}
	}

	names := fixtureWebEnvNames(t)
	if len(names) == 0 {
		t.Fatalf("%s sets no CONCOURSE_* name in web.env; this rule would pass vacuously", concourseHomeFixture)
	}

	for _, name := range names {
		if !declared[name] {
			t.Errorf("%s sets web.env %s, but no `concourse web` flag reads it. go-flags ignores "+
				"an unknown environment variable, so the setting does nothing. Drop it from the "+
				"fixture and from home-infra apps/concourse.yaml.", concourseHomeFixture, name)
		}
	}
}

// webFlagEnvNames returns the environment name of every `concourse web`
// option, derived exactly as cmd/concourse/main.go does: the dynamic credential
// manager and metrics emitter groups wired in, then twentythousandtonnesofcrudeoil
// assigning CONCOURSE_ + the upper-snake long name to every option without an
// explicit `env` tag.
//
// cmd/concourse is package main and cannot be imported, so its WebCommand is
// mirrored here: atccmd.RunCommand plus the one option WebCommand adds. A new
// WebCommand option surfaces as an unknown name below, never as a silent pass.
func webFlagEnvNames(t *testing.T) map[string]bool {
	t.Helper()

	var cmd struct {
		Web struct {
			PeerAddress string `long:"peer-address"`

			*atccmd.RunCommand
		} `command:"web"`
	}
	cmd.Web.RunCommand = &atccmd.RunCommand{}

	parser := flags.NewParser(&cmd, flags.None)
	parser.NamespaceDelimiter = "-"

	web := parser.Find("web")
	if web == nil {
		t.Fatal("the mirrored parser has no web command")
	}
	cmd.Web.WireDynamicFlags(web)

	twentythousandtonnesofcrudeoil.TheEnvironmentIsPerfectlySafe(parser, "CONCOURSE_")

	names := map[string]bool{}
	var walk func(*flags.Group)
	walk = func(g *flags.Group) {
		for _, opt := range g.Options() {
			if opt.EnvDefaultKey != "" {
				names[opt.EnvDefaultKey] = true
			}
		}
		for _, sub := range g.Groups() {
			walk(sub)
		}
	}
	walk(web.Group)

	return names
}

func fixtureWebEnvNames(t *testing.T) []string {
	t.Helper()

	raw, err := os.ReadFile(concourseHomeFixture)
	if err != nil {
		t.Fatalf("reading %s: %v", concourseHomeFixture, err)
	}

	var values struct {
		Web struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
		} `json:"web"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("decoding %s: %v", concourseHomeFixture, err)
	}

	var names []string
	for _, e := range values.Web.Env {
		if strings.HasPrefix(e.Name, "CONCOURSE_") {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)

	return names
}
