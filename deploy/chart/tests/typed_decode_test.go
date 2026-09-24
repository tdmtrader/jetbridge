package tests

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// What the API server does to a render, and what no other test here does.
//
// Every other assertion in this directory is a substring, a hand-rolled struct
// with three fields, or a `map[string]any`. All three are true of YAML the
// Kubernetes API server refuses. A template whitespace chomp once glued `env:`
// onto the last element of `command`, so `command` became a list of maps and
// `env` ceased to exist, on all three output controller Deployments -- and the
// whole suite, the flag-drift guard and two CI jobs were green with the plane
// undeployable. `kubectl create --dry-run=client` does not catch it either: it
// round-trips through an unstructured decode and prints `env: null` back.
//
// So decode each document into the Go type its own `kind` claims, with the
// decoder the API server uses, in STRICT mode -- unknown fields and duplicate
// keys are errors. That is the only shape that sees a field which silently
// stopped existing.

// typedModes are the render configurations this guard covers. "Every supported
// mode" means every combination an operator can select that changes which
// objects exist, not every value: a mode here is a set of objects, and a new
// template that renders under none of them is caught by kindsCovered below.
var typedModes = []struct {
	name string
	sets []string
	// floor is the smallest document count that is not evidence the render
	// collapsed. A mode that renders fewer objects than this passes every
	// rule in this file vacuously.
	floor int
}{
	{
		name:  "default (every switch off)",
		sets:  nil,
		floor: 12,
	},
	{
		name:  "base execution control only",
		sets:  baseControlSets,
		floor: 14,
	},
	{
		name:  "base control and the output facet",
		sets:  outputSets,
		floor: 25,
	},
	{
		name: "output facet with every optional surface on",
		sets: append(append([]string{}, outputSets...),
			"networkPolicy.enabled=true",
			// The output plane has its OWN NetworkPolicy switch, and without it
			// this mode decoded the web and db policies while the four output
			// ones were never rendered in any mode at all.
			"hangarOutput.networkPolicy.enabled=true",
			"alertingRules.enabled=true",
			"metrics.enabled=true",
			"serviceMonitor.enabled=true",
			"ingress.enabled=true",
			"ingress.hosts[0]=ci.example",
			"pdb.enabled=true",
			"postgresql.enabled=true",
			"artifactDaemon.durable.store=gcs",
			"artifactDaemon.durable.bucket=jb-durable",
			"rbac.brineLive=true",
		),
		floor: 34,
	},
	{
		// Web capture renders the result-download scratch volume, and Run
		// intake the signing key and the credential worker pins.
		name: "web capture with Run intake and credential pins",
		sets: append(append([]string{}, outputSets...),
			"hangarOutput.webEnabled=true",
			"web.runInputSigningKeySecret=review-input-key",
			"web.runCredentialWorkerImages[0]=registry.example/review-worker@sha256:"+
				"abababababababababababababababababababababababababababababababab",
		),
		floor: 25,
	},
	{
		// The Run epoch is rendered beside, and independently of, the Hangar epoch.
		name: "web capture with a raised Run activation epoch",
		sets: append(append([]string{}, outputSets...),
			"hangarOutput.webEnabled=true",
			"web.pipelineRunActivationEpoch=4",
		),
		floor: 25,
	},
	{
		name: "activation Job, mode=attest facet=base",
		sets: append(append([]string{}, outputSets...),
			"hangarOutput.activation.job.mode=attest",
			"hangarOutput.activation.job.facet=base",
		),
		floor: 27,
	},
	{
		name: "activation Job, mode=enable facet=output",
		sets: append(append([]string{}, outputSets...),
			"hangarOutput.activation.job.mode=enable",
			"hangarOutput.activation.job.facet=output",
		),
		floor: 27,
	},
	{
		name: "activation Job, mode=drain facet=all",
		sets: append(append([]string{}, outputSets...),
			"hangarOutput.activation.job.mode=drain",
			"hangarOutput.activation.job.facet=all",
		),
		floor: 27,
	},
}

