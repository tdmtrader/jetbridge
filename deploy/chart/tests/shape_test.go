package tests

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The chart's shape budget (docs/adr/0008-the-chart-offers-choices-not-flags.md).
//
// These constants are the whole policy. Raising one is a one-line diff a
// reviewer sees, and ADR-0008 allows a raise only in the commit that removes
// the pass-through or step value the new value replaces, with the reason
// written beside the constant. Every track that removes values lowers
// maxValues in the same commit.

// maxValues is how many values the chart may have. A value is a schema leaf:
// a property whose type is not object, or an object on openMaps. Inside
// deferredGroups, whose schema is not yet written out, a value is a
// values.yaml leaf, with an empty map or list counted as one. Array items and
// the keys on removedKeys are not values.
//
// The plan's starting figure, 276, counted values.yaml leaves outside the
// deferred groups too. Under this definition the nine resources, probe and
// security-context defaults are one value each, not the 40 leaves their
// values.yaml examples hold, so today's count is 245: 131 values.yaml values,
// the three template-only keys (fullnameOverride, nameOverride,
// serviceAccount.name) and the 111 leaves of the deferred groups.
const maxValues = 245

// allowedSwitches are the only booleans the chart may have. A switch stays
// only when it reflects something the cluster has or lacks. Booleans inside
// deferredGroups are exempt with their group.
var allowedSwitches = []string{
	"alertingRules.enabled",
	"artifactDaemon.networkPolicy.enabled",
	"artifactDaemon.preemption.enabled",
	"ingress.enabled",
	"kubernetes.credentialManager.enabled",
	"kubernetes.stepPodGrants[].privileged",
	"metrics.enabled",
	"networkPolicy.enabled",
	"otelMetrics.otlpUseTLS",
	"pdb.enabled",
	"postgresql.enabled",
	"postgresql.persistence.enabled",
	"rbac.brineLive",
	"rbac.create",
	"secrets.create",
	"serviceAccount.create",
	"serviceMonitor.enabled",
	"tracing.otlpUseTLS",
	"web.tls.enabled",
}

// openMaps are the free-form Kubernetes maps and lists, the only objects the
// schema leaves open. Each is one value; nothing inside one is checked.
var openMaps = []string{
	"alertingRules.labels",
	"artifactDaemon.affinity",
	"artifactDaemon.nodeSelector",
	"artifactDaemon.resources",
	"artifactDaemon.tolerations",
	"ingress.annotations",
	"ingress.tls",
	"networkPolicy.ingressFrom",
	"networkPolicy.taskEgressTo",
	"otelMetrics.otlpHeaders",
	"postgresql.containerSecurityContext",
	"postgresql.podSecurityContext",
	"postgresql.resources",
	"service.annotations",
	"service.labels",
	"serviceAccount.annotations",
	"serviceMonitor.labels",
	"tracing.otlpHeaders",
	"web.affinity",
	"web.containerSecurityContext",
	"web.env",
	"web.extraVolumeMounts",
	"web.extraVolumes",
	"web.livenessProbe",
	"web.nodeSelector",
	"web.podSecurityContext",
	"web.readinessProbe",
	"web.resources",
	"web.startupProbe",
	"web.strategy",
	"web.tolerations",
}

// deferredGroups are closed at their top level only: each key directly under
// one needs an entry, and nothing deeper is checked. Tracks 2-4 of
// chart_valid_configurations_only empty this list.
var deferredGroups = []string{
	"artifactDaemon.durable",
	"artifactDaemon.hangar",
	"hangarBootstrap",
	"hangarOutput",
	"hangarStorage",
}

// bannedKeys are the pass-through shapes: a list of raw arguments, raw
// environment, or a generic map of arbitrary flags. Each pattern matches a
// key's own name, case-insensitively.
var bannedKeys = []string{
	`(extra|additional)?(args|arguments)`,
	`(extra|additional)?(env|envs|envvars|envfrom|environment)`,
	`(extra|additional)?(flags|options|opts)`,
}

// grandfathered are the pass-through values that predate the ban. Track 6
// removes both.
var grandfathered = []string{
	"web.env",
	"web.extraArgs",
}

// removedKeys stay in the schema as open types for one release, so the
// removed-keys table in templates/_validate.tpl can name the replacement.
// They are not values.
var removedKeys = []string{
	"artifactDaemon.enabled",
	"artifactDaemon.tls.enabled",
	"web.enablePipelineRunCreation",
}

