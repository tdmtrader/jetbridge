package integration_test

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"github.com/concourse/concourse/fly/rc"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"

	"github.com/concourse/concourse/atc"
)

var _ = Describe("login", func() {
	var (
		loginATCServer *ghttp.Server
	)

	Describe("with no target name", func() {
		var (
			flyCmd *exec.Cmd
		)

		BeforeEach(func() {
			loginATCServer = ghttp.NewServer()
			loginATCServer.AppendHandlers(
				infoHandler(),
			)
			flyCmd = exec.Command(flyPath, "login", "-c", loginATCServer.URL())
		})

		AfterEach(func() {
			loginATCServer.Close()
		})

		It("instructs the user to specify --target", func() {
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(1))

			Expect(sess.Err).To(gbytes.Say(`name for the target must be specified \(--target/-t\)`))
		})
	})

	Context("with no team name", func() {
		BeforeEach(func() {
			loginATCServer = ghttp.NewServer()
		})

		AfterEach(func() {
			loginATCServer.Close()
		})

		It("falls back to atc.DefaultTeamName team", func() {
			loginATCServer.AppendHandlers(
				infoHandler(),
				tokenHandler(),
				userInfoHandler(),
			)

			flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-u", "user", "-p", "pass")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess).Should(gbytes.Say("logging in to team 'main'"))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
		})

		Context("when already logged in as different team", func() {
			BeforeEach(func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				setupFlyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "some-team", "-u", "user", "-p", "pass")
				err := setupFlyCmd.Run()
				Expect(err).NotTo(HaveOccurred())
			})

			It("uses the saved team name", func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-u", "user", "-p", "pass")
				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				Eventually(sess).Should(gbytes.Say("logging in to team 'some-team'"))

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})
		})
	})

	Context("with no specified flag but extra arguments ", func() {

		BeforeEach(func() {
			loginATCServer = ghttp.NewServer()
		})

		AfterEach(func() {
			loginATCServer.Close()
		})

		It("return error indicating login failed with unknown arguments", func() {

			flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "unknown-argument", "blah")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`unexpected argument \[unknown-argument, blah\]`))
		})
	})

	Context("with a team name", func() {

		BeforeEach(func() {
			loginATCServer = ghttp.NewServer()
		})

		AfterEach(func() {
			loginATCServer.Close()
		})

		It("uses specified team", func() {
			loginATCServer.AppendHandlers(
				infoHandler(),
				tokenHandler(),
				userInfoHandler(),
			)

			flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "some-team", "-u", "user", "-p", "pass")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess).Should(gbytes.Say("logging in to team 'some-team'"))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
		})

		Context("when tracing is not enabled", func() {
			It("does not print out API calls", func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "some-team", "-u", "user", "-p", "pass")

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				Consistently(sess.Err).ShouldNot(gbytes.Say("HTTP/1.1 200 OK"))
				Consistently(sess.Out).ShouldNot(gbytes.Say("HTTP/1.1 200 OK"))

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})
		})

		Context("when tracing is enabled", func() {
			It("prints out API calls", func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				flyCmd := exec.Command(flyPath, "--verbose", "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "some-team", "-u", "user", "-p", "pass")

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				Eventually(sess.Err).Should(gbytes.Say("HTTP/1.1 200 OK"))

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})
		})

		Context("when already logged in as different team", func() {
			BeforeEach(func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				setupFlyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "some-team", "-u", "user", "-p", "pass")
				err := setupFlyCmd.Run()
				Expect(err).NotTo(HaveOccurred())
			})

			It("passes provided team name", func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-n", "some-other-team", "-u", "user", "-p", "pass")

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})
		})
	})

	Describe("with ca cert", func() {
		BeforeEach(func() {
			loginATCServer = ghttp.NewUnstartedServer()
			cert, err := tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
			Expect(err).NotTo(HaveOccurred())

			loginATCServer.HTTPTestServer.TLS = &tls.Config{
				Certificates: []tls.Certificate{cert},
			}
			loginATCServer.HTTPTestServer.StartTLS()
		})

		AfterEach(func() {
			loginATCServer.Close()
		})

		Context("when already logged in with ca cert", func() {
			var caCertFilePath string

			BeforeEach(func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					userInfoHandler(),
				)

				caCertFile, err := os.CreateTemp("", "fly-login-test")
				Expect(err).NotTo(HaveOccurred())
				caCertFilePath = caCertFile.Name()

				err = os.WriteFile(caCertFilePath, []byte(serverCert), os.ModePerm)
				Expect(err).NotTo(HaveOccurred())

				setupFlyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "some-team", "--ca-cert", caCertFilePath, "-u", "user", "-p", "pass")

				sess, err := gexec.Start(setupFlyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())
				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})

			AfterEach(func() {
				os.RemoveAll(caCertFilePath)
			})

			Context("when ca cert is not provided", func() {
				It("is using saved ca cert", func() {
					loginATCServer.AppendHandlers(
						infoHandler(),
						tokenHandler(),
						userInfoHandler(),
					)

					flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-n", "some-team", "-u", "user", "-p", "pass")

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					<-sess.Exited
					Expect(sess.ExitCode()).To(Equal(0))
				})
			})
		})
	})

	Describe("login", func() {
		var (
			flyCmd *exec.Cmd
		)

		BeforeEach(func() {
			loginATCServer = ghttp.NewServer()
		})

		AfterEach(func() {
			loginATCServer.Close()
		})

		Context("with authorization_code grant", func() {
			BeforeEach(func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					userInfoHandler(),
				)
			})

			It("allows providing the token via stdin", func() {
				flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL())

				stdin, err := flyCmd.StdinPipe()
				Expect(err).NotTo(HaveOccurred())

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				Eventually(sess.Out).Should(gbytes.Say("navigate to the following URL in your browser:"))
				Eventually(sess.Out).Should(gbytes.Say("http://127.0.0.1:(\\d+)/sky/issuer/auth\\?"))
				Eventually(sess.Out).Should(gbytes.Say("or enter token manually"))

				_, err = fmt.Fprintf(stdin, "Bearer some-token\r")
				Expect(err).NotTo(HaveOccurred())

				err = stdin.Close()
				Expect(err).NotTo(HaveOccurred())

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})

			Context("when the token from stdin is malformed", func() {
				It("logs an error and accepts further input", func() {
					flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL())

					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("or enter token manually"))

					_, err = fmt.Fprintf(stdin, "not a token\r")
					Expect(err).NotTo(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("token must be of the format 'TYPE VALUE', e.g. 'Bearer ...'"))

					_, err = fmt.Fprintf(stdin, "Bearer ok-this-time-its-the-real-deal\r")
					Expect(err).NotTo(HaveOccurred())

					err = stdin.Close()
					Expect(err).NotTo(HaveOccurred())

					<-sess.Exited
					Expect(sess.ExitCode()).To(Equal(0))
				})
			})

			Context("when the token from stdin is terminated with an EOF", func() {
				It("accepts the input", func() {
					flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL())

					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					Eventually(sess.Out).Should(gbytes.Say("or enter token manually"))

					_, err = fmt.Fprintf(stdin, "bearer no-new-line-here\r")
					Expect(err).NotTo(HaveOccurred())

					err = stdin.Close()
					Expect(err).NotTo(HaveOccurred())

					<-sess.Exited
					Expect(sess.ExitCode()).To(Equal(0))
				})

				It("ignores empty input", func() {
					flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL())

					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					err = stdin.Close()
					Expect(err).NotTo(HaveOccurred())

					Consistently(sess.Out).ShouldNot(gbytes.Say("error"))

					sess.Kill()
				})
			})

			Context("authorization code callback", func() {
				It("checks state and exchanges a PKCE-bound code, retaining renewal credentials", func() {
					idToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".signature"
					var loginURL *url.URL
					loginATCServer.RouteToHandler("POST", "/sky/issuer/token", func(w http.ResponseWriter, r *http.Request) {
						Expect(r.ParseForm()).To(Succeed())
						Expect(r.PostForm.Get("client_id")).To(Equal("fly-browser"))
						Expect(r.PostForm.Get("grant_type")).To(Equal("authorization_code"))
						Expect(r.PostForm.Get("code")).To(Equal("one-time-code"))
						digest := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
						Expect(base64.RawURLEncoding.EncodeToString(digest[:])).To(Equal(loginURL.Query().Get("code_challenge")))
						Expect(r.PostForm.Get("redirect_uri")).To(Equal(loginURL.Query().Get("redirect_uri")))
						ghttp.RespondWithJSONEncoded(200, map[string]string{"token_type": "Bearer", "access_token": "access", "id_token": idToken, "refresh_token": "renewable-credential"})(w, r)
					})
					flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL())
					stdin, err := flyCmd.StdinPipe()
					Expect(err).NotTo(HaveOccurred())
					defer stdin.Close()
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())
					defer sess.Kill()
					Eventually(sess.Out).Should(gbytes.Say("or enter token manually"))
					match := regexp.MustCompile(`http://127.0.0.1:\d+/sky/issuer/auth\?[^\s]+`).FindString(string(sess.Out.Contents()))
					Expect(match).NotTo(BeEmpty())
					loginURL, err = url.Parse(match)
					Expect(err).NotTo(HaveOccurred())
					Expect(loginURL.Query().Get("code_challenge_method")).To(Equal("S256"))
					callback, err := url.Parse(loginURL.Query().Get("redirect_uri"))
					Expect(err).NotTo(HaveOccurred())
					values := url.Values{"code": {"one-time-code"}, "state": {"wrong-state"}}
					callback.RawQuery = values.Encode()
					response, err := http.Get(callback.String())
					Expect(err).NotTo(HaveOccurred())
					response.Body.Close()
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
					values.Set("state", loginURL.Query().Get("state"))
					callback.RawQuery = values.Encode()
					response, err = http.Get(callback.String())
					Expect(err).NotTo(HaveOccurred())
					response.Body.Close()
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Eventually(sess).Should(gexec.Exit(0))
					targets, err := rc.LoadTargets()
					Expect(err).NotTo(HaveOccurred())
					Expect(targets["some-target"].Token.RefreshToken).To(Equal("renewable-credential"))
					Expect(targets["some-target"].Token.OAuthClientID).To(Equal("fly-browser"))
					Expect(string(sess.Out.Contents())).NotTo(ContainSubstring("renewable-credential"))
				})
			})

		})

		Context("with password grant", func() {
			BeforeEach(func() {
				credentials := base64.StdEncoding.EncodeToString([]byte("fly:Zmx5"))
				loginATCServer.AppendHandlers(
					infoHandler(),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("POST", "/sky/issuer/token"),
						ghttp.VerifyHeaderKV("Content-Type", "application/x-www-form-urlencoded"),
						ghttp.VerifyHeaderKV("Authorization", fmt.Sprintf("Basic %s", credentials)),
						ghttp.VerifyFormKV("grant_type", "password"),
						ghttp.VerifyFormKV("username", "some_username"),
						ghttp.VerifyFormKV("password", "some_password"),
						ghttp.VerifyFormKV("scope", "openid profile email federated:id groups offline_access"),
						ghttp.RespondWithJSONEncoded(200, map[string]string{
							"token_type":   "Bearer",
							"access_token": "access-token",
						}),
					),
					userInfoHandler(),
				)
			})

			It("takes username and password as cli arguments", func() {
				flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-u", "some_username", "-p", "some_password")
				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				Consistently(sess.Out.Contents).ShouldNot(ContainSubstring("some_password"))

				Eventually(sess.Out).Should(gbytes.Say("target saved"))

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})

			Context("after logging in succeeds", func() {
				BeforeEach(func() {
					flyCmd = exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-u", "some_username", "-p", "some_password")
					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					Consistently(sess.Out.Contents).ShouldNot(ContainSubstring("some_password"))

					Eventually(sess.Out).Should(gbytes.Say("target saved"))

					<-sess.Exited
					Expect(sess.ExitCode()).To(Equal(0))
				})

				It("flyrc is backwards-compatible with pre-v5.4.0", func() {
					flyRcContents, err := os.ReadFile(homeDir + "/.flyrc")
					Expect(err).NotTo(HaveOccurred())
					Expect(string(flyRcContents)).To(HavePrefix("targets:"))
				})

				Describe("running other commands", func() {
					BeforeEach(func() {
						loginATCServer.AppendHandlers(
							infoHandler(),
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", "/api/v1/teams/main/pipelines"),
								ghttp.VerifyHeaderKV("Authorization", "Bearer access-token"),
								ghttp.RespondWithJSONEncoded(200, []atc.Pipeline{
									{Name: "pipeline-1"},
								}),
							),
						)
					})

					It("uses the saved token", func() {
						otherCmd := exec.Command(flyPath, "-t", "some-target", "pipelines")

						sess, err := gexec.Start(otherCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).NotTo(HaveOccurred())

						<-sess.Exited

						Expect(sess).To(gbytes.Say("pipeline-1"))

						Expect(sess.ExitCode()).To(Equal(0))
					})
				})

				Describe("logging in again with the same target", func() {
					BeforeEach(func() {
						credentials := base64.StdEncoding.EncodeToString([]byte("fly:Zmx5"))

						loginATCServer.AppendHandlers(
							infoHandler(),
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("POST", "/sky/issuer/token"),
								ghttp.VerifyHeaderKV("Content-Type", "application/x-www-form-urlencoded"),
								ghttp.VerifyHeaderKV("Authorization", fmt.Sprintf("Basic %s", credentials)),
								ghttp.VerifyFormKV("grant_type", "password"),
								ghttp.VerifyFormKV("username", "some_other_user"),
								ghttp.VerifyFormKV("password", "some_other_pass"),
								ghttp.VerifyFormKV("scope", "openid profile email federated:id groups offline_access"),
								ghttp.RespondWithJSONEncoded(200, map[string]string{
									"token_type":   "Bearer",
									"access_token": "some-new-token",
								}),
							),
							userInfoHandler(),
							infoHandler(),
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("GET", "/api/v1/teams/main/pipelines"),
								ghttp.VerifyHeaderKV("Authorization", "Bearer some-new-token"),
								ghttp.RespondWithJSONEncoded(200, []atc.Pipeline{
									{Name: "pipeline-2"},
								}),
							),
						)
					})

					It("updates the token", func() {
						loginAgainCmd := exec.Command(flyPath, "-t", "some-target", "login", "-u", "some_other_user", "-p", "some_other_pass")

						sess, err := gexec.Start(loginAgainCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).NotTo(HaveOccurred())

						Consistently(sess.Out.Contents).ShouldNot(ContainSubstring("some_other_pass"))

						Eventually(sess.Out).Should(gbytes.Say("target saved"))

						<-sess.Exited
						Expect(sess.ExitCode()).To(Equal(0))

						otherCmd := exec.Command(flyPath, "-t", "some-target", "pipelines")

						sess, err = gexec.Start(otherCmd, GinkgoWriter, GinkgoWriter)
						Expect(err).NotTo(HaveOccurred())

						<-sess.Exited

						Expect(sess).To(gbytes.Say("pipeline-2"))

						Expect(sess.ExitCode()).To(Equal(0))
					})
				})
			})
		})

		Context("cannot successfully login", func() {
			Context("team does not exist", func() {
				It("returns a warning", func() {
					loginATCServer.AppendHandlers(
						infoHandler(),
						tokenHandler(),
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", "/api/v1/user"),
							ghttp.RespondWithJSONEncoded(200, map[string]any{
								"user_name": "user",
								"teams": map[string][]string{
									"other_team": {"owner"},
								},
							}),
						),
					)

					flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "any-team", "-u", "user", "-p", "pass")

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					Eventually(sess.Err).Should(gbytes.Say("you are not a member of 'any-team' or the team does not exist"))

					<-sess.Exited
					Expect(sess.ExitCode()).To(Equal(1))
				})
			})
			Context("/api/v1/user returns garbage", func() {
				It("returns a warning", func() {
					loginATCServer.AppendHandlers(
						infoHandler(),
						tokenHandler(),
						ghttp.CombineHandlers(
							ghttp.VerifyRequest("GET", "/api/v1/user"),
							ghttp.RespondWithJSONEncoded(200, map[string]any{
								"a-key": "a-value",
							}),
						),
					)

					flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "any-team", "-u", "user", "-p", "pass")

					sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
					Expect(err).NotTo(HaveOccurred())

					Eventually(sess.Err).Should(gbytes.Say("unable to verify role on team"))

					<-sess.Exited
					Expect(sess.ExitCode()).To(Equal(1))
				})
			})
		})

		Context("when logging in as an admin user", func() {
			It("can login to any team that exists", func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/user"),
						ghttp.RespondWithJSONEncoded(200, map[string]any{
							"user_name": "admin_user",
							"is_admin":  true,
						}),
					),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/teams"),
						ghttp.RespondWithJSONEncoded(200, []atc.Team{
							{
								ID:   1,
								Name: "any-team",
								Auth: atc.TeamAuth{
									"owner": map[string][]string{
										"groups": []string{},
										"users":  []string{},
									},
								},
							},
						}),
					),
				)

				flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "any-team", "-u", "admin_user", "-p", "pass")

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				Eventually(sess.Out).Should(gbytes.Say("target saved"))

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(0))
			})
			It("fails to login if the team does not exist", func() {
				loginATCServer.AppendHandlers(
					infoHandler(),
					tokenHandler(),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/user"),
						ghttp.RespondWithJSONEncoded(200, map[string]any{
							"user_name": "admin_user",
							"is_admin":  true,
						}),
					),
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", "/api/v1/teams"),
						ghttp.RespondWithJSONEncoded(200, []atc.Team{
							{
								ID:   1,
								Name: "main",
								Auth: atc.TeamAuth{
									"owner": map[string][]string{
										"groups": []string{},
										"users":  []string{},
									},
								},
							},
						}),
					),
				)

				flyCmd := exec.Command(flyPath, "-t", "some-target", "login", "-c", loginATCServer.URL(), "-n", "doesNotExist", "-u", "admin_user", "-p", "pass")

				sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
				Expect(err).NotTo(HaveOccurred())

				Eventually(sess.Err).Should(gbytes.Say("error: team 'doesNotExist' does not exist"))

				<-sess.Exited
				Expect(sess.ExitCode()).To(Equal(1))
			})
		})
	})
})
