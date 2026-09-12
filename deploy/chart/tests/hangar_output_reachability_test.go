package tests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// Can the ATC reach the daemon it was told to dial?
//
// Every other rule in this directory reads one object. This one reads two and
// compares them, because the defect it exists for is invisible in either alone:
// the output DaemonSet declared a `containerPort` and no `hostPort`, which is a
// perfectly well-formed Pod, while the ATC dials the NODE IP -- the resolver at
// atc/worker/jetbridge/output_control.go composes
// `scheme://<node InternalIP>:<port>` and the capture control init dials
// `${HOST_IP}:${PORT}` from `status.hostIP`. A DaemonSet pod with neither
// `hostNetwork` nor `hostPort` listens on the POD IP only, so nothing was bound
// where every client calls and the base facet did nothing at all on a cluster.
//
// It was invisible to every tier because the one kubelet-level test builds its
// own stand-in daemon and gives it `HostNetwork: true`, which is the shape the
// chart does not render; the chart's DaemonSet is applied by no tier at all.
//
// The port is read out of the WEB pod's own flag rather than written here.
// Typing 7781 into this file would pin the chart to a constant instead of to
// the thing that matters, which is that the two halves of one render agree.

// nodeDialedDaemons are the DaemonSets the ATC addresses by node IP, and the
// web flag that tells it which port to use. A daemon in this list must publish
// that port on the host.
var nodeDialedDaemons = []struct {
	component string
	portFlag  string
	sets      []string
}{
	{
		component: "artifact-daemon",
		portFlag:  "--kubernetes-artifact-daemon-port=",
		sets:      baseControlSets,
	},
	{
		component: outputDaemonComponent,
		portFlag:  "--kubernetes-hangar-output-daemon-port=",
		sets:      baseControlSets,
	},
	{
		component: outputDaemonComponent,
		portFlag:  "--kubernetes-hangar-output-daemon-port=",
		sets:      outputSets,
	},
}

func TestEveryDaemonTheATCDialsByNodeIPPublishesThatPortOnTheNode(t *testing.T) {
	checked := 0

	for _, daemon := range nodeDialedDaemons {
		out := render(t, daemon.sets...)

		port := flagValueInRender(t, out, daemon.portFlag)
		if port == "" {
			t.Fatalf("no container in this render is given %s, so this rule has nothing "+
				"to compare and would pass vacuously", daemon.portFlag)
		}

		set := objectNamed(t, out, "DaemonSet", "-"+daemon.component)

		var object appsv1.DaemonSet
		if err := yaml.UnmarshalStrict([]byte(set.body), &object); err != nil {
			t.Fatalf("%s: %v", set.source, err)
		}
		spec := object.Spec.Template.Spec

		if spec.HostNetwork {
			// Host network publishes every containerPort on the node by
			// definition. Nothing else to check.
			checked++

			continue
		}

		published := false
		var declared []int32
		for _, container := range spec.Containers {
			for _, containerPort := range container.Ports {
				declared = append(declared, containerPort.ContainerPort)
				if formatPort(containerPort.ContainerPort) != port {
					continue
				}
				if containerPort.HostPort == containerPort.ContainerPort {
					published = true
				}
			}
		}
		if !published {
			t.Errorf("%s DaemonSet %s is dialed by the ATC at <node IP>:%s -- the web pod "+
				"carries %s%s in this same render -- and it publishes no hostPort for it "+
				"(declared container ports: %v, hostNetwork: false).\n\n"+
				"A DaemonSet pod with neither hostNetwork nor a hostPort listens on the POD "+
				"IP only, so nothing is bound where every client calls: the pre-start source "+
				"hold, the writer ticket, the seal, the publication and the read grant all "+
				"fail, and the facet does nothing at all on a cluster. The render is "+
				"well-formed and every suite stays green.",
				set.source, set.name, port, daemon.portFlag, port, declared)
		}
		checked++
	}

	if checked != len(nodeDialedDaemons) {
		t.Fatalf("checked %d of %d node-dialed daemons", checked, len(nodeDialedDaemons))
	}
}

// flagValueInRender returns the value of the first `- --flag=value` argument in
// the render whose flag matches prefix, or "" when no container carries it.
func flagValueInRender(t *testing.T, out, prefix string) string {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- "+prefix) {
			continue
		}

		return strings.TrimPrefix(trimmed, "- "+prefix)
	}

	return ""
}

func formatPort(port int32) string {
	digits := ""
	for value := port; value > 0; value /= 10 {
		digits = string(rune('0'+value%10)) + digits
	}
	if digits == "" {
		return "0"
	}

	return digits
}

// containersOf is every container in a pod spec, init and regular alike.
func containersOf(spec corev1.PodSpec) []corev1.Container {
	return append(append([]corev1.Container{}, spec.Containers...), spec.InitContainers...)
}