// bareRenderNames are the values a render with no values file must name when
// it fails, so an operator who forgot one is told which.
var bareRenderNames = []string{
	"mcp.clients",
}

func TestTheChartHoldsItsShape(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "deploy", "chart")
	for _, guard := range shapeGuards {
		t.Run(guard.name, func(t *testing.T) {
			for _, violation := range guard.check(dir) {
				t.Error(violation)
			}
		})
	}
}

// Each guard is shown to catch its own break, made on a copy of the chart.
func TestEachShapeGuardCatchesItsBreak(t *testing.T) {
	root := repoRoot(t)
	for _, guard := range shapeGuards {
		t.Run(guard.name, func(t *testing.T) {
			dir := copyChart(t, filepath.Join(root, "deploy", "chart"))
			guard.breakIt(t, dir)
			violations := guard.check(dir)
			if len(violations) == 0 {
				t.Fatal("the guard accepted a chart that breaks its rule")
			}
			if !strings.Contains(strings.Join(violations, "\n"), guard.names) {
				t.Fatalf("the guard did not name %s: %v", guard.names, violations)
			}
		})
	}
}

type shapeGuard struct {
	name    string
	check   func(dir string) []string
	breakIt func(t *testing.T, dir string)
	// names is what the guard's report must name for the break above.
	names string
}

var shapeGuards = []shapeGuard{
	{
		name:  "the number of values stays within maxValues",
		names: "maxValues",
		check: checkValueBudget,
		breakIt: func(t *testing.T, dir string) {
			count, err := countValues(dir)
			if err != nil {
				t.Fatal(err)
			}
			editSchema(t, dir, func(schema map[string]any) {
				web := schemaProperties(schema, "web")
				for i := 0; i <= maxValues-count; i++ {
					web[fmt.Sprintf("addedValue%d", i)] = map[string]any{"type": "string"}
				}
			})
		},
	},
	{
		name:  "every boolean is an allowed switch",
		names: "web.addedSwitch",
		check: checkSwitches,
		breakIt: func(t *testing.T, dir string) {
			editSchema(t, dir, func(schema map[string]any) {
				schemaProperties(schema, "web")["addedSwitch"] = map[string]any{"type": "boolean"}
			})
		},
	},
	{
		name:  "no pass-through value outside grandfathered",
		names: "artifactDaemon.extraFlags",
		check: checkBannedKeys,
		breakIt: func(t *testing.T, dir string) {
			editValues(t, dir, func(values map[string]any) {
				values["artifactDaemon"].(map[string]any)["extraFlags"] = map[string]any{}
			})
		},
	},
	{
		name:  "every key has a schema entry",
		names: "web.unlisted",
		check: checkSchemaEntries,
		breakIt: func(t *testing.T, dir string) {
			editValues(t, dir, func(values map[string]any) {
				values["web"].(map[string]any)["unlisted"] = ""
			})
		},
	},
	{
		name:  "every object outside openMaps is closed",
		names: `"service"`,
		check: checkClosedObjects,
		breakIt: func(t *testing.T, dir string) {
			editSchema(t, dir, func(schema map[string]any) {
				delete(schema["properties"].(map[string]any)["service"].(map[string]any), "additionalProperties")
			})
		},
	},
	{
		name:  "a render with no values names what is missing",
		names: "mcp.clients",
		check: checkBareRender,
		breakIt: func(t *testing.T, dir string) {
			// Without minItems the empty registration passes the schema, and
			// the render fails on the next missing value instead.
			editSchema(t, dir, func(schema map[string]any) {
				delete(schemaProperties(schema, "mcp")["clients"].(map[string]any), "minItems")
			})
		},
	},
}

// checkValueBudget: the number of values never exceeds maxValues.
func checkValueBudget(dir string) []string {
	count, err := countValues(dir)
	if err != nil {
		return []string{err.Error()}
	}
	if count > maxValues {
		return []string{fmt.Sprintf("the chart has %d values, more than maxValues (%d). A raise is allowed only in the commit that removes the pass-through or step value the new value replaces (ADR-0008)", count, maxValues)}
	}
	return nil
}

