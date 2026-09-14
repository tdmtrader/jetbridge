package commands

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/fly/rc"
)

type LogoutCommand struct {
	All bool `short:"a" long:"all" description:"Logout of all targets"`
}

func (command *LogoutCommand) Execute(args []string) error {
	if Fly.Target != "" && !command.All {
		return command.logoutSingleTarget(Fly.Target)
	} else if Fly.Target == "" && command.All {

		targets, err := rc.LoadTargets()
		if err != nil {
			return err
		}

		errs := []error{}
		for targetName := range targets {
			if err := command.logoutSingleTarget(targetName); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", string(targetName), err))
			}
		}

		if len(errs) > 0 {
			return errors.Join(errs...)
		}

		fmt.Println("logged out of all targets")
	} else {
		return errors.New("must specify either --target or --all")
	}

	return nil
}

func (cmd *LogoutCommand) logoutSingleTarget(targetName rc.TargetName) error {
	if err := rc.LogoutTargetRemotely(targetName); err != nil {
		return fmt.Errorf("target %q: %w", targetName, err)
	}

	fmt.Printf("logged out of target: %s\n", targetName)
	return nil
}