// typedObjectFor returns an empty instance of the Go type the API server
// decodes this kind into. A kind with no entry is either a CRD (handled by
// crdKinds) or a gap in this guard, and the test says which.
func typedObjectFor(kind string) any {
	switch kind {
	case "ClusterRole":
		return &rbacv1.ClusterRole{}
	case "ClusterRoleBinding":
		return &rbacv1.ClusterRoleBinding{}
	case "ConfigMap":
		return &corev1.ConfigMap{}
	case "DaemonSet":
		return &appsv1.DaemonSet{}
	case "Deployment":
		return &appsv1.Deployment{}
	case "Ingress":
		return &networkingv1.Ingress{}
	case "Job":
		return &batchv1.Job{}
	case "NetworkPolicy":
		return &networkingv1.NetworkPolicy{}
	case "PersistentVolumeClaim":
		return &corev1.PersistentVolumeClaim{}
	case "PodDisruptionBudget":
		return &policyv1.PodDisruptionBudget{}
	case "Role":
		return &rbacv1.Role{}
	case "RoleBinding":
		return &rbacv1.RoleBinding{}
	case "Secret":
		return &corev1.Secret{}
	case "Service":
		return &corev1.Service{}
	case "ServiceAccount":
		return &corev1.ServiceAccount{}
	}

	return nil
}

// crdKinds are the kinds whose schema is not in k8s.io/api because they are
// custom resources the Prometheus operator installs. They get the strict YAML
// half of the check -- duplicate keys are still rejected -- and not the typed
// half, because there is no type here to be strict against. The list is closed
// on purpose: a NEW standard kind that this file forgot lands in
// typedObjectFor's default and fails, rather than joining an "unchecked" bucket.
var crdKinds = map[string]bool{
	"PrometheusRule": true,
	"ServiceMonitor": true,
}

// kindsCovered is every kind any template in the chart can emit. Rendering a
// mode set that never produces one of these means this guard has a hole, so the
// union across all modes must equal it exactly. Keep it in step with
// `grep -h '^kind:' deploy/chart/templates/*.yaml | sort -u`.
var kindsCovered = []string{
	"ClusterRole",
	"ClusterRoleBinding",
	"ConfigMap",
	"DaemonSet",
	"Deployment",
	"Ingress",
	"Job",
	"NetworkPolicy",
	"PersistentVolumeClaim",
	"PodDisruptionBudget",
	"PrometheusRule",
	"Role",
	"RoleBinding",
	"Secret",
	"Service",
	"ServiceAccount",
	"ServiceMonitor",
}

func TestEveryRenderedObjectDecodesAsTheKubernetesObjectItClaimsToBe(t *testing.T) {
	seen := map[string]bool{}

	for _, mode := range typedModes {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			documents := 0
			for _, chunk := range splitDocuments(out) {
				var head renderedObject
				if err := yaml.Unmarshal([]byte(chunk), &head); err != nil {
					t.Fatalf("%s: the render is not YAML at all: %v\n%s",
						sourceOf(chunk), err, chunk)
				}
				if head.Kind == "" {
					continue
				}
				documents++
				seen[head.Kind] = true

				object := typedObjectFor(head.Kind)
				if object == nil {
					if !crdKinds[head.Kind] {
						t.Errorf("%s renders kind %q and typedObjectFor has no case for it; "+
							"add the k8s.io/api type so this object is actually decoded, or "+
							"add it to crdKinds with the reason",
							sourceOf(chunk), head.Kind)

						continue
					}
					// CRD: strict YAML only. A map accepts any field, so this
					// catches duplicate keys and malformed structure and makes
					// no claim about the schema.
					var loose map[string]any
					if err := yaml.UnmarshalStrict([]byte(chunk), &loose); err != nil {
						t.Errorf("%s (%s) is not strictly-decodable YAML: %v",
							sourceOf(chunk), head.Kind, err)
					}

					continue
				}

				if err := yaml.UnmarshalStrict([]byte(chunk), object); err != nil {
					t.Errorf("%s renders a %s the Kubernetes API server rejects: %v\n%s",
						sourceOf(chunk), head.Kind, err, numbered(chunk))
				}
			}

			if documents < mode.floor {
				t.Fatalf("%s rendered only %d objects, below the floor of %d; the render "+
					"collapsed and every decode above passed vacuously",
					mode.name, documents, mode.floor)
			}
		})
	}

	var missing []string
	for _, kind := range kindsCovered {
		if !seen[kind] {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("no mode in typedModes renders %s, so nothing typed-decodes %s; "+
			"add a mode that does, or drop the kind from kindsCovered if no template emits it",
			strings.Join(missing, ", "), plural(len(missing)))
	}
}