func countValues(dir string) (int, error) {
	shape, err := loadShape(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	shape.walkSchema(func(path string, node map[string]any, inItems bool) {
		if inItems || inDeferredGroup(path) || slices.Contains(removedKeys, path) {
			return
		}
		if !isObjectSchema(node) || slices.Contains(openMaps, path) {
			count++
		}
	})
	for _, group := range deferredGroups {
		count += countLeaves(valueAt(shape.values, group))
	}
	return count, nil
}

// checkSwitches: every boolean is on allowedSwitches, and every entry there
// is still a boolean.
func checkSwitches(dir string) []string {
	shape, err := loadShape(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var violations []string
	seen := map[string]bool{}
	shape.walkSchema(func(path string, node map[string]any, _ bool) {
		if inDeferredGroup(path) || !hasType(node, "boolean") {
			return
		}
		seen[path] = true
		if !slices.Contains(allowedSwitches, path) {
			violations = append(violations, fmt.Sprintf("%s is a boolean and not on allowedSwitches: a switch stays only when it reflects something the cluster has or lacks (ADR-0008)", path))
		}
	})
	for _, path := range allowedSwitches {
		if !seen[path] {
			violations = append(violations, fmt.Sprintf("allowedSwitches lists %s, which is no longer a boolean; remove it", path))
		}
	}
	return violations
}

// checkBannedKeys: no key, in values.yaml or the schema, has a pass-through
// name unless it is grandfathered, and every grandfathered key still exists.
func checkBannedKeys(dir string) []string {
	shape, err := loadShape(dir)
	if err != nil {
		return []string{err.Error()}
	}
	banned := regexp.MustCompile(`(?i)^(` + strings.Join(bannedKeys, "|") + `)$`)
	found := map[string]bool{}
	check := func(path string) {
		if banned.MatchString(path[strings.LastIndex(path, ".")+1:]) {
			found[path] = true
		}
	}
	walkValues(shape.values, "", func(path string, _ any) bool {
		check(path)
		return !slices.Contains(openMaps, path)
	})
	shape.walkSchema(func(path string, _ map[string]any, _ bool) { check(path) })
	var violations []string
	for _, path := range sortedPaths(found) {
		if !slices.Contains(grandfathered, path) {
			violations = append(violations, fmt.Sprintf("%s is a pass-through value; give the setting a typed value of its own (ADR-0008)", path))
		}
	}
	for _, path := range grandfathered {
		if !found[path] {
			violations = append(violations, fmt.Sprintf("grandfathered lists %s, which no longer exists; remove it", path))
		}
	}
	return violations
}

// checkSchemaEntries: every values.yaml key outside openMaps has a schema
// entry (inside a deferred group, only the keys directly under it), every
// entry outside deferredGroups has a type unless it is a removed key, and
// every name on the lists above is in the schema.
func checkSchemaEntries(dir string) []string {
	shape, err := loadShape(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var violations []string
	walkValues(shape.values, "", func(path string, _ any) bool {
		if _, ok := shape.schemaAt(path); !ok {
			violations = append(violations, fmt.Sprintf("values.yaml sets %s, which has no entry in values.schema.json", path))
			return false
		}
		return !slices.Contains(openMaps, path) && (!inDeferredGroup(path) || slices.Contains(deferredGroups, path))
	})
	shape.walkSchema(func(path string, node map[string]any, _ bool) {
		if !inDeferredGroup(path) && !slices.Contains(removedKeys, path) && !isTyped(shape.schema, node) {
			violations = append(violations, fmt.Sprintf("the schema entry for %s has no type", path))
		}
	})
	for _, list := range [][]string{openMaps, deferredGroups, removedKeys} {
		for _, path := range list {
			if _, ok := shape.schemaAt(path); !ok {
				violations = append(violations, fmt.Sprintf("%s is listed in shape_test.go but has no schema entry", path))
			}
		}
	}
	return violations
}

// checkClosedObjects: every object in the schema refuses keys it does not
// list, except an object on openMaps; a deferred group is closed at its top.
func checkClosedObjects(dir string) []string {
	shape, err := loadShape(dir)
	if err != nil {
		return []string{err.Error()}
	}
	var violations []string
	closed := func(path string, node map[string]any) {
		if isObjectSchema(node) && node["additionalProperties"] != false {
			violations = append(violations, fmt.Sprintf("the schema object %q is open: set additionalProperties to false, or list it on openMaps if it is a free-form Kubernetes map", path))
		}
	}
	closed("", shape.schema)
	shape.walkSchema(func(path string, node map[string]any, _ bool) {
		if slices.Contains(openMaps, path) || (inDeferredGroup(path) && !slices.Contains(deferredGroups, path)) {
			return
		}
		closed(path, node)
	})
	return violations
}

// checkBareRender: a render with no values fails, and names what is missing.
func checkBareRender(dir string) []string {
	out, err := exec.Command("helm", "template", "jb", dir).CombinedOutput()
	if err == nil {
		return []string{"a render with no values succeeded; it must fail naming each required value"}
	}
	normalized := strings.ReplaceAll(string(out), "/", ".")
	var violations []string
	for _, name := range bareRenderNames {
		if !strings.Contains(normalized, name) {
			violations = append(violations, fmt.Sprintf("a render with no values failed without naming %s:\n%s", name, out))
		}
	}
	return violations
}

// The schema refuses what the guards above cannot see: an unknown key, a
// value outside its enum, and a malformed duration or port.

func TestAnUnknownKeyFailsTheRenderNamingIt(t *testing.T) {
	shape, err := loadShape(filepath.Join(repoRoot(t), "deploy", "chart"))
	if err != nil {
		t.Fatal(err)
	}
	closed := []string{""}
	shape.walkSchema(func(path string, node map[string]any, inItems bool) {
		if !inItems && node["additionalProperties"] == false {
			closed = append(closed, path)
		}
	})
	var sets, names []string
	for i, path := range closed {
		name := fmt.Sprintf("unknownKey%d", i)
		names = append(names, name)
		sets = append(sets, strings.TrimPrefix(path+"."+name, ".")+"=1")
	}
	out := renderHangarError(t, sets...)
	for i, name := range names {
		if !strings.Contains(out, name) {
			t.Errorf("an unknown key under %q rendered or was not named: %s", closed[i], name)
		}
	}
	if len(closed) < 30 {
		t.Fatalf("only %d closed objects were found; the walk lost the schema", len(closed))
	}
}

func TestAValueTheCodeDoesNotAcceptFailsTheRender(t *testing.T) {
	for _, setting := range []string{
		"web.logLevel=banana",
		"service.type=Nonsense",
		"kubernetes.podStartupTimeout=banana",
		"service.httpPort=0",
		"artifactDaemon.metrics.port=65536",
		"serviceMonitor.interval=30",
		"cacheStore=pvc",
	} {
		t.Run(setting, func(t *testing.T) {
			out := strings.ReplaceAll(renderHangarError(t, setting), "/", ".")
			if key := setting[:strings.Index(setting, "=")]; !strings.Contains(out, key) {
				t.Fatalf("the failure does not name %s: %s", key, out)
			}
		})
	}
}

func TestOptionalPortsAndDurationsAcceptTheirForms(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		want, deny string
	}{
		{[]string{"--set-string", "artifactDaemon.metrics.port=9392"}, "--metrics-port=9392", ""},
		{[]string{"--set", "artifactDaemon.metrics.port=9392"}, "--metrics-port=9392", ""},
		{[]string{"--set", "postgresql.connectTimeout="}, "", "--postgres-connect-timeout"},
		{[]string{"--set", "postgresql.connectTimeout=90s"}, "--postgres-connect-timeout=90s", ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			args := append([]string{"template", "jb", "deploy/chart", "-f", "deploy/chart/tests/testdata/required-values.yaml"}, tc.args...)
			cmd := exec.Command("helm", args...)
			cmd.Dir = repoRoot(t)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helm template %v: %v\n%s", tc.args, err, out)
			}
			if tc.want != "" && !strings.Contains(string(out), tc.want) {
				t.Errorf("render lacks %s", tc.want)
			}
			if tc.deny != "" && strings.Contains(string(out), tc.deny) {
				t.Errorf("render carries %s", tc.deny)
			}
		})
	}
}

