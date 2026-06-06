package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// daemonServesTLS reports whether the deployed daemon is serving HTTPS, detected
// from the pod's readiness probe scheme (set by the chart when TLS is enabled).
func daemonServesTLS(pod *corev1.Pod) bool {
	c := mainContainer(pod)
	if c.ReadinessProbe != nil && c.ReadinessProbe.HTTPGet != nil {
		return c.ReadinessProbe.HTTPGet.Scheme == corev1.URISchemeHTTPS
	}
	return false
}

// daemonTLSSecretName returns the secret backing the daemon's "daemon-tls"
// volume, or "" if TLS is not mounted.
func daemonTLSSecretName(pod *corev1.Pod) string {
	for _, v := range pod.Spec.Volumes {
		if v.Name == "daemon-tls" && v.Secret != nil {
			return v.Secret.SecretName
		}
	}
	return ""
}

// daemonMTLSClients builds two HTTPS clients from the daemon-tls secret: one
// presenting the client certificate (withCert) and one trusting only the server
// CA (noCert). The daemon's server cert carries 127.0.0.1/localhost SANs, so
// both verify the server over a port-forward.
func daemonMTLSClients(pod *corev1.Pod) (withCert, noCert *http.Client) {
	secretName := daemonTLSSecretName(pod)
	Expect(secretName).ToNot(BeEmpty(), "expected daemon-tls secret to be mounted on the daemon pod")

	secret, err := kubeClient.CoreV1().Secrets(config.Namespace).Get(
		context.Background(), secretName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())

	caPool := x509.NewCertPool()
	Expect(caPool.AppendCertsFromPEM(secret.Data["ca.crt"])).To(BeTrue(), "daemon CA cert should parse")

	clientCert, err := tls.X509KeyPair(secret.Data["client.crt"], secret.Data["client.key"])
	Expect(err).ToNot(HaveOccurred(), "client cert/key should load")

	withCert = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      caPool,
			Certificates: []tls.Certificate{clientCert},
		}},
	}
	noCert = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool}},
	}
	return withCert, noCert
}

