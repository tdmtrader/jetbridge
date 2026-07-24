package integration_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

const workflowDefYAML = `schema_version: 3
name: standard-dev
description: integration test workflow
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: Do the work.
`

const historicalWorkflowDefYAML = `schema_version: 1
name: standard-dev
description: historical integration test workflow
prompts:
  work: Do the work.
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`

var _ = Describe("fly agent workflows", func() {
	Describe("run", func() {
		It("creates a pinned workflow run with named snapshot inputs and lossless IDs", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/runs"),
					ghttp.VerifyContentType("application/json"),
					ghttp.VerifyJSONRepresenting(map[string]any{
						"version":         3,
						"inputs":          map[string]any{"after": "9007199254740995", "before": "9007199254740993"},
						"idempotency_key": "cli-key",
					}),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "running",
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "run", "standard-dev",
				"--input", "before=9007199254740993",
				"--input", "after=9007199254740995",
				"--version", "3",
				"--idempotency-key", "cli-key",
				"--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"pipeline_run_id": 73`))
		})

		It("generates an idempotency key when one is not supplied", func() {
			atcServer.AppendHandlers(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal(http.MethodPost))
				Expect(r.URL.Path).To(Equal("/api/v1/agent/workflows/standard-dev/runs"))
				var request map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&request)).To(Succeed())
				Expect(request).NotTo(HaveKey("version"))
				Expect(request).NotTo(HaveKey("inputs"))
				Expect(request["idempotency_key"]).To(BeAssignableToTypeOf(""))
				Expect(request["idempotency_key"]).NotTo(BeEmpty())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				Expect(json.NewEncoder(w).Encode(map[string]any{
					"workflow_run_id":  "9007199254740997",
					"pipeline_run_id":  nil,
					"workflow_name":    "standard-dev",
					"workflow_version": 4,
					"status":           "admitting",
				})).To(Succeed())
			})

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "run", "standard-dev")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`workflow run 9007199254740997`))
			Expect(sess.Out).To(gbytes.Say(`pipeline run: none`))
		})

		It("waits for the durable workflow run rather than the linked pipeline run", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/runs"),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "running",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "succeeded",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "run", "standard-dev",
				"--idempotency-key", "wait-key", "--wait", "--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"status": "succeeded"`))
			Expect(string(sess.Out.Contents())).NotTo(ContainSubstring(`"status": "running"`))
		})

		It("prints a waited terminal failure and exits nonzero", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/runs"),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "running",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "failed",
						"error_message":    "review contract failed",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "run", "standard-dev",
				"--idempotency-key", "failed-wait-key", "--wait", "--follow", "--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"status": "failed"`))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740997: running`))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740997: failed`))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740997 finished with status failed`))
		})
	})

	Describe("runs", func() {
		It("lists workflow-scoped runs with combined filters and lossless durable IDs", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest(
						"GET",
						"/api/v1/agent/workflows/standard-dev/runs",
						"limit=7&origin_kind=ticket&origin_reference=42&status=running",
					),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []map[string]any{{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "running",
						"origin_kind":      "ticket",
						"origin_reference": "42",
					}}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "runs", "standard-dev",
				"--status", "running",
				"--origin-kind", "ticket",
				"--origin-reference", "42",
				"--limit", "7",
				"--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"pipeline_run_id": 73`))
		})

		It("prints durable and pipeline run IDs as distinct table columns", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs", "limit=100"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []map[string]any{{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  nil,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "succeeded",
						"origin_kind":      "cli",
						"origin_reference": "",
					}}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "runs", "standard-dev")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`9007199254740997\s+none\s+3\s+succeeded\s+cli`))
		})
	})

	Describe("show-run", func() {
		It("inspects one durable run without treating the pipeline run ID as its identity", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "running",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "show-run",
				"standard-dev", "9007199254740997", "--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"pipeline_run_id": 73`))
		})

		It("prints the separately addressable output manifest", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997/outputs"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"workflow_run_id": "9007199254740997",
						"outputs": []map[string]any{{
							"port": "review",
							"snapshot": map[string]any{
								"id":                 "9007199254740999",
								"type":               "review/v1",
								"digest":             "sha256:" + strings.Repeat("a", 64),
								"byte_size":          12,
								"file_count":         1,
								"representation":     "application/x-tar",
								"intrinsic_metadata": map[string]any{},
								"content_state":      "available",
								"created_at":         "2026-07-22T12:00:00Z",
							},
						}},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "show-run",
				"standard-dev", "9007199254740997", "--outputs", "--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"id": "9007199254740999"`))
		})

		It("follows status transitions on stderr and prints one final result", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "running",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "succeeded",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "show-run",
				"standard-dev", "9007199254740997", "--follow",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740997: running`))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740997: succeeded`))
			Expect(sess.Out).To(gbytes.Say(`status: succeeded`))
			Expect(string(sess.Out.Contents())).NotTo(ContainSubstring(`status: running`))
		})
	})

	Describe("cancel-run", func() {
		It("requests cancellation by durable workflow run ID", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997/cancel"),
					ghttp.RespondWithJSONEncoded(http.StatusAccepted, map[string]any{
						"workflow_run_id":  "9007199254740997",
						"pipeline_run_id":  73,
						"workflow_name":    "standard-dev",
						"workflow_version": 3,
						"status":           "canceling",
						"inputs":           []any{},
						"outputs":          []any{},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "cancel-run",
				"standard-dev", "9007199254740997", "--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254740997"`))
			Expect(sess.Out).To(gbytes.Say(`"status": "canceling"`))
		})
	})

	Describe("retry-run", func() {
		It("creates a new run linked to the durable source run", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/runs/9007199254740997/retry"),
					ghttp.VerifyContentType("application/json"),
					ghttp.VerifyJSONRepresenting(map[string]any{"idempotency_key": "retry-key"}),
					ghttp.RespondWithJSONEncoded(http.StatusCreated, map[string]any{
						"workflow_run_id":          "9007199254741001",
						"pipeline_run_id":          74,
						"workflow_name":            "standard-dev",
						"workflow_version":         3,
						"status":                   "running",
						"retry_of_workflow_run_id": "9007199254740997",
						"inputs":                   []any{},
						"outputs":                  []any{},
					}),
				),
			)

			flyCmd := exec.Command(
				flyPath, "-t", targetName, "agent", "workflows", "retry-run",
				"standard-dev", "9007199254740997", "--idempotency-key", "retry-key", "--json",
			)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`"workflow_run_id": "9007199254741001"`))
			Expect(sess.Out).To(gbytes.Say(`"retry_of_workflow_run_id": "9007199254740997"`))
		})
	})

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
		It("prints the raw YAML for an explicit version", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/versions/2"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "standard-dev", Version: 2, ContentHash: "abc123",
						SchemaVersion: 1, Live: true, RawYAML: historicalWorkflowDefYAML,
					}),
				),
			)
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "show", "standard-dev", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`schema_version: 1`))
			Expect(sess.Out).To(gbytes.Say(`name: standard-dev`))
		})

		It("follows bounded version cursors to find an older live version", func() {
			atcServer.AppendHandlers(
				func(response http.ResponseWriter, request *http.Request) {
					Expect(request.Method).To(Equal(http.MethodGet))
					Expect(request.URL.Path).To(Equal("/api/v1/agent/workflows/standard-dev/versions"))
					Expect(request.URL.Query()).To(Equal(url.Values{"limit": {"100"}}))
					response.Header().Set("Content-Type", "application/json")
					response.Header().Set("X-Next-Cursor", "101")
					Expect(json.NewEncoder(response).Encode([]workflow.Definition{
						{Name: "standard-dev", Version: 101},
						{Name: "standard-dev", Version: 102},
					})).To(Succeed())
				},
				func(response http.ResponseWriter, request *http.Request) {
					Expect(request.Method).To(Equal(http.MethodGet))
					Expect(request.URL.Path).To(Equal("/api/v1/agent/workflows/standard-dev/versions"))
					Expect(request.URL.Query()).To(Equal(url.Values{
						"cursor": {"101"},
						"limit":  {"100"},
					}))
					response.Header().Set("Content-Type", "application/json")
					Expect(json.NewEncoder(response).Encode([]workflow.Definition{
						{Name: "standard-dev", Version: 1, Live: true},
						{Name: "standard-dev", Version: 100},
					})).To(Succeed())
				},
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/versions/1"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "standard-dev", Version: 1, ContentHash: "abc123",
						SchemaVersion: 1, Live: true, RawYAML: historicalWorkflowDefYAML,
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "show", "standard-dev")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
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

		It("rejects a valid schema-1 definition locally, before any import API call", func() {
			legacy := filepath.Join(GinkgoT().TempDir(), "legacy-v1.yaml")
			Expect(os.WriteFile(legacy, []byte(historicalWorkflowDefYAML), 0644)).To(Succeed())
			requestsBefore := len(atcServer.ReceivedRequests())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", legacy)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`workflow: unsupported schema_version 1; only schema_version 3 is supported`))
			for _, request := range atcServer.ReceivedRequests()[requestsBefore:] {
				Expect(request.URL.Path).NotTo(Equal("/api/v1/agent/workflows/standard-dev/versions"))
			}
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
		const dirWorkflowYAML = "schema_version: 3\nname: dev\ndescription: dir import\nsignature_version: 1\ninputs: []\noutputs: []\nplan:\n  - agent: work\n    function_id: work\n    prompt_file: prompts/work.md\n    skills: [tdd]\n"
		const legacyDirWorkflowYAML = "schema_version: 2\nname: dev\ntrigger: {type: manual}\nworkspace: {type: git, repo: example/repo}\nprompts:\n  work: Do the work.\nsteps:\n  - agent: work\n    prompt: work\n    outputs: [workspace]\n"

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

		It("rejects a valid schema-2 directory locally, before any import API call", func() {
			Expect(os.WriteFile(filepath.Join(srcDir, "workflow.yml"), []byte(legacyDirWorkflowYAML), 0o644)).To(Succeed())
			requestsBefore := len(atcServer.ReceivedRequests())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", srcDir)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`workflow: unsupported schema_version 2; only schema_version 3 is supported`))
			for _, request := range atcServer.ReceivedRequests()[requestsBefore:] {
				Expect(request.URL.Path).NotTo(Equal("/api/v1/agent/workflows/dev/versions"))
			}
		})

		It("promotes with --set-live", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/dev/versions/4/live"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.PromotionResult{
						PreviousLive: &workflow.VersionMetadata{Version: 3, SchemaVersion: 3, SignatureVersion: 1},
						Target:       workflow.VersionMetadata{Version: 4, SchemaVersion: 3, SignatureVersion: 1},
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