// chartShape is a chart's values.yaml and values.schema.json, parsed.
type chartShape struct {
	values map[string]any
	schema map[string]any
}

func loadShape(dir string) (chartShape, error) {
	var shape chartShape
	raw, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		return shape, err
	}
	if err := yaml.Unmarshal(raw, &shape.values); err != nil {
		return shape, fmt.Errorf("values.yaml: %w", err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "values.schema.json"))
	if err != nil {
		return shape, err
	}
	if err := json.Unmarshal(raw, &shape.schema); err != nil {
		return shape, fmt.Errorf("values.schema.json: %w", err)
	}
	return shape, nil
}

// walkSchema visits every property in the schema, by dotted path, with its
// $ref resolved. It descends into objects and into the object items of
// arrays (as path[]), but not into an object on openMaps or below a deferred
// group's own keys.
func (shape chartShape) walkSchema(visit func(path string, node map[string]any, inItems bool)) {
	var walk func(prefix string, node map[string]any, inItems bool)
	walk = func(prefix string, node map[string]any, inItems bool) {
		props, _ := node["properties"].(map[string]any)
		for _, name := range sortedPaths(props) {
			child := resolveSchema(shape.schema, props[name].(map[string]any))
			path := strings.TrimPrefix(prefix+"."+name, ".")
			visit(path, child, inItems)
			if slices.Contains(openMaps, path) || (inDeferredGroup(path) && !slices.Contains(deferredGroups, path)) {
				continue
			}
			if slices.Contains(deferredGroups, path) {
				// The group's own keys are visited; nothing below them.
				for _, key := range sortedPaths(child["properties"].(map[string]any)) {
					visit(path+"."+key, resolveSchema(shape.schema, child["properties"].(map[string]any)[key].(map[string]any)), inItems)
				}
				continue
			}
			walk(path, child, inItems)
			if items, ok := child["items"].(map[string]any); ok {
				walk(path+"[]", resolveSchema(shape.schema, items), true)
			}
		}
	}
	walk("", shape.schema, false)
}