var _ = Describe("Artifact Daemon Security", func() {
	var daemonPod *corev1.Pod

	BeforeEach(func() {
		// Find the running artifact daemon pod.
		pods := getPods("app.kubernetes.io/component=artifact-daemon")
		Expect(pods).ToNot(BeEmpty(), "expected at least one artifact daemon pod")
		daemonPod = &pods[0]
		Expect(daemonPod.Status.Phase).To(Equal(corev1.PodRunning),
			"expected daemon pod to be Running")
	})

	It("has hardened container SecurityContext", func() {
		c := mainContainer(daemonPod)
		Expect(c.SecurityContext).ToNot(BeNil(), "expected SecurityContext on daemon container")

		By("verifying AllowPrivilegeEscalation is false")
		Expect(c.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
		Expect(*c.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())

		By("verifying capabilities are dropped")
		Expect(c.SecurityContext.Capabilities).ToNot(BeNil())
		Expect(c.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))

		By("verifying seccomp profile is RuntimeDefault")
		Expect(c.SecurityContext.SeccompProfile).ToNot(BeNil())
		Expect(c.SecurityContext.SeccompProfile.Type).To(
			Equal(corev1.SeccompProfileTypeRuntimeDefault))
	})

	It("can write to hostPath storage", func() {
		By("port-forwarding to the daemon pod (pod IPs are not routable from the test host)")
		daemonURL, stop := portForwardDaemon(daemonPod.Name, 7780)
		defer stop()

		// When the daemon is TLS-hardened, /artifacts is a protected path:
		// connect over HTTPS and present the client cert. Otherwise plain HTTP.
		client := &http.Client{Timeout: 10 * time.Second}
		if daemonServesTLS(daemonPod) {
			daemonURL = strings.Replace(daemonURL, "http://", "https://", 1)
			client, _ = daemonMTLSClients(daemonPod)
		}

		By("verifying /healthz is reachable")
		Eventually(func() int {
			resp, err := client.Get(daemonURL + "/healthz")
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}, 30*time.Second, time.Second).Should(Equal(http.StatusOK))

		By("PUT-ing a test artifact to verify hostPath writes")
		testKey := fmt.Sprintf("security-test-%d", time.Now().UnixNano())
		testContent := "hostpath-write-verification"

		req, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodPut,
			daemonURL+"/artifacts/"+testKey,
			strings.NewReader(testContent),
		)
		Expect(err).ToNot(HaveOccurred())

		resp, err := client.Do(req)
		Expect(err).ToNot(HaveOccurred(), "PUT to daemon should succeed — if this fails, the daemon likely cannot write to its hostPath due to SecurityContext permissions")
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusCreated),
			"daemon should be able to write artifacts to hostPath")

		By("GET-ing the artifact back to confirm round-trip")
		getResp, err := client.Get(daemonURL + "/artifacts/" + testKey)
		Expect(err).ToNot(HaveOccurred())
		defer getResp.Body.Close()
		Expect(getResp.StatusCode).To(Equal(http.StatusOK))

		body, err := io.ReadAll(getResp.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(body)).To(Equal(testContent))

		By("cleaning up the test artifact")
		delReq, _ := http.NewRequest(http.MethodDelete,
			daemonURL+"/artifacts/"+testKey, nil)
		delResp, _ := client.Do(delReq)
		if delResp != nil {
			delResp.Body.Close()
		}
	})

	It("enforces mTLS on protected paths when TLS is enabled", func() {
		if !daemonServesTLS(daemonPod) {
			Skip("daemon not deployed with TLS — set ARTIFACT_DAEMON_TLS=true to run this check")
		}

		// Reaching this point already proves the HTTPS readiness/liveness
		// probes pass: the suite waits for the daemon pod to become Ready,
		// and with TLS on those probes use scheme: HTTPS.
		By("port-forwarding to the TLS daemon")
		daemonURL, stop := portForwardDaemon(daemonPod.Name, 7780)
		defer stop()
		daemonURL = strings.Replace(daemonURL, "http://", "https://", 1)
		withCert, noCert := daemonMTLSClients(daemonPod)

		key := fmt.Sprintf("mtls-test-%d", time.Now().UnixNano())

		By("rejecting a protected path (PUT /artifacts) without a client cert → 401")
		req, err := http.NewRequest(http.MethodPut, daemonURL+"/artifacts/"+key, strings.NewReader("x"))
		Expect(err).ToNot(HaveOccurred())
		resp, err := noCert.Do(req)
		Expect(err).ToNot(HaveOccurred(),
			"TLS handshake should succeed (server cert is trusted); only the client cert is absent")
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized),
			"protected paths must require a client certificate")

		By("allowing the exempt /healthz path without a client cert → 200")
		hresp, err := noCert.Get(daemonURL + "/healthz")
		Expect(err).ToNot(HaveOccurred())
		hresp.Body.Close()
		Expect(hresp.StatusCode).To(Equal(http.StatusOK),
			"/healthz is exempt and must work without a client cert over HTTPS")

		By("allowing a protected path WITH a valid client cert → 201")
		preq, err := http.NewRequest(http.MethodPut, daemonURL+"/artifacts/"+key, strings.NewReader("mtls-ok"))
		Expect(err).ToNot(HaveOccurred())
		presp, err := withCert.Do(preq)
		Expect(err).ToNot(HaveOccurred())
		presp.Body.Close()
		Expect(presp.StatusCode).To(Equal(http.StatusCreated),
			"a valid client cert must be accepted on protected paths")

		By("cleaning up the test artifact")
		delReq, _ := http.NewRequest(http.MethodDelete, daemonURL+"/artifacts/"+key, nil)
		if delResp, _ := withCert.Do(delReq); delResp != nil {
			delResp.Body.Close()
		}
	})

	It("pod-level seccomp profile is set", func() {
		sc := daemonPod.Spec.SecurityContext
		Expect(sc).ToNot(BeNil(), "expected pod-level SecurityContext")
		Expect(sc.SeccompProfile).ToNot(BeNil())
		Expect(sc.SeccompProfile.Type).To(
			Equal(corev1.SeccompProfileTypeRuntimeDefault))
	})

	It("does NOT run as non-root (hostPath requires root)", func() {
		// This test documents the intentional decision: the daemon cannot
		// run as non-root because hostPath volumes are created as root:root
		// and K8s does not apply fsGroup to hostPath. If this test starts
		// failing because RunAsNonRoot is set, the daemon will fail to write
		// artifacts in production.
		sc := daemonPod.Spec.SecurityContext
		if sc != nil && sc.RunAsNonRoot != nil {
			By("verifying RunAsNonRoot is NOT true — hostPath requires root writes")
			_ = fmt.Sprintf("WARNING: RunAsNonRoot=%v — this will cause hostPath write failures", *sc.RunAsNonRoot)
			if *sc.RunAsNonRoot {
				// Verify the daemon can still write — if this PUT fails,
				// RunAsNonRoot is breaking hostPath access.
				daemonURL, stop := portForwardDaemon(daemonPod.Name, 7780)
				defer stop()
				client := &http.Client{Timeout: 10 * time.Second}
				req, _ := http.NewRequest(http.MethodPut,
					daemonURL+"/artifacts/non-root-check",
					bytes.NewReader([]byte("test")))
				resp, err := client.Do(req)
				if err != nil || resp.StatusCode != http.StatusCreated {
					Fail("RunAsNonRoot=true and daemon cannot write to hostPath — " +
						"remove runAsNonRoot from DaemonSet pod SecurityContext")
				}
				if resp != nil {
					resp.Body.Close()
				}
			}
		}
	})
})
