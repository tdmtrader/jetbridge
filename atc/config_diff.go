package atc

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/aryann/difflib"
	"github.com/mgutz/ansi"
	"github.com/onsi/gomega/gexec"
	"sigs.k8s.io/yaml"
)

type Index interface {
	FindEquivalent(any) (any, bool)
	Slice() []any
}

type Diffs []Diff

type Diff struct {
	Before any
	After  any
}

type DisplayDiff struct {
	Before *DisplayConfig
	After  *DisplayConfig
}

func name(v any) string {
	return reflect.ValueOf(v).FieldByName("Name").String()
}

func (diff Diff) Render(to io.Writer, label string) {

	if diff.Before != nil && diff.After != nil {
		fmt.Fprintf(to, ansi.Color("%s %s has changed:", "yellow")+"\n", label, name(diff.Before))

		payloadA, _ := yaml.Marshal(diff.Before)
		payloadB, _ := yaml.Marshal(diff.After)

		renderDiff(to, string(payloadA), string(payloadB))
	} else if diff.Before != nil {
		fmt.Fprintf(to, ansi.Color("%s %s has been removed:", "yellow")+"\n", label, name(diff.Before))

		payloadA, _ := yaml.Marshal(diff.Before)

		renderDiff(to, string(payloadA), "")
	} else {
		fmt.Fprintf(to, ansi.Color("%s %s has been added:", "yellow")+"\n", label, name(diff.After))

		payloadB, _ := yaml.Marshal(diff.After)

		renderDiff(to, "", string(payloadB))
	}
}

func (diff DisplayDiff) Render(to io.Writer) {
	label := "display configuration"
	if diff.Before != nil && diff.After != nil {
		fmt.Fprintf(to, ansi.Color("%s has changed:", "yellow")+"\n", label)
		payloadA, _ := yaml.Marshal(diff.Before)
		payloadB, _ := yaml.Marshal(diff.After)
		renderDiff(to, string(payloadA), string(payloadB))
	} else if diff.Before != nil {
		fmt.Fprintf(to, ansi.Color("%s has been removed:", "yellow")+"\n", label)
		payloadA, _ := yaml.Marshal(diff.Before)
		renderDiff(to, string(payloadA), "")
	} else {
		fmt.Fprintf(to, ansi.Color("%s has been added:", "yellow")+"\n", label)
		payloadB, _ := yaml.Marshal(diff.After)
		renderDiff(to, "", string(payloadB))
	}
}

type GroupIndex GroupConfigs

func (index GroupIndex) Slice() []any {
	slice := make([]any, len(index))
	for i, object := range index {
		slice[i] = object
	}

	return slice
}

func (index GroupIndex) FindEquivalentWithOrder(obj any) (any, int, bool) {
	return GroupConfigs(index).Lookup(name(obj))
}

type VarSourceIndex VarSourceConfigs

func (index VarSourceIndex) Slice() []any {
	slice := make([]any, len(index))
	for i, object := range index {
		slice[i] = object
	}

	return slice
}

func (index VarSourceIndex) FindEquivalent(obj any) (any, bool) {
	return VarSourceConfigs(index).Lookup(name(obj))
}

type JobIndex JobConfigs

func (index JobIndex) Slice() []any {
	slice := make([]any, len(index))
	for i, object := range index {
		slice[i] = object
	}

	return slice
}

func (index JobIndex) FindEquivalent(obj any) (any, bool) {
	return JobConfigs(index).Lookup(name(obj))
}

type ResourceIndex ResourceConfigs

func (index ResourceIndex) Slice() []any {
	slice := make([]any, len(index))
	for i, object := range index {
		slice[i] = object
	}

	return slice
}

func (index ResourceIndex) FindEquivalent(obj any) (any, bool) {
	return ResourceConfigs(index).Lookup(name(obj))
}

type ResourceTypeIndex ResourceTypes

func (index ResourceTypeIndex) Slice() []any {
	slice := make([]any, len(index))
	for i, object := range index {
		slice[i] = object
	}

	return slice
}

func (index ResourceTypeIndex) FindEquivalent(obj any) (any, bool) {
	return ResourceTypes(index).Lookup(name(obj))
}

// TemplateDiff renders a change to a pipeline's template classification. The
// flag decides whether a `fly set-pipeline` is editing an ordinary pipeline or
// a template, so a change to it alone must still count as a change.
type TemplateDiff struct {
	Before bool
	After  bool
}

func (diff TemplateDiff) Render(to io.Writer) {
	fmt.Fprintln(to, ansi.Color("template has changed:", "yellow"))

	renderDiff(to,
		fmt.Sprintf("template: %t\n", diff.Before),
		fmt.Sprintf("template: %t\n", diff.After),
	)
}