// schemaAt finds the entry for a dotted values path.
func (shape chartShape) schemaAt(path string) (map[string]any, bool) {
	node := shape.schema
	for _, name := range strings.Split(path, ".") {
		props, _ := node["properties"].(map[string]any)
		child, ok := props[name].(map[string]any)
		if !ok {
			return nil, false
		}
		node = resolveSchema(shape.schema, child)
	}
	return node, true
}

func resolveSchema(root, node map[string]any) map[string]any {
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		node = root["definitions"].(map[string]any)[strings.TrimPrefix(ref, "#/definitions/")].(map[string]any)
	}
}

func isObjectSchema(node map[string]any) bool {
	return hasType(node, "object")
}

func hasType(node map[string]any, want string) bool {
	switch typ := node["type"].(type) {
	case string:
		return typ == want
	case []any:
		return slices.Contains(typ, any(want))
	}
	return false
}

func isTyped(root, node map[string]any) bool {
	if node["type"] != nil || node["enum"] != nil || node["const"] != nil {
		return true
	}
	branches, _ := node["anyOf"].([]any)
	for _, branch := range branches {
		if !isTyped(root, resolveSchema(root, branch.(map[string]any))) {
			return false
		}
	}
	return len(branches) > 0
}

func inDeferredGroup(path string) bool {
	for _, group := range deferredGroups {
		if path == group || strings.HasPrefix(path, group+".") {
			return true
		}
	}
	return false
}

// walkValues visits every key in a values tree by dotted path, descending
// into a non-empty map when visit returns true.
func walkValues(node map[string]any, prefix string, visit func(path string, value any) bool) {
	for _, name := range sortedPaths(node) {
		path := strings.TrimPrefix(prefix+"."+name, ".")
		value := node[name]
		if visit(path, value) {
			if child, ok := value.(map[string]any); ok {
				walkValues(child, path, visit)
			}
		}
	}
}

func valueAt(values map[string]any, path string) any {
	var node any = values
	for _, name := range strings.Split(path, ".") {
		node = node.(map[string]any)[name]
	}
	return node
}

func countLeaves(value any) int {
	m, ok := value.(map[string]any)
	if !ok || len(m) == 0 {
		return 1
	}
	count := 0
	for _, child := range m {
		count += countLeaves(child)
	}
	return count
}

func sortedPaths[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func schemaProperties(schema map[string]any, name string) map[string]any {
	return schema["properties"].(map[string]any)[name].(map[string]any)["properties"].(map[string]any)
}

// copyChart copies the chart, without its tests, into a temporary directory.
func copyChart(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "chart")
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if entry.IsDir() {
			if rel == "tests" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func editSchema(t *testing.T, dir string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "values.schema.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	edit(schema)
	if raw, err = json.MarshalIndent(schema, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func editValues(t *testing.T, dir string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "values.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	edit(values)
	if raw, err = yaml.Marshal(values); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
