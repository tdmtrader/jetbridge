package commands

import (
	"github.com/go-jose/go-jose/v4"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/commands/internal/templatehelpers"
	"github.com/concourse/concourse/fly/commands/internal/validatepipelinehelpers"

	// dynamically registered credential managers
	_ "github.com/concourse/concourse/atc/creds/conjur"
	_ "github.com/concourse/concourse/atc/creds/credhub"
	_ "github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/creds/idtoken"
	_ "github.com/concourse/concourse/atc/creds/kubernetes"
	_ "github.com/concourse/concourse/atc/creds/secretsmanager"
	_ "github.com/concourse/concourse/atc/creds/ssm"
	_ "github.com/concourse/concourse/atc/creds/vault"
)

type ValidatePipelineCommand struct {
	Config atc.PathFlag `short:"c" long:"config" required:"true"  description:"Pipeline configuration file"`
	Strict bool         `short:"s" long:"strict"                  description:"Fail on warnings"`
	Output bool         `short:"o" long:"output"                  description:"Output templated pipeline to stdout"`

	Var     []flaghelpers.VariablePairFlag     `short:"v"  long:"var"       unquote:"false"  value-name:"[NAME=STRING]"  description:"Specify a string value to set for a variable in the pipeline"`
	YAMLVar []flaghelpers.YAMLVariablePairFlag `short:"y"  long:"yaml-var"  unquote:"false"  value-name:"[NAME=YAML]"    description:"Specify a YAML value to set for a variable in the pipeline"`

	VarsFrom []atc.PathFlag `short:"l"  long:"load-vars-from"  description:"Variable flag that can be used for filling in template values in configuration from a YAML file"`
}

func (command *ValidatePipelineCommand) Execute(args []string) error {

	// the idtoken credential manager needs to have an issuer and a signingKeyFactory configured to work (and without it validation of pipelines using it would fail)
	idtoken.UpdateGlobalManagerFactory(func(f *idtoken.ManagerFactory) {
		f.SetIssuer("http://localhost")
	})
	idtoken.UpdateGlobalManagerFactory(func(f *idtoken.ManagerFactory) {
		f.SetSigningKeyFactory(noSigningKeys{})
	})

	yamlTemplate := templatehelpers.NewYamlTemplateWithParams(command.Config, command.VarsFrom, command.Var, command.YAMLVar, nil)
	return validatepipelinehelpers.Validate(yamlTemplate, command.Strict, command.Output)
}

// noSigningKeys satisfies db.SigningKeyFactory for `fly validate-pipeline`,
// which constructs the idtoken credential manager purely so a pipeline that
// references it can be validated -- nothing is ever signed, so no key is ever
// needed.
//
// This used to be dbfakes.FakeSigningKeyFactory. A counterfeiter double is
// generated test scaffolding, and importing it here linked the whole dbfakes
// package into the shipped fly binary. The behaviour is identical: the fake
// returned zero values and nil errors too.
type noSigningKeys struct{}

func (noSigningKeys) CreateKey(jose.JSONWebKey) error { return nil }

func (noSigningKeys) GetAllKeys() ([]db.SigningKey, error) { return nil, nil }

func (noSigningKeys) GetNewestKey(db.SigningKeyType) (db.SigningKey, error) { return nil, nil }
