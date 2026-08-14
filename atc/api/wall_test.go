package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/testhelpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Wall API", func() {
	var (
		response *http.Response
		realdb   *realDB
	)

	BeforeEach(func() {
		realdb = useRealDB()
	})

	Context("Gets a wall message", func() {
		BeforeEach(func() {
			useProfile(anonymousProfile)

			// A real banner row, set through the same Wall the handler reads.
			Expect(realdb.Deps.wall.SetWall(atc.Wall{Message: "test message"})).To(Succeed())
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/wall", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns 200", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
		})

		It("returns Content-Type 'application/json'", func() {
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
		})

		Context("the message does not expire", func() {

			It("returns only message", func() {
				Expect(io.ReadAll(response.Body)).To(MatchJSON(`{"message":"test message"}`))
			})
		})

		Context("and the message does expire", func() {
			var (
				expectedDuration time.Duration
			)
			BeforeEach(func() {
				expectedDuration = time.Minute
				Expect(realdb.Deps.wall.SetWall(atc.Wall{
					Message: "test message", TTL: expectedDuration,
				})).To(Succeed())
			})

			It("returns the expiration with the message", func() {
				var msg atc.Wall
				err := json.NewDecoder(response.Body).Decode(&msg)
				Expect(err).ToNot(HaveOccurred())
				Expect(msg.Message).To(Equal("test message"))
				// The TTL is recomputed from expires_at against the database's
				// clock on each read, so it counts down rather than coming back
				// as the literal that was stored.
				Expect(msg.TTL).To(BeNumerically("~", expectedDuration, time.Second))
			})
		})
	})

	Context("Sets a wall message", func() {
		var expectedWall atc.Wall
		BeforeEach(func() {
			expectedWall = atc.Wall{
				Message: "test message",
				TTL:     time.Minute,
			}

		})

		JustBeforeEach(func() {
			payload, err := json.Marshal(expectedWall)
			Expect(err).NotTo(HaveOccurred())

			req, err := http.NewRequest("PUT", server.URL+"/api/v1/wall",
				io.NopCloser(bytes.NewBuffer(payload)))
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			Context("and is admin", func() {
				BeforeEach(func() {
					useProfile(adminProfile)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("sets the message and expiration", func() {
					stored, err := realdb.Deps.wall.GetWall()
					Expect(err).NotTo(HaveOccurred())
					Expect(stored.Message).To(Equal(expectedWall.Message))
					Expect(stored.TTL).To(BeNumerically("~", expectedWall.TTL, time.Second))
				})

				Context("when message is empty", func() {
					BeforeEach(func() {
						expectedWall = atc.Wall{
							Message: "",
							TTL:     time.Minute,
						}
					})

					It("returns 400 Bad Request", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						body, _ := io.ReadAll(response.Body)
						Expect(string(body)).To(Equal("Wall message cannot be empty"))
					})

					It("stores nothing", func() {
						stored, err := realdb.Deps.wall.GetWall()
						Expect(err).NotTo(HaveOccurred())
						Expect(stored.Message).To(BeEmpty())
					})
				})
			})

			Context("and is not admin", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))

					stored, err := realdb.Deps.wall.GetWall()
					Expect(err).NotTo(HaveOccurred())
					Expect(stored.Message).To(BeEmpty())
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))

				stored, err := realdb.Deps.wall.GetWall()
				Expect(err).NotTo(HaveOccurred())
				Expect(stored.Message).To(BeEmpty())
			})
		})
	})

	Context("Clears the wall message", func() {
		BeforeEach(func() {
			Expect(realdb.Deps.wall.SetWall(atc.Wall{Message: "to be cleared"})).To(Succeed())
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/wall", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			Context("is an admin", func() {
				BeforeEach(func() {
					useProfile(adminProfile)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("clears the stored banner", func() {
					stored, err := realdb.Deps.wall.GetWall()
					Expect(err).NotTo(HaveOccurred())
					Expect(stored.Message).To(BeEmpty())
				})
			})
			Context("is not an admin", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))

					stored, err := realdb.Deps.wall.GetWall()
					Expect(err).NotTo(HaveOccurred())
					Expect(stored.Message).To(Equal("to be cleared"))
				})
			})
		})
		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))

				stored, err := realdb.Deps.wall.GetWall()
				Expect(err).NotTo(HaveOccurred())
				Expect(stored.Message).To(Equal("to be cleared"))
			})

		})

	})
})
