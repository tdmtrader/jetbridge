package core_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

type reportingRunner struct{ *memRunner }

func (r reportingRunner) FailedTests(string) []string { return []string{"TestOne", "TestTwo"} }
func (r reportingRunner) BuiltOn(string) string       { return "job test build 7" }

var _ = Describe("Failing test names", func() {
	It("An eject keeps the failing test names and the build they came from", func() {
		ctx, store, comp, adm := context.Background(), &memStore{}, &memComposer{}, &memAdmissions{}
		d := &core.Driver{Store: store, Composer: comp, Runner: reportingRunner{&memRunner{verdict: failsWith("b")}}, Lander: &memLander{c: comp},
			Notifier: &memNotifier{}, Admissions: adm, Main: "core", Owner: "runner", Log: func(string, ...any) {},
			NewStrategy: func() core.Strategy { return &core.Serial{Max: 1, Policy: core.Policy{RetryNone: 1}} }}
		adm.push(core.Pending{ID: "b", Commit: "sha-b"})
		for range 12 {
			Expect(d.Step(ctx)).To(Succeed())
		}
		Expect(store.snap().Settled[0].Failure).To(Equal(core.Failure{Failed: []string{"TestOne", "TestTwo"}, FailedOn: "job test build 7"}))
	})
})
