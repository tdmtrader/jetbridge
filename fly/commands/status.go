package commands

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
)

type StatusCommand struct{}

func (c *StatusCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	tToken := target.Token()

	if tToken == nil || tToken.Value == "" {
		displayhelpers.Failf("logged out")
		return nil
	}

	_, err = target.Client().UserInfo()
	if err != nil {
		displayhelpers.FailWithErrorf("please login again.\n\ntoken validation failed with error", err)
		return nil
	}

	if creds, credErr := target.Client().AgentUserCredentialStatus(); credErr == nil {
		for _, cred := range creds {
			if cred.ExpiresAt == 0 {
				continue
			}
			until := time.Until(time.Unix(cred.ExpiresAt, 0))
			if until < 0 {
				fmt.Printf("WARNING: your agent %s credential expired %d days ago — run `fly -t %s agent auth` to refresh\n",
					cred.Kind, int((-until).Hours()/24), Fly.Target)
			} else if until < 30*24*time.Hour {
				fmt.Printf("WARNING: your agent %s credential expires in %d days — run `fly -t %s agent auth` to refresh\n",
					cred.Kind, int(until.Hours()/24), Fly.Target)
			}
		}
	}

	fmt.Println("logged in successfully")
	return nil
}
