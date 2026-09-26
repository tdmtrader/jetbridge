package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"sigs.k8s.io/yaml"
)

// The installed template is what the adapter talks to: its input and result
// names must be the adapter's, and its result producer must run exactly the
// pinned worker image, or the platform refuses the credential handoff.
func TestInstalledTemplateMatchesTheWorkload(t *testing.T) {
	const pinned = "registry.example/review-worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "implement-template.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer("((review_worker_image))", pinned, "((implement_model))", "some-model").Replace(string(data))
	if strings.Contains(strings.ReplaceAll(text, "((run_id))", ""), "((") {
		t.Fatal("the template has an operator variable this test does not know")
	}
	var config atc.Config
	if err := yaml.Unmarshal([]byte(text), &config); err != nil {
		t.Fatal(err)
	}
	if !config.Template {
		t.Fatal("implement template is not a template")
	}
	declarations, err := atc.RunTaskDeclarations(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 1 {
		t.Fatalf("expected one result producer, got %+v", declarations)
	}
	author := declarations[0]
	if author.Result == nil || author.Result.Name != ChangeResult || len(author.Inputs) != 1 || author.Inputs[0].Name != SnapshotInput || author.Inputs[0].Input != "source" {
		t.Fatalf("template does not take %q and publish %q: %+v", SnapshotInput, ChangeResult, author)
	}
	if got := atc.RunTaskImage(config, author.TaskID); got != "docker:///"+pinned {
		t.Fatalf("the change producer runs %q, not the pinned worker image", got)
	}
	task := config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	script := strings.Join(task.Config.Run.Args, "\n")
	for _, want := range []string{"jb-review-worker implement", "--input source/snapshot", "--auth-socket /dev/shm/jb-review/auth.sock", "some-model", "--run-id=((run_id))", "change/report/change.patch change/report/summary.json change/"} {
		if !strings.Contains(script, want) {
			t.Fatalf("author task does not run %q:\n%s", want, script)
		}
	}
}
