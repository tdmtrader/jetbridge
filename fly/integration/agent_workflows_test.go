package integration_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

const workflowDefYAML = `schema_version: 1
name: standard-dev
description: integration test workflow
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`

var _ = Describe("fly agent workflows", func() {
	Describe("list", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []map[string]any{
						{"name": "standard-dev", "description": "the seed", "latest_version": 3, "schema_version": 3, "signature_version": 2, "live_version": 2, "content_hash": "abc123", "created_at": 1751900000},
					}),
				),
			)
		})

		It("prints name, latest, schema, signature, live, and description", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "list")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`standard-dev\s+3\s+3\s+2\s+2\s+the seed`))
		})
	})

	Describe("show", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/versions/2"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "standard-dev", Version: 2, ContentHash: "abc123",
						Live: true, RawYAML: workflowDefYAML,
					}),
				),
			)
		})

		It("prints the raw YAML for an explicit version", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "show", "standard-dev", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`schema_version: 1`))
			Expect(sess.Out).To(gbytes.Say(`name: standard-dev`))
		})
	})

	Describe("import", func() {
		var defFile string

		BeforeEach(func() {
			dir := GinkgoT().TempDir()
			defFile = filepath.Join(dir, "standard-dev.yaml")
			Expect(os.WriteFile(defFile, []byte(workflowDefYAML), 0644)).To(Succeed())

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/versions"),
					ghttp.VerifyBody([]byte(workflowDefYAML)),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "standard-dev", Version: 1, ContentHash: workflow.Hash([]byte(workflowDefYAML)),
					}),
				),
			)
		})

		It("POSTs the raw YAML and reports the assigned version", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", defFile)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`imported standard-dev version 1`))
		})

		It("rejects an invalid definition locally, before any API call", func() {
			bad := filepath.Join(GinkgoT().TempDir(), "bad.yaml")
			Expect(os.WriteFile(bad, []byte("schema_version: 1\nname: x\nsteps: []\n"), 0644)).To(Succeed())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", bad)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`at least one step is required`))
		})
	})

	Describe("set-live", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/standard-dev/versions/2/live"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.PromotionResult{
						PreviousLive:     &workflow.VersionMetadata{Version: 1, SchemaVersion: 3, SignatureVersion: 1},
						Target:           workflow.VersionMetadata{Version: 2, SchemaVersion: 3, SignatureVersion: 2},
						SignatureChanged: true,
					}),
				),
			)
		})

		It("PUTs the live marker and confirms", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "set-live", "standard-dev", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`warning: public signature changed from 1 to 2`))
			Expect(sess.Out).To(gbytes.Say(`workflow standard-dev version 2 is now live`))
		})
	})

	Describe("set-live against an unknown version", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/standard-dev/versions/9/live"),
					ghttp.RespondWith(http.StatusNotFound, "unknown workflow version"),
				),
			)
		})

		It("exits non-zero with the server message", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "set-live", "standard-dev", "9")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`unknown workflow version`))
		})
	})

	Describe("import from a directory", func() {
		var srcDir string
		const dirWorkflowYAML = "schema_version: 2\nname: dev\ndescription: dir import\nskills: [tdd]\nprompt_files:\n  work: prompts/work.md\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n"

		BeforeEach(func() {
			srcDir = GinkgoT().TempDir()
			Expect(os.MkdirAll(filepath.Join(srcDir, "prompts"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(srcDir, "skills", "tdd"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "workflow.yml"), []byte(dirWorkflowYAML), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "prompts", "work.md"), []byte("Do the work."), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "skills", "tdd", "SKILL.md"), []byte("# tdd"), 0o644)).To(Succeed())
			// hidden junk must be excluded from the posted manifest
			Expect(os.WriteFile(filepath.Join(srcDir, ".DS_Store"), []byte("junk"), 0o644)).To(Succeed())

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/dev/versions"),
					ghttp.VerifyContentType("application/json"),
					ghttp.VerifyJSONRepresenting(map[string]any{"files": map[string]any{
						"workflow.yml":        dirWorkflowYAML,
						"prompts/work.md":     "Do the work.",
						"skills/tdd/SKILL.md": "# tdd",
					}}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "dev", Version: 4, ContentHash: "deadbeefdeadbeef",
					}),
				),
			)
		})

		It("packages the directory as a manifest and posts it", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", srcDir)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`imported dev version 4`))
		})

		It("promotes with --set-live", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/dev/versions/4/live"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.PromotionResult{
						PreviousLive: &workflow.VersionMetadata{Version: 3, SchemaVersion: 2, SignatureVersion: 0},
						Target:       workflow.VersionMetadata{Version: 4, SchemaVersion: 2, SignatureVersion: 0},
					}),
				),
			)
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", srcDir, "--set-live")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`workflow dev version 4 is now live`))
		})
	})
})
