package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every service-account-shaped value the chart renders is a route to the
// reclaimer's cloud identity, and the validation has to enumerate them rather
// than list them.
//
// `validatePrincipals` has now been extended four times, each time by someone
// finding the next override nobody had thought of: the activation account's
// name, its Workload Identity annotation, the top-level `serviceAccount.name`
// -- and `kubernetes.serviceAccount`, the TASK pod's account, which Req 54
// names in its own words ("task credentials are activation failures") and which
// pointed at the reclaimer's KSA rendered happily. Task pods are arbitrary
// user-supplied code and the reclaimer holds the only `storage.objects.delete`
// on the output bucket.
//
// A hand-maintained list is the wrong shape for that. This rule reads the
// TEMPLATES for every `.Values.…serviceAccount[.name]` the chart actually
// consults, points each one in turn at another workload's rendered account, and
// requires the render to be refused. A fifth override cannot be added without
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

	// Five output roles, the web pod's, and the task pods'. A floor rather than
	// an exact list, so a new one joins the rule instead of replacing it.
	if len(paths) < 7 {
		t.Fatalf("found only %d service-account values across the chart's templates (%v); "+
			"the scan failed and this rule would pass vacuously", len(paths), paths)
	}

	const (
		reclaimer = "jb-concourse-jetbridge-hangar-output-reclaimer"
		daemon    = "jb-concourse-jetbridge-hangar-output-daemon"
	)

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// Point it at a DIFFERENT workload's rendered account. The
			// reclaimer's, because it is the one principal in the system
			// holding storage.objects.delete on the output bucket -- except
			// for the reclaimer's own value, where that is not a collision.
			collidesWith := reclaimer
			if strings.Contains(path, "reclaimer") {
				collidesWith = daemon
			}

			message := renderOutputError(t, path+"="+collidesWith)
			if !strings.Contains(message, "service account") {
				t.Errorf("setting %s to %s was refused, but not by the service-account "+
					"rule:\n%s", path, collidesWith, message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The attestor's four identities
// ---------------------------------------------------------------------------
//
// The policy attestor compares the bucket's IAM policy against the four
// principals this plane says it has. It was fed KUBERNETES SERVICE ACCOUNT
// NAMES.
//
// `--publisher-identity=jb-concourse-jetbridge-hangar-output-daemon` and its
// three siblings rendered even with the Workload Identity annotations set,
// while the flag's own help text ("Cloud identity bound to the …"), the
// matcher (`normalizeMember`, which strips a `serviceAccount:` prefix off an
// IAM member) and the attestor's own tests all expect a cloud member. Against a
// real bucket NO member matches: all four roles attest `insufficient_role`,
// every real principal on the bucket becomes a stranger, and the epoch is
// permanently at risk. It fails closed, so it is not a data-loss bug -- it is
// Req 54's "activation verifies the KSA-to-cloud identity bindings" being
// unable to succeed as deployed, and AC 18's "bound to the verified cloud
// principals" being untrue of the render.
//
// Nothing caught it because tier 2 has no permission system at all and the
// chart tests asserted only that the four values were distinct -- which four
// KSA names are.

// workloadIdentities are the four annotations and the flag each one has to
// reach. One dict, so a fifth role cannot be added with an identity flag and no
// annotation.
var workloadIdentities = []struct {
	valuesPath string
	flag       string
	member     string
}{
	{"hangarOutput.daemon", "--publisher-identity", "publisher@p.iam.gserviceaccount.com"},
	{"hangarOutput.inventory", "--inventory-identity", "inventory@p.iam.gserviceaccount.com"},
	{"hangarOutput.reclaimer", "--reclaimer-identity", "reclaimer@p.iam.gserviceaccount.com"},
	{"hangarOutput.policyAttestor", "--attestor-identity", "attestor@p.iam.gserviceaccount.com"},
}

// clearedIdentity is the --set that blanks one role's annotation. outputSets
// declares all four; this takes one back out.
func clearedIdentity(valuesPath string) string {
	return valuesPath + `.serviceAccount.annotations.iam\.gke\.io/gcp-service-account=`
}

func TestTheAttestorIsGivenCloudIdentitiesAndNotServiceAccountNames(t *testing.T) {
	attestor := objectNamed(t, renderOutput(t),
		"Deployment", "-"+outputAttestorComponent)

	for _, identity := range workloadIdentities {
		want := "- " + identity.flag + "=" + identity.member
		if !strings.Contains(attestor.body, want) {
			t.Errorf("the policy attestor renders no %q. It compares the bucket's IAM policy "+
				"against these four values, so a Kubernetes service account name here matches "+
				"no member on any real bucket: every role attests insufficient_role, every "+
				"real principal is a stranger, and the epoch is permanently at risk.\n\n%s",
				want, identityFlagsIn(attestor.body))
		}
		if strings.Contains(attestor.body, "- "+identity.flag+"=jb-concourse-jetbridge-") {
			t.Errorf("%s carries a Kubernetes service account name", identity.flag)
		}
	}
}

// An empty annotation is refused rather than rendered as an empty identity.
//
// An empty one is worse than a wrong one: `normalizeMember("")` is "", which
// matches no binding and is indistinguishable from a principal with no role,
// so the attestor reports a plane whose publisher has no grant at all -- for a
// deployment whose Workload Identity is perfectly configured and simply not
// declared to the chart.
func TestTheOutputFacetRefusesAWorkloadWithNoCloudIdentity(t *testing.T) {
	for _, identity := range workloadIdentities {
		message := renderOutputError(t, clearedIdentity(identity.valuesPath))
		if !strings.Contains(message, identity.valuesPath+".serviceAccount.annotations") {
			t.Errorf("the output facet rendered with no cloud identity for %s, or was "+
				"refused by something else:\n%s", identity.valuesPath, message)
		}
	}
}

func identityFlagsIn(body string) string {
	var found []string
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); strings.Contains(trimmed, "-identity=") {
			found = append(found, trimmed)
		}
	}

	return strings.Join(found, "\n")
}
