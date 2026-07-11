package commands

import (
	"fmt"

	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
)

type RunPipelineCommand struct {
	Pipeline flaghelpers.PipelineFlag           `short:"p" long:"pipeline" required:"true" description:"Template pipeline to run"`
	Var      []flaghelpers.VariablePairFlag     `short:"v" long:"var"      unquote:"false" value-name:"[NAME=STRING]" description:"Param value (string)"`
	YAMLVar  []flaghelpers.YAMLVariablePairFlag `short:"y" long:"yaml-var" unquote:"false" value-name:"[NAME=YAML]"   description:"Param value (typed YAML)"`
	Team     flaghelpers.TeamFlag               `long:"team" description:"Name of the team, if different from the target default"`
}

func (command *RunPipelineCommand) Validate() error {
	_, err := command.Pipeline.Validate()
	return err
}

func (command *RunPipelineCommand) Execute(args []string) error {
	err := command.Validate()
	if err != nil {
		return err
	}

	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	err = target.Validate()
	if err != nil {
		return err
	}

	team, err := command.Team.LoadTeam(target)
	if err != nil {
		return err
	}

	params := map[string]any{}
	for _, pair := range command.Var {
		params[pair.Ref.Path] = pair.Value
	}
	for _, pair := range command.YAMLVar {
		params[pair.Ref.Path] = pair.Value
	}

	ref := command.Pipeline.Ref()
	run, err := team.CreatePipelineRun(ref, params)
	if err != nil {
		return err
	}

	fmt.Printf("created run %s#%d\n", ref.Name, run.Number)
	fmt.Printf("watch it at %s/teams/%s/pipelines/%s?vars.run=%d\n",
		target.URL(), team.Name(), ref.Name, run.Number)

	return nil
}
