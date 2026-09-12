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

// ---------------------------------------------------------------------------
// The scheme the ATC dials, and the trust it dials with
// ---------------------------------------------------------------------------
//
// The output daemon's control API is HTTPS-only: its --tls-* flags render
// unconditionally and cmd/hangar-output-daemon's buildControlTLSConfig has no
// plaintext branch. The ATC's scheme used to be derived from
// `artifactDaemon.tls.enabled` -- a switch belonging to a DIFFERENT daemon,
// serving a different bucket under a different identity, and false by default
// -- so under the chart's own documented values the ATC dialed `http://` at an
// HTTPS listener and the base facet was unreachable a second way.
//
// Behind that was a trust defect with no rendered evidence at all: even with
// that switch on, the ATC presented `client.crt`/`client.key` out of the
// ARTIFACT daemon's Secret while the output daemon's ClientCAs pool came from
// its own, and the control routes refuse any operation whose request carries no
// verified peer certificate. Two independently-provisioned Secrets handshake
// and then refuse every call.
//
// The shape this pins: the output plane has its own trust domain -- its own
// client Secret, its own CA, its own server name -- and exactly one supported
// mode, because the daemon can serve exactly one. `artifactDaemon.tls.enabled`
// is deliberately varied in both directions below: the output plane's scheme
// must not move with it.

var outputTLSModes = []struct {
	name string
	sets []string
}{
	{name: "base control only", sets: baseControlSets},
	{
		name: "base control, artifact daemon TLS on",
		sets: append(append([]string{}, baseControlSets...), "artifactDaemon.tls.enabled=true"),
	},
	{name: "output facet", sets: outputSets},
	{
		name: "output facet, artifact daemon TLS on",
		sets: append(append([]string{}, outputSets...), "artifactDaemon.tls.enabled=true"),
	},
}

func TestTheSchemeTheATCDialsIsTheSchemeTheOutputDaemonServes(t *testing.T) {
	for _, mode := range outputTLSModes {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			// The server half: the daemon is given a certificate, a key and a
			// client CA, and both probes speak HTTPS.
			daemon := objectNamed(t, out, "DaemonSet", "-"+outputDaemonComponent)
			for _, flag := range []string{"--tls-cert=", "--tls-key=", "--tls-ca-cert="} {
				if !strings.Contains(daemon.body, flag) {
					t.Errorf("the output daemon renders no %s; its control API has no "+
						"plaintext branch, so it would not start", flag)
				}
			}
			if strings.Count(daemon.body, "scheme: HTTPS") != 2 {
				t.Errorf("the output daemon's liveness and readiness probes do not both " +
					"speak HTTPS; the kubelet would be talking plaintext to a TLS listener")
			}

			// The client half: the ATC is given the OUTPUT plane's client
			// certificate, its CA, and the name to verify the daemon by.
			web := objectNamed(t, out, "Deployment", "-web")
			for _, flag := range []string{
				"--kubernetes-hangar-output-tls-cert=",
				"--kubernetes-hangar-output-tls-key=",
				"--kubernetes-hangar-output-tls-ca-cert=",
				"--kubernetes-hangar-output-tls-server-name=",
			} {
				if !strings.Contains(web.body, flag) {
					t.Errorf("the web pod renders no %s, so the ATC dials the output daemon "+
						"with no client certificate of its own. The daemon's control routes "+
						"refuse every operation whose request carries no verified peer "+
						"certificate: the plane handshakes and then does nothing.", flag)
				}
			}

			// And the material is the output plane's own, not the artifact
			// daemon's. Two planes that may not share a bucket or a client do
			// not share a certificate authority either.
			if strings.Contains(web.body, "/etc/concourse/daemon-tls/client.crt") &&
				!strings.Contains(web.body, "--kubernetes-artifact-daemon-tls-cert=") {
				t.Error("the web pod takes output-plane client material out of the artifact " +
					"daemon's Secret")
			}
			if !strings.Contains(web.body, "op-output-daemon-client-tls") {
				t.Error("the web pod does not mount hangarOutput.daemon.tls.clientSecret; " +
					"its client certificate has to be issued in the output plane's own " +
					"trust domain, because that is the CA the daemon verifies against")
			}
		})
	}
}

// The daemon's SERVER key is private to the daemon Pod.
//
// The attest Job mounted `hangarOutput.daemon.tls.existingSecret` -- tls.crt,
// tls.key AND ca.crt -- and passed them as its client certificate. The
// template's own header called it "the daemon control API's client
// certificate", but it is the Secret the daemon SERVES with: an exploit of the
// activation Job obtained the material to impersonate the output daemon to the
// ATC. The existing "private key absent from web/controllers/tasks" rule does
// not cover tls.key.
func TestTheOutputDaemonsServerKeyIsMountedInTheDaemonPodAndNowhereElse(t *testing.T) {
	modes := append(append([]struct {
		name string
		sets []string
	}{}, outputTLSModes...), struct {
		name string
		sets []string
	}{
		name: "the attest Job, which needs a client certificate",
		sets: append(append([]string{}, outputSets...),
			"hangarOutput.activation.job.mode=attest",
			"hangarOutput.activation.job.facet=base"),
	})

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			out := render(t, mode.sets...)

			carriers := []string{}
			for _, subject := range documentsIn(t, out) {
				if !strings.Contains(subject.body, "op-output-daemon-tls") {
					continue
				}
				carriers = append(carriers, subject.kind+"/"+subject.name)
			}
			if len(carriers) == 0 {
				t.Fatal("nothing references the output daemon's server TLS Secret; this " +
					"rule would pass vacuously")
			}
			for _, carrier := range carriers {
				if !strings.Contains(carrier, outputDaemonComponent) {
					t.Errorf("%s references %q, the Secret the output daemon SERVES with. "+
						"It holds tls.key: whatever holds it can impersonate the daemon to "+
						"the ATC. A client needs a client certificate, which is a different "+
						"Secret in the same trust domain.", carrier, "op-output-daemon-tls")
				}
			}
		})
	}
}