// ---------------------------------------------------------------------------
// The structural arm: a decoder is not a validator
// ---------------------------------------------------------------------------
//
// Phase 8 review R2-F2, and it is the round-1 blocker one layer on. The blanket
// decode above catches a field that changed SHAPE -- a list that became a list
// of maps, a duplicate key, an unknown field. It cannot catch a field that was
// REMOVED, because absence decodes: delete `image:` from the output controllers'
// container and every `UnmarshalStrict` in this file succeeds, `Image` is "",
// and the whole directory stays green on a chart the API server refuses with
// `spec.template.spec.containers[0].image: Required value`. Measured at
// cc1d77ad5e: `go test ./deploy/chart/tests/` was **ok 27.5s** with that line
// deleted from _hangar-output-controller.tpl.
//
// So this arm states the fields the API server itself requires, over every Pod
// template every mode renders. It is deliberately small: only rules whose
// violation the API server rejects outright, never house style, because a
// structural guard that also encodes preference is one refactor away from being
// disabled wholesale.
//
// Non-vacuity is a floor on the number of Pod templates walked, not a nonzero
// check: the bug this replaces was a guard that stopped looking.
//
// And the kinds it does NOT walk are declared rather than fallen through. The
// switch below used to end in `default: continue`, which is the same shape as
// the defect this whole file exists for: a CronJob or a StatefulSet added to
// this chart tomorrow carries a pod template the API server validates, and it
// would have been skipped in silence. kindsWithoutPodTemplates is checked
// against kindsCovered at the end, so a new kind lands in one list or the
// other by decision.
func TestEveryRenderedPodTemplateCarriesTheFieldsTheAPIServerRequires(t *testing.T) {
	// Every mode, because the controllers only exist in some of them and the
	// DaemonSet only in others.
	walked := 0
	walkedKinds := map[string]bool{}

	for _, mode := range typedModes {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			for _, chunk := range splitDocuments(out) {
				var head renderedObject
				if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind == "" {
					continue
				}

				var (
					template corev1.PodTemplateSpec
					selector map[string]string
					isJob    bool
				)

				switch head.Kind {
				case "Deployment":
					var object appsv1.Deployment
					if err := yaml.UnmarshalStrict([]byte(chunk), &object); err != nil {
						continue // the blanket decode above reports this
					}
					template = object.Spec.Template
					if object.Spec.Selector == nil {
						t.Errorf("%s Deployment %s has no spec.selector; the API server "+
							"requires it and rejects the object outright",
							sourceOf(chunk), head.Metadata.Name)

						continue
					}
					selector = object.Spec.Selector.MatchLabels
				case "DaemonSet":
					var object appsv1.DaemonSet
					if err := yaml.UnmarshalStrict([]byte(chunk), &object); err != nil {
						continue
					}
					template = object.Spec.Template
					if object.Spec.Selector == nil {
						t.Errorf("%s DaemonSet %s has no spec.selector",
							sourceOf(chunk), head.Metadata.Name)

						continue
					}
					selector = object.Spec.Selector.MatchLabels
				case "Job":
					var object batchv1.Job
					if err := yaml.UnmarshalStrict([]byte(chunk), &object); err != nil {
						continue
					}
					template = object.Spec.Template
					isJob = true
				default:
					if !kindsWithoutPodTemplates[head.Kind] {
						t.Errorf("%s renders kind %q, which this guard neither walks nor "+
							"declares free of pod templates. If it carries one, add a case "+
							"for it; if it does not, say so in kindsWithoutPodTemplates -- "+
							"a kind that falls through in silence is exactly the shape of "+
							"defect this file exists for.", sourceOf(chunk), head.Kind)
					}

					continue
				}

				walked++
				walkedKinds[head.Kind] = true
				where := fmt.Sprintf("%s %s %s", sourceOf(chunk), head.Kind, head.Metadata.Name)

				// A Pod with no regular container is rejected.
				if len(template.Spec.Containers) == 0 {
					t.Errorf("%s renders a Pod template with no containers; "+
						"spec.template.spec.containers is Required value", where)
				}

				// name and image are Required value on EVERY container, init
				// and regular alike. This is the arm the removed `image:` needs.
				for kindOfContainer, containers := range map[string][]corev1.Container{
					"containers":     template.Spec.Containers,
					"initContainers": template.Spec.InitContainers,
				} {
					names := map[string]bool{}
					for i, container := range containers {
						if strings.TrimSpace(container.Name) == "" {
							t.Errorf("%s: %s[%d] has no name; it is Required value",
								where, kindOfContainer, i)
						}
						if strings.TrimSpace(container.Image) == "" {
							t.Errorf("%s: %s[%d] (%q) has no image. The API server rejects "+
								"this with `image: Required value`, and a strict DECODE "+
								"cannot see it, because an absent string decodes to \"\".",
								where, kindOfContainer, i, container.Name)
						}
						if names[container.Name] {
							t.Errorf("%s: %s has two containers named %q; container names "+
								"must be unique within a Pod",
								where, kindOfContainer, container.Name)
						}
						names[container.Name] = true
					}
				}

				// Every volumeMount must name a volume the Pod declares. AC 19
				// states this for the generated capture Pod; it is true of every
				// Pod, and the API server refuses the mismatch.
				volumes := map[string]bool{}
				for _, volume := range template.Spec.Volumes {
					volumes[volume.Name] = true
				}
				for _, container := range append(
					append([]corev1.Container{}, template.Spec.Containers...),
					template.Spec.InitContainers...,
				) {
					for _, mount := range container.VolumeMounts {
						if !volumes[mount.Name] {
							t.Errorf("%s: container %q mounts volume %q, which the Pod "+
								"template does not declare",
								where, container.Name, mount.Name)
						}
					}
				}

				// A Job's Pod template must set restartPolicy, and only to
				// Never or OnFailure; the default Always is rejected.
				if isJob {
					switch template.Spec.RestartPolicy {
					case corev1.RestartPolicyNever, corev1.RestartPolicyOnFailure:
					default:
						t.Errorf("%s: a Job's pod template restartPolicy is %q; the API "+
							"server accepts only Never or OnFailure",
							where, template.Spec.RestartPolicy)
					}
				}

				// The selector must be present and must select the template the
				// controller owns, or the controller adopts nothing and the
				// API server rejects the object at admission.
				if selector != nil {
					if len(selector) == 0 {
						t.Errorf("%s has an empty spec.selector.matchLabels; an empty "+
							"selector selects every Pod in the namespace", where)
					}
					for key, value := range selector {
						if template.Labels[key] != value {
							t.Errorf("%s: spec.selector wants %s=%s but the pod template "+
								"has %s=%q; `selector does not match template labels`",
								where, key, value, key, template.Labels[key])
						}
					}
				}
			}
		})
	}

	// Seven modes; the smallest renders the web Deployment and the worker's,
	// and the largest adds the DaemonSet, three controllers and a Job. Well
	// under the real number, and far above zero.
	if walked < 20 {
		t.Fatalf("this guard walked only %d pod templates across %d modes; it has stopped "+
			"looking at the render", walked, len(typedModes))
	}

	// Every kind the chart can emit is accounted for: walked, or declared to
	// carry no pod template. A kind in neither list is a hole.
	for _, kind := range kindsCovered {
		if walkedKinds[kind] || kindsWithoutPodTemplates[kind] {
			continue
		}
		t.Errorf("kind %q is in kindsCovered and this guard neither walked it nor declared "+
			"it free of pod templates", kind)
	}
}

