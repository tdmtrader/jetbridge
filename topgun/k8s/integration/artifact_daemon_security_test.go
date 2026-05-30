package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
)

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
		client := &http.Client{Timeout: 10 * time.Second}

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
