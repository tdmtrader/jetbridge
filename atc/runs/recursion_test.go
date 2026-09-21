package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Direct recursion, bounded at the port.
//
// A template whose entry job carries a `run_pipeline` naming itself reads as
// harmless in the config and produces an unbounded chain of runs the first
// time it is admitted. Nothing downstream can notice: each admission is a
// perfectly ordinary one, and the row it writes is indistinguishable from the
// row a person would have caused.
//
// Real Postgres, real templates and real payload pipelines, because the whole
// check is over identities the database assigns -- the payload's own pipeline
// id and the pipeline_run row that ties it back to its template. There is no
// way to state the claim against a double that would not also be stating the
// answer.
var _ = Describe("bounding direct recursion", func() {
	var ctx context.Context

	const contractKey = "recursion-test/some-call"

	BeforeEach(func() {
		ctx = context.Background()
	})

	admit := func(ref runs.TemplateRef, principal runs.Principal) (runs.Run, error) {
		GinkgoHelper()

		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		run, err := admitter.AdmitRun(ctx, tx, runs.Admission{
			Template:    ref,
			Principal:   principal,
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

	// buildInsidePayload is one of the entry builds CreateRunInTx wrote into
	// the run's payload pipeline -- the build that would be executing the
	// template's own `run_pipeline` step. Read back rather than constructed,
	// because the point is that its pipeline really is the payload the
	// admission above created.
	buildInsidePayload := func(run runs.Run) db.Build {
		GinkgoHelper()

		var buildID int
		Expect(dbConn.QueryRow(
			"SELECT id FROM builds WHERE pipeline_id = $1 ORDER BY id LIMIT 1",
			run.PayloadPipelineID,
		).Scan(&buildID)).To(Succeed())

		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "the run's payload has no entry build; these specs would be vacuous")

		return build
	}

	// One hop: a build whose own pipeline is the template it names.
	//
	// The builds row is written by hand, and that is the finding rather than a
	// shortcut. Two guards in atc/db already make this state unreachable
	// through the factories -- db.Job.CreateBuild refuses a template outright
	// ("pipeline templates cannot create builds directly"), and SavePipeline
	// refuses to turn a pipeline with build history into one ("pipeline with
	// ordinary job history or task caches cannot become a template"). So today
	// no build of a template can exist at all.
	//
	// The port refuses it anyway, and is tested against a row those guards
	// would not have written, because the port's own rule must not rest on two
	// rules stated somewhere else for other reasons. If either is ever relaxed
	// -- and "a pipeline may become a template" is a reasonable thing to want
	// -- this is the refusal that is already there.
	It("refuses a build whose own pipeline is the template it names", func() {
		entry, found, err := templatePipeline.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		var buildID int
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, job_id, pipeline_id, team_id, status)
			VALUES ('1', $1, $2, $3, 'started')
			RETURNING id
		`, entry.ID(), templatePipeline.ID(), defaultTeam.ID()).Scan(&buildID)).To(Succeed())

		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		before := countRunRows()

		_, err = admit(templateRef, runs.Principal{Build: buildPrincipalFor(build)})
		Expect(err).To(MatchError(runs.ErrCallerIsTemplate))
		Expect(countRunRows()).To(Equal(before))
	})

	// The loop that actually happens, and the one a person writes by
	// accident. The caller here is a real build of a real payload pipeline
	// admitted a moment ago, so its lineage is the one the schema wrote, not
	// one this spec arranged.
	It("refuses a build inside a run's payload that names the run's own template", func() {
		first, err := admit(templateRef, buildPrincipal)
		Expect(err).NotTo(HaveOccurred())

		inside := buildInsidePayload(first)

		before := countRunRows()

		_, err = admit(templateRef, runs.Principal{Build: buildPrincipalFor(inside)})
		Expect(err).To(MatchError(runs.ErrCallerIsRunOfTemplate))
		Expect(countRunRows()).To(Equal(before), "the chain this refusal exists to stop got one link longer")
	})

	// The two refusals are distinct values because the two configs a person
	// would have to go and fix are different, and a caller that merged them
	// would be telling one of them to look in the wrong place.
	It("tells the two loops apart", func() {
		Expect(runs.ErrCallerIsTemplate).NotTo(MatchError(runs.ErrCallerIsRunOfTemplate))
	})

	// The controls. Without them every refusal above would be satisfied by a
	// port that refused every build-initiated admission, and by one that
	// refused every admission from inside a payload.
	Describe("what it does not refuse", func() {
		It("admits a build of an ordinary pipeline naming an unrelated template", func() {
			run, err := admit(templateRef, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(run.ID).To(BeNumerically(">", 0))
		})

		It("admits a build inside a payload naming a different template", func() {
			first, err := admit(templateRef, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())

			inside := buildInsidePayload(first)

			// A second template on the same team, so the only thing that
			// differs from the refused case is which template was named.
			savePipeline(defaultTeam, "elsewhere", templateConfig(nil))
			elsewhere := runs.TemplateRef{
				Team:     defaultTeam.Name(),
				Pipeline: atc.PipelineRef{Name: "elsewhere"},
			}

			run, err := admit(elsewhere, runs.Principal{Build: buildPrincipalFor(inside)})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.ID).To(BeNumerically(">", 0))
		})

		// A person asking over HTTP has no calling build and so cannot be in
		// a loop. Admitting the same template a build was just refused for is
		// the clearest way to say that the refusal is about the caller.
		It("admits a person naming the same template a build was refused for", func() {
			first, err := admit(templateRef, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())

			inside := buildInsidePayload(first)
			_, err = admit(templateRef, runs.Principal{Build: buildPrincipalFor(inside)})
			Expect(err).To(MatchError(runs.ErrCallerIsRunOfTemplate))

			run, err := admit(templateRef, memberPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(run.ID).To(BeNumerically(">", 0))
		})
	})
})
