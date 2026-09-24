package commands

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/vars"
)

type pipelineRunCreator interface {
	CreatePipelineRun(string, atc.CreatePipelineRunV2Request) (atc.PipelineRun, error)
}

type RunPipelineCommand struct {
	Pipeline flaghelpers.PipelineFlag           `short:"p" long:"pipeline" required:"true" description:"Name of the template pipeline to run"`
	Vars     []flaghelpers.VariablePairFlag     `short:"v" long:"var" value-name:"NAME=STRING" description:"Set a string pipeline parameter"`
	JSONVars []flaghelpers.JSONVariablePairFlag `long:"json-var" value-name:"NAME=JSON" description:"Set a JSON scalar pipeline parameter"`
	Team     flaghelpers.TeamFlag               `long:"team" description:"Name of the team to which the pipeline belongs, if different from the target default"`
	Key      string                             `long:"invocation-key" value-name:"KEY" description:"Idempotency key for this invocation (1-128 of A-Z a-z 0-9 . _ ~ -). Repeating a command with the same key and variables returns the run it already started instead of starting another. Generated and printed when omitted."`
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
	err := command.validate()
	if err != nil {
		return err
	}

	key := command.Key
	if key == "" {
		key, err = newInvocationKey()
		if err != nil {
			return err
		}
	}

	run, err := client.CreatePipelineRun(command.Pipeline.Name, atc.CreatePipelineRunV2Request{
		InvocationKey: key,
		Vars:          atc.RunParams(command.variables()),
	})
	if err != nil {
		// A refusal is the server's answer and stands on its own. Anything
		// else may have lost a response after the run was admitted, so name
		// the key a retry must present to get that run back.
		var refused concourse.InvalidPipelineRunError
		if errors.As(err, &refused) {
			return err
		}
		return fmt.Errorf("%w (retry with --invocation-key %s to avoid starting a second run)", err, key)
	}
	// The run number and its detail URL are the Run's durable identity -- the
	// identifier the user works with everywhere else (`fly runs`, the runs
	// table) -- and they outlive its payload. A replay of an invocation whose
	// Run has since been reclaimed carries no payload reference, and is still
	// a successful answer.
	verb := "started"
	if run.AdmissionOutcome == atc.RunAdmissionReplayed {
		verb = "already started"
	}
	state := string(run.Status)
	if run.Reclaimed {
		state += ", payload reclaimed"
	}
	if _, err := fmt.Fprintf(output, "%s %s run #%d (%s)\n", verb, command.Pipeline.Name, run.Number, state); err != nil {
		return err
	}

	detailURL, err := pipelineRunURL(targetURL, teamName, command.Pipeline.Name, run.Number)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, detailURL); err != nil {
		return err
	}

	if run.InstanceRef == nil {
		return nil
	}
	payloadURL, err := payloadPipelineURL(targetURL, teamName, *run.InstanceRef)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "payload: "+payloadURL)
	return err
}

// newInvocationKey mints a fresh key for a command given none. It is random
// rather than derived from the variables, so running the same command twice
// on purpose starts two runs; the key is printed on failure so a retry after a
// lost response can present it.
func newInvocationKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "fly." + hex.EncodeToString(raw[:]), nil
}

func (command *RunPipelineCommand) validate() error {
	if len(command.Pipeline.InstanceVars) > 0 {
		return fmt.Errorf("cannot run an instanced pipeline")
	}
	if command.Key != "" && !atc.ValidRunInvocationToken(command.Key) {
		return fmt.Errorf("--invocation-key must be 1-128 characters of A-Z a-z 0-9 . _ ~ -")
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

// pipelineRunURL is the web UI's durable detail route for a Run, the one the
// runs table links: the template (never instanced) and the Run's number.
func pipelineRunURL(targetURL, teamName, templateName string, number int) (string, error) {
	runURL, err := url.Parse(targetURL)
	if err != nil {
		return "", err
	}

	path := strings.TrimSuffix(runURL.Path, "/")
	escapedPath := strings.TrimSuffix(runURL.EscapedPath(), "/")
	suffix := fmt.Sprintf("/runs/%d", number)
	runURL.Path = path + "/teams/" + teamName + "/pipelines/" + templateName + suffix
	runURL.RawPath = escapedPath + "/teams/" + url.PathEscape(teamName) + "/pipelines/" + url.PathEscape(templateName) + suffix

	return runURL.String(), nil
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
