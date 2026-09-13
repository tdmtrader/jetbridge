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
			"alertingRules.enabled=true",
			"metrics.enabled=true",
			"serviceMonitor.enabled=true",
			"ingress.enabled=true",
			"ingress.hosts[0]=ci.example",
			"pdb.enabled=true",
			"postgresql.enabled=true",
			"artifactDaemon.durable.store=gcs",
			"artifactDaemon.durable.bucket=jb-durable",
		),
		floor: 32,
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
		outputAttestorComponent:  false,
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
