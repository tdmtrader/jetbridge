package skycmd

import (
	"encoding/json"

	"github.com/concourse/dex/connector/bitbucketcloud"
)

func init() {
	RegisterConnector(&Connector{
		id:         "bitbucket-cloud",
		config:     &BitbucketCloudFlags{},
		teamConfig: &BitbucketCloudTeamFlags{},
	})
}

type BitbucketCloudFlags struct {
	ClientID     string `long:"client-id" description:"(Required) Client id"`
	ClientSecret string `long:"client-secret" description:"(Required) Client secret"`
}

func (flag *BitbucketCloudFlags) Name() string {
	return "Bitbucket Cloud"
}

func (flag *BitbucketCloudFlags) Validate() error {
	return validateClientCredentials(flag.ClientID, flag.ClientSecret)
}

func (flag *BitbucketCloudFlags) Serialize(redirectURI string) ([]byte, error) {
	if err := flag.Validate(); err != nil {
		return nil, err
	}

	return json.Marshal(bitbucketcloud.Config{
		ClientID:          flag.ClientID,
		ClientSecret:      flag.ClientSecret,
		RedirectURI:       redirectURI,
		IncludeTeamGroups: true,
	})
}

type BitbucketCloudTeamFlags struct {
	Users []string `long:"user" description:"A whitelisted Bitbucket Cloud user" value-name:"USERNAME"`
	Teams []string `long:"team" description:"A whitelisted Bitbucket Cloud team" value-name:"TEAM_NAME"`
}

func (flag *BitbucketCloudTeamFlags) GetUsers() []string {
	return flag.Users
}

func (flag *BitbucketCloudTeamFlags) GetGroups() []string {
	return flag.Teams
}
