package schema_test

// Small stdlib-only assertion helpers. The agent/schema module is frozen at
// zero external requires (shared-contracts decision 2), so its specs use the
// standard testing package rather than ginkgo/gomega.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// requireNoErr fails the test if err is non-nil (gomega Succeed / NotTo(HaveOccurred)).
func requireNoErr(t *testing.T, err error, msgAndArgs ...interface{}) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected no error, got: %v%s", err, ctx(msgAndArgs))
	}
}

// requireErr fails the test if err is nil (gomega To(HaveOccurred)).
func requireErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// requireErrContains fails unless err is non-nil and its message contains sub.
func requireErrContains(t *testing.T, err error, sub string, msgAndArgs ...interface{}) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil%s", sub, ctx(msgAndArgs))
	}
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("expected error containing %q, got: %v%s", sub, err, ctx(msgAndArgs))
	}
}

// requireEqual fails unless got and want are deeply equal.
func requireEqual(t *testing.T, got, want interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatch: got %#v, want %#v%s", got, want, ctx(msgAndArgs))
	}
}

// requireApprox fails unless got is within tol of want.
func requireApprox(t *testing.T, got, want, tol float64) {
	t.Helper()
	d := got - want
	if d < 0 {
		d = -d
	}
	if d > tol {
		t.Fatalf("expected %v to be within %v of %v", got, tol, want)
	}
}

// requireContains fails unless s contains sub.
func requireContains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("expected %q to contain %q", s, sub)
	}
}

// requireNotContains fails unless s does not contain sub.
func requireNotContains(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Fatalf("expected %q not to contain %q", s, sub)
	}
}

// requireJSONEqual fails unless got and want are equal JSON documents.
func requireJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w interface{}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("invalid JSON in got: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("invalid JSON in want: %v (%s)", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// requireLen fails unless v (slice/map/array/string) has length n.
func requireLen(t *testing.T, v interface{}, n int) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if rv.Len() != n {
		t.Fatalf("expected length %d, got %d (%#v)", n, rv.Len(), v)
	}
}

// requireTrue / requireFalse assert a boolean condition.
func requireTrue(t *testing.T, b bool, msgAndArgs ...interface{}) {
	t.Helper()
	if !b {
		t.Fatalf("expected true%s", ctx(msgAndArgs))
	}
}

func requireFalse(t *testing.T, b bool, msgAndArgs ...interface{}) {
	t.Helper()
	if b {
		t.Fatalf("expected false%s", ctx(msgAndArgs))
	}
}

// requireHasKey / requireNoKey / requireKeyVal check a decoded JSON object.
func requireHasKey(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Fatalf("expected object to have key %q; keys=%v", key, keysOf(m))
	}
}

func requireNoKey(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	if _, ok := m[key]; ok {
		t.Fatalf("expected object not to have key %q", key)
	}
}

func requireKeyVal(t *testing.T, m map[string]interface{}, key string, want interface{}) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Fatalf("expected object to have key %q", key)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("key %q: got %#v, want %#v", key, got, want)
	}
}

func keysOf(m map[string]interface{}) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// ctx formats an optional trailing message (fmt-style) for assertion context.
func ctx(msgAndArgs []interface{}) string {
	if len(msgAndArgs) == 0 {
		return ""
	}
	if f, ok := msgAndArgs[0].(string); ok {
		return " — " + fmt.Sprintf(f, msgAndArgs[1:]...)
	}
	return ""
}
