package tests

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// The web node finds artifact daemons by listing their Service's
// EndpointSlices, and verifies each one's certificate against
// <service>.<namespace>.svc. Both follow the daemon's namespace - the release
// namespace, where the chart puts the DaemonSet, its headless Service and the
// certificate's SANs - not kubernetes.namespace, which only moves step pods.
// With the two split, a web node told only the step namespace lists slices in
// a namespace holding none of the daemon's, and would verify against a name
// absent from the certificate.
//
// This is the web-side twin of
// TestArtifactDaemonCertificateCarriesTheNameItsPeersVerify.
func TestTheWebNodeLooksForTheArtifactDaemonInTheReleaseNamespace(t *testing.T) {
	out := renderInNamespace(t, "jbns",
		"artifactDaemon.tls.source=generated",
		"artifactDaemon.tls.existingSecret=",
		"kubernetes.namespace=tasks",
	)

	var webArgs []string
	var serverCert []byte
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
		case meta.Kind == "Deployment" && strings.HasSuffix(meta.Metadata.Name, "-web"):
			var dep appsv1.Deployment
			if err := yaml.UnmarshalStrict([]byte(doc), &dep); err != nil {
				t.Fatalf("decode web Deployment: %v", err)
			}
			for _, c := range dep.Spec.Template.Spec.Containers {
				if c.Name == "concourse-web" {
					webArgs = append(append([]string{}, c.Command...), c.Args...)
				}
			}
		case meta.Kind == "Secret" && strings.HasSuffix(meta.Metadata.Name, "-artifact-daemon-tls"):
			var secret corev1.Secret
			if err := yaml.UnmarshalStrict([]byte(doc), &secret); err != nil {
				t.Fatalf("decode artifact daemon TLS Secret: %v", err)
			}
			serverCert = secret.Data["tls.crt"]
		}
	}
	if webArgs == nil {
		t.Fatal("no web container rendered")
	}
	if serverCert == nil {
		t.Fatal("no artifact daemon TLS Secret rendered")
	}

	if got := flagValue(t, webArgs, "--kubernetes-namespace"); got != "tasks" {
		t.Fatalf("--kubernetes-namespace = %q, want %q: the render did not split the namespaces", got, "tasks")
	}

	namespace := flagValue(t, webArgs, "--kubernetes-artifact-daemon-namespace")
	if namespace != "jbns" {
		t.Errorf("--kubernetes-artifact-daemon-namespace = %q, want the release namespace %q: "+
			"the daemon's Service and EndpointSlices live there", namespace, "jbns")
	}

	service := flagValue(t, webArgs, "--kubernetes-artifact-daemon-service")
	name := service + "." + namespace + ".svc"
	if err := parseCert(t, serverCert).VerifyHostname(name); err != nil {
		t.Errorf("the web node verifies daemons against %q, which the chart's certificate does not carry: %v", name, err)
	}
}
