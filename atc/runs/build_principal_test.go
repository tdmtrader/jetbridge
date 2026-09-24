package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The build form of the principal.
//
// A build presents no token, so there is nothing here for the accessor to
// weigh: the whole of a build's authority is the team it belongs to, and the
// rule is the one set_pipeline already applies to a build mutating pipeline
// configs -- its own team and no other. These specs are about that rule, the
// identity it records, and the two ways a principal can fail to be one
// identity at all.
//
// Like admitter_test.go this file may name atc/db and does not need to. The
// created_by assertion reads the column rather than the returned model,
// because the claim is about what was persisted under a build's name.
var _ = Describe("a build acting for itself", func() {
	var ctx context.Context

	const contractKey = "build-principal-test.some-call"

	BeforeEach(func() {
		ctx = context.Background()
	})

	admit := func(adm runs.Admission) (runs.Run, error) {
		GinkgoHelper()
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		if adm.ContractKey == "" {
			adm.ContractKey = contractKey
		}

		run, err := admitIn(ctx, admitter, tx, adm)
		if err != nil {
			return runs.Run{}, err
		}
		Expect(tx.Commit()).To(Succeed())

		return run, nil
	}

	countRunRows := func() int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow("SELECT count(*) FROM pipeline_runs").Scan(&count)).To(Succeed())

		return count
	}

	It("admits on its own team, and records which build asked", func() {
		before := countRunRows()

		run, err := admit(runs.Admission{Template: templateRef, Principal: buildPrincipal})
		Expect(err).NotTo(HaveOccurred())
		Expect(countRunRows()).To(Equal(before + 1))

		// The column, not the model. The prefix is what keeps this from
		// colliding with a display user id, which is whatever a connector's
		// user_id claim held and is under nobody's control here.
		var createdBy string
		Expect(dbConn.QueryRow("SELECT created_by FROM pipeline_runs WHERE id = $1", run.ID).
			Scan(&createdBy)).To(Succeed())
		Expect(createdBy).To(Equal(buildCreatedBy))
		Expect(createdBy).To(Equal(run.CreatedBy))
		Expect(createdBy).To(HavePrefix("build:"))
	})

	// A build's invocation scope is its team plus the pipeline it runs in, read
	// off the verified builds row. So another build of the same pipeline
	// presenting the same key replays the Run -- which is what lets a parked
	// run_pipeline step resume -- while a build of another pipeline, or a
	// person, presenting that key gets a Run of its own.
	It("scopes its invocation to its team and calling pipeline", func() {
		first, err := admit(runs.Admission{Template: templateRef, Principal: buildPrincipal})
		Expect(err).NotTo(HaveOccurred())

		callerJob, found, err := callerPipeline.Job("release")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		sibling, err := callerJob.CreateBuild("someone")
		Expect(err).NotTo(HaveOccurred())

		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		samePipeline, replayed, err := admitter.AdmitVersionedRun(ctx, tx, runs.Admission{
			Template: templateRef, Principal: runs.Principal{Build: buildPrincipalFor(sibling)}, ContractKey: contractKey,
		}, testEpoch)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Expect(replayed).To(BeTrue())
		Expect(samePipeline.ID).To(Equal(first.ID))

		other := runningBuildOn(defaultTeam, "other-caller")
		otherPipeline, err := admit(runs.Admission{Template: templateRef, Principal: runs.Principal{Build: buildPrincipalFor(other)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(otherPipeline.ID).NotTo(Equal(first.ID))

		person, err := admit(runs.Admission{Template: templateRef, Principal: memberPrincipal})
		Expect(err).NotTo(HaveOccurred())
		Expect(person.ID).NotTo(Equal(first.ID))
		Expect(person.ID).NotTo(Equal(otherPipeline.ID))
		Expect(countRunRows()).To(Equal(3))
	})

	// The fold mirrors findTeam's, and not the accessor's, deliberately. The
	// accessor's verdict is case-sensitive because it keys a map by team name;
	// there is no such map here, only the principal's own team name compared
	// with the reference's. Folding is the right comparison for it because
	// team names are unique case-insensitively in the schema
	// (index_teams_name_unique_case_insensitive), so at most one team can
	// match either way and the match cannot be widened by the choice.
	//
	// Both directions, because both halves have to fold: the verdict above and
	// the resolution in findTeam. If only one did, a match here would hand
	// resolveTemplate a nil team and a build would be told it is unauthorized
	// for the team it belongs to.
	DescribeTable("matching its team without regard to case",
		func(referenceTeam, principalTeam string) {
			run, err := admit(runs.Admission{
				Template: runs.TemplateRef{
					Team:     referenceTeam,
					Pipeline: atc.PipelineRef{Name: "runnable"},
				},
				Principal: runs.Principal{Build: &runs.BuildPrincipal{
					// The real build, with only the spelling of its team
					// changed. The other four fields are held against the
					// builds row, so a principal invented here would be
					// refused for a reason that has nothing to do with case.
					TeamName:     principalTeam,
					PipelineName: callerBuild.PipelineName(),
					JobName:      callerBuild.JobName(),
					BuildName:    callerBuild.Name(),
					BuildID:      callerBuild.ID(),
				}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.ID).To(BeNumerically(">", 0))
		},
		Entry("the same case on both sides", "runs-team", "runs-team"),
		Entry("the principal shouting", "runs-team", "RUNS-TEAM"),
		Entry("the reference shouting", "RUNS-TEAM", "runs-team"),
		Entry("neither agreeing with the other", "Runs-Team", "rUNS-tEAM"),
	)

	It("refuses another team's template and creates no row", func() {
		before := countRunRows()

		// otherTeamRef is a real template on a team that really exists, so the
		// refusal is about the build's standing on it and not about the
		// reference being unresolvable.
		_, err := admit(runs.Admission{Template: otherTeamRef, Principal: buildPrincipal})
		Expect(err).To(MatchError(runs.ErrUnauthorized))
		Expect(countRunRows()).To(Equal(before))
	})

	// Not ErrTemplateNotFound: a build learns nothing about another team's
	// pipeline names, exactly as a person does not. The two answers are the
	// same value, which is what makes admission not an existence oracle.
	It("answers a nonexistent team the same way as one it has no standing on", func() {
		_, err := admit(runs.Admission{Template: missingTeam, Principal: buildPrincipal})
		Expect(err).To(MatchError(runs.ErrUnauthorized))
	})

	// The admin short-circuit is a property of a person's roles on a team
	// marked admin. No build has roles, so no build inherits it -- including a
	// build on the admin team itself, which is the case that would otherwise
	// hand every build on that team authority over every other team.
	// A build's standing on its team is the SaveConfig role: somebody with
	// at least that role put the config it is running there. It is held
	// against CreatePipelineRun exactly as a person's role would be, so the
	// one mapping ValidateCustomRoles permits -- run creation stronger than
	// saving a config -- cannot be walked around by a member saving a job
	// that carries run_pipeline and letting the build create the run.
	Describe("under the operator's role mapping", func() {
		admitUnder := func(customRoles map[string]string) (runs.Run, error) {
			GinkgoHelper()
			displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
			Expect(err).NotTo(HaveOccurred())

			port := runs.NewAdmitter(dbConn, runFactory, teamFactory, displayUserIds, customRoles)

			tx, err := port.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			run, err := admitIn(ctx, port, tx, runs.Admission{
				Template:    templateRef,
				Principal:   buildPrincipal,
				ContractKey: contractKey,
			})
			if err != nil {
				return runs.Run{}, err
			}
			Expect(tx.Commit()).To(Succeed())

			return run, nil
		}

		It("refuses a build when creating a run requires more than saving a config", func() {
			before := countRunRows()

			_, err := admitUnder(map[string]string{atc.CreatePipelineRunV2: "owner"})

			// A refusal, not the invalid-mapping fault: the mapping is one
			// the operator is allowed to configure, and it is this build
			// that does not measure up to it.
			Expect(err).To(MatchError(runs.ErrUnauthorized))
			Expect(countRunRows()).To(Equal(before))
		})

		It("admits a build when config and Run write actions were raised together", func() {
			before := countRunRows()

			run, err := admitUnder(map[string]string{
				atc.CreatePipelineRunV2:    "owner",
				atc.UploadPipelineRunInput: "owner",
				atc.SaveConfig:             "owner",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.CreatedBy).To(Equal(buildCreatedBy))
			Expect(countRunRows()).To(Equal(before + 1))
		})
	})

	It("is never an admin, not even on the admin team", func() {
		_, err := admit(runs.Admission{
			Template:  missingTeam,
			Principal: runs.Principal{Build: buildPrincipalFor(adminTeamBuild)},
		})

		// ErrUnauthorized and not ErrTemplateNotFound: the answer an admin
		// gets for a team that is not there is the answer this build does not
		// get. adminPrincipal's spec in admitter_test.go is the other half of
		// this comparison.
		Expect(err).To(MatchError(runs.ErrUnauthorized))
		Expect(err).NotTo(MatchError(runs.ErrTemplateNotFound))
	})

	Describe("a principal that is not one identity", func() {
		It("refuses one presenting both a token's claims and a build", func() {
			before := countRunRows()

			_, err := admit(runs.Admission{
				Template: templateRef,
				Principal: runs.Principal{
					Claims: claimsFor("member-user", "member-id"),
					Build:  buildPrincipal.Build,
				},
			})
			Expect(err).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
			Expect(countRunRows()).To(Equal(before))
		})

		// Both halves of the ambiguity, because an implementation that
		// checked only for "both set" would let an empty principal through to
		// the accessor, where a claimless verification is a verdict rather
		// than a refusal.
		It("refuses one presenting neither", func() {
			before := countRunRows()

			_, err := admit(runs.Admission{Template: templateRef, Principal: runs.Principal{}})
			Expect(err).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
			Expect(countRunRows()).To(Equal(before))
		})

		// Before the team read and before the template is resolved: an
		// unauthorized principal must not learn whether a template exists, and
		// a malformed one is in no better position. The reference here names a
		// team the principal would fail on anyway; the refusal is the
		// ambiguity, not the team.
		It("refuses the ambiguity rather than the reference", func() {
			_, err := admit(runs.Admission{
				Template: otherTeamRef,
				Principal: runs.Principal{
					Claims: claimsFor("member-user", "member-id"),
					Build:  buildPrincipal.Build,
				},
			})
			Expect(err).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
			Expect(err).NotTo(MatchError(runs.ErrUnauthorized))
		})

		// The contract key is checked before any principal is looked at, so
		// an admission that is malformed in both ways reports the key. Stated
		// because the ordering is the port's contract and not an accident:
		// nothing is decided on a caller's behalf before it is established
		// that the call could ever have been attributed to them.
		It("still reports the missing contract key first", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admitIn(ctx, admitter, tx, runs.Admission{
				Template:  templateRef,
				Principal: runs.Principal{},
			})
			Expect(err).To(MatchError(runs.ErrInvalidInvocationKey))
		})
	})
})
