package workflow_test

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func TestOnlyVersionThreeEngineeringSeedsRemain(t *testing.T) {
	entries, err := os.ReadDir("seeds")
	if err != nil {
		t.Fatalf("read seeds: %v", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
		if !entry.IsDir() {
			t.Errorf("seed root entry %q is not a directory", entry.Name())
		}
	}
	sort.Strings(names)

	want := []string{
		"anonymization-audit-v3",
		"code-review-v3",
		"log-diagnosis-v3",
		"merge-delivery-v3",
		"small-fix-v3",
		"version-upgrade-v3",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("seed root entries = %q, want %q", names, want)
	}
}

func TestVersionThreeEngineeringSeedsCompileAndRender(t *testing.T) {
	tests := []struct {
		directory         string
		name              string
		inputs            []workflow.SignaturePort
		outputs           []workflow.SignaturePort
		dispositionOutput string
		humanWait         bool
		publisher         bool
	}{
		{
			directory: "seeds/code-review-v3",
			name:      "code-review",
			inputs: []workflow.SignaturePort{
				{Name: "before", Type: snapshot.TypeRef("repository/v1")},
				{Name: "after", Type: snapshot.TypeRef("repository/v1")},
			},
			outputs:           []workflow.SignaturePort{{Name: "review", Type: snapshot.TypeRef("review/v1")}},
			dispositionOutput: "review",
		},
		{
			directory: "seeds/small-fix-v3",
			name:      "small-fix",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "work-item", Type: snapshot.TypeRef("work-item/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "report", Type: snapshot.TypeRef("opaque/v1")},
			},
			dispositionOutput: "change",
			humanWait:         true,
		},
		{
			directory: "seeds/version-upgrade-v3",
			name:      "version-upgrade",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "request", Type: snapshot.TypeRef("upgrade-request/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "report", Type: snapshot.TypeRef("upgrade-report/v1")},
			},
			dispositionOutput: "change",
			humanWait:         true,
		},
		{
			directory: "seeds/merge-delivery-v3",
			name:      "merge-delivery",
			inputs: []workflow.SignaturePort{
				{Name: "base", Type: snapshot.TypeRef("repository/v1")},
				{Name: "candidate", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "target", Type: snapshot.TypeRef("repository/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "merged-change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "merge-report", Type: snapshot.TypeRef("validation-report/v1")},
			},
			dispositionOutput: "merged-change",
			humanWait:         true,
			publisher:         true,
		},
		{
			directory: "seeds/anonymization-audit-v3",
			name:      "anonymization-audit",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "database", Type: snapshot.TypeRef("database-snapshot/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "findings", Type: snapshot.TypeRef("audit-findings/v1")},
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1"), Optional: true},
			},
		},
		{
			directory: "seeds/log-diagnosis-v3",
			name:      "log-diagnosis",
			inputs: []workflow.SignaturePort{
				{Name: "logs", Type: snapshot.TypeRef("log-bundle/v1")},
				{Name: "deployment", Type: snapshot.TypeRef("deployment-snapshot/v1"), Optional: true},
			},
			outputs: []workflow.SignaturePort{
				{Name: "diagnosis", Type: snapshot.TypeRef("diagnosis/v1")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := workflow.ManifestFromDir(test.directory)
			if err != nil {
				t.Fatalf("ManifestFromDir: %v", err)
			}
			definition, err := workflow.NewMemoryStore().ImportManifest(test.name, manifest, "seed-test")
			if err != nil {
				t.Fatalf("compile/import seed: %v", err)
			}
			if definition.SchemaVersion != 3 || definition.SignatureVersion != 1 {
				t.Fatalf("version identity = schema %d signature %d", definition.SchemaVersion, definition.SignatureVersion)
			}
			signature, err := definition.Compiled.PublicSignature()
			if err != nil {
				t.Fatalf("PublicSignature: %v", err)
			}
			if !reflect.DeepEqual(signature.Inputs, test.inputs) || !reflect.DeepEqual(signature.Outputs, test.outputs) {
				t.Fatalf("signature = %+v, want inputs=%+v outputs=%+v", signature, test.inputs, test.outputs)
			}
			if definition.Compiled.Function.DispositionOutput != test.dispositionOutput {
				t.Fatalf("disposition_output = %q, want %q", definition.Compiled.Function.DispositionOutput, test.dispositionOutput)
			}

			target, err := workflow.FullFunctionTarget(*definition)
			if err != nil {
				t.Fatalf("FullFunctionTarget: %v", err)
			}
			rendered, err := workflow.RenderFunction(target)
			if err != nil {
				t.Fatalf("RenderFunction: %v", err)
			}
			if len(rendered.Config.Jobs) != 1 || len(rendered.Config.Jobs[0].PlanSequence) <= len(test.inputs) {
				t.Fatalf("rendered plan omitted the authored DAG: %+v", rendered.Config.Jobs)
			}
			var waits, publishers int
			unsupported := func(kind string) error {
				return fmt.Errorf("version-3 seed rendered unsupported visible %s step", kind)
			}
			recursor := atc.StepRecursor{
				OnTask:            func(*atc.TaskStep) error { return nil },
				OnAgent:           func(*atc.AgentStep) error { return nil },
				OnLoadSnapshot:    func(*atc.LoadSnapshotStep) error { return nil },
				OnAwaitSnapshot:   func(*atc.AwaitSnapshotStep) error { waits++; return nil },
				OnPublishSnapshot: func(*atc.PublishSnapshotStep) error { publishers++; return nil },
				OnGet:             func(*atc.GetStep) error { return unsupported("get") },
				OnPut:             func(*atc.PutStep) error { return unsupported("put") },
				OnRun:             func(*atc.RunStep) error { return unsupported("run") },
				OnSetPipeline:     func(*atc.SetPipelineStep) error { return unsupported("set_pipeline") },
				OnLoadVar:         func(*atc.LoadVarStep) error { return unsupported("load_var") },
			}
			for _, step := range rendered.Config.Jobs[0].PlanSequence {
				if err := step.Config.Visit(recursor); err != nil {
					t.Fatalf("inspect rendered plan: %v", err)
				}
			}
			if (waits > 0) != test.humanWait || (publishers > 0) != test.publisher {
				t.Fatalf("visible boundaries: waits=%d publishers=%d, want wait=%t publisher=%t", waits, publishers, test.humanWait, test.publisher)
			}
			if strings.Contains(string(manifest["workflow.yml"]), "ticket_id") || strings.Contains(string(manifest["workflow.yml"]), "workspace") {
				t.Fatal("version-3 seed is coupled to the legacy ticket/workspace model")
			}
		})
	}
}
