// An external test package, unlike the rest of the root rules: atc/atccmd
// imports the root package for its version strings, so `package concourse`
// cannot import it back.
package concourse_test

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/concourse/concourse/atc/atccmd"
	flags "github.com/jessevdk/go-flags"
	"github.com/vito/twentythousandtonnesofcrudeoil"
)

// webTemplates are the chart templates that name the web container's
// environment: the Deployment and the helper that tabulates the typed web
// settings (concourse.web.settingsEnv).
var webTemplates = []string{
	"deploy/chart/templates/web-deployment.yaml",
	"deploy/chart/templates/_helpers.tpl",
}

// go-flags ignores an environment variable no option claims, silently. So when
// a flag is removed, a CONCOURSE_* name rendered for it keeps reaching the pod
// and does nothing: concourse.home carried six such names
// (CONCOURSE_ENABLE_GLOBAL_RESOURCES, CONCOURSE_GC_INTERVAL,
// CONCOURSE_BUILD_TRACKER_INTERVAL, ...) for months after their flags went,
// through the web.env pass-through its typed values replaced.
//
// The rule: every CONCOURSE_* name the chart's web templates render is the
// environment name of a `concourse web` option. Removing a flag now fails here
// until the chart value that renders it goes too.
func TestEveryConcourseEnvVarTheChartRendersIsAWebFlag(t *testing.T) {
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
			t.Errorf("%s is not among the derived names; the derivation is wrong, not the chart", known)
		}
	}

	names := chartWebEnvNames(t)
	if len(names) < 10 {
		t.Fatalf("%v name only %d CONCOURSE_* variables; the scan lost the templates", webTemplates, len(names))
	}

	for _, name := range names {
		if !declared[name] {
			t.Errorf("the chart renders %s onto the web, but no `concourse web` flag reads it. go-flags ignores "+
				"an unknown environment variable, so the setting does nothing. Remove the value that renders it.", name)
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

// chartWebEnvNames returns every CONCOURSE_* name webTemplates spell out.
func chartWebEnvNames(t *testing.T) []string {
	t.Helper()

	name := regexp.MustCompile(`CONCOURSE_[A-Z0-9_]*[A-Z0-9]`)
	seen := map[string]bool{}
	for _, path := range webTemplates {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, match := range name.FindAllString(string(raw), -1) {
			seen[match] = true
		}
	}

	var names []string
	for match := range seen {
		names = append(names, match)
	}
	sort.Strings(names)

	return names
}
