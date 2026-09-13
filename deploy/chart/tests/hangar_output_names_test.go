package tests

import (
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Names, at the release names Helm actually permits.
//
// Two failures live here, and both are truncation. A Kubernetes object name is
// 63 characters, and the chart composed `<fullname>-hangar-output-<role>` and
// then truncated the WHOLE string -- so once the fullname reached 49 characters
// the four roles had fewer than 14 distinguishing characters left and collapsed
// to one name. `validatePrincipals` then refused the render, correctly and with
// a message about service accounts, for a problem the operator cannot fix
// except by renaming the release. The fullname is `<release>-concourse-jetbridge`,
// so the threshold was a 28-character release name; Helm permits 53.
//
// The second is worse because it does not fail. Three of the output
// NetworkPolicies derived their component label by trimming the fullname prefix
// off the already-truncated workload name, while the Deployments set the same
// label from a literal. Once truncation bit, the two disagreed and the
// podSelector matched nothing -- and a NetworkPolicy that selects no pod is not
// an error and is not a deny. The controllers simply had no policy, silently,
// from a render that looks right.

// releaseNameLengths spans what Helm permits. 20 and 27 are where the
// NetworkPolicy labels used to start disagreeing, 28 is where the render used
// to stop working, and 53 is the maximum.
var releaseNameLengths = []int{5, 20, 27, 28, 40, 53}

// renderRelease is `render` with a release name of its own.
func renderRelease(t *testing.T, release string, sets ...string) (string, error) {
	t.Helper()

	args := []string{"template", release, "deploy/chart"}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	cmd := exec.Command("helm", args...)
	cmd.Dir = repoRoot(t)

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func releaseNameOfLength(n int) string {
	return "r" + strings.Repeat("e", n-1)
}

func TestTheOutputPlaneRendersAtEveryReleaseNameHelmPermits(t *testing.T) {
	for _, length := range releaseNameLengths {
		release := releaseNameOfLength(length)

		t.Run(release[:1]+"…"+string(rune('0'+length/10))+string(rune('0'+length%10)), func(t *testing.T) {
			out, err := renderRelease(t, release, append(append([]string{}, outputSets...),
				"hangarOutput.networkPolicy.enabled=true",
				"hangarOutput.activation.job.mode=attest",
				"hangarOutput.activation.job.facet=base")...)
			if err != nil {
				t.Fatalf("the output facet does not render at a %d-character release name, "+
					"which Helm permits (it permits 53):\n%s", length, out)
			}

			names := map[string][]string{}
			longest := ""
			for _, chunk := range splitDocuments(out) {
				var head renderedObject
				if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind == "" {
					continue
				}
				key := head.Kind + "/" + head.Metadata.Name
				names[key] = append(names[key], sourceOf(chunk))
				if len(head.Metadata.Name) > len(longest) {
					longest = head.Metadata.Name
				}
			}
			if len(names) < 20 {
				t.Fatalf("only %d objects rendered at release name length %d; the render "+
					"collapsed", len(names), length)
			}
			for key, sources := range names {
				if len(sources) > 1 {
					t.Errorf("%s is rendered %d times, from %v. Two objects with one name "+
						"are one object, and apply order decides which", key, len(sources), sources)
				}
			}
			// Only the output plane's own objects: the chart's pre-existing
			// Services are too long at these lengths on core as well, and that
			// is a separate follow-up.
			for key := range names {
				name := key[strings.Index(key, "/")+1:]
				if !strings.Contains(name, "hangar-output") {
					continue
				}
				if len(name) > 63 {
					t.Errorf("%s is %d characters; a Kubernetes object name is 63, and a "+
						"Job's `job-name` label carries the same bound", key, len(name))
				}
			}
		})
	}
}

// A selector written in this chart's own label vocabulary selects a pod this
// chart renders.
//
// A NetworkPolicy, PodDisruptionBudget or Service whose selector matches
// nothing is not an error and produces no event: the policy is absent, the
// budget guards nothing, the Service has no endpoints. That is the worst shape
// a failure can take, and the existing uniqueness rule would pass on a chart
// whose every policy selected nothing.
//
// Selectors in OTHER vocabularies are deliberately out of scope: the task
// egress policy selects `concourse.ci/worker`, which is a label the runtime
// puts on pods the chart does not render.
func TestEverySelectorInTheChartsOwnVocabularySelectsARenderedPod(t *testing.T) {
	for _, length := range []int{5, 20, 27, 53} {
		release := releaseNameOfLength(length)

		t.Run(release[:1]+"…", func(t *testing.T) {
			out, err := renderRelease(t, release, append(append([]string{}, outputSets...),
				"networkPolicy.enabled=true",
				"hangarOutput.networkPolicy.enabled=true",
				"artifactDaemon.networkPolicy.enabled=true",
				"pdb.enabled=true",
				"postgresql.enabled=true")...)
			if err != nil {
				t.Fatalf("render failed at release name length %d:\n%s", length, out)
			}

			var templates []map[string]string
			type selector struct {
				where  string
				labels map[string]string
			}
			var selectors []selector

			for _, chunk := range splitDocuments(out) {
				var head renderedObject
				if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind == "" {
					continue
				}
				where := head.Kind + "/" + head.Metadata.Name + " (" + sourceOf(chunk) + ")"

				switch head.Kind {
				case "Deployment":
					var object appsv1.Deployment
					if yaml.UnmarshalStrict([]byte(chunk), &object) == nil {
						templates = append(templates, object.Spec.Template.Labels)
					}
				case "DaemonSet":
					var object appsv1.DaemonSet
					if yaml.UnmarshalStrict([]byte(chunk), &object) == nil {
						templates = append(templates, object.Spec.Template.Labels)
					}
				case "NetworkPolicy":
					var object networkingv1.NetworkPolicy
					if yaml.UnmarshalStrict([]byte(chunk), &object) == nil {
						if labels := ownVocabulary(&object.Spec.PodSelector); labels != nil {
							selectors = append(selectors, selector{where, labels})
						}
					}
				case "PodDisruptionBudget":
					var object policyv1.PodDisruptionBudget
					if yaml.UnmarshalStrict([]byte(chunk), &object) == nil {
						if labels := ownVocabulary(object.Spec.Selector); labels != nil {
							selectors = append(selectors, selector{where, labels})
						}
					}
				case "Service":
					var object corev1.Service
					if yaml.UnmarshalStrict([]byte(chunk), &object) == nil && len(object.Spec.Selector) > 0 {
						if labels := ownVocabulary(&metav1.LabelSelector{
							MatchLabels: object.Spec.Selector,
						}); labels != nil {
							selectors = append(selectors, selector{where, labels})
						}
					}
				}
			}

			if len(selectors) < 8 || len(templates) < 5 {
				t.Fatalf("found %d selectors and %d pod templates; the scan failed and this "+
					"rule would pass vacuously", len(selectors), len(templates))
			}

			for _, one := range selectors {
				matched := false
				for _, labels := range templates {
					if selects(one.labels, labels) {
						matched = true

						break
					}
				}
				if !matched {
					t.Errorf("%s selects %v, which matches NO pod template this render "+
						"produces.\n\nThe object is silently inert: a NetworkPolicy that "+
						"selects nothing is not a deny, a PodDisruptionBudget that selects "+
						"nothing guards nothing, and a Service that selects nothing has no "+
						"endpoints. None of the three is an error and none produces an event.",
						one.where, one.labels)
				}
			}
		})
	}
}

// ownVocabulary returns the selector's matchLabels when every key is one of
// this chart's own `app.kubernetes.io/*` labels and there are no expressions,
// and nil otherwise.
func ownVocabulary(selector *metav1.LabelSelector) map[string]string {
	if selector == nil || len(selector.MatchExpressions) > 0 || len(selector.MatchLabels) == 0 {
		return nil
	}
	for key := range selector.MatchLabels {
		if !strings.HasPrefix(key, "app.kubernetes.io/") {
			return nil
		}
	}

	return selector.MatchLabels
}

func selects(selector, labels map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}

	return true
}

// The activation Job's name carries the epoch, so rotation is possible through
// the chart at all.
//
// A Job's `spec.template` is immutable and the `--epoch=` argument lives inside
// it. With the name built from the mode alone there were four possible Job
// names for the life of the release, so running `attest` for epoch 8 after
// `attest` ran for epoch 7 made `helm upgrade` fail with
// `Job.batch … field is immutable`, and the only way past it was deleting the
// Job by hand -- in the middle of the transition sequence decision F1 spent a
// page getting right.
func TestTheActivationJobNameCarriesTheEpochAndTheMode(t *testing.T) {
	jobNameFor := func(epoch, mode string, extra ...string) string {
		sets := append(append([]string{}, outputSets...),
			"hangarOutput.activationEpoch="+epoch,
			"hangarOutput.receipt.publicKeys[0].epoch="+epoch,
			"hangarOutput.activation.job.mode="+mode,
			"hangarOutput.activation.job.facet=base")

		return objectNamed(t, render(t, append(sets, extra...)...), "Job", "").name
	}

	seven := jobNameFor("7", "attest")
	eight := jobNameFor("8", "attest")
	if seven == eight {
		t.Errorf("mode=attest renders the Job name %q for both epoch 7 and epoch 8. A Job's "+
			"pod template is immutable and the epoch is an argument inside it, so the "+
			"second `helm upgrade` fails with `field is immutable` and rotation cannot be "+
			"driven through the chart.", seven)
	}
	if !strings.Contains(seven, "attest") || !strings.HasSuffix(seven, "-7") {
		t.Errorf("the activation Job name %q does not carry its mode and epoch", seven)
	}

	// And the finalize flag, which is a different transition under one mode.
	drain := jobNameFor("7", "drain", "hangarOutput.activation.job.facet=all")
	finalize := jobNameFor("7", "drain",
		"hangarOutput.activation.job.facet=all",
		"hangarOutput.activation.job.finalize=true")
	if drain == finalize {
		t.Errorf("a drain and a finalizing drain render the same Job name %q, and they are "+
			"different transitions with different pod templates", drain)
	}
}

// A completed Job is not cleaned up by anything, and the name now varies per
// epoch, so they accumulate one per mode per epoch.
func TestCompletedActivationJobsExpire(t *testing.T) {
	job := objectNamed(t, renderOutput(t,
		"hangarOutput.activation.job.mode=attest",
		"hangarOutput.activation.job.facet=base"), "Job", "")
	if !strings.Contains(job.body, "ttlSecondsAfterFinished:") {
		t.Error("the activation Job sets no ttlSecondsAfterFinished; completed Jobs " +
			"accumulate one per mode per epoch, forever")
	}
}
