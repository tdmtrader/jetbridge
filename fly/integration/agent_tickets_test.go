package integration_test

import (
	"net/http"
	"os/exec"
	"strings"

	"github.com/concourse/concourse/agent/api/tickets"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("fly agent tickets", func() {
	Describe("list", func() {
		It("renders durable workflow-run identity losslessly without deriving it from pipeline diagnostics", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			pipelineRunID := 321
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets", "state=queued&limit=50"),
					ghttp.RespondWithJSONEncoded(200, []tickets.Ticket{
						{ID: 7, Title: "fix X", State: tickets.StateQueued,
							Repo: "tdmtrader/concourse", UserName: "tdm", WorkflowRunID: &workflowRunID},
						{ID: 8, Title: "pipeline only", State: tickets.StateQueued,
							Repo: "tdmtrader/concourse", UserName: "tdm", PipelineRunID: &pipelineRunID},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "--print-table-headers", "agent", "tickets", "list", "--state", "queued")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("7"))
			Expect(sess.Out).To(gbytes.Say("queued"))
			Expect(sess.Out).To(gbytes.Say("tdmtrader/concourse"))
			Expect(sess.Out).To(gbytes.Say("fix X"))
			output := string(sess.Out.Contents())
			Expect(output).To(ContainSubstring("workflow run"))
			Expect(output).To(ContainSubstring("9007199254740993"))
			for _, line := range strings.Split(output, "\n") {
				if strings.Contains(line, "pipeline only") {
					Expect(line).NotTo(ContainSubstring("321"))
					Expect(line).NotTo(ContainSubstring("9007199254740993"))
				}
			}
			Expect(output).NotTo(ContainSubstring("agent-ticket-"))
		})
	})

	Describe("create", func() {
		It("posts origin fly and prints the new ticket id", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.VerifyJSON(`{"title":"fix X","body":"details","origin":"fly","repo":"tdmtrader/concourse","target_branch":"main","budget_usd":5}`),
					ghttp.RespondWithJSONEncoded(201, tickets.Ticket{ID: 8, State: tickets.StateDraft}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "create",
				"--title", "fix X", "--body", "details",
				"--repo", "tdmtrader/concourse", "--budget", "5")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("created ticket #8"))
		})
	})

	Describe("show", func() {
		It("prints ticket, spec, and plan", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, Title: "fix X", State: tickets.StateRunning,
							Origin: "fly", Repo: "tdmtrader/concourse", TargetBranch: "main", Body: "details"},
						Spec: &tickets.Spec{Version: 2, Title: "the spec"},
						Tasks: []tickets.Task{
							{Ordering: 1, Title: "one", Status: tickets.TaskDone},
							{Ordering: 2, Title: "two", Status: tickets.TaskPending},
						},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`ticket #7: fix X`))
			Expect(sess.Out).To(gbytes.Say(`state: running`))
			Expect(sess.Out).To(gbytes.Say(`spec v2: the spec`))
			Expect(sess.Out).To(gbytes.Say(`1. \[done\] one`))
			Expect(sess.Out).To(gbytes.Say(`2. \[pending\] two`))
		})

		It("exits 1 when the ticket does not exist", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99"),
					ghttp.RespondWith(404, ""),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "99")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("ticket 99 not found"))
		})
	})

	Describe("queue", func() {
		It("transitions draft to queued", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, State: tickets.StateQueued}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "queue", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now queued"))
		})

		It("prints advisory spec-lint warnings on stderr without failing", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{
						ID: 7, State: tickets.StateQueued,
						Title: "wire the flight recorder",
						Body:  "record all agent sessions",
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "queue", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now queued"))
			Expect(sess.Err).To(gbytes.Say(`spec-lint: "flight recorder"`))
		})

		It("assigns the workflow before transitioning when --workflow is given", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{Ticket: tickets.Ticket{ID: 7, WorkflowName: "", State: tickets.StateDraft}}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7"),
					ghttp.VerifyJSON(`{"workflow_name":"foo"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, WorkflowName: "foo", State: tickets.StateDraft}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, State: tickets.StateQueued}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "queue", "--id", "7", "--workflow", "foo")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`assigned workflow "foo" to ticket #7`))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now queued"))
		})
	})

	Describe("transition", func() {
		It("puts the requested edge with optional metadata", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"needs_review","to":"concluded"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, State: tickets.StateConcluded}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "transition",
				"--id", "7", "--from", "needs_review", "--to", "concluded")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now concluded"))
		})

		It("defaults --from to the ticket's current server state", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, State: tickets.StateRunning},
					}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7/state"),
					ghttp.VerifyJSON(`{"from":"running","to":"needs_review"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, State: tickets.StateNeedsReview}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "transition",
				"--id", "7", "--to", "needs_review")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("ticket #7 is now needs_review"))
		})
	})

	Describe("dispatch", func() {
		It("posts the dispatch and prints durable identity before the pipeline diagnostic", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 321, PipelineName: "must-not-print", WorkflowRunID: &workflowRunID,
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatched ticket #7 as workflow run 9007199254740993 \(pipeline run 321\)`))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("must-not-print"))
		})

		It("prints server spec-lint warnings on stderr without failing", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 321, PipelineName: "must-not-print", WorkflowRunID: &workflowRunID,
						Warnings: []string{`"flight recorder": reword before the CLI refuses it`},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatched ticket #7 as workflow run 9007199254740993 \(pipeline run 321\)`))
			Expect(sess.Err).To(gbytes.Say(`spec-lint: "flight recorder": reword before the CLI refuses it`))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("must-not-print"))
		})

		It("rejects a malformed successful dispatch without durable identity and sends no compensating mutation", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 321, PipelineName: "pipeline-diagnostic",
					}),
				),
			)
			before := len(atcServer.ReceivedRequests())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("dispatch response for ticket 7 omitted workflow_run_id"))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("dispatched ticket"))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("pipeline-diagnostic"))
			Expect(requestPaths(atcServer.ReceivedRequests()[before:])).To(Equal([]string{
				"/api/v1/info",
				"/api/v1/agent/tickets/7/dispatch",
			}))
		})

		It("assigns the workflow before dispatching when --workflow is given", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{Ticket: tickets.Ticket{ID: 7, WorkflowName: "", State: tickets.StateQueued}}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7"),
					ghttp.VerifyJSON(`{"workflow_name":"foo"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, WorkflowName: "foo", State: tickets.StateQueued}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 321, PipelineName: "must-not-print", WorkflowRunID: &workflowRunID,
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7", "--workflow", "foo")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`assigned workflow "foo" to ticket #7`))
			Expect(sess.Out).To(gbytes.Say(`dispatched ticket #7 as workflow run 9007199254740993 \(pipeline run 321\)`))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("must-not-print"))
		})

		It("rolls the assigned workflow back to its prior value when dispatch fails (WF-5 no-worse-than-before)", func() {
			restored := false
			atcServer.AppendHandlers(
				// Ticket already has a valid workflow "deploy".
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{Ticket: tickets.Ticket{ID: 7, WorkflowName: "deploy", State: tickets.StateQueued}}),
				),
				// User assigns a typo'd workflow.
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7"),
					ghttp.VerifyJSON(`{"workflow_name":"deploi"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, WorkflowName: "deploi", State: tickets.StateQueued}),
				),
				// Dispatch cannot resolve it.
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWith(422, "workflow definition not found: deploi live"),
				),
				// Compensating rollback restores the prior "deploy".
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/7"),
					ghttp.VerifyJSON(`{"workflow_name":"deploy"}`),
					func(w http.ResponseWriter, r *http.Request) { restored = true },
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 7, WorkflowName: "deploy", State: tickets.StateQueued}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7", "--workflow", "deploi")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(restored).To(BeTrue(), "a failed dispatch must roll the just-assigned workflow back to its prior value")
		})

		It("issues only the dispatch POST when no --workflow is given (backward compat)", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/7/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 321, PipelineName: "must-not-print", WorkflowRunID: &workflowRunID,
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "dispatch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`dispatched ticket #7 as workflow run 9007199254740993 \(pipeline run 321\)`))
			// Only the dispatch POST handler is registered here: had the command
			// sent a workflow-assign PUT, the ghttp server would have failed the
			// spec for lack of a matching handler. This proves backward compat —
			// no --workflow means no update call.
		})
	})

	Describe("create --dispatch", func() {
		It("creates, queues, and dispatches in one command", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.RespondWithJSONEncoded(201, tickets.Ticket{ID: 9, State: tickets.StateDraft}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/9/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 9, State: tickets.StateQueued}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/9/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 5, PipelineName: "must-not-print", WorkflowRunID: &workflowRunID,
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "create",
				"--title", "one shot", "--repo", "tdmtrader/concourse", "--workflow", "analyze", "--dispatch")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say("created ticket #9"))
			Expect(sess.Out).To(gbytes.Say("queued"))
			Expect(sess.Out).To(gbytes.Say(`dispatched ticket #9 as workflow run 9007199254740993 \(pipeline run 5\)`))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("must-not-print"))
		})

		It("preserves created-and-queued context when a malformed dispatch omits durable identity", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets"),
					ghttp.RespondWithJSONEncoded(201, tickets.Ticket{ID: 9, State: tickets.StateDraft}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/tickets/9/state"),
					ghttp.VerifyJSON(`{"from":"draft","to":"queued"}`),
					ghttp.RespondWithJSONEncoded(200, tickets.Ticket{ID: 9, State: tickets.StateQueued}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/tickets/9/dispatch"),
					ghttp.RespondWithJSONEncoded(201, tickets.DispatchResponse{
						RunID: 5, PipelineName: "pipeline-diagnostic",
					}),
				),
			)
			before := len(atcServer.ReceivedRequests())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "create",
				"--title", "one shot", "--repo", "tdmtrader/concourse", "--workflow", "analyze", "--dispatch")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say(`created #9 \(queued\); dispatch failed: dispatch response for ticket 9 omitted workflow_run_id`))
			Expect(sess.Out).To(gbytes.Say("created ticket #9"))
			Expect(sess.Out).To(gbytes.Say("ticket #9 is now queued"))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("dispatched ticket"))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("pipeline-diagnostic"))
			Expect(requestPaths(atcServer.ReceivedRequests()[before:])).To(Equal([]string{
				"/api/v1/info",
				"/api/v1/agent/tickets",
				"/api/v1/agent/tickets/9/state",
				"/api/v1/agent/tickets/9/dispatch",
			}))
		})
	})

	Describe("watch", func() {
		It("follows the ticket's durable workflow run through a status transition with one target validation", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{
							ID: 7, WorkflowName: "review-source", WorkflowRunID: &workflowRunID,
						},
					}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/review-source/runs/9007199254740993"),
					ghttp.RespondWithJSONEncoded(200, workflowrunsapi.RunDetail{RunSummary: workflowrunsapi.RunSummary{
						WorkflowRunID: workflowRunID, WorkflowName: "review-source", WorkflowVersion: 3,
						Status: db.AgentWorkflowRunStatusRunning,
					}}),
				),
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/review-source/runs/9007199254740993"),
					ghttp.RespondWithJSONEncoded(200, workflowrunsapi.RunDetail{RunSummary: workflowrunsapi.RunSummary{
						WorkflowRunID: workflowRunID, WorkflowName: "review-source", WorkflowVersion: 3,
						Status: db.AgentWorkflowRunStatusSucceeded,
					}}),
				),
			)
			before := len(atcServer.ReceivedRequests())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "watch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740993: running`))
			Expect(sess.Err).To(gbytes.Say(`workflow run 9007199254740993: succeeded`))
			Expect(sess.Out).To(gbytes.Say(`workflow run 9007199254740993`))
			paths := requestPaths(atcServer.ReceivedRequests()[before:])
			Expect(paths).To(Equal([]string{
				"/api/v1/info",
				"/api/v1/agent/tickets/7",
				"/api/v1/agent/workflows/review-source/runs/9007199254740993",
				"/api/v1/agent/workflows/review-source/runs/9007199254740993",
			}))
			Expect(strings.Join(paths, "\n")).NotTo(ContainSubstring("builds"))
			Expect(strings.Join(paths, "\n")).NotTo(ContainSubstring("events"))
			Expect(string(sess.Out.Contents()) + string(sess.Err.Contents())).NotTo(ContainSubstring("agent-ticket-"))
		})

		It("rejects a ticket without a durable workflow run before any build or event request", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, WorkflowName: "review"},
					}),
				),
			)
			before := len(atcServer.ReceivedRequests())
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "watch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("ticket 7 has no workflow run"))
			Expect(strings.Join(requestPaths(atcServer.ReceivedRequests()[before:]), "\n")).NotTo(ContainSubstring("build"))
		})

		It("rejects a durable run with a blank workflow name", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, WorkflowRunID: &workflowRunID},
					}),
				),
			)
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "watch", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("ticket 7 has workflow run 9007199254740993 but no workflow name"))
		})

		It("reports a missing ticket", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/99"),
					ghttp.RespondWith(404, ""),
				),
			)
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "watch", "--id", "99")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))
			Expect(sess.Err).To(gbytes.Say("ticket 99 not found"))
		})
	})

	Describe("show surfaces the run", func() {
		It("prints the durable show-run command and a separate pipeline diagnostic", func() {
			workflowRunID := snapshot.WorkflowRunID(9007199254740993)
			pipelineRunID := 42
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, Title: "t", State: tickets.StateRunning,
							Origin: "fly", Repo: "r", TargetBranch: "main", WorkflowName: "review/source",
							WorkflowRunID: &workflowRunID, PipelineRunID: &pipelineRunID},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`workflow run: 9007199254740993 · inspect with: fly -t ` + targetName + ` agent workflows show-run review/source 9007199254740993`))
			Expect(sess.Out).To(gbytes.Say(`pipeline run: 42`))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("agent-ticket-"))
			Expect(sess.Out.Contents()).NotTo(ContainSubstring("agent tickets watch"))
		})

		It("prints only the pipeline diagnostic when durable identity is absent", func() {
			pipelineRunID := 42
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/tickets/7"),
					ghttp.RespondWithJSONEncoded(200, tickets.TicketDetail{
						Ticket: tickets.Ticket{ID: 7, Title: "t", State: tickets.StateRunning,
							Origin: "fly", Repo: "r", TargetBranch: "main", PipelineRunID: &pipelineRunID},
					}),
				),
			)

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "tickets", "show", "--id", "7")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`pipeline run: 42`))
			output := string(sess.Out.Contents())
			Expect(output).NotTo(ContainSubstring("workflow run:"))
			Expect(output).NotTo(ContainSubstring("inspect with"))
			Expect(output).NotTo(ContainSubstring("show-run"))
			Expect(output).NotTo(ContainSubstring("agent-ticket-"))
			Expect(output).NotTo(ContainSubstring("watch"))
		})
	})

	Describe("help", func() {
		It("describes durable workflow-run dispatch and watch", func() {
			flyCmd := exec.Command(flyPath, "agent", "tickets", "-h")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			output := string(sess.Out.Contents()) + string(sess.Err.Contents())
			Expect(output).To(ContainSubstring("Dispatch a queued ticket as a durable workflow run"))
			Expect(output).To(ContainSubstring("Follow a ticket's durable workflow run"))
			Expect(output).NotTo(ContainSubstring("build events"))
			Expect(output).NotTo(ContainSubstring("ticket-pipeline"))
		})

		It("retains only --id for watch", func() {
			flyCmd := exec.Command(flyPath, "agent", "tickets", "watch", "-h")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			output := string(sess.Out.Contents()) + string(sess.Err.Contents())
			Expect(output).To(ContainSubstring("--id"))
			Expect(output).NotTo(ContainSubstring("--timestamps"))
		})
	})
})

func requestPaths(requests []*http.Request) []string {
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		paths = append(paths, request.URL.EscapedPath())
	}
	return paths
}
