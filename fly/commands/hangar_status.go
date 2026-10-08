package commands

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

// HangarStatusCommand reports the Hangar output plane's drain state, and
// resolves an integrity finding by id.
//
// The drain runbook (docs/hangar.md): turn hangarOutput.webEnabled off, run
// this until it reports drained, then remove the node daemons' output plane.
type HangarStatusCommand struct {
	ResolveFinding int64 `long:"resolve-finding" value-name:"ID" description:"Resolve the open integrity finding with this id, once its cause is repaired"`
	Json           bool  `long:"json" description:"Print command result as JSON"`
}

func (command *HangarStatusCommand) Execute([]string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	if command.ResolveFinding != 0 {
		if command.ResolveFinding < 0 {
			return fmt.Errorf("a finding id is a positive integer")
		}
		if err := target.Client().ResolveHangarFinding(command.ResolveFinding); err != nil {
			return fmt.Errorf("resolving finding %d: %w", command.ResolveFinding, err)
		}
		fmt.Printf("resolved integrity finding %d\n", command.ResolveFinding)
		return nil
	}

	status, err := target.Client().HangarStatus()
	if err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(status)
	}

	state := "in service"
	switch {
	case !status.Enabled && status.Drained:
		state = "out of service, drained"
	case !status.Enabled:
		state = "out of service, draining"
	}
	fmt.Printf("hangar output plane: %s\n", state)
	if status.AtRisk {
		fmt.Println("at risk: open integrity findings block new work until resolved")
	}
	fmt.Println()

	residue := ui.Table{Headers: ui.TableRow{
		{Contents: "residue", Color: color.New(color.Bold)},
		{Contents: "count", Color: color.New(color.Bold)},
	}}
	for _, row := range []struct {
		name  string
		count int
	}{
		{"pending captures", status.Residue.PendingCaptures},
		{"publishing captures", status.Residue.PublishingCaptures},
		{"unreleased captures", status.Residue.UnreleasedCaptures},
		{"open claims", status.Residue.OpenClaims},
		{"total", status.Residue.Total},
		{"captures released without node acknowledgement", status.Residue.UnacknowledgedReleases},
		{"open integrity findings", len(status.Findings)},
		{"live generations", status.LiveGenerations},
	} {
		residue.Data = append(residue.Data, ui.TableRow{
			{Contents: row.name},
			{Contents: strconv.Itoa(row.count)},
		})
	}
	if err := residue.Render(os.Stdout, Fly.PrintTableHeaders); err != nil {
		return err
	}

	if len(status.Findings) == 0 {
		return nil
	}
	fmt.Println()

	findings := ui.Table{Headers: ui.TableRow{
		{Contents: "id", Color: color.New(color.Bold)},
		{Contents: "violation", Color: color.New(color.Bold)},
		{Contents: "subject", Color: color.New(color.Bold)},
		{Contents: "observed", Color: color.New(color.Bold)},
		{Contents: "blocks admission", Color: color.New(color.Bold)},
	}}
	for _, finding := range status.Findings {
		findings.Data = append(findings.Data, ui.TableRow{
			{Contents: strconv.FormatInt(finding.ID, 10)},
			{Contents: finding.Violation},
			{Contents: finding.Subject},
			{Contents: time.Unix(finding.ObservedAt, 0).UTC().Format(time.RFC3339)},
			{Contents: strconv.FormatBool(finding.BlocksAdmission)},
		})
	}

	return findings.Render(os.Stdout, Fly.PrintTableHeaders)
}
