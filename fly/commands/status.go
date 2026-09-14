package commands

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/go-concourse/concourse"
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
		if errors.Is(err, concourse.ErrUnauthorized) {
			displayhelpers.FailWithErrorf("please login again.\n\ntoken validation failed with error", err)
			return nil
		}
		displayhelpers.FailWithErrorf("could not verify login status", err)
		return nil
	}

	fmt.Println("logged in successfully")
	return nil
}
