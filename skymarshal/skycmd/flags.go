package skycmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/flag/v2"
)

type AuthFlags struct {
	SecureCookies               bool              `long:"cookie-secure" description:"Mark cookies Secure even when --external-url is http (a TLS-terminating proxy in front). An https --external-url always does."`
	Expiration                  time.Duration     `long:"auth-duration" default:"24h" description:"Lifetime of an access token. Renewable logins refresh automatically after expiry."`
	RefreshTokenIdleTimeout     time.Duration     `long:"auth-refresh-idle-timeout" default:"720h" description:"Maximum time a renewable login may remain unused (default 30 days)."`
	RefreshTokenAbsoluteTimeout time.Duration     `long:"auth-refresh-absolute-timeout" default:"2160h" description:"Maximum renewable login lifetime before another login (default 90 days)."`
	RefreshTokenReuseInterval   time.Duration     `long:"auth-refresh-reuse-interval" default:"5s" description:"Brief recovery window for a retried refresh exchange."`
	SigningKey                  *flag.PrivateKey  `long:"session-signing-key" required:"true" description:"File containing an RSA private key, used to sign auth tokens."`
	PasswordConnector           string            `long:"password-connector" default:"local" choice:"local" description:"Connector to use when authenticating via 'fly login -u ... -p ...'"`
	LocalUsers                  map[string]string `long:"add-local-user" description:"List of username:password combinations for all your local users. The password can be bcrypted - if so, it must have a minimum cost of 10." value-name:"USERNAME:PASSWORD"`
	Clients                     map[string]string `long:"add-client" description:"List of client_id:client_secret combinations" value-name:"CLIENT_ID:CLIENT_SECRET"`
}

type AuthTeamFlags struct {
	LocalUsers []string  `long:"local-user" description:"A whitelisted local concourse user. These are the users you've added at web startup with the --add-local-user flag." value-name:"USERNAME"`
	Config     flag.File `short:"c" long:"config" description:"Configuration file for specifying team params"`
}

func (flag *AuthTeamFlags) Format() (atc.TeamAuth, error) {

	if path := flag.Config.Path(); path != "" {
		return flag.formatFromFile()

	}
	return flag.formatFromFlags()

}

// Team files accept only local membership; reject other keys before returning
// any auth so a mixed file cannot silently remove existing team members.
func (flag *AuthTeamFlags) formatFromFile() (atc.TeamAuth, error) {
	content, err := os.ReadFile(flag.Config.Path())
	if err != nil {
		return nil, err
	}
	var data struct {
		Roles []map[string]any `json:"roles"`
	}
	if err := yaml.UnmarshalStrict(content, &data); err != nil {
		return nil, err
	}
	auth := atc.TeamAuth{}
	for _, role := range data.Roles {
		roleName, ok := role["name"].(string)
		if !ok || roleName == "" {
			return nil, fmt.Errorf("team role must have a name")
		}
		for key := range role {
			if key != "name" && key != "local" {
				return nil, fmt.Errorf("unsupported connector %q in team role %q: only local authentication is supported", key, roleName)
			}
		}
		// Decode the local section strictly too, so misspelled membership keys
		// cannot turn an intended update into a partial one.
		local := struct {
			Users []string `json:"users"`
		}{}
		if config, exists := role["local"]; exists {
			encoded, err := yaml.Marshal(config)
			if err != nil {
				return nil, err
			}
			if err := yaml.UnmarshalStrict(encoded, &local); err != nil {
				return nil, err
			}
		}
		users := []string{}
		for _, user := range local.Users {
			if user != "" {
				users = append(users, "local:"+strings.ToLower(user))
			}
		}
		if len(users) == 0 {
			continue
		}
		auth[roleName] = map[string][]string{"users": users, "groups": {}}
	}
	if err := auth.Validate(); err != nil {
		return nil, err
	}
	return auth, nil
}

func (flag *AuthTeamFlags) formatFromFlags() (atc.TeamAuth, error) {

	users := []string{}

	for _, user := range flag.LocalUsers {
		if user != "" {
			users = append(users, "local:"+strings.ToLower(user))
		}
	}

	if len(users) == 0 {
		return nil, atc.ErrAuthConfigInvalid
	}

	return atc.TeamAuth{
		"owner": map[string][]string{
			"users":  users,
			"groups": {},
		},
	}, nil
}

type skyDisplayUserIdGenerator struct {
	mapConnectorUserid map[string]string
}

func NewSkyDisplayUserIdGenerator(config map[string]string) (atc.DisplayUserIdGenerator, error) {
	generator := &skyDisplayUserIdGenerator{
		mapConnectorUserid: map[string]string{},
	}

	for connectorId, fieldName := range config {
		if connectorId != "local" {
			return nil, fmt.Errorf("invalid connector: %s", connectorId)
		}

		switch fieldName {
		case "user_id", "name", "username", "email":
		default:
			return nil, fmt.Errorf("invalid user field %s of connector %s", fieldName, connectorId)
		}

		generator.mapConnectorUserid[connectorId] = fieldName
	}
	return generator, nil
}

func (g *skyDisplayUserIdGenerator) DisplayUserId(connector, userid, username, preferredUsername, email string) string {
	if fieldName, ok := g.mapConnectorUserid[connector]; ok {
		switch fieldName {
		case "user_id":
			return userid
		case "name":
			return username
		case "username":
			return preferredUsername
		case "email":
			return email
		}
	}

	// For unconfigured connector, applies a default rule.
	if preferredUsername != "" {
		return preferredUsername
	}
	if userid != "" {
		return userid
	}
	return username
}
