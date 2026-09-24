package tests

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// A daemon verifies each peer against <--service-name>.<--namespace>.svc
// (cmd/artifact-daemon peerTLSServerName), because peers are dialed by pod IP
// and no pod IP is on the certificate. That name is composed from the
// daemon's flags, and the certificate's SANs from the chart's own values, so
// the two can disagree with every suite green — and a disagreement fails every
// peer handshake, silently, since peer paths fail closed. This renders both and
// makes the certificate answer for the name the daemon will ask it for.
//
// kubernetes.namespace is set to something other than the release namespace
// because that is exactly the case that can split them: it moves where step
// pods run, not where the daemon, its Service or its certificate live.
func TestArtifactDaemonCertificateCarriesTheNameItsPeersVerify(t *testing.T) {
	out := renderInNamespace(t, "jbns",
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
		"kubernetes.namespace=step-pods",
	)

	var daemonArgs []string
	var serverCert *x509.Certificate
	for _, doc := range splitYAMLDocs(out) {
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatalf("decode document: %v", err)
		}
		switch {
		case meta.Kind == "DaemonSet" && strings.HasSuffix(meta.Metadata.Name, "-artifact-daemon"):
			var ds appsv1.DaemonSet
			if err := yaml.UnmarshalStrict([]byte(doc), &ds); err != nil {
				t.Fatalf("decode artifact daemon DaemonSet: %v", err)
			}
			for _, c := range ds.Spec.Template.Spec.Containers {
				if c.Name == "artifact-daemon" {
					daemonArgs = append(c.Command, c.Args...)
				}
			}
		case meta.Kind == "Secret" && strings.HasSuffix(meta.Metadata.Name, "-artifact-daemon-tls"):
			var secret corev1.Secret
			if err := yaml.UnmarshalStrict([]byte(doc), &secret); err != nil {
				t.Fatalf("decode artifact daemon TLS Secret: %v", err)
			}
			serverCert = parseCert(t, secret.Data["tls.crt"])
		}
	}
	if daemonArgs == nil {
		t.Fatal("no artifact-daemon container rendered")
	}
	if serverCert == nil {
		t.Fatal("no artifact daemon TLS Secret rendered")
	}

	service := flagValue(t, daemonArgs, "--service-name")
	namespace := flagValue(t, daemonArgs, "--namespace")
	peerName := service + "." + namespace + ".svc"

	if err := serverCert.VerifyHostname(peerName); err != nil {
		t.Errorf("daemons verify peers against %q (from --service-name and --namespace), "+
			"but the chart's certificate does not carry it (SANs %v): every peer handshake fails: %v",
			peerName, serverCert.DNSNames, err)
	}
	if namespace != "jbns" {
		t.Errorf("--namespace = %q, want the release namespace %q: the daemon's headless Service "+
			"and its EndpointSlices live there, so peer discovery lists the wrong namespace otherwise",
			namespace, "jbns")
	}
}

// Secret.Data decodes base64 itself, so the bytes here are PEM.
func parseCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatalf("tls.crt is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse tls.crt: %v", err)
	}
	return cert
}

func flagValue(t *testing.T, args []string, name string) string {
	t.Helper()
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
	}
	t.Fatalf("artifact-daemon is not passed %s", name)
	return ""
}
