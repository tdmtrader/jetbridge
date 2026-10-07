package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The landing queue form of the principal (ADR-0009): core's own component
// acting for the queue's team, authorized for that team and no other, with
// nothing to verify against because it is not a caller across a boundary.
var _ = Describe("a landing queue acting for its team", func() {
	var ctx context.Context
	var queue runs.Principal

	BeforeEach(func() {
		ctx = context.Background()
		queue = runs.Principal{Queue: &runs.QueuePrincipal{TeamName: defaultTeam.Name(), QueueName: "trunk"}}
	})

	admit := func(adm runs.Admission) (runs.Run, bool, error) {
		GinkgoHelper()
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		if adm.ContractKey == "" {
			adm.ContractKey = "queue-principal-test.entry-1"
		}
		run, replayed, err := admitter.AdmitVersionedRun(ctx, tx, adm, testEpoch)
		if err != nil {
			return runs.Run{}, false, err
		}
		Expect(tx.Commit()).To(Succeed())
		return run, replayed, nil
	}

	It("A landing queue admits a Run on its own team and is recorded as the creator", func() {
		run, _, err := admit(runs.Admission{Template: templateRef, Principal: queue})
		Expect(err).NotTo(HaveOccurred())
		var createdBy string
		Expect(dbConn.QueryRow("SELECT created_by FROM pipeline_runs WHERE id = $1", run.ID).Scan(&createdBy)).To(Succeed())
		Expect(createdBy).To(Equal("landing-queue/" + defaultTeam.Name() + "/trunk"))
		Expect(createdBy).To(Equal(run.CreatedBy))
	})

	It("A landing queue presenting the same key for an entry gets the Run it already admitted", func() {
		first, replayed, err := admit(runs.Admission{Template: templateRef, Principal: queue})
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeFalse())
		again, replayed, err := admit(runs.Admission{Template: templateRef, Principal: queue})
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeTrue())
		Expect(again.ID).To(Equal(first.ID))

		other := runs.Principal{Queue: &runs.QueuePrincipal{TeamName: defaultTeam.Name(), QueueName: "other"}}
		separate, _, err := admit(runs.Admission{Template: templateRef, Principal: other})
		Expect(err).NotTo(HaveOccurred())
		Expect(separate.ID).NotTo(Equal(first.ID))
	})

	It("A landing queue is refused on another team, with no name, and beside another form", func() {
		_, _, err := admit(runs.Admission{Template: templateRef, Principal: runs.Principal{Queue: &runs.QueuePrincipal{TeamName: "another-team", QueueName: "trunk"}}})
		Expect(err).To(MatchError(runs.ErrUnauthorized))
		_, _, err = admit(runs.Admission{Template: templateRef, Principal: runs.Principal{Queue: &runs.QueuePrincipal{TeamName: defaultTeam.Name()}}})
		Expect(err).To(MatchError(runs.ErrUnauthorized))
		both := runs.Principal{Queue: queue.Queue, Claims: memberPrincipal.Claims}
		_, _, err = admit(runs.Admission{Template: templateRef, Principal: both})
		Expect(err).To(MatchError(runs.ErrPrincipalAmbiguous))
	})
})
