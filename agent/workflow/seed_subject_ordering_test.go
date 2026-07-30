package workflow_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// The sealer requires a record's subjects to be lexicographically sorted by id
// (contracts.ValidateEntityIDs, called from Record.validateEnvelopeShape). That
// gate runs at seal time, i.e. AFTER the agent has spent its whole budget
// slice: a seed prompt that dictates a different order — "declare X as the
// primary subject and Y as context" with Y < X — buys a guaranteed rejection at
// the end of a paid step.
//
// So a shipped prompt may not name subjects out of order, and a prompt that
// declares more than one subject has to state the ordering rule rather than
// leave the agent to infer it from the example.
func TestSeedPromptsDeclareSubjectsInLexicographicOrder(t *testing.T) {
	for _, seed := range seedPromptsUnderTest(t) {
		t.Run(seed.name, func(t *testing.T) {
			for _, prompt := range seed.prompts {
				named := subjectIDsNamedInDeclarationOrder(prompt.text, prompt.names)
				if len(named) == 0 {
					continue
				}

				sorted := append([]string(nil), named...)
				sort.Strings(sorted)
				if !equalStrings(named, sorted) {
					t.Errorf("%s: prompt names subjects in the order %q; the sealer requires %q "+
						"(lexicographic by id) and rejects anything else after the step has already been paid for",
						prompt.label, named, sorted)
				}

				if len(named) > 1 && !strings.Contains(strings.ToLower(prompt.text), "lexicographic") {
					t.Errorf("%s: prompt declares %d subjects but never states the ordering rule; "+
						"an agent that follows the example order for a different subject set gets rejected at seal time",
						prompt.label, len(named))
				}
			}
		})
	}
}

type seedUnderTest struct {
	name    string
	prompts []seedPrompt
}

type seedPrompt struct {
	label string
	text  string
	// names is every artifact name the step may legitimately declare as a
	// subject: its declared inputs plus its declared outputs.
	names []string
}

// seedPromptsUnderTest compiles every shipped seed so prompt_file content is
// inlined the same way the renderer inlines it, then returns one entry per
// agent step.
func seedPromptsUnderTest(t *testing.T) []seedUnderTest {
	t.Helper()

	entries, err := os.ReadDir("seeds")
	if err != nil {
		t.Fatalf("read seeds: %v", err)
	}
	var seeds []seedUnderTest
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directory := filepath.Join("seeds", entry.Name())
		manifest, err := workflow.ManifestFromDir(directory)
		if err != nil {
			t.Fatalf("ManifestFromDir(%q): %v", directory, err)
		}
		if _, isNodePackage := manifest[workflow.NodeFileName]; isNodePackage {
			if _, alsoWorkflow := manifest[workflow.WorkflowFileName]; alsoWorkflow {
				t.Fatalf("%q mixes %s and %s; shipped seed packages must have exactly one manifest kind",
					directory, workflow.NodeFileName, workflow.WorkflowFileName)
			}
			// This enumerator inspects workflow prompts. Reusable-node packages
			// are compiled and checked by their dedicated seed contract test.
			continue
		}
		compiled, err := workflow.CompileDefinition(manifest)
		if err != nil {
			t.Fatalf("CompileDefinition(%q): %v", directory, err)
		}

		seed := seedUnderTest{name: entry.Name()}
		for _, step := range compiled.Function.Plan {
			agentStep, ok := step.Config.(*atc.AgentStep)
			if !ok {
				continue
			}
			if strings.TrimSpace(agentStep.Prompt) == "" {
				continue
			}
			names := append([]string(nil), agentStep.Inputs...)
			names = append(names, agentStep.Outputs...)
			seed.prompts = append(seed.prompts, seedPrompt{
				label: entry.Name() + "/" + agentStep.Name,
				text:  agentStep.Prompt,
				names: names,
			})
		}
		if len(seed.prompts) == 0 {
			continue
		}
		seeds = append(seeds, seed)
	}
	if len(seeds) == 0 {
		t.Fatal("no seed prompts found; this test would pass vacuously")
	}
	return seeds
}

// subjectIDsNamedInDeclarationOrder returns the artifact names a prompt names
// inside its subject-declaring sentences, in the order the prompt names them.
// Only sentences that actually talk about subjects are considered, so ordinary
// narrative mentions of an input do not count.
func subjectIDsNamedInDeclarationOrder(prompt string, names []string) []string {
	var ordered []string
	seen := make(map[string]struct{}, len(names))
	for _, sentence := range splitSentences(prompt) {
		if !strings.Contains(strings.ToLower(sentence), "subject") {
			continue
		}
		type hit struct {
			at   int
			name string
		}
		var hits []hit
		for _, name := range names {
			if at := nameMentionIndex(sentence, name); at >= 0 {
				hits = append(hits, hit{at: at, name: name})
			}
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
		for _, h := range hits {
			if _, found := seen[h.name]; found {
				continue
			}
			seen[h.name] = struct{}{}
			ordered = append(ordered, h.name)
		}
	}
	return ordered
}

func splitSentences(text string) []string {
	normalized := strings.ReplaceAll(text, "\n", " ")
	return strings.Split(normalized, ". ")
}

// nameMentionIndex finds the artifact name as a whole token. The identifier
// alphabet matches contracts.ValidateIdentifier, so "repository" does not match
// inside "repository-change/v1".
func nameMentionIndex(sentence, name string) int {
	pattern := regexp.MustCompile(`(^|[^A-Za-z0-9._:/-])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9._:/-]|$)`)
	match := pattern.FindStringSubmatchIndex(sentence)
	if match == nil {
		return -1
	}
	return match[0] + (match[3] - match[2])
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
