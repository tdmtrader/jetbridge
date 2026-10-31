package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

var _ = Describe("validate-pipeline", func() {
	It("refuses a removed var_sources section with the server's sentence", func() {
		config := filepath.Join(GinkgoT().TempDir(), "pipeline.yml")
		Expect(os.WriteFile(config, []byte("var_sources:\n- name: retired\n  type: vault\n  config: {}\njobs: []\n"), 0600)).To(Succeed())
		sess, err := gexec.Start(exec.Command(flyPath, "validate-pipeline", "-c", config), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gexec.Exit(1))
		Expect(sess.Err).To(gbytes.Say("var_sources: is not a pipeline section; pipeline-scoped credential managers were removed"))
		Expect(sess.Out).NotTo(gbytes.Say("looks good"))
	})

	It("refuses a removed prototypes section with the server's sentence", func() {
		// This fails if fly's plain yaml.Unmarshal drops the key and prints
		// "looks good" for a file the API refuses with a 400.
		config := filepath.Join(GinkgoT().TempDir(), "pipeline.yml")
		Expect(os.WriteFile(config, []byte("prototypes:\n- name: some-prototype\n  type: registry-image\njobs:\n- name: entry\n  plan: []\n"), 0600)).To(Succeed())
		sess, err := gexec.Start(exec.Command(flyPath, "validate-pipeline", "-c", config), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gexec.Exit(1))
		Expect(sess.Err).To(gbytes.Say("prototypes: is not a pipeline section; prototypes and the run step were removed"))
		Expect(sess.Out).NotTo(gbytes.Say("looks good"))
	})

	It("refuses a resource type whose image is not a registry-image reference", func() {
		config := filepath.Join(GinkgoT().TempDir(), "pipeline.yml")
		Expect(os.WriteFile(config, []byte(`resource_types:
- name: git-backed
  type: git
  source: {uri: https://example.com/repo}
jobs:
- name: entry
  plan: []
`), 0600)).To(Succeed())
		sess, err := gexec.Start(exec.Command(flyPath, "validate-pipeline", "-c", config), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gexec.Exit(1))
		Expect(sess.Err).To(gbytes.Say("resource_types.git-backed: an image declared with type 'git' is not registry-image: an image is a registry-image reference, declared as 'type: registry-image' with a source, or for a resource type a direct 'image:'"))
		Expect(sess.Out).NotTo(gbytes.Say("looks good"))
	})

	It("refuses an inline task whose image_resource is not a registry-image reference", func() {
		config := filepath.Join(GinkgoT().TempDir(), "pipeline.yml")
		Expect(os.WriteFile(config, []byte(`jobs:
- name: entry
  plan:
  - task: build
    config:
      platform: linux
      image_resource: {type: docker-image, source: {repository: busybox}}
      run: {path: "true"}
`), 0600)).To(Succeed())
		sess, err := gexec.Start(exec.Command(flyPath, "validate-pipeline", "-c", config), GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gexec.Exit(1))
		Expect(sess.Err).To(gbytes.Say("image_resource: an image declared with type 'docker-image' is not registry-image"))
		Expect(sess.Out).NotTo(gbytes.Say("looks good"))
	})

	It("returns valid on valid configuration", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/testConfigValid.yml",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(sess).Should(gbytes.Say("looks good"))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("returns valid on valid configuration to stdout", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/testConfigValid.yml",
			"-o",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(sess).Should(gbytes.Say("groups:"))
		Eventually(sess).Should(gbytes.Say("jobs:"))
		Eventually(sess).Should(gbytes.Say("resources:"))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("returns valid on templated configuration with variables", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/vars-pipeline.yml",
			"-l", "fixtures/vars-pipeline-params-types.yml",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(sess).Should(gbytes.Say("looks good"))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("returns invalid on validation error", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/testConfigError.yml",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(sess.Err).Should(gbytes.Say("WARNING:"))
		Eventually(sess.Err).Should(gbytes.Say("  - invalid resources:"))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(1))

		Expect(sess.Err).To(gbytes.Say("configuration invalid"))
	})

	It("returns invalid on duplicate keys", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/testConfigDuplicate.yml",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(sess.Err).Should(gbytes.Say("error parsing yaml before applying templates"))
		Eventually(sess.Err).Should(gbytes.Say(`mapping key "resources" already defined`))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(1))
	})

	It("returns valid when there are merge keys", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/testConfigMergeKeys.yml",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gbytes.Say("looks good"))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
	})

	It("returns valid on a pipeline with unknown keys", func() {
		flyCmd := exec.Command(
			flyPath,
			"validate-pipeline",
			"-c", "fixtures/testConfigUnknownKeys.yml",
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		Eventually(sess).Should(gbytes.Say("looks good"))

		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
	})

	When("strict", func() {
		It("returns invalid on validation warning", func() {
			flyCmd := exec.Command(
				flyPath,
				"validate-pipeline",
				"-c", "fixtures/testConfigWarning.yml",
				"--strict",
			)

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess.Err).Should(gbytes.Say("DEPRECATION WARNING:"))
			Eventually(sess.Err).Should(gbytes.Say("  - jobs.some-job.plan"))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))

			Expect(sess.Err).To(gbytes.Say("configuration invalid"))
		})

		It("returns invalid on unknown keys", func() {
			flyCmd := exec.Command(
				flyPath,
				"validate-pipeline",
				"-c", "fixtures/testConfigUnknownKeys.yml",
				"--strict",
			)

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess.Err).Should(gbytes.Say("json: unknown field \"user_key\""))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
		})
	})

})
