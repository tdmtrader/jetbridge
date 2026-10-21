package commands

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/fly/rc"
)

type EditTargetCommand struct {
	NewName rc.TargetName `long:"target-name" description:"Update target name"`
	Url     string        `short:"u" long:"concourse-url" description:"Update concourse URL"`
	Team    string        `short:"n" long:"team-name" description:"Update team name"`
}

func (command *EditTargetCommand) Execute([]string) error {
	_, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	if command.NewName == "" && command.Url == "" && command.Team == "" {
		return errors.New("error: no attributes specified to update")
	}

	// Rename first so a refused rename leaves the target untouched.
	targetName := Fly.Target
	if command.NewName != "" {
		err = rc.UpdateTargetName(Fly.Target, command.NewName)
		if err != nil {
			return err
		}
		targetName = command.NewName
	}

	targetProps := rc.TargetProps{}
	targetProps.API = command.Url
	targetProps.TeamName = command.Team

	if command.Url != "" || command.Team != "" {
		err = rc.UpdateTargetProps(targetName, targetProps)
		if err != nil {
			return err
		}
	}

	fmt.Println("Updated target: " + Fly.Target)

	return nil
}
