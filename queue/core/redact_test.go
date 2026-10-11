package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

// leak is an error whose text holds a URL with a password, as a port's error may.
var leak = errors.New("refused by https://user:SECRET@host/repo")

type leakyStore struct{ *memStore }

func (leakyStore) Acquire(context.Context, string, time.Duration) (core.Lease, error) {
	return core.Lease{}, leak
}

type leakyComposer struct{}

func (leakyComposer) Compose(context.Context, string, []core.Entry) (string, error) { return "", leak }

type leakyRunner struct{ *memRunner }

func (leakyRunner) Poll(context.Context, string) (core.Verdict, bool, error) { return "", false, leak }

type leakyLander struct{ *memLander }

func (leakyLander) Land(context.Context, string, string, uint64) error { return leak }
func (leakyLander) Contains(context.Context, string, string, uint64) (bool, error) {
	return false, leak
}

type leakyAdmissions struct{}

func (leakyAdmissions) Pending(context.Context, []core.Entry) ([]core.Pending, error) {
	return []core.Pending{{ID: "x", Commit: "cx", Why: leak.Error()}}, leak
}
func (leakyAdmissions) Done(context.Context, string, string) error { return leak }

type leakyResumes struct{}

func (leakyResumes) Pending(context.Context) ([]core.ResumeRequest, error) {
	return []core.ResumeRequest{{Seq: 1, SHA: "sha"}}, leak
}
func (leakyResumes) Done(context.Context, core.ResumeRequest) error { return leak }

var _ = Describe("Driver redaction", func() {
	DescribeTable("A credential in any port's error reaches no snapshot, event or log line",
		func(leaky func(d *core.Driver)) {
			store, comp, note, logs := &memStore{}, &memComposer{}, &memNotifier{}, []string{}
			d := &core.Driver{
				Store: store, Composer: comp, Runner: &memRunner{}, Lander: &memLander{c: comp}, Notifier: note,
				NewStrategy: func() core.Strategy { return &core.Serial{Max: 4} },
				Main:        "core", Owner: "runner",
				Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
			}
			Expect(d.Admit(context.Background(), entry("a"))).To(Succeed())
			leaky(d)
			done, cancel := context.WithCancel(context.Background())
			cancel()
			for range 4 {
				Expect(d.Run(done, time.Hour)).To(MatchError(context.Canceled)) // one Step, its error logged
			}
			events, err := json.Marshal(note.events)
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).NotTo(BeEmpty(), "the port's error was logged")
			for what, text := range map[string]string{"snapshot": string(store.data), "events": string(events), "logs": strings.Join(logs, "\n")} {
				Expect(text).NotTo(ContainSubstring("SECRET"), what)
			}
		},
		Entry("Store", func(d *core.Driver) { d.Store = leakyStore{d.Store.(*memStore)} }),
		Entry("Composer", func(d *core.Driver) { d.Composer = leakyComposer{} }),
		Entry("Runner", func(d *core.Driver) { d.Runner = leakyRunner{d.Runner.(*memRunner)} }),
		Entry("Lander", func(d *core.Driver) { d.Lander = leakyLander{d.Lander.(*memLander)} }),
		Entry("Admissions", func(d *core.Driver) { d.Admissions = leakyAdmissions{} }),
		Entry("Resumes", func(d *core.Driver) { d.Resumes = leakyResumes{} }),
	)
})