// CacheScopeDiff renders a change to a template's task cache scope. It is
// rendered like the other template-only fields because turning the scope on
// commits the cluster to node-local cache directories nothing reclaims, and
// turning it off silently orphans whatever the previous scope wrote.
type CacheScopeDiff struct {
	Before string
	After  string
}

func (diff CacheScopeDiff) Render(to io.Writer) {
	fmt.Fprintln(to, ansi.Color("cache scope has changed:", "yellow"))

	renderDiff(to,
		fmt.Sprintf("cache_scope: %s\n", cacheScopeLabel(diff.Before)),
		fmt.Sprintf("cache_scope: %s\n", cacheScopeLabel(diff.After)),
	)
}

// An undeclared cache scope means CacheScopeNone, so the diff says so rather
// than rendering a blank line the reader has to interpret.
func cacheScopeLabel(scope string) string {
	if scope == "" {
		return CacheScopeNone
	}
	return scope
}

// RunRetentionDiff renders a change to a template's run retention policy.
type RunRetentionDiff struct {
	Before *RunRetentionConfig
	After  *RunRetentionConfig
}

func (diff RunRetentionDiff) Render(to io.Writer) {
	label := "run retention"
	if diff.Before != nil && diff.After != nil {
		fmt.Fprintf(to, ansi.Color("%s has changed:", "yellow")+"\n", label)
		payloadA, _ := yaml.Marshal(diff.Before)
		payloadB, _ := yaml.Marshal(diff.After)
		renderDiff(to, string(payloadA), string(payloadB))
	} else if diff.Before != nil {
		fmt.Fprintf(to, ansi.Color("%s has been removed:", "yellow")+"\n", label)
		payloadA, _ := yaml.Marshal(diff.Before)
		renderDiff(to, string(payloadA), "")
	} else {
		fmt.Fprintf(to, ansi.Color("%s has been added:", "yellow")+"\n", label)
		payloadB, _ := yaml.Marshal(diff.After)
		renderDiff(to, "", string(payloadB))
	}
}

type ParamIndex []ParamSchema

func (index ParamIndex) Slice() []any {
	slice := make([]any, len(index))
	for i, object := range index {
		slice[i] = object
	}

	return slice
}

func (index ParamIndex) FindEquivalent(obj any) (any, bool) {
	target := name(obj)
	for _, schema := range index {
		if schema.Name == target {
			return schema, true
		}
	}

	return nil, false
}

func diffRunRetention(oldRetention, newRetention *RunRetentionConfig) (RunRetentionDiff, bool) {
	if oldRetention == nil && newRetention == nil {
		return RunRetentionDiff{
			Before: nil,
			After:  nil,
		}, false
	}

	return RunRetentionDiff{
		Before: oldRetention,
		After:  newRetention,
	}, practicallyDifferent(oldRetention, newRetention)
}

func groupDiffIndices(oldIndex GroupIndex, newIndex GroupIndex) Diffs {
	diffs := Diffs{}

	for oldIndexNum, thing := range oldIndex.Slice() {
		newThing, newIndexNum, found := newIndex.FindEquivalentWithOrder(thing)
		if !found {
			diffs = append(diffs, Diff{
				Before: thing,
				After:  nil,
			})
			continue
		}

		if practicallyDifferent(thing, newThing) {
			diffs = append(diffs, Diff{
				Before: thing,
				After:  newThing,
			})
		}

		if oldIndexNum != newIndexNum {
			diffs = append(diffs, Diff{
				Before: thing,
				After:  newThing,
			})
		}
	}

	for _, thing := range newIndex.Slice() {
		_, _, found := oldIndex.FindEquivalentWithOrder(thing)
		if !found {
			diffs = append(diffs, Diff{
				Before: nil,
				After:  thing,
			})
			continue
		}
	}

	return diffs
}

func diffIndices(oldIndex Index, newIndex Index) Diffs {
	diffs := Diffs{}

	for _, thing := range oldIndex.Slice() {
		newThing, found := newIndex.FindEquivalent(thing)
		if !found {
			diffs = append(diffs, Diff{
				Before: thing,
				After:  nil,
			})
			continue
		}

		if practicallyDifferent(thing, newThing) {
			diffs = append(diffs, Diff{
				Before: thing,
				After:  newThing,
			})
		}
	}

	for _, thing := range newIndex.Slice() {
		_, found := oldIndex.FindEquivalent(thing)
		if !found {
			diffs = append(diffs, Diff{
				Before: nil,
				After:  thing,
			})
			continue
		}
	}

	return diffs
}

