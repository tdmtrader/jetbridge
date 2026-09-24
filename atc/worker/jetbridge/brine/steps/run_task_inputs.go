package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

func RunTaskInputDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{brine.DefineCheck[RunInputAdmission]("its consuming task prepares with {string}", func(in RunInputAdmission, p brine.Params, _ *brine.Recorder) error {
		mode, _ := p.GetString(0)
		if in.Err != nil || in.Run.ID == 0 {
			return fmt.Errorf("named input admission failed: %v", in.Err)
		}
		factory := db.NewPipelineRunFactory(in.Source.Start.DB.Conn, in.Source.Start.DB.LockFactory)
		starter := &runs.ExecutionStarter{Conn: in.Source.Start.DB.Conn, Factory: factory, Epoch: executioncontrol.ActivationEpoch(hangarEpoch)}
		port, ok := any(starter).(interface {
			PrepareTask(context.Context, int, atc.TaskPlan, runtime.ContainerSpec) (runtime.ContainerSpec, error)
		})
		if !ok {
			return fmt.Errorf("the Run starter has no retained task preparation port")
		}
		definition, found, err := factory.Definition(in.Run.ID)
		if err != nil || !found {
			return fmt.Errorf("missing consuming definition: %v", err)
		}
		task := definition.Materialized.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
		plan := atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunInputs: task.RunInputs, RunResult: task.RunResult, Config: task.Config}
		var buildID int
		if err := in.Source.Start.DB.Conn.QueryRow(`SELECT id FROM builds WHERE pipeline_run_id=$1 AND run_job_name=$2`, in.Run.ID, definition.Materialized.Jobs[0].Name).Scan(&buildID); err != nil {
			return err
		}
		spec := runtime.ContainerSpec{TeamID: in.Template.TeamID(), Dir: "/workspace", Type: db.ContainerTypeTask}
		for _, route := range task.RunInputs {
			var input runtime.Input
			data, err := json.Marshal(map[string]any{"RunInput": route.Name, "DestinationPath": filepath.Join(spec.Dir, route.Input)})
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &input); err != nil {
				return err
			}
			spec.Inputs = append(spec.Inputs, input)
		}
		switch mode {
		case "changed route":
			plan.RunInputs[0].Name = "other"
		case "changed input path":
			plan.Config.Inputs[0].Path = "elsewhere"
		case "missing runtime slot":
			spec.Inputs = nil
		case "prebound raw tree":
			ref := in.Source.Candidate.Record.Ref
			spec.Inputs[0].HangarTree = &ref
		case "wrong task":
			plan.TaskID = freshUUID()
		case "wrong team":
			spec.TeamID++
		case "wrong build":
			buildID = in.Source.Start.Creation.EntryBuilds[0].ID()
		case "aborted build":
			_, err = in.Source.Start.DB.Conn.Exec(`UPDATE builds SET aborted=true WHERE id=$1`, buildID)
		case "a Run admission hold":
			// Holding admission stops new Runs, not running ones.
			_, err = db.ReconcilePipelineRunActivation(context.Background(), in.Source.Start.DB.Conn, 0)
		case "a disabled Hangar epoch":
			// Reading a bound input is Hangar work under the input's epoch.
			_, err = in.Source.Start.DB.Conn.Exec(`UPDATE hangar_output_activation_epochs
				SET output_state='disabled', base_state='disabled', revision=revision+1, updated_at=now() WHERE epoch_id=$1`, int64(hangarEpoch))
		case "edited template":
			updated := definition.Template
			updated.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep).Config.Inputs[0].Path = "new-template-path"
			team, found, findErr := in.Source.Start.DB.TeamFactory.FindTeam(in.Template.TeamName())
			if findErr != nil || !found {
				return fmt.Errorf("missing template team: %v", findErr)
			}
			_, _, err = team.SavePipeline(in.Template.PipelineRef(), updated, in.Template.ConfigVersion(), false)
		}
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		got, err := port.PrepareTask(ctx, buildID, plan, spec)
		valid := mode == "retained inputs" || mode == "edited template" || mode == "repeated preparation" || mode == "a Run admission hold"
		if !valid {
			if err == nil {
				return fmt.Errorf("task preparation accepted %s", mode)
			}
			return nil
		}
		if err != nil {
			return err
		}
		if len(got.Inputs) == 0 || len(got.Inputs) != len(task.RunInputs) {
			return fmt.Errorf("task preparation lost input routes")
		}
		for i, input := range got.Inputs {
			if input.HangarTree == nil || *input.HangarTree != in.Source.Candidate.Record.Ref || input.Artifact != nil || input.DestinationPath != spec.Inputs[i].DestinationPath {
				return fmt.Errorf("prepared task did not receive its exact retained input at every slot")
			}
		}
		if mode == "repeated preparation" {
			again, err := port.PrepareTask(ctx, buildID, plan, spec)
			if err != nil || !reflect.DeepEqual(got, again) {
				return fmt.Errorf("repeated preparation changed the inputs: %v", err)
			}
		}
		return nil
	})}
}
