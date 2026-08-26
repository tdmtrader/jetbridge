package commands

import (
	"bytes"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/vars"
	"github.com/jessevdk/go-flags"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type recordingRunCreator struct {
	calls int
	name  string
	vars  map[string]any
	run   atc.PipelineRun
	err   error
}

func (client *recordingRunCreator) CreatePipelineRun(name string, variables map[string]any) (atc.PipelineRun, error) {
	client.calls++
	client.name = name
	client.vars = variables
	return client.run, client.err
}

var _ = Describe("RunPipelineCommand", func() {
	It("registers run-pipeline without an alias", func() {
		parser := flags.NewParser(&FlyCommand{}, flags.HelpFlag)

		_, err := parser.ParseArgs([]string{"run-pipeline", "--help"})
		Expect(err).To(MatchError(ContainSubstring("[run-pipeline command options]")))
	})

	It("rejects an instanced pipeline before loading its target", func() {
		command := &RunPipelineCommand{Pipeline: flaghelpers.PipelineFlag{
			Name:         "template",
			InstanceVars: atc.InstanceVars{"branch": "main"},
		}}

		Expect(command.Execute(nil)).To(MatchError(ContainSubstring("instanced")))
	})

	It("passes string and typed JSON variables by the base template name", func() {
		client := &recordingRunCreator{run: atc.PipelineRun{InstanceRef: &atc.PipelineIdentifier{PipelineName: "payload"}}}
		output := new(bytes.Buffer)
		command := &RunPipelineCommand{
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			Vars: []flaghelpers.VariablePairFlag{
				{Ref: vars.Reference{Path: "literal"}, Value: "12"},
				{Ref: vars.Reference{Path: "override"}, Value: "string"},
			},
			JSONVars: []flaghelpers.JSONVariablePairFlag{
				{Ref: vars.Reference{Path: "number"}, Value: float64(12.5)},
				{Ref: vars.Reference{Path: "override"}, Value: true},
			},
		}

		Expect(command.run(client, "https://ci.example", "main", output)).To(Succeed())
		Expect(client.calls).To(Equal(1))
		Expect(client.name).To(Equal("template"))
		Expect(client.vars).To(Equal(map[string]any{
			"literal":  "12",
			"number":   float64(12.5),
			"override": true,
		}))
	})

	It("names the created run and then the escaped ordinary URL for its payload", func() {
		client := &recordingRunCreator{run: atc.PipelineRun{
			Number: 7,
			Status: atc.RunStatusRunning,
			InstanceRef: &atc.PipelineIdentifier{
				PipelineName: "payload/name",
				InstanceVars: atc.InstanceVars{"branch": "a&b", "z": float64(2)},
			},
		}}
		output := new(bytes.Buffer)
		command := &RunPipelineCommand{Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, "https://ci.example", "ops team", output)).To(Succeed())
		Expect(output.String()).To(Equal(
			"started template run #7 (running)\n" +
				"https://ci.example/teams/ops%20team/pipelines/payload%2Fname?vars.branch=%22a%5Cu0026b%22&vars.z=2\n",
		))
	})

	It("rejects an instanced template without calling the client", func() {
		client := &recordingRunCreator{}
		command := &RunPipelineCommand{Pipeline: flaghelpers.PipelineFlag{
			Name:         "template",
			InstanceVars: atc.InstanceVars{"branch": "main"},
		}}

		Expect(command.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(MatchError(ContainSubstring("instanced")))
		Expect(client.calls).To(BeZero())
	})
	It("refuses a dotted parameter name instead of nesting it, for both flags", func() {
		client := &recordingRunCreator{}

		dotted := &RunPipelineCommand{
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			Vars:     []flaghelpers.VariablePairFlag{{Ref: vars.Reference{Path: "a", Fields: []string{"b"}}, Value: "1"}},
		}
		err := dotted.run(client, "https://ci.example", "main", new(bytes.Buffer))
		Expect(err).To(MatchError(ContainSubstring("--var parameter name a.b")))
		Expect(err).To(MatchError(ContainSubstring(`^[A-Za-z_][A-Za-z0-9_]*$`)))

		dottedJSON := &RunPipelineCommand{
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			JSONVars: []flaghelpers.JSONVariablePairFlag{{Ref: vars.Reference{Path: "a", Fields: []string{"b"}}, Value: float64(1)}},
		}
		Expect(dottedJSON.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(
			MatchError(ContainSubstring("--json-var parameter name a.b")),
		)

		sourced := &RunPipelineCommand{
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			JSONVars: []flaghelpers.JSONVariablePairFlag{{Ref: vars.Reference{Source: "vault", Path: "a"}, Value: float64(1)}},
		}
		Expect(sourced.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(
			MatchError(ContainSubstring("--json-var parameter name vault:a")),
		)

		illFormed := &RunPipelineCommand{
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			Vars:     []flaghelpers.VariablePairFlag{{Ref: vars.Reference{Path: "not-a-name"}, Value: "1"}},
		}
		Expect(illFormed.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(
			MatchError(ContainSubstring("--var parameter name not-a-name")),
		)

		Expect(client.calls).To(BeZero())
	})

	It("still accepts a plain parameter name on both flags", func() {
		client := &recordingRunCreator{run: atc.PipelineRun{Number: 1, Status: atc.RunStatusRunning, InstanceRef: &atc.PipelineIdentifier{PipelineName: "payload"}}}
		command := &RunPipelineCommand{
			Pipeline: flaghelpers.PipelineFlag{Name: "template"},
			Vars:     []flaghelpers.VariablePairFlag{{Ref: vars.Reference{Path: "_count"}, Value: "1"}},
			JSONVars: []flaghelpers.JSONVariablePairFlag{{Ref: vars.Reference{Path: "Enabled2"}, Value: true}},
		}

		Expect(command.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(Succeed())
		Expect(client.vars).To(Equal(map[string]any{"_count": "1", "Enabled2": true}))
	})

	It("fails when the server omits the returned payload reference", func() {
		client := &recordingRunCreator{run: atc.PipelineRun{Number: 9}}
		command := &RunPipelineCommand{Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(MatchError(ContainSubstring("instance_ref")))
		Expect(client.calls).To(Equal(1))
	})

	It("returns a client error unchanged", func() {
		apiErr := errors.New("server says no")
		client := &recordingRunCreator{err: apiErr}
		command := &RunPipelineCommand{Pipeline: flaghelpers.PipelineFlag{Name: "template"}}

		Expect(command.run(client, "https://ci.example", "main", new(bytes.Buffer))).To(MatchError(apiErr))
	})
})
