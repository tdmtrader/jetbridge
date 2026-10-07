package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every service-account-shaped value the chart renders is a route to one of
// the output plane's two cloud identities -- the artifact daemon's, which may
// create, and the web's, which may delete -- and the validation has to
// enumerate them rather than list them.
//
// `validatePrincipals` was extended four times, each time by someone finding
// the next override nobody had thought of -- the last was
// `kubernetes.serviceAccount`, the TASK pod's account, which Req 54 names in
// its own words ("task credentials are activation failures") and which,
// pointed at a delete-holding KSA, rendered happily. Task pods are arbitrary
// user-supplied code.
//
// A hand-maintained list is the wrong shape for that. This rule reads the
// TEMPLATES for every `.Values.…serviceAccount[.name]` the chart actually
// consults, points each one in turn at another workload's rendered account, and
// requires the render to be refused. A new override cannot be added without
// either being covered or turning this red, because adding it means writing
// `.Values.something.serviceAccount` into a template.

// serviceAccountValue matches a chart reference to a service-account value.
var serviceAccountValue = regexp.MustCompile(`\.Values\.([A-Za-z0-9_.]*serviceAccount(?:\.name)?)\b`)

// serviceAccountNamePaths are the values paths that NAME an account, read out
// of the templates. A bare `…serviceAccount` path counts only when its value is
// a string: `serviceAccount.create` and `…serviceAccount.annotations` reach the
// same prefix and are not names.
func serviceAccountNamePaths(t *testing.T) []string {
	t.Helper()

	root := repoRoot(t)
	templates, err := filepath.Glob(filepath.Join(root, "deploy", "chart", "templates", "*"))
	if err != nil {
		t.Fatalf("listing templates: %v", err)
	}

	found := map[string]bool{}
	for _, template := range templates {
		body, err := os.ReadFile(template)
		if err != nil {
			t.Fatalf("reading %s: %v", template, err)
		}
		for _, match := range serviceAccountValue.FindAllStringSubmatch(string(body), -1) {
			found[match[1]] = true
		}
	}

	var paths []string
	for path := range found {
		if strings.HasSuffix(path, ".name") {
			paths = append(paths, path)

			continue
		}
		// A bare path is a name only when the chart's own default for it is a
		// string. `serviceAccount` is a map of create/annotations;
		// `kubernetes.serviceAccount` is the task pods' account.
		if found[path+".name"] {
			continue
		}
		if valuesPathIsAString(t, path) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	return paths
}

// valuesPathIsAString reports whether values.yaml gives this dotted path a
// scalar string default. It reads the file's indentation rather than parsing
// YAML, which is enough for a path this shallow and keeps this directory free
// of a YAML dependency it otherwise does not need.
func valuesPathIsAString(t *testing.T, path string) bool {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("reading values.yaml: %v", err)
	}

	segments := strings.Split(path, ".")
	depth := 0
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		if indent != depth*2 {
			continue
		}
		key, rest, found := strings.Cut(trimmed, ":")
		if !found || key != segments[depth] {
			continue
		}
		depth++
		if depth == len(segments) {
			return strings.TrimSpace(rest) != ""
		}
	}

	return false
}

func TestEveryServiceAccountValueTheChartRendersIsRefusedWhenItCollides(t *testing.T) {
	paths := serviceAccountNamePaths(t)

	// The web pod's and the task pods'. A floor rather than an exact list, so
	// a new one joins the rule instead of replacing it.
	if len(paths) < 2 {
		t.Fatalf("found only %d service-account values across the chart's templates (%v); "+
			"the scan failed and this rule would pass vacuously", len(paths), paths)
	}

	const (
		daemon = "jb-concourse-jetbridge-artifact-daemon"
		web    = "jb-concourse-jetbridge-web"
	)

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// Point it at a DIFFERENT workload's rendered account: the
			// publisher's, which holds create on the output bucket -- except
			// for a value that names the web's own account, which would be no
			// collision; that one is pointed at the publisher too, so the
			// web and the publisher would be one identity.
			message := renderOutputError(t, path+"="+daemon)
			if !strings.Contains(message, "service account") {
				t.Errorf("setting %s to %s was refused, but not by the service-account "+
					"rule:\n%s", path, daemon, message)
			}
		})
	}

	// And the task pods' account may not be the web's, which holds delete.
	message := renderOutputError(t, "kubernetes.serviceAccount="+web)
	if !strings.Contains(message, "service account") {
		t.Errorf("the task pods were allowed to run as the web's delete-holding account:\n%s", message)
	}
}

func TestGCSDoesNotRequireDeclaredCloudIdentities(t *testing.T) {
	out := renderOutput(t,
		`artifactDaemon.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`,
		`serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`)
	if strings.Contains(out, "hangar-output-policy-attestor") {
		t.Fatal("retired policy attestor rendered")
	}
}
