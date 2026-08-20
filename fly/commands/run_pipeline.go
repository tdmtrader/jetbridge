package commands

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/vars"
)

type pipelineRunCreator interface {
	CreatePipelineRun(string, map[string]any) (atc.PipelineRun, error)
}

type RunPipelineCommand struct {
	Pipeline flaghelpers.PipelineFlag           `short:"p" long:"pipeline" required:"true" description:"Name of the template pipeline to run"`
	Vars     []flaghelpers.VariablePairFlag     `short:"v" long:"var" value-name:"NAME=STRING" description:"Set a string pipeline parameter"`
	JSONVars []flaghelpers.JSONVariablePairFlag `long:"json-var" value-name:"NAME=JSON" description:"Set a JSON scalar pipeline parameter"`
	Team     flaghelpers.TeamFlag               `long:"team" description:"Name of the team to which the pipeline belongs, if different from the target default"`
}

func (command *RunPipelineCommand) Execute([]string) error {
	if err := command.validate(); err != nil {
		return err
	}

	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	team, err := command.Team.LoadTeam(target)
	if err != nil {
		return err
	}

	return command.run(team, target.URL(), team.Name(), os.Stdout)
}

func (command *RunPipelineCommand) run(client pipelineRunCreator, targetURL, teamName string, output io.Writer) error {
	if err := command.validate(); err != nil {
		return err
	}

	run, err := client.CreatePipelineRun(command.Pipeline.Name, command.variables())
	if err != nil {
		return err
	}
	if run.InstanceRef == nil {
		return fmt.Errorf("create pipeline run response missing instance_ref")
	}

	payloadURL, err := payloadPipelineURL(targetURL, teamName, *run.InstanceRef)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, payloadURL)
	return err
}

func (command *RunPipelineCommand) validate() error {
	if len(command.Pipeline.InstanceVars) > 0 {
		return fmt.Errorf("cannot run an instanced pipeline")
	}
	_, err := command.Pipeline.Validate()
	return err
}

func (command *RunPipelineCommand) variables() map[string]any {
	pairs := make(vars.KVPairs, 0, len(command.Vars)+len(command.JSONVars))
	for _, pair := range command.Vars {
		pairs = append(pairs, vars.KVPair(pair))
	}
	for _, pair := range command.JSONVars {
		pairs = append(pairs, vars.KVPair(pair))
	}
	return pairs.Expand()
}

func payloadPipelineURL(targetURL, teamName string, identifier atc.PipelineIdentifier) (string, error) {
	payloadURL, err := url.Parse(targetURL)
	if err != nil {
		return "", err
	}

	path := strings.TrimSuffix(payloadURL.Path, "/")
	escapedPath := strings.TrimSuffix(payloadURL.EscapedPath(), "/")
	payloadURL.Path = path + "/teams/" + teamName + "/pipelines/" + identifier.PipelineName
	payloadURL.RawPath = escapedPath + "/teams/" + url.PathEscape(teamName) + "/pipelines/" + url.PathEscape(identifier.PipelineName)
	payloadURL.RawQuery = atc.PipelineRef{
		Name:         identifier.PipelineName,
		InstanceVars: identifier.InstanceVars,
	}.QueryParams().Encode()

	return payloadURL.String(), nil
}
