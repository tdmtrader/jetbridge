package commands

import (
	"fmt"

	"github.com/concourse/concourse/fly/commands/internal/flaghelpers"
	"github.com/concourse/concourse/fly/rc"
)

type CopyResourceVersionsCommand struct {
	Resource    flaghelpers.ResourceFlag `short:"r" long:"resource" required:"true" value-name:"PIPELINE/RESOURCE" description:"Name of the resource to copy versions into"`
	FromScopeID int                      `long:"from-scope" value-name:"SCOPE_ID" description:"ID of the previous version history (scope) to copy versions from (use without flag to list the resource's previous histories)"`
	Team        flaghelpers.TeamFlag     `long:"team" description:"Name of the team to which the pipeline belongs, if different from the target default"`
}

func (command *CopyResourceVersionsCommand) Execute(args []string) error {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return err
	}

	err = target.Validate()
	if err != nil {
		return err
	}

	team, err := command.Team.LoadTeam(target)
	if err != nil {
		return err
	}

	pipelineRef := command.Resource.PipelineRef
	resourceName := command.Resource.ResourceName

	if command.FromScopeID == 0 {
		// List the version histories the resource used before: deprecated
		// ones, and shared ones it left that are kept.
		scopes, err := team.ListDeprecatedScopes(pipelineRef, resourceName)
		if err != nil {
			return err
		}

		if len(scopes) == 0 {
			fmt.Println("no previous version histories found for this resource")
			return nil
		}

		fmt.Println("previous version histories available to copy:")
		fmt.Println()
		for _, scope := range scopes {
			state := "kept"
			if scope.DeprecatedAt != "" {
				state = "deprecated at: " + scope.DeprecatedAt
			}
			lastChecked := ""
			if scope.LastChecked != "" {
				lastChecked = ", last checked: " + scope.LastChecked
			}
			fmt.Printf("  scope %d  (%s, %d versions%s, config id: %d)\n", scope.ID, state, scope.Versions, lastChecked, scope.ConfigID)
		}
		fmt.Println()
		fmt.Println("re-run with --from-scope <SCOPE_ID> to copy versions")
		return nil
	}

	// Copy versions from the specified scope
	copied, err := team.CopyResourceVersions(pipelineRef, resourceName, command.FromScopeID)
	if err != nil {
		return err
	}

	fmt.Printf("copied %d versions from scope %d\n", copied, command.FromScopeID)
	return nil
}