func diffDisplay(oldDisplay, newDisplay *DisplayConfig) (DisplayDiff, bool) {
	if oldDisplay == nil && newDisplay == nil {
		return DisplayDiff{
			Before: nil,
			After:  nil,
		}, false
	}

	return DisplayDiff{
		Before: oldDisplay,
		After:  newDisplay,
	}, practicallyDifferent(oldDisplay, newDisplay)
}

func renderDiff(to io.Writer, a, b string) {
	diffs := difflib.Diff(strings.Split(a, "\n"), strings.Split(b, "\n"))
	indent := gexec.NewPrefixedWriter("\b\b", to)

	for _, diff := range diffs {
		text := diff.Payload

		switch diff.Delta {
		case difflib.RightOnly:
			fmt.Fprintf(indent, "%s %s\n", ansi.Color("+", "green"), ansi.Color(text, "green"))
		case difflib.LeftOnly:
			fmt.Fprintf(indent, "%s %s\n", ansi.Color("-", "red"), ansi.Color(text, "red"))
		case difflib.Common:
			fmt.Fprintf(to, "%s\n", text)
		}
	}
}

func practicallyDifferent(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return false
	}

	// prevent silly things like 300 != 300.0 due to YAML vs. JSON
	// inconsistencies
	marshalledA, errA := yaml.Marshal(a)
	marshalledB, errB := yaml.Marshal(b)

	// If we can't marshal either value, they're different
	if errA != nil || errB != nil {
		return true
	}

	return !bytes.Equal(marshalledA, marshalledB)
}

func (c Config) Diff(out io.Writer, newConfig Config) bool {
	var diffExists bool

	indent := gexec.NewPrefixedWriter("  ", out)

	groupDiffs := groupDiffIndices(GroupIndex(c.Groups), GroupIndex(newConfig.Groups))
	if len(groupDiffs) > 0 {
		diffExists = true
		fmt.Fprintln(out, "groups:")

		for _, diff := range groupDiffs {
			diff.Render(indent, "group")
		}
	}

	varSourceDiffs := diffIndices(VarSourceIndex(c.VarSources), VarSourceIndex(newConfig.VarSources))
	if len(varSourceDiffs) > 0 {
		diffExists = true
		fmt.Fprintln(out, "variable source:")

		for _, diff := range varSourceDiffs {
			diff.Render(indent, "variable source")
		}
	}

	resourceDiffs := diffIndices(ResourceIndex(c.Resources), ResourceIndex(newConfig.Resources))
	if len(resourceDiffs) > 0 {
		diffExists = true
		fmt.Fprintln(out, "resources:")

		for _, diff := range resourceDiffs {
			diff.Render(indent, "resource")
		}
	}

	resourceTypeDiffs := diffIndices(ResourceTypeIndex(c.ResourceTypes), ResourceTypeIndex(newConfig.ResourceTypes))
	if len(resourceTypeDiffs) > 0 {
		diffExists = true
		fmt.Fprintln(out, "resource types:")

		for _, diff := range resourceTypeDiffs {
			diff.Render(indent, "resource type")
		}
	}

	jobDiffs := diffIndices(JobIndex(c.Jobs), JobIndex(newConfig.Jobs))
	if len(jobDiffs) > 0 {
		diffExists = true
		fmt.Fprintln(out, "jobs:")

		for _, diff := range jobDiffs {
			diff.Render(indent, "job")
		}
	}

	displayDiff, diff := diffDisplay(c.Display, newConfig.Display)
	if diff {
		diffExists = true
		displayDiff.Render(indent)
	}

	if c.Template != newConfig.Template {
		diffExists = true
		TemplateDiff{Before: c.Template, After: newConfig.Template}.Render(indent)
	}

	if cacheScopeLabel(c.CacheScope) != cacheScopeLabel(newConfig.CacheScope) {
		diffExists = true
		CacheScopeDiff{Before: c.CacheScope, After: newConfig.CacheScope}.Render(indent)
	}

	paramDiffs := diffIndices(ParamIndex(c.Params), ParamIndex(newConfig.Params))
	if len(paramDiffs) > 0 {
		diffExists = true
		fmt.Fprintln(out, "parameters:")

		for _, paramDiff := range paramDiffs {
			paramDiff.Render(indent, "parameter")
		}
	}

	retentionDiff, retentionChanged := diffRunRetention(c.RunRetention, newConfig.RunRetention)
	if retentionChanged {
		diffExists = true
		retentionDiff.Render(indent)
	}

	return diffExists
}
