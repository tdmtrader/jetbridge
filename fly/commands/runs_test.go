package commands

import (
	"bytes"
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/jessevdk/go-flags"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type recordingRunLister struct {
	calls int
	name  string
	page  concourse.Page
	runs  []atc.PipelineRun
	err   error
}

func (client *recordingRunLister) PipelineRuns(name string, page concourse.Page) ([]atc.PipelineRun, concourse.Pagination, error) {
	client.calls++
	client.name = name
	client.page = page
	return client.runs, concourse.Pagination{}, client.err
}

var _ = Describe("RunsCommand", func() {
	It("registers runs without an alias", func() {
		parser := flags.NewParser(&FlyCommand{}, flags.HelpFlag)

		_, err := parser.ParseArgs([]string{"runs", "--help"})
		Expect(err).To(MatchError(ContainSubstring("[runs command options]")))
	})

	It("rejects a nonpositive count before loading its target", func() {
		command := &RunsCommand{Count: 0, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.Execute(nil)).To(MatchError(ContainSubstring("count must be positive")))
	})

	It("uses the base template name and exact count page", func() {
		client := &recordingRunLister{}
		command := &RunsCommand{Count: 7, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, new(bytes.Buffer))).To(Succeed())
		Expect(client.calls).To(Equal(1))
		Expect(client.name).To(Equal("template"))
		Expect(client.page).To(Equal(concourse.Page{Limit: 7}))
	})

	It("renders sorted params and deterministic running and completed durations", func() {
		now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
		completed := now.Add(-30 * time.Second)
		client := &recordingRunLister{runs: []atc.PipelineRun{
			{
				Number:    11,
				Params:    &atc.Params{"z": "two", "a": float64(1)},
				Status:    atc.RunStatusRunning,
				CreatedAt: now.Add(-75 * time.Second),
			},
			{
				Number:      10,
				Status:      atc.RunStatusSucceeded,
				CreatedAt:   now.Add(-2 * time.Minute),
				CompletedAt: &completed,
			},
		}}
		output := new(bytes.Buffer)
		command := &RunsCommand{
			Count:    50,
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			now:      func() time.Time { return now },
		}

		Expect(command.run(client, output)).To(Succeed())
		Expect(output.String()).To(Equal("11  a:1,z:two  running    2026-08-19@11:58:45+0000  1m15s+\n10  n/a        succeeded  2026-08-19@11:58:00+0000  1m30s \n"))
	})

	DescribeTable("rejects invalid input without calling the client",
		func(command *RunsCommand) {
			client := &recordingRunLister{}
			Expect(command.run(client, new(bytes.Buffer))).To(HaveOccurred())
			Expect(client.calls).To(BeZero())
		},
		Entry("nonpositive count", &RunsCommand{Count: 0, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}),
		Entry("instanced pipeline", &RunsCommand{Count: 1, Pipeline: flaghelpers.PipelineFlag{
			Name:         "template",
			InstanceVars: atc.InstanceVars{"branch": "main"},
		}}),
	)

	It("returns a client error unchanged", func() {
		apiErr := errors.New("server says no")
		client := &recordingRunLister{err: apiErr}
		command := &RunsCommand{Count: 1, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, new(bytes.Buffer))).To(MatchError(apiErr))
	})
})
