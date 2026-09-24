package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Holding a build principal against the builds row.
//
// A build principal is a plain struct handed over an in-process boundary that
// signs nothing. Comparing the team name it carries with the team name in the
// reference -- which is all the port used to do -- compares a caller's
// assertion with itself: it catches a consumer that honestly names another
// team and catches nothing at all from one that names the target in both
// fields. These specs are about the row being the evidence.
//
// Real Postgres and real builds throughout. The claim is about what the
// builds table says, so a double would be asserting that a method was called
// rather than that a forgery was refused.
var _ = Describe("verifying a build principal against the builds table", func() {
	var ctx context.Context

	const contractKey = "caller-build-test.some-call"

	BeforeEach(func() {
		ctx = context.Background()
	})

	admit := func(principal *runs.BuildPrincipal) (runs.Run, error) {
		GinkgoHelper()

		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		run, err := admitIn(ctx, admitter, tx, runs.Admission{
			Template:    templateRef,
			Principal:   runs.Principal{Build: principal},
			ContractKey: contractKey,
		})
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

	// The control. Without it every refusal below would be satisfied by a port
	// that refused all builds.
	It("admits the build the row describes", func() {
		run, err := admit(buildPrincipalFor(callerBuild))
		Expect(err).NotTo(HaveOccurred())
		Expect(run.ID).To(BeNumerically(">", 0))
	})

	// The forgery that the old team-name comparison could not see: a build
	// that really exists, on a team that really exists, claiming to belong to
	// the team whose template it wants. Both names in the admission agree with
	// each other -- that is exactly why agreement between them proves nothing.
	It("refuses a build that claims a team it does not belong to", func() {
		before := countRunRows()

		stranger := runningBuildOn(otherTeamHandle, "stranger-caller")

		forged := buildPrincipalFor(stranger)
		forged.TeamName = defaultTeam.Name()

		_, err := admit(forged)
		Expect(err).To(MatchError(runs.ErrUnauthorized))
		Expect(countRunRows()).To(Equal(before))
	})

	// No row at all. A consumer holding a build id it made up is in no better
	// position than one holding another team's.
	It("refuses a build id that names no row", func() {
		before := countRunRows()

		invented := buildPrincipalFor(callerBuild)
		invented.BuildID = callerBuild.ID() + 100000

		_, err := admit(invented)
		Expect(err).To(MatchError(runs.ErrUnauthorized))
		Expect(countRunRows()).To(Equal(before))
	})

	// Zero is the id a principal assembled from an empty StepMetadata carries,
	// which is the shape a wiring mistake produces rather than a forgery. It is
	// refused the same way, because the port cannot tell the two apart and the
	// answer is the same either way.
	It("refuses a build id of zero", func() {
		_, err := admit(&runs.BuildPrincipal{
			TeamName:     defaultTeam.Name(),
			PipelineName: "caller",
			JobName:      "release",
			BuildName:    "1",
			BuildID:      0,
		})
		Expect(err).To(MatchError(runs.ErrUnauthorized))
	})

	// A finished build has no authority left to lend. Whatever is still
	// running a step on its behalf has outlived it, and the port is the last
	// place that can say so.
	DescribeTable("refusing a build that is no longer running",
		func(finish func()) {
			finish()

			before := countRunRows()

			_, err := admit(buildPrincipalFor(callerBuild))
			Expect(err).To(MatchError(runs.ErrUnauthorized))
			Expect(countRunRows()).To(Equal(before))
		},
		Entry("one that succeeded", func() {
			Expect(callerBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
		}),
		Entry("one that failed", func() {
			Expect(callerBuild.Finish(db.BuildStatusFailed)).To(Succeed())
		}),
		Entry("one that errored", func() {
			Expect(callerBuild.Finish(db.BuildStatusErrored)).To(Succeed())
		}),
		// MarkAsAborted rather than Finish: `aborted` is set at the moment the
		// abort is requested, while the build may still be executing this very
		// step. That is exactly when the port should stop honouring it.
		Entry("one whose abort has been requested", func() {
			Expect(callerBuild.MarkAsAborted()).To(Succeed())
		}),
	)

	// Not authorization in its own right -- a build gains no standing by
	// misnaming its job -- but these three fields are the whole of
	// created_by, and a run attributed to a build that did not ask for it is a
	// provenance record that lies. They come off the same row, so there is no
	// reason to record them unchecked.
	DescribeTable("refusing a principal whose names disagree with the row",
		func(forge func(*runs.BuildPrincipal)) {
			forged := buildPrincipalFor(callerBuild)
			forge(forged)

			_, err := admit(forged)
			Expect(err).To(MatchError(runs.ErrUnauthorized))
		},
		Entry("a pipeline name that is not the build's", func(p *runs.BuildPrincipal) {
			p.PipelineName = "some-other-pipeline"
		}),
		Entry("a job name that is not the build's", func(p *runs.BuildPrincipal) {
			p.JobName = "some-other-job"
		}),
		Entry("a build name that is not the build's", func(p *runs.BuildPrincipal) {
			p.BuildName = "9999"
		}),
		// Exactly, not folded: both sides come from the same column, so
		// anything but equality is a principal that was not assembled from
		// this build.
		Entry("the build's own job name, shouted", func(p *runs.BuildPrincipal) {
			p.JobName = "RELEASE"
		}),
	)

	// The team comparison stays case-insensitive on both halves, and the row
	// is compared the same way: team names are unique case-insensitively in
	// the schema, so folding cannot widen the match, and refusing here would
	// refuse a build whose metadata spelled its own team differently from the
	// reference. build_principal_test.go's table is the other half of this.
	It("still folds the team name when comparing it with the row", func() {
		shouting := buildPrincipalFor(callerBuild)
		shouting.TeamName = "RUNS-TEAM"

		_, err := admit(shouting)
		Expect(err).NotTo(HaveOccurred())
	})
})
