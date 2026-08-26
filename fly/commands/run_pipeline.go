package commands

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
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

	// The run number is the identifier the user works with everywhere else --
	// `fly runs`, the runs table, the run URL -- so name it here instead of
	// leaving it to be dug out of the payload pipeline's query string.
	if _, err := fmt.Fprintf(output, "started %s run #%d (%s)\n", command.Pipeline.Name, run.Number, run.Status); err != nil {
		return err
	}

	_, err = fmt.Fprintln(output, payloadURL)
	return err
}

func (command *RunPipelineCommand) validate() error {
	if len(command.Pipeline.InstanceVars) > 0 {
		return fmt.Errorf("cannot run an instanced pipeline")
	}
	if _, err := command.Pipeline.Validate(); err != nil {
		return err
	}

	for _, pair := range command.Vars {
		if err := validateParameterName("--var", pair.Ref); err != nil {
			return err
		}
	}
	for _, pair := range command.JSONVars {
		if err := validateParameterName("--json-var", pair.Ref); err != nil {
			return err
		}
	}

	return nil
}

// validateParameterName refuses a flag name that fly would otherwise turn
// silently into something no template parameter can be. vars.ParseReference
// splits "a.b" into path "a" with field "b" (vars/variables.go:26), and
// vars.KVPairs.Expand then nests the value under a parameter named "a"
// (vars/static_vars.go:107), so the server is asked about a parameter the user
// never named and answers "unknown parameter a" or "parameter a must be a
// string" instead of naming the real rule.
//
// A declared parameter name has exactly one grammar, and it is the server's:
// configvalidate.ParamNamePattern.
//
// The check lives here rather than in flaghelpers.VariablePairFlag because
// that flag type is shared with set-pipeline, validate-pipeline and execute,
// where a dotted name is a legitimate nested template var.
func validateParameterName(flag string, ref vars.Reference) error {
	if ref.Source != "" || len(ref.Fields) > 0 || !configvalidate.ParamNamePattern.MatchString(ref.Path) {
		return fmt.Errorf("%s parameter name %s must match %s", flag, ref, configvalidate.ParamNamePattern.String())
	}
	return nil
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
