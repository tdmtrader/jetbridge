package integration_test

import (
	"net/http"
	"os"
	"os/exec"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("strict configuration writes", func() {
	DescribeTable("selects the strict route only when opted in and never downgrades", func(strict, exists bool, status int) {
		file, err := os.CreateTemp("", "strict-pipeline-*.yml")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(file.Name())
		_, err = file.WriteString("jobs:\n- name: unit\n  plan: []\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(file.Close()).To(Succeed())
		path := "/api/v1/teams/main/pipelines/strict-pipeline"
		if exists {
			atcServer.RouteToHandler("GET", path+"/config", ghttp.RespondWithJSONEncoded(200, atc.ConfigResponse{Config: atc.Config{}}, http.Header{atc.ConfigVersionHeader: {"42"}}))
		} else {
			atcServer.RouteToHandler("GET", path+"/config", ghttp.RespondWith(404, ""))
		}
		atcServer.RouteToHandler("GET", path, ghttp.RespondWithJSONEncoded(200, atc.Pipeline{Name: "strict-pipeline", TeamName: "main"}))
		suffix, version := "/config", "42"
		if !exists {
			version = ""
		}
		if strict {
			suffix += "/conditional"
			if !exists {
				version = "0"
			}
		}
		calls := 0
		atcServer.RouteToHandler("PUT", path+suffix, ghttp.CombineHandlers(ghttp.VerifyHeaderKV(atc.ConfigVersionHeader, version), func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set(atc.ConfigVersionHeader, "43")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if status == 409 {
				_, _ = w.Write([]byte(`{"code":"config_version_conflict","errors":["changed"]}`))
			} else {
				_, _ = w.Write([]byte(`{}`))
			}
		}))
		args := []string{"-t", targetName, "set-pipeline", "-p", "strict-pipeline", "-c", file.Name(), "-n"}
		if strict {
			args = append(args, "--strict-config-write")
		}
		session, err := gexec.Start(exec.Command(flyPath, args...), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		want := 0
		if status >= 400 {
			want = 1
		}
		Eventually(session).Should(gexec.Exit(want))
		Expect(calls).To(Equal(1))
	}, Entry("legacy update", false, true, 200), Entry("strict update", true, true, 200), Entry("strict create", true, false, 201), Entry("strict conflict", true, true, 409), Entry("old or mixed-version replica", true, true, 404))
})
