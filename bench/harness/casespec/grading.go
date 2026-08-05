package casespec

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Grading is the mechanical part of a case's `grading:` block — the legs a
// grader can execute without a human reading prose.
//
// The prose in that block is NOT optional context. Several cases carry caveats
// that change the verdict (fix-jb-001's overlay clobbers a deliverable the task
// asked for; its withheld spec pins wording task.md leaves open). Protocol
// carries that text forward so a report can print it instead of pretending the
// structured fields are the whole rubric.
type Grading struct {
	// Protocol is the case's free-text grading protocol, when it has one.
	Protocol string
	// Comments are the `#` lines inside the grading block. Several cases put
	// load-bearing caveats there rather than in `protocol:`.
	Comments   []string
	FailToPass []Leg
	PassToPass []Leg
	// PostgresRequired and ClusterRequired come from grading.environment.
	PostgresRequired bool
	ClusterRequired  bool
	EnvironmentNotes string
}

// WithheldSpec is one withheld test file with BOTH ends named. The corpus used
// to leave the destination implied, which a grader can only resolve by guessing.
type WithheldSpec struct {
	// Source is case-relative (normally under ground_truth/withheld_tests/).
	Source string
	// Destination is repository-relative.
	Destination string
	// SelfRestoring marks a spec the leg's own cmd puts in place, usually via
	// $CASE_DIR or `git show`. The grader then does not restore it, but the
	// declaration still records what the leg overwrites.
	SelfRestoring bool
}

// Leg is one runnable grading command.
type Leg struct {
	Cmd string
	// WithheldTestPaths name the specs a leg needs restored. They are NOT one
	// thing across the corpus: fix-jb-001 spells them repo-relative and mirrors
	// them under ground_truth/withheld_tests/, fix-jb-003 spells them
	// case-relative with the repository destination left implied, fix-jb-004
	// restores them inside its own cmd via $CASE_DIR, and fix-jb-002 declares a
	// path it ships no copy of at all. A grader must resolve, not assume.
	WithheldTestPaths []string
	// WithheldTests is the normalized form. WithheldTestPaths is the legacy
	// spelling, retained only so a stale case fails a shape test loudly instead
	// of being silently skipped.
	WithheldTests []WithheldSpec
	FocusSpec     string
	// Role and Destructive gate legs the corpus does not want run by default.
	// fix-jb-004 carries a `fallback-only` + `destructive: true` leg that
	// rewrites a spec from a post-cut commit.
	Role        string
	Destructive bool
	// Note is the leg's own prose. Several notes reclassify what the leg means
	// (fix-jb-003's second fail_to_pass entry is an over-correction guard).
	Note string
}

// LoadGrading reads the mechanical grading legs for one case.
//
// A case with no `grading:` block, or with no fail_to_pass leg, is not an error
// here — judge and outcome cases legitimately have neither. The caller decides
// whether it can grade what it got.
func LoadGrading(corpusDir, caseID string) (*Grading, error) {
	path := filepath.Join(corpusDir, caseID, "case.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read case: %w", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse case %s: %w", caseID, err)
	}
	grading := &Grading{}
	node := mappingValue(documentRoot(&root), "grading")
	if node == nil {
		return grading, nil
	}
	grading.Comments = collectComments(node)
	grading.Protocol = scalarValue(mappingValue(node, "protocol"))

	var document struct {
		Grading struct {
			FailToPass []struct {
				Cmd               string   `yaml:"cmd"`
				WithheldTestPaths []string `yaml:"withheld_test_paths"`
				WithheldTests     []struct {
					Source        string `yaml:"source"`
					Destination   string `yaml:"destination"`
					SelfRestoring bool   `yaml:"self_restoring"`
				} `yaml:"withheld_tests"`
				FocusSpec   string `yaml:"focus_spec"`
				Role        string `yaml:"role"`
				Destructive bool   `yaml:"destructive"`
				Note        string `yaml:"note"`
			} `yaml:"fail_to_pass"`
			PassToPass []struct {
				Cmd               string   `yaml:"cmd"`
				WithheldTestPaths []string `yaml:"withheld_test_paths"`
				WithheldTests     []struct {
					Source        string `yaml:"source"`
					Destination   string `yaml:"destination"`
					SelfRestoring bool   `yaml:"self_restoring"`
				} `yaml:"withheld_tests"`
				Role        string `yaml:"role"`
				Destructive bool   `yaml:"destructive"`
				Note        string `yaml:"note"`
			} `yaml:"pass_to_pass"`
			Environment struct {
				PostgresRequired bool   `yaml:"postgres_required"`
				ClusterRequired  bool   `yaml:"cluster_required"`
				Notes            string `yaml:"notes"`
			} `yaml:"environment"`
		} `yaml:"grading"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse case %s grading: %w", caseID, err)
	}
	for _, leg := range document.Grading.FailToPass {
		var withheldTests []WithheldSpec
		for _, spec := range leg.WithheldTests {
			withheldTests = append(withheldTests, WithheldSpec{
				Source: spec.Source, Destination: spec.Destination, SelfRestoring: spec.SelfRestoring,
			})
		}
		grading.FailToPass = append(grading.FailToPass, Leg{
			Cmd: leg.Cmd, WithheldTestPaths: leg.WithheldTestPaths, WithheldTests: withheldTests,
			FocusSpec: leg.FocusSpec, Role: leg.Role, Destructive: leg.Destructive, Note: leg.Note,
		})
	}
	for _, leg := range document.Grading.PassToPass {
		var withheldTests []WithheldSpec
		for _, spec := range leg.WithheldTests {
			withheldTests = append(withheldTests, WithheldSpec{
				Source: spec.Source, Destination: spec.Destination, SelfRestoring: spec.SelfRestoring,
			})
		}
		grading.PassToPass = append(grading.PassToPass, Leg{
			Cmd: leg.Cmd, WithheldTestPaths: leg.WithheldTestPaths, WithheldTests: withheldTests,
			Role: leg.Role, Destructive: leg.Destructive, Note: leg.Note,
		})
	}
	grading.PostgresRequired = document.Grading.Environment.PostgresRequired
	grading.ClusterRequired = document.Grading.Environment.ClusterRequired
	grading.EnvironmentNotes = document.Grading.Environment.Notes
	return grading, nil
}

func documentRoot(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return node.Content[0]
	}
	return node
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func scalarValue(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return node.Value
}

// collectComments gathers every comment attached to a node and its descendants.
// yaml.v3 splits comments across Head/Line/Foot on whichever node they precede,
// so the only reliable way to keep a grading block's caveats is to walk it.
func collectComments(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	var comments []string
	for _, text := range []string{node.HeadComment, node.LineComment, node.FootComment} {
		if text != "" {
			comments = append(comments, text)
		}
	}
	for _, child := range node.Content {
		comments = append(comments, collectComments(child)...)
	}
	return comments
}