// kindsWithoutPodTemplates carry no `spec.template.spec` for the API server to
// validate. Declared rather than defaulted: the point is that adding a kind is
// a decision somebody writes down.
var kindsWithoutPodTemplates = map[string]bool{
	"ClusterRole":           true,
	"ClusterRoleBinding":    true,
	"ConfigMap":             true,
	"Ingress":               true,
	"NetworkPolicy":         true,
	"PersistentVolumeClaim": true,
	"PodDisruptionBudget":   true,
	"PrometheusRule":        true,
	"Role":                  true,
	"RoleBinding":           true,
	"Secret":                true,
	"Service":               true,
	"ServiceAccount":        true,
	"ServiceMonitor":        true,
}

// The specific shape that got past everything: a workload's `command` is a list
// of strings and its `env` exists. Stated separately from the blanket decode
// because the blanket decode's failure message names a JSON path, and the next
// person to chomp a newline should be told what the chomp did.
func TestEveryOutputWorkloadKeepsItsCommandAndItsEnvironment(t *testing.T) {
	out := render(t, outputSets...)

	wanted := map[string]bool{
		outputDaemonComponent:    false,
		outputInventoryComponent: false,
		outputReclaimerComponent: false,
	}

	for _, chunk := range splitDocuments(out) {
		var head renderedObject
		if err := yaml.Unmarshal([]byte(chunk), &head); err != nil || head.Kind == "" {
			continue
		}

		var spec corev1.PodSpec
		switch head.Kind {
		case "Deployment":
			var object appsv1.Deployment
			if err := yaml.UnmarshalStrict([]byte(chunk), &object); err != nil {
				t.Errorf("%s: %v", sourceOf(chunk), err)

				continue
			}
			spec = object.Spec.Template.Spec
		case "DaemonSet":
			var object appsv1.DaemonSet
			if err := yaml.UnmarshalStrict([]byte(chunk), &object); err != nil {
				t.Errorf("%s: %v", sourceOf(chunk), err)

				continue
			}
			spec = object.Spec.Template.Spec
		default:
			continue
		}

		for _, container := range spec.Containers {
			if _, ok := wanted[container.Name]; !ok {
				continue
			}
			wanted[container.Name] = true

			if len(container.Command) == 0 {
				t.Errorf("%s: container %q has no command", sourceOf(chunk), container.Name)
			}
			for _, argument := range container.Command {
				if strings.ContainsAny(argument, "\n:") && strings.HasSuffix(argument, ":") {
					t.Errorf("%s: container %q command argument %q ends in a colon; a "+
						"template block was glued onto it",
						sourceOf(chunk), container.Name, argument)
				}
			}
			if len(container.Env) == 0 {
				t.Errorf("%s: container %q renders no env at all; every output workload "+
					"takes at least its database or its identity from the environment",
					sourceOf(chunk), container.Name)
			}
		}
	}

	for name, found := range wanted {
		if !found {
			t.Errorf("no rendered workload has a container named %q; the render changed "+
				"shape and this guard stopped looking at anything", name)
		}
	}
}

func numbered(chunk string) string {
	var out strings.Builder
	for i, line := range strings.Split(chunk, "\n") {
		fmt.Fprintf(&out, "%4d\t%s\n", i+1, line)
	}

	return out.String()
}

func plural(n int) string {
	if n == 1 {
		return "it"
	}

	return "them"
}
