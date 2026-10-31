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

var _ = Describe("set-team", func() {
	var (
		flyCmd    *exec.Cmd
		cmdParams []string
	)

	JustBeforeEach(func() {
		params := append([]string{"-t", targetName, "set-team", "--team-name", "venture"}, cmdParams...)
		flyCmd = exec.Command(flyPath, params...)
	})

	yes := func(stdin io.Writer) {
		fmt.Fprintf(stdin, "y\r")
	}

	no := func(stdin io.Writer) {
		fmt.Fprintf(stdin, "n\r")
	}

	// interruptAtPrompt sends SIGINT once the confirmation prompt has rendered.
	// set-team has no signal handler of its own: SIGINT is caught only by the
	// prompt (bubbletea registers it at the start of Program.Run, before its
	// first frame, and turns it into ErrInterrupted, so fly exits 1). A SIGINT
	// that lands any earlier meets the Go runtime's default disposition and the
	// process dies with 130 -- the race CI hit under -p.
	interruptAtPrompt := func(sess *gexec.Session) {
		Eventually(sess.Out).Should(gbytes.Say(`apply team configuration\? \[yN\]`))
		sess.Interrupt()
	}

	Context("using a config file", func() {
		Describe("config validation", func() {
			Context("unsupported connectors", func() {
				BeforeEach(func() {
					cmdParams = []string{"-c", "fixtures/team_config_mixed.yml", "--non-interactive"}
				})
				It("rejects mixed membership without submitting a reduced team", func() {
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					Eventually(sess).Should(gexec.Exit(1))
					Expect(sess.Err).To(gbytes.Say(`unsupported connector "(github|oauth)" in team role "owner"`))
					for _, request := range atcServer.ReceivedRequests() {
						Expect(request.Method).NotTo(Equal(http.MethodPut))
					}
				})
			})

			Describe("no auth", func() {
				Context("auth config is missing auth for role", func() {
					BeforeEach(func() {
						cmdParams = []string{"-c", "fixtures/team_config_no_auth_for_role.yml"}
					})

					It("discard role with missing auth", func() {
						sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).ToNot(HaveOccurred())

						Eventually(sess.Out).Should(gbytes.Say("setting team: venture"))

						Eventually(sess.Out).Should(gbytes.Say("role member:"))
						Eventually(sess.Out).Should(gbytes.Say("users:"))
						Eventually(sess.Out).Should(gbytes.Say("- local:some-user"))
						Eventually(sess.Out).Should(gbytes.Say("groups:"))
						Eventually(sess.Out).Should(gbytes.Say("none"))
						Eventually(sess.Out).Should(gbytes.Say("role owner:"))
						Eventually(sess.Out).Should(gbytes.Say("users:"))
						Eventually(sess.Out).Should(gbytes.Say("- local:some-admin"))
						Eventually(sess.Out).Should(gbytes.Say("groups:"))
						Eventually(sess.Out).Should(gbytes.Say("none"))

						Eventually(sess.Out).ShouldNot(gbytes.Say("role viewer:"))

						interruptAtPrompt(sess)
						Eventually(sess).Should(gexec.Exit(1))
					})
				})

				Context("auth config contains empty user", func() {
					BeforeEach(func() {
						cmdParams = []string{"-c", "fixtures/team_config_empty_users.yml"}
					})

					It("discard role with no user and group", func() {
						sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).ToNot(HaveOccurred())
						Eventually(sess.Out).Should(gbytes.Say("setting team: venture"))

						Eventually(sess.Out).Should(gbytes.Say("role owner:"))
						Eventually(sess.Out).Should(gbytes.Say("users:"))
						Eventually(sess.Out).Should(gbytes.Say("- local:some-admin"))
						Eventually(sess.Out).Should(gbytes.Say("groups:"))
						Eventually(sess.Out).Should(gbytes.Say("none"))

						Eventually(sess.Out).ShouldNot(gbytes.Say("role viewer:"))

						interruptAtPrompt(sess)
						Eventually(sess).Should(gexec.Exit(1))
					})
				})
			})
		})

		Describe("Display", func() {
			Context("Setting local auth", func() {
				BeforeEach(func() {
					cmdParams = []string{"-c", "fixtures/team_config_with_local_auth.yml"}
				})

				It("shows the users configured for local auth for a given role", func() {
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("setting team: venture"))

					Eventually(sess.Out).Should(gbytes.Say("role member:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:some-member"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					Eventually(sess.Out).Should(gbytes.Say("role owner:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:some-owner"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					Eventually(sess.Out).Should(gbytes.Say("role viewer:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:some-viewer"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					interruptAtPrompt(sess)
					Eventually(sess).Should(gexec.Exit(1))
				})
			})

			Context("Setting auth with empty values", func() {
				BeforeEach(func() {
					cmdParams = []string{"-c", "fixtures/team_config_empty_values.yml"}
				})

				It("shows the users and groups configured for a given role", func() {
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("setting team: venture"))

					Eventually(sess.Out).Should(gbytes.Say("role member:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:some-member"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					Eventually(sess.Out).Should(gbytes.Say("role owner:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:some-admin"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					Eventually(sess.Out).Should(gbytes.Say("role viewer:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:some-viewer"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					interruptAtPrompt(sess)
					Eventually(sess).Should(gexec.Exit(1))
				})
			})
		})

		Describe("confirmation", func() {
			BeforeEach(func() {
				cmdParams = []string{"-c", "fixtures/team_config.yml"}
			})

			Context("when the user presses y/yes", func() {
				BeforeEach(func() {
					confirmHandlers()
				})

				It("exits 0", func() {
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
					yes(stdin)

					Eventually(sess).Should(gexec.Exit(0))
				})
			})

			Context("when the user presses n/no", func() {
				It("exits 1", func() {
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
					no(stdin)

					Eventually(sess.Err).Should(gbytes.Say("bailing out"))
					Eventually(sess).Should(gexec.Exit(1))
				})
			})
		})

		Describe("sending", func() {
			BeforeEach(func() {
				cmdParams = []string{"-c", "fixtures/team_config_with_local_auth.yml"}

				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("PUT", "/api/v1/teams/venture"),
						ghttp.VerifyJSON(`{
							"auth": {
								"owner":{
									"users": [
										"local:some-owner"
									],
									"groups": []
								},
								"member":{
									"users": [
										"local:some-member"
									],
									"groups": []
								},
								"viewer":{
									"users": [
										"local:some-viewer"
									],
									"groups": []
								}
							}
						}`),
						ghttp.RespondWithJSONEncoded(http.StatusCreated, atc.Team{
							Name: "venture",
							ID:   8,
						}),
					),
				)
			})

			It("sends the expected request", func() {
				stdin, err := flyCmd.StdinPipe()
				Expect(err).NotTo(HaveOccurred())

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).ToNot(HaveOccurred())

				Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
				yes(stdin)

				Eventually(sess).Should(gexec.Exit(0))
			})

			It("Outputs created for new team", func() {
				stdin, err := flyCmd.StdinPipe()
				Expect(err).NotTo(HaveOccurred())

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).ToNot(HaveOccurred())

				Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
				yes(stdin)

				Eventually(sess.Out).Should(gbytes.Say("team created"))

				Eventually(sess).Should(gexec.Exit(0))
			})
		})

		Describe("handling server response", func() {
			BeforeEach(func() {
				cmdParams = []string{"-c", "fixtures/team_config_with_local_auth.yml"}
			})

			Context("when the server returns 500", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("PUT", "/api/v1/teams/venture"),
							ghttp.VerifyJSON(`{
							"auth": {
								"owner":{
									"users": [
										"local:some-owner"
									],
									"groups": []
								},
								"member":{
									"users": [
										"local:some-member"
									],
									"groups": []
								},
								"viewer":{
									"users": [
										"local:some-viewer"
									],
									"groups": []
								}
							}
						}`),
							ghttp.RespondWith(http.StatusInternalServerError, "sorry bro"),
						),
					)
				})

				It("reports the error", func() {
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
					yes(stdin)

					Eventually(sess.Err).Should(gbytes.Say("sorry bro"))

					Eventually(sess).Should(gexec.Exit(1))
				})
			})
		})
	})

	Context("using command line args", func() {
		Describe("flag validation", func() {
			Describe("no auth", func() {
				Context("auth flag not provided", func() {
					BeforeEach(func() {
						cmdParams = []string{}
					})

					It("returns an error", func() {
						sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).ToNot(HaveOccurred())
						Eventually(sess.Err).Should(gbytes.Say("auth config for the team does not have users and groups configured"))
						Eventually(sess).Should(gexec.Exit(1))
					})
				})

				Context("empty auth values provided", func() {
					BeforeEach(func() {
						cmdParams = []string{"--local-user", ""}
					})

					It("returns an error", func() {
						sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).ToNot(HaveOccurred())
						Eventually(sess.Err).Should(gbytes.Say("auth config for the team does not have users and groups configured"))
						Eventually(sess).Should(gexec.Exit(1))
					})
				})

				Context("empty auth file is provided", func() {
					BeforeEach(func() {
						cmdParams = []string{"-c", "fixtures/team_config_empty.yml"}
					})

					It("returns an error", func() {
						sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).ToNot(HaveOccurred())
						Eventually(sess.Err).Should(gbytes.Say("auth config for the team must not be empty"))
						Eventually(sess).Should(gexec.Exit(1))
					})
				})
			})
		})

		Describe("Display", func() {
			Context("Setting local auth", func() {
				BeforeEach(func() {
					cmdParams = []string{"--local-user", "brock-samson"}
				})

				It("shows the users configured for local auth", func() {
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("setting team: venture"))
					Eventually(sess.Out).Should(gbytes.Say("role owner:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:brock-samson"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					interruptAtPrompt(sess)
					Eventually(sess).Should(gexec.Exit(1))
				})
			})

			Context("Setting auth with empty arguments", func() {
				BeforeEach(func() {
					cmdParams = []string{"--local-user", "", "--local-user", "samson", "--local-user", ""}
				})

				It("ignores empty arguments", func() {
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("setting team: venture"))
					Eventually(sess.Out).Should(gbytes.Say("role owner:"))
					Eventually(sess.Out).Should(gbytes.Say("users:"))
					Eventually(sess.Out).Should(gbytes.Say("- local:samson"))
					Eventually(sess.Out).Should(gbytes.Say("groups:"))
					Eventually(sess.Out).Should(gbytes.Say("none"))

					interruptAtPrompt(sess)
					Eventually(sess).Should(gexec.Exit(1))
				})
			})
		})

		Describe("confirmation", func() {
			BeforeEach(func() {
				cmdParams = []string{"--local-user", "brock-samson"}
			})

			Context("when the user presses y/yes", func() {
				BeforeEach(func() {
					confirmHandlers()
				})

				It("exits 0", func() {
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
					yes(stdin)

					Eventually(sess).Should(gexec.Exit(0))
				})
			})

			Context("when the user presses n/no", func() {
				It("exits 1", func() {
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
					no(stdin)

					Eventually(sess.Err).Should(gbytes.Say("bailing out"))
					Eventually(sess).Should(gexec.Exit(1))
				})
			})
		})

		Describe("sending", func() {
			BeforeEach(func() {
				cmdParams = []string{
					"--local-user", "brock-obama",
					"--local-user", "lisa",
					"--local-user", "frank",
				}

				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("PUT", "/api/v1/teams/venture"),
						ghttp.VerifyJSON(`{
							"auth": {
								"owner":{
									"users": [
										"local:brock-obama", "local:lisa", "local:frank"
									],
									"groups": []
								}
							}
						}`),
						ghttp.RespondWithJSONEncoded(http.StatusCreated, atc.Team{
							Name: "venture",
							ID:   8,
						}),
					),
				)
			})

			It("sends the expected request", func() {
				stdin, err := flyCmd.StdinPipe()
				Expect(err).NotTo(HaveOccurred())

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).ToNot(HaveOccurred())

				Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
				yes(stdin)

				Eventually(sess).Should(gexec.Exit(0))
			})

			It("Outputs created for new team", func() {
				stdin, err := flyCmd.StdinPipe()
				Expect(err).NotTo(HaveOccurred())

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).ToNot(HaveOccurred())

				Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
				yes(stdin)

				Eventually(sess.Out).Should(gbytes.Say("team created"))

				Eventually(sess).Should(gexec.Exit(0))
			})
		})

		Describe("handling server response", func() {
			BeforeEach(func() {
				cmdParams = []string{"--local-user", "brock-obama"}
			})

			Context("when the server returns 500", func() {
				BeforeEach(func() {
					atcServer.AppendHandlers(
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("PUT", "/api/v1/teams/venture"),
							ghttp.VerifyJSON(`{
							"auth": {
								"owner":{
									"users": [
										"local:brock-obama"
									],
									"groups": []
								}
							}
						}`),
							ghttp.RespondWith(http.StatusInternalServerError, "sorry bro"),
						),
					)
				})

				It("reports the error", func() {
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).ToNot(HaveOccurred())

					Eventually(sess).Should(gbytes.Say(`apply team configuration\? \[yN\]: `))
					yes(stdin)

					Eventually(sess.Err).Should(gbytes.Say("sorry bro"))

					Eventually(sess).Should(gexec.Exit(1))
				})
			})
		})
	})
})

func confirmHandlers() {
	atcServer.AppendHandlers(
		ghttp.CombineHandlers(
			ghttp.VerifyRequest("PUT", "/api/v1/teams/venture"),
			ghttp.RespondWithJSONEncoded(http.StatusCreated, atc.Team{
				Name: "venture",
				ID:   8,
			}),
		),
	)
}
