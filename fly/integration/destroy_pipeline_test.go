package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("destroy-pipeline", func() {
	var (
		stdin io.Writer
		args  []string
		sess  *gexec.Session
	)

	BeforeEach(func() {
		stdin = nil
		args = []string{}
	})

	JustBeforeEach(func() {
		var err error

		flyCmd := exec.Command(flyPath, append([]string{"-t", targetName, "destroy-pipeline"}, args...)...)
		stdin, err = flyCmd.StdinPipe()
		Expect(err).NotTo(HaveOccurred())

		sess, err = gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when a pipeline name is not specified", func() {
		It("asks the user to specify a pipeline name", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "destroy-pipeline")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess).Should(gexec.Exit(1))

			Expect(sess.Err).To(gbytes.Say("error: the required flag `" + osFlag("p", "pipeline") + "' was not specified"))
		})
	})

	Context("when the pipeline flag is invalid", func() {
		It("fails and print invalid flag error", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "destroy-pipeline", "-p", "forbidden/pipelinename")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))

			Expect(sess.Err).To(gbytes.Say("error: invalid argument for flag `" + osFlag("p", "pipeline")))
		})
	})

	Context("when a pipeline name is specified", func() {
		BeforeEach(func() {
			args = append(args, "-p", "some-pipeline/branch:master")
		})

		yes := func() {
			GinkgoHelper()
			Eventually(sess).Should(gbytes.Say(`are you sure\? \[yN\]: `))
			fmt.Fprintf(stdin, "y\r")
		}

		no := func() {
			GinkgoHelper()
			Eventually(sess).Should(gbytes.Say(`are you sure\? \[yN\]: `))
			fmt.Fprintf(stdin, "n\r")
		}

		queryParams := "vars.branch=%22master%22"

		Context("when the pipeline exists", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/teams/main/pipelines/some-pipeline", queryParams),
						ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Pipeline{Name: "some-pipeline"}),
					),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("DELETE", "/api/v1/teams/main/pipelines/some-pipeline", queryParams),
						ghttp.RespondWith(204, ""),
					),
				)
			})

			It("warns that it's about to do bad things", func() {
				Eventually(sess).Should(gbytes.Say("!!! this will remove all data for pipeline `some-pipeline/branch:master`"))
			})

			It("bails out if the user says no", func() {
				no()
				Eventually(sess).Should(gbytes.Say(`bailing out`))
				Eventually(sess).Should(gexec.Exit(0))
			})

			It("succeeds if the user says yes", func() {
				yes()
				Eventually(sess).Should(gbytes.Say("`some-pipeline/branch:master` deleted"))
				Eventually(sess).Should(gexec.Exit(0))
			})

			Context("when run noninteractively", func() {
				BeforeEach(func() {
					args = append(args, "-n")
				})

				It("destroys the pipeline without confirming", func() {
					Eventually(sess).Should(gbytes.Say("`some-pipeline/branch:master` deleted"))
					Eventually(sess).Should(gexec.Exit(0))
				})
			})
		})

		Context("and the pipeline does not exist", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/teams/main/pipelines/some-pipeline", queryParams),
						ghttp.RespondWith(404, ""),
					),
				)
			})

			It("writes that it did not exist without confirmation and exits successfully", func() {
				Eventually(sess).Should(gbytes.Say("`some-pipeline/branch:master` does not exist"))
				Eventually(sess).Should(gexec.Exit(0))
			})
		})

		Context("and the api returns an unexpected status code on check", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/teams/main/pipelines/some-pipeline", queryParams),
						ghttp.RespondWith(500, ""),
					),
				)
			})

			It("writes an error message to stderr", func() {
				Eventually(sess.Err).Should(gbytes.Say("Unexpected Response"))
				Eventually(sess).Should(gexec.Exit(1))
			})
		})

		Context("with a team specified", func() {
			BeforeEach(func() {
				args = append(args, "--team", "team-two")

				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/teams/team-two"),
						ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Team{
							Name: "team-two",
						}),
					),
				)
			})

			Context("when the pipeline exists", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", "/api/v1/teams/team-two/pipelines/some-pipeline", queryParams),
							ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Pipeline{Name: "some-pipeline"}),
						),
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("DELETE", "/api/v1/teams/team-two/pipelines/some-pipeline", queryParams),
							ghttp.RespondWith(204, ""),
						),
					)
				})

				It("warns that it's about to do bad things", func() {
					Eventually(sess).Should(gbytes.Say("!!! this will remove all data for pipeline `some-pipeline/branch:master`"))
				})

				It("bails out if the user says no", func() {
					no()
					Eventually(sess).Should(gbytes.Say(`bailing out`))
					Eventually(sess).Should(gexec.Exit(0))
				})

				It("succeeds if the user says yes", func() {
					yes()
					Eventually(sess).Should(gbytes.Say("`some-pipeline/branch:master` deleted"))
					Eventually(sess).Should(gexec.Exit(0))
				})
			})

			Context("and the pipeline does not exist", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", "/api/v1/teams/team-two/pipelines/some-pipeline", queryParams),
							ghttp.RespondWith(404, ""),
						),
					)
				})

				It("writes that it did not exist without confirmation and exits successfully", func() {
					Eventually(sess).Should(gbytes.Say("`some-pipeline/branch:master` does not exist"))
					Eventually(sess).Should(gexec.Exit(0))
				})
			})
		})
	})

	Context("when the pipeline is a template", func() {
		template := true
		templatePath := "/api/v1/teams/main/pipelines/some-template"

		pipelineExists := ghttp.CombineHandlers(
			ghttp.VerifyRequest("GET", templatePath),
			ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Pipeline{Name: "some-template", Template: &template}),
		)
		runsListed := ghttp.CombineHandlers(
			ghttp.VerifyRequest("GET", templatePath+"/runs", "limit=500"),
			ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.PipelineRun{{ID: 3, Number: 3}, {ID: 2, Number: 2}, {ID: 1, Number: 1}}),
		)
		refused := func(reason string) http.HandlerFunc {
			return ghttp.RespondWithJSONEncoded(http.StatusConflict, atc.SaveConfigResponse{Errors: []string{reason}})
		}

		Context("without --purge-run-history, when it has Run history", func() {
			BeforeEach(func() {
				args = append(args, "-p", "some-template", "-n")
				atcServer.AppendHandlers(
					pipelineExists,
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("DELETE", templatePath, ""),
						refused(atc.ErrPipelineTemplateHasRunHistory.Error()),
					),
				)
			})

			It("says why it was refused and what --purge-run-history would do, and fails", func() {
				Eventually(sess).Should(gexec.Exit(1))
				Expect(sess.Err).To(gbytes.Say("`some-template` is a template with durable Run history, so it was not destroyed."))
				Expect(sess.Err).To(gbytes.Say("--purge-run-history also permanently deletes every Run of it and all their durable history"))
				Expect(sess.Err).NotTo(gbytes.Say("Unexpected Response"))
			})
		})

		Context("with --purge-run-history", func() {
			BeforeEach(func() {
				args = append(args, "-p", "some-template", "--purge-run-history")
			})

			warns := func() {
				GinkgoHelper()
				Eventually(sess).Should(gbytes.Say("!!! this will PERMANENTLY DELETE pipeline `some-template` AND EVERY RUN OF IT, with all their durable history:"))
				Eventually(sess).Should(gbytes.Say(`each Run's builds and build logs`))
				Eventually(sess).Should(gbytes.Say(`its results and captured outputs`))
				Eventually(sess).Should(gbytes.Say(`its bound inputs and input uploads \(their Hangar claims are released, so the stored trees become reclaimable\)`))
				Eventually(sess).Should(gbytes.Say(`its credential handoffs and cancellation records`))
				Eventually(sess).Should(gbytes.Say("`some-template` has 3 Runs."))
				Eventually(sess).Should(gbytes.Say(`this cannot be undone. it is not archive-pipeline, which keeps the pipeline and its history.`))
			}

			expectNoDelete := func() {
				GinkgoHelper()
				for _, request := range atcServer.ReceivedRequests() {
					Expect(request.Method).NotTo(Equal(http.MethodDelete), "nothing may be deleted")
				}
			}

			typeName := func(name string) {
				GinkgoHelper()
				Eventually(sess).Should(gbytes.Say(`type the pipeline name to confirm: `))
				fmt.Fprintf(stdin, "%s\r", name)
			}

			Context("when the purge succeeds", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						pipelineExists,
						runsListed,
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("DELETE", templatePath, "purge_run_history=true"),
							ghttp.RespondWithJSONEncoded(http.StatusOK, atc.DestroyPipelineResponse{PurgedRuns: 3}),
						),
					)
				})

				It("warns, and purges once the pipeline name is typed exactly", func() {
					warns()
					typeName("some-template")
					Eventually(sess).Should(gbytes.Say("`some-template` deleted; 3 Runs purged"))
					Eventually(sess).Should(gexec.Exit(0))
				})

				It("bails out without deleting when anything else is typed", func() {
					warns()
					typeName("some-templat")
					Eventually(sess).Should(gexec.Exit(1))
					Expect(sess.Err).To(gbytes.Say(`incorrect pipeline name; bailing out`))
					expectNoDelete()
				})

				It("bails out without deleting when the answer is yes", func() {
					warns()
					typeName("y")
					Eventually(sess).Should(gexec.Exit(1))
					expectNoDelete()
				})

				Context("when run noninteractively", func() {
					BeforeEach(func() {
						args = append(args, "-n")
					})

					It("still warns, then purges without asking", func() {
						warns()
						Eventually(sess).Should(gbytes.Say("`some-template` deleted; 3 Runs purged"))
						Eventually(sess).Should(gexec.Exit(0))
						Expect(sess.Out.Contents()).NotTo(ContainSubstring("type the pipeline name"))
					})
				})
			})

			Context("when a Run of the template is still active", func() {
				BeforeEach(func() {
					args = append(args, "-n")
					atcServer.AppendHandlers(
						pipelineExists,
						runsListed,
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("DELETE", templatePath, "purge_run_history=true"),
							refused("template has a run that is still running or still being cancelled"),
						),
					)
				})

				It("prints the server's reason and fails", func() {
					warns()
					Eventually(sess).Should(gexec.Exit(1))
					Expect(sess.Err).To(gbytes.Say("error: template has a run that is still running or still being cancelled"))
				})
			})
		})
	})
})
