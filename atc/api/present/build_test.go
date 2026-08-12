package present_test

import (
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Build", func() {
	var (
		team    db.Team
		dbBuild db.BuildForAPI
	)

	Describe("Comments", func() {
		var comment string = "🎉 Comments Work! 🥳"

		BeforeEach(func() {
			var err error
			team, err = teamFactory.CreateTeam(atc.Team{
				Name: "some-team",
				Auth: atc.TeamAuth{accessor.ViewerRole: {"users": {"test:some-user"}}},
			})
			Expect(err).NotTo(HaveOccurred())

			build, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			Expect(build.SetComment(comment)).To(Succeed())

			var found bool
			dbBuild, found, err = buildFactory.BuildForAPI(build.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(dbBuild.Comment()).To(Equal(comment))
		})

		checkComment := func(expect bool, job db.Job, access accessor.Access) {
			GinkgoHelper()

			build := present.Build(dbBuild, job, access)

			if expect {
				Expect(build.Comment).To(Equal(comment))
			} else {
				Expect(build.Comment).To(BeEmpty())
			}
		}

		// jobWithVisibility saves a pipeline whose only job carries the given
		// public flag and hands back the job row as PostgreSQL stored it.
		jobWithVisibility := func(public bool) db.Job {
			GinkgoHelper()

			pipeline, _, err := team.SavePipeline(
				atc.PipelineRef{Name: fmt.Sprintf("public-%v-pipeline", public)},
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job", Public: public}}},
				db.ConfigVersion(0),
				false,
			)
			Expect(err).NotTo(HaveOccurred())

			job, found, err := pipeline.Job("some-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(job.Public()).To(Equal(public))
			return job
		}

		// accessFor builds the accessor a request would really carry: the team
		// grants its viewer role to test:some-user and to nobody else.
		accessFor := func(authorized bool) accessor.Access {
			userID := "some-other-user"
			if authorized {
				userID = "some-user"
			}

			return accessor.NewAccessor(
				accessor.Verification{
					HasToken:     true,
					IsTokenValid: true,
					RawClaims: map[string]any{
						"federated_claims": map[string]any{
							"connector_id": "test",
							"user_id":      userID,
						},
					},
				},
				accessor.ViewerRole,
				"sub",
				[]string{"system"},
				[]db.Team{team},
				nil,
			)
		}

		It("should not be set if neither job nor accessor is passed in", func() {
			checkComment(false, nil, nil)
		})

		for _, v := range []bool{false, true} {
			It(fmt.Sprintf("should be set if job is public (%v)", v), func() {
				checkComment(v, jobWithVisibility(v), nil)
			})

			It(fmt.Sprintf("should be set if accessor allows it (%v)", v), func() {
				access := accessFor(v)
				Expect(access.IsAuthorized(dbBuild.TeamName())).To(Equal(v))

				checkComment(v, nil, access)
			})
		}
	})
})
