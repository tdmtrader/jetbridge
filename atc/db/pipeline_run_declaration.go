package db

import (
	"fmt"
	"sort"

	"github.com/concourse/concourse/atc"
)

func saveRunTaskIdentities(tx Tx, templateID int, declarations []atc.RunTaskDeclaration) error {
	// Concurrent template edits acquiring more than one global identity use the
	// same order. A rejected edit rolls back the template and ownership together.
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].TaskID < declarations[j].TaskID })
	for _, declaration := range declarations {
		var owner int
		err := tx.QueryRow(`INSERT INTO pipeline_template_task_identities (task_id, template_pipeline_id)
			VALUES ($1, $2) ON CONFLICT (task_id) DO UPDATE SET task_id = EXCLUDED.task_id
			RETURNING template_pipeline_id`, declaration.TaskID, templateID).Scan(&owner)
		if err != nil {
			return err
		}
		if owner != templateID {
			return fmt.Errorf("task_id %s is already owned by another template", declaration.TaskID)
		}
	}
	return nil
}
