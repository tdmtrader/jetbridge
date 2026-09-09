package steps

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/brine-dev/brine-go/pkg/brine"
)

type actionValue struct {
	Name  string
	Count int
}

func TestActionCarriesStateResourcesAndErrorsThroughBrine(t *testing.T) {
	for _, fault := range []string{"none", "resource action", "plain action"} {
		t.Run(fault, func(t *testing.T) {
			boom := errors.New("operation refused")
			defs := []brine.StepDefinition{
				TransformUsing[brine.Empty, actionValue]("create {string} with {int}", []string{"base"},
					func(_ brine.Empty, a Args, res brine.Resources) (actionValue, error) {
						if fault == "resource action" {
							return actionValue{}, boom
						}
						return actionValue{Name: a.String(0), Count: res.Get("base").(int) + a.Int(1)}, nil
					}),
				Transform[actionValue, actionValue]("add {int}", func(in actionValue, a Args) (actionValue, error) {
					if fault == "plain action" {
						return in, boom
					}
					in.Count += a.Int(0)
					return in, nil
				}),
				CheckThat[actionValue]("the result is complete", func(in actionValue) error {
					if in.Name != "artifact" || in.Count != 15 {
						return fmt.Errorf("lost state or resource: %+v", in)
					}
					return nil
				}),
			}
			if uses := defs[0].Uses(); len(uses) != 1 || uses[0] != "base" {
				t.Fatalf("resource declaration lost: %v", uses)
			}
			resources, err := brine.NewResourceRegistry([]brine.ResourceDefinition{
				{Name: "base", Scope: brine.ScopeScenario, Factory: func(map[string]any) (any, error) { return 7, nil }},
			})
			if err != nil {
				t.Fatal(err)
			}
			feature := brine.ParseFeatureText("action.feature", `Feature: Action dispatch
  Scenario: Carry state
    Given create "artifact" with 5
    When add 3
    Then the result is complete
`)
			var events bytes.Buffer
			pipeline := brine.NewPipeline(brine.NewStepRegistry(defs), brine.NewEmitter(&events)).WithResources(brine.NewResourceState(resources))
			result, code, err := pipeline.Run([]*brine.ParsedFeature{feature}, brine.TagFilter{}, nil)
			if err != nil || result.Scenarios != 1 || result.Undefined != 0 || result.Unsatisfied != 0 {
				t.Fatalf("invalid run: %+v, %v; %s", result, err, events.String())
			}
			if fault == "none" {
				if code != 0 || result.Passed != 1 {
					t.Fatalf("lost transition: %+v; %s", result, events.String())
				}
			} else if code != 1 || result.Failed != 1 || !strings.Contains(events.String(), boom.Error()) {
				t.Fatalf("operation failure was swallowed: %+v; %s", result, events.String())
			}
		})
	}
}

func TestActionValidatesBeforeInvokingOperation(t *testing.T) {
	const pattern = "carry {string} and {int}"
	def := Transform[brine.Empty, actionValue](pattern, func(brine.Empty, Args) (actionValue, error) { return actionValue{}, nil })
	for _, tc := range []struct {
		name   string
		params brine.Params
		valid  bool
	}{
		{"missing", brine.Params{}, false},
		{"overflow", paramsFor(t, def, `carry "x" and 99999999999999999999999999999`), false},
		{"empty string and negative number", paramsFor(t, def, `carry "" and -7`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			out, err := applyAction(pattern, brine.Empty{}, tc.params, func(_ brine.Empty, a Args) (actionValue, error) {
				called = true
				return actionValue{Name: a.String(0), Count: a.Int(1)}, nil
			})
			if tc.valid {
				if err != nil || !called || out.Name != "" || out.Count != -7 {
					t.Fatalf("valid input changed: %+v, %v", out, err)
				}
			} else if err == nil || called || !strings.Contains(err.Error(), "carry") {
				t.Fatalf("invalid parameters reached operation: called=%v, err=%v", called, err)
			}
		})
	}
}

func TestActionPreservesErrorIdentityAndRejectsUndeclaredReads(t *testing.T) {
	boom := errors.New("original operation error")
	out, err := applyAction("run", "input", brine.Params{}, func(string, Args) (string, error) { return "partial", boom })
	if out != "partial" || !errors.Is(err, boom) {
		t.Fatalf("lost operation result: %q, %v", out, err)
	}
	_, err = applyAction("run", "input", brine.Params{}, func(in string, a Args) (string, error) { return in + a.String(9), nil })
	if err == nil || !strings.Contains(err.Error(), "parameter 9") {
		t.Fatalf("undeclared read passed: %v", err)
	}
}
