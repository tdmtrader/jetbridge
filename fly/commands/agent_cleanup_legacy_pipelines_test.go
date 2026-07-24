package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse/concoursefakes"
)

// A representative, deliberately-shuffled mixed listing. Only unarchived
// pipelines whose BASE name matches ^agent-ticket-[1-9][0-9]*$ are legacy
// targets: instances of a ticket template (same base name) are included;
// agent-workflow-* v3 pipelines, unrelated names, non-numeric/zero suffixes,
// and already-archived pipelines are excluded.
var cleanupMixedPipelines = []atc.Pipeline{
	{Name: "agent-ticket-2"},
	{Name: "agent-workflow-foo"}, // v3 workflow pipeline: never
	{Name: "agent-ticket-10"},
	{Name: "agent-ticket-1", InstanceVars: atc.InstanceVars{"run": 3}}, // instance of a ticket template: yes
	{Name: "some-other-pipeline"},
	{Name: "agent-ticket-0"},                 // zero is not [1-9][0-9]*: never
	{Name: "agent-ticket-foo"},               // non-numeric suffix: never
	{Name: "agent-ticket-7", Archived: true}, // already archived: never
	{Name: "agent-ticket-1"},
}

// sorted by PipelineRef.String()
var cleanupWantOrder = []string{
	"agent-ticket-1",
	"agent-ticket-1/run:3",
	"agent-ticket-10",
	"agent-ticket-2",
}

func cleanupNeverPrinted() []string {
	return []string{
		"agent-workflow-foo", "some-other-pipeline",
		"agent-ticket-0", "agent-ticket-foo", "agent-ticket-7",
	}
}

func newCleanupTeam(name string) *concoursefakes.FakeTeam {
	team := new(concoursefakes.FakeTeam)
	team.NameReturns(name)
	team.ListPipelinesReturns(cleanupMixedPipelines, nil)
	team.ArchivePipelineReturns(true, nil)
	return team
}

func alwaysConfirm(string) (bool, error) { return true, nil }

// assertLexicalOrder asserts every want string is present and that their first
// occurrences appear in the given (already-sorted) order.
func assertLexicalOrder(t *testing.T, output string, want []string) {
	t.Helper()
	prev := -1
	for _, w := range want {
		at := strings.Index(output, w)
		if at < 0 {
			t.Errorf("output missing target %q:\n%s", w, output)
			continue
		}
		if at <= prev {
			t.Errorf("target %q printed out of lexical order:\n%s", w, output)
		}
		prev = at
	}
}

func TestAgentCleanupLegacyPipelines(t *testing.T) {
	t.Run("dry run prints exact targets in lexical order and archives nothing", func(t *testing.T) {
		team := newCleanupTeam(atc.DefaultTeamName)
		var out bytes.Buffer
		if err := cleanupLegacyPipelines(team, false, false, &out, alwaysConfirm); err != nil {
			t.Fatalf("dry run error: %v", err)
		}
		assertLexicalOrder(t, out.String(), cleanupWantOrder)
		for _, never := range cleanupNeverPrinted() {
			if strings.Contains(out.String(), never) {
				t.Errorf("dry run printed non-target %q:\n%s", never, out.String())
			}
		}
		if n := team.ArchivePipelineCallCount(); n != 0 {
			t.Errorf("dry run made %d archive calls, want 0", n)
		}
		if !strings.Contains(out.String(), "--apply") {
			t.Errorf("dry run did not print the --apply rerun instruction:\n%s", out.String())
		}
	})

	t.Run("apply prints the complete list before the first archive call", func(t *testing.T) {
		team := newCleanupTeam(atc.DefaultTeamName)
		var out bytes.Buffer
		// on the FIRST archive call the whole target list must already be printed
		team.ArchivePipelineCalls(func(atc.PipelineRef) (bool, error) {
			if team.ArchivePipelineCallCount() == 1 {
				for _, want := range cleanupWantOrder {
					if !strings.Contains(out.String(), want) {
						t.Errorf("archiving began before %q was printed:\n%s", want, out.String())
					}
				}
			}
			return true, nil
		})
		if err := cleanupLegacyPipelines(team, true, true, &out, alwaysConfirm); err != nil {
			t.Fatalf("apply error: %v", err)
		}
		if n := team.ArchivePipelineCallCount(); n != len(cleanupWantOrder) {
			t.Fatalf("apply made %d archive calls, want %d", n, len(cleanupWantOrder))
		}
		for i, want := range cleanupWantOrder {
			if got := team.ArchivePipelineArgsForCall(i).String(); got != want {
				t.Errorf("archive #%d = %q, want %q", i, got, want)
			}
		}
	})

	t.Run("non-main team fails before listing or mutation", func(t *testing.T) {
		team := newCleanupTeam("some-other-team")
		var out bytes.Buffer
		err := cleanupLegacyPipelines(team, true, true, &out, alwaysConfirm)
		if err == nil {
			t.Fatal("expected an error for a non-main team")
		}
		if !strings.Contains(err.Error(), "main") {
			t.Errorf("error = %q, want it to mention the main-team requirement", err)
		}
		if n := team.ListPipelinesCallCount(); n != 0 {
			t.Errorf("listed pipelines %d times on a non-main team, want 0", n)
		}
		if n := team.ArchivePipelineCallCount(); n != 0 {
			t.Errorf("archived %d pipelines on a non-main team, want 0", n)
		}
	})

	t.Run("apply with a declined confirmation archives nothing", func(t *testing.T) {
		team := newCleanupTeam(atc.DefaultTeamName)
		var out bytes.Buffer
		decline := func(string) (bool, error) { return false, nil }
		if err := cleanupLegacyPipelines(team, true, false, &out, decline); err != nil {
			t.Fatalf("declined apply error: %v", err)
		}
		// the targets are still shown before the prompt
		assertLexicalOrder(t, out.String(), cleanupWantOrder)
		if n := team.ArchivePipelineCallCount(); n != 0 {
			t.Errorf("declined apply archived %d pipelines, want 0", n)
		}
	})
}
