package commands

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/fatih/color"
)

type TargetsCommand struct{}

func (command *TargetsCommand) Execute([]string) error {
	targets, err := rc.LoadTargets()
	if err != nil {
		return err
	}

	table := ui.Table{
		Headers: ui.TableRow{
			{Contents: "name", Color: color.New(color.Bold)},
			{Contents: "url", Color: color.New(color.Bold)},
			{Contents: "team", Color: color.New(color.Bold)},
			{Contents: "expiry", Color: color.New(color.Bold)},
		},
	}

	for targetName, targetValues := range targets {
		expirationTime := getExpirationFromString(targetName, targetValues.Token)

		row := ui.TableRow{
			{Contents: string(targetName)},
			{Contents: targetValues.API},
			{Contents: targetValues.TeamName},
			{Contents: expirationTime},
		}

		table.Data = append(table.Data, row)
	}

	sort.Sort(table.Data)

	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

func getExpirationFromString(targetName rc.TargetName, ttoken *rc.TargetToken) string {
	if ttoken == nil || ttoken.Type == "" || ttoken.Value == "" {
		return "n/a"
	}

	expiry, err := token.Factory{}.ParseExpiry(ttoken.Value)
	if err != nil {
		return fmt.Sprintf("expiry unavailable (run fly -t %s status)", shellQuoteTargetAlias(targetName))
	}

	return expiry.UTC().Format(time.RFC1123)
}
