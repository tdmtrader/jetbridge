package tests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

// Can the ATC reach the daemon it was told to dial?
//
// Every other rule in this directory reads one object. This one reads two and
// compares them, because the defect it exists for is invisible in either alone:
// the output daemon's DaemonSet (since folded into the artifact daemon's)
// declared a `containerPort` and no `hostPort`, which is a
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
// Typing 7780 into this file would pin the chart to a constant instead of to
// the thing that matters, which is that the two halves of one render agree.
//
// There is one node-dialed daemon: the artifact daemon serves the output plane
// on its own port, so the output plane's reachability is the artifact daemon's
// in every mode the plane can be in.

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
		sets:      nil,
	},
	{
		component: outputDaemonComponent,
		portFlag:  "--kubernetes-artifact-daemon-port=",
		sets:      baseControlSets,
	},
	{
		component: outputDaemonComponent,
		portFlag:  "--kubernetes-artifact-daemon-port=",
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
				"hold, the writer ticket, the seal, the publication and the read warrant all "+
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

// ---------------------------------------------------------------------------
// The scheme the ATC dials, and the trust it dials with
// ---------------------------------------------------------------------------
//
// The output plane is served by the artifact daemon, over the artifact
// daemon's TLS: one listener, one server certificate, one client certificate.
// It is TLS-only -- its off-node routes refuse any operation whose request
// carries no verified peer certificate -- so the daemon must render its three
// TLS flags and HTTPS probes, and the web must render the artifact daemon's
// client certificate and nothing of a second trust domain. Artifact daemon
// certificate ownership is varied below: the output plane moves with it.

var outputTLSModes = []struct {
	name string
	sets []string
}{
	{name: "base control only", sets: baseControlSets},
	{
		name: "base control, generated artifact daemon certificates",
		sets: append(append([]string{}, baseControlSets...), "artifactDaemon.tls.source=generated", "artifactDaemon.tls.existingSecret="),
	},
	{name: "output facet", sets: outputSets},
	{
		name: "output facet, generated artifact daemon certificates",
		sets: append(append([]string{}, outputSets...), "artifactDaemon.tls.source=generated", "artifactDaemon.tls.existingSecret="),
	},
}

func TestTheOutputPlaneIsServedOverTheArtifactDaemonsTLS(t *testing.T) {
	for _, mode := range outputTLSModes {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			// The server half: the daemon is given a certificate, a key and a
			// client CA, and both probes speak HTTPS.
			daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
			for _, flag := range []string{"--tls-cert=", "--tls-key=", "--tls-ca-cert=", "--control-key-file="} {
				if !strings.Contains(daemon.body, flag) {
					t.Errorf("the artifact daemon renders no %s with the output plane on", flag)
				}
			}
			if strings.Count(daemon.body, "scheme: HTTPS") != 2 {
				t.Errorf("the artifact daemon's liveness and readiness probes do not both " +
					"speak HTTPS; the kubelet would be talking plaintext to a TLS listener")
			}

			// The client half: the ATC presents the artifact daemon's client
			// certificate, and no second output-plane credential exists.
			web := objectNamed(t, out, "Deployment", "-web")
			for _, flag := range []string{
				"--kubernetes-artifact-daemon-tls-cert=",
				"--kubernetes-artifact-daemon-tls-key=",
				"--kubernetes-artifact-daemon-tls-ca-cert=",
			} {
				if !strings.Contains(web.body, flag) {
					t.Errorf("the web pod renders no %s, so the ATC dials the output plane "+
						"with no client certificate: its off-node routes refuse every "+
						"operation whose request carries no verified peer certificate", flag)
				}
			}
			for _, gone := range []string{
				"--kubernetes-hangar-output-tls-", "--kubernetes-hangar-output-daemon-port",
			} {
				if strings.Contains(web.body, gone) {
					t.Errorf("the web pod renders %s; the output plane has no transport of "+
						"its own any more", gone)
				}
			}
		})
	}
}

// The daemon's SERVER key is private to the daemon Pod.
//
// The web dials the cohort with a client certificate. It takes it out of the
// artifact daemon's TLS Secret, which also holds tls.key -- the key the daemon
// SERVES with -- so it projects the client material and the CA and never
// tls.key: whatever held it could impersonate the daemon to the ATC.
func TestTheDaemonsServerKeyIsMountedInTheDaemonPodAndNowhereElse(t *testing.T) {
	for _, mode := range outputTLSModes {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			carriers := 0
			for _, subject := range documentsIn(t, out) {
				if !strings.Contains(subject.body, "artifact-daemon-tls") &&
					!strings.Contains(subject.body, "test-daemon-tls") {
					continue
				}
				if subject.kind == "Secret" || subject.kind == "ConfigMap" ||
					(subject.kind == "DaemonSet" && strings.HasSuffix(subject.name, "-artifact-daemon")) {
					continue
				}
				carriers++
				if strings.Contains(subject.body, "key: tls.key") {
					t.Errorf("%s/%s mounts tls.key out of the artifact daemon's TLS Secret, "+
						"the key the daemon SERVES with", subject.kind, subject.name)
				}
			}
			if carriers == 0 {
				t.Fatal("nothing but the daemon references its TLS Secret; the web's client " +
					"certificate is missing and this rule would pass vacuously")
			}
		})
	}
}
