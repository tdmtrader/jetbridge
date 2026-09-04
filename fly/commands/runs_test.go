package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
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

type pagedRunResponse struct {
	runs       []atc.PipelineRun
	pagination concourse.Pagination
}

type pagedRunLister struct {
	responses []pagedRunResponse
	requested []concourse.Page
}

func (client *pagedRunLister) PipelineRuns(_ string, page concourse.Page) ([]atc.PipelineRun, concourse.Pagination, error) {
	client.requested = append(client.requested, page)
	if len(client.responses) == 0 {
		return nil, concourse.Pagination{}, errors.New("asked for a page the server was never scripted to answer")
	}
	response := client.responses[0]
	client.responses = client.responses[1:]
	return response.runs, response.pagination, nil
}

func numberedRuns(highest, count int) []atc.PipelineRun {
	runs := make([]atc.PipelineRun, count)
	for i := range runs {
		runs[i] = atc.PipelineRun{Number: highest - i, Status: atc.RunStatusSucceeded}
	}
	return runs
}

// captureRunsStdout collects what displayhelpers.JsonPrint writes, which goes
// to os.Stdout rather than the writer command.run renders its table into.
func captureRunsStdout(run func()) string {
	GinkgoHelper()

	reader, writer, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()

	collected := make(chan string, 1)
	go func() {
		var buffer bytes.Buffer
		_, _ = io.Copy(&buffer, reader)
		collected <- buffer.String()
	}()

	run()
	Expect(writer.Close()).To(Succeed())
	return <-collected
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

	It("prints a supplied number parameter as supplied, not in scientific notation", func() {
		// This fails if params are formatted with %v: fmt renders float64(1e6)
		// as "1e+06", and the table is the only place the value is shown.
		now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
		client := &recordingRunLister{runs: []atc.PipelineRun{{
			Number: 3,
			Params: &atc.Params{
				"build_id": float64(1000000),
				"ratio":    float64(0.000001),
				"count":    json.Number("1000000"),
				"name":     "release",
			},
			Status:      atc.RunStatusSucceeded,
			CreatedAt:   now,
			CompletedAt: &now,
		}}}
		output := new(bytes.Buffer)
		command := &RunsCommand{
			Count:    50,
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			now:      func() time.Time { return now },
		}

		Expect(command.run(client, output)).To(Succeed())
		Expect(output.String()).To(ContainSubstring("build_id:1000000,count:1000000,name:release,ratio:0.000001"))
	})

	It("prints the runs as JSON and renders no table when --json is set", func() {
		now := time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC)
		client := &recordingRunLister{runs: []atc.PipelineRun{{
			ID:        42,
			Number:    3,
			Params:    &atc.Params{"build_id": float64(1000000)},
			Status:    atc.RunStatusSucceeded,
			CreatedBy: "creator",
			CreatedAt: now,
		}}}
		output := new(bytes.Buffer)
		command := &RunsCommand{
			Count:    50,
			Json:     true,
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			now:      func() time.Time { return now },
		}

		var runErr error
		printed := captureRunsStdout(func() { runErr = command.run(client, output) })
		Expect(runErr).To(Succeed())
		Expect(output.String()).To(BeEmpty(), "the table must not be rendered alongside JSON")

		var decoded []atc.PipelineRun
		Expect(json.Unmarshal([]byte(printed), &decoded)).To(Succeed())
		Expect(decoded).To(Equal(client.runs))
		Expect(printed).To(ContainSubstring(`"build_id": 1000000`))
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

	It("pages past the server's limit cap instead of returning a short list", func() {
		// This fails if -c above atc.PaginationAPIMaxLimit is sent through as
		// one limit: the server clamps it without saying so, and the user gets
		// 500 rows that look like the whole answer.
		client := &pagedRunLister{responses: []pagedRunResponse{
			{runs: numberedRuns(600, 500), pagination: concourse.Pagination{Next: &concourse.Page{To: 101, Limit: 500}}},
			{runs: numberedRuns(100, 100), pagination: concourse.Pagination{Next: &concourse.Page{To: 1, Limit: 500}}},
		}}
		output := new(bytes.Buffer)
		command := &RunsCommand{Count: 600, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, output)).To(Succeed())
		Expect(client.requested).To(Equal([]concourse.Page{
			{Limit: atc.PaginationAPIMaxLimit},
			{To: 101, Limit: 100},
		}))
		Expect(bytes.Count(output.Bytes(), []byte("\n"))).To(Equal(600))
	})

	It("stops once the requested count is collected", func() {
		client := &pagedRunLister{responses: []pagedRunResponse{
			{runs: numberedRuns(10, 3), pagination: concourse.Pagination{Next: &concourse.Page{To: 7, Limit: 3}}},
		}}
		command := &RunsCommand{Count: 3, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, new(bytes.Buffer))).To(Succeed())
		Expect(client.requested).To(HaveLen(1))
	})

	It("stops when the server has no further page", func() {
		client := &pagedRunLister{responses: []pagedRunResponse{
			{runs: numberedRuns(200, 200)},
		}}
		output := new(bytes.Buffer)
		command := &RunsCommand{Count: 600, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, output)).To(Succeed())
		Expect(client.requested).To(HaveLen(1))
		Expect(bytes.Count(output.Bytes(), []byte("\n"))).To(Equal(200))
	})

	It("returns a client error unchanged", func() {
		apiErr := errors.New("server says no")
		client := &recordingRunLister{err: apiErr}
		command := &RunsCommand{Count: 1, Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, new(bytes.Buffer))).To(MatchError(apiErr))
	})
})
