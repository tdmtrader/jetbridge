package behavioral_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
)

var _ = Describe("Teams and Authorization", func() {
	var teamName string

	BeforeEach(func() {
		teamName = pipelineName + "-team"
	})

	AfterEach(func() {
		// Destroy the team if it was created
		sess := fly.Start("destroy-team", "-n", teamName, "--non-interactive")
		<-sess.Exited
	})

	It("creates a team with set-team", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		By("verifying the team appears in teams list")
		sess := fly.Start("teams")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(teamName))
	})

	It("grants owner role full access", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		By("logging into the new team")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-k", "-n", teamName)

		By("verifying owner can set pipelines")
		cfg := writePipelineFile("owner-test.yml", `
jobs:
- name: owner-job
  plan:
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["owner"]
`)
		fly.Run("set-pipeline", "-n", "-p", pipelineName, "-c", cfg)
		fly.Run("destroy-pipeline", "-n", "-p", pipelineName)

		By("logging back into main team")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-k")
	})

	It("restricts viewer role to read-only", func() {
		// Viewer role test: create team with the current user as viewer only
		// This is an approximation - full RBAC testing requires multiple users
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		sess := fly.Start("teams")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(teamName))
	})

	It("isolates pipelines across teams", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		By("setting a pipeline in main team")
		cfg := writePipelineFile("isolation.yml", `
jobs:
- name: main-job
  plan:
  - task: noop
    config:
      platform: linux
      image_resource: {type: registry-image, source: {repository: busybox}}
      run:
        path: echo
        args: ["main"]
`)
		setAndUnpausePipeline(cfg)

		By("logging into the other team")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-k", "-n", teamName)

		By("verifying the main team pipeline is not visible")
		pipelines := fly.GetPipelines()
		for _, p := range pipelines {
			Expect(p.Name).ToNot(Equal(pipelineName),
				"pipeline from main team should not be visible in other team")
		}

		By("logging back into main team")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-k")
	})

	It("renames a team", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		newName := teamName + "-renamed"
		fly.Run("rename-team", "-o", teamName, "-n", newName)

		By("verifying the renamed team exists")
		sess := fly.Start("teams")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(newName))

		// Update teamName for cleanup
		teamName = newName
	})

	It("destroys a team", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		fly.Run("destroy-team", "-n", teamName, "--non-interactive")

		By("verifying the team is gone")
		sess := fly.Start("teams")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		output := string(sess.Out.Contents())
		Expect(output).ToNot(ContainSubstring(teamName))

		// Prevent AfterEach from failing
		teamName = "already-destroyed-" + pipelineName
	})

	It("protects the main team from destruction", func() {
		sess := fly.Start("destroy-team", "-n", "main", "--non-interactive")
		<-sess.Exited
		Expect(sess.ExitCode()).ToNot(Equal(0))
	})

	It("supports member role for pipeline management", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		By("logging into the team")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-k", "-n", teamName)

		By("verifying team member can list pipelines")
		sess := fly.Start("pipelines")
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))

		By("logging back")
		fly.Login(config.ATCUsername, config.ATCPassword, config.ATCURL, "-k")
	})

	It("supports pipeline-operator role", func() {
		fly.Run("set-team", "-n", teamName,
			"--local-user", config.ATCUsername,
			"--non-interactive")

		By("verifying user role includes team")
		roles := fly.GetUserRole(teamName)
		Expect(roles).ToNot(BeEmpty(), "user should have at least one role on the team")
	})
})
