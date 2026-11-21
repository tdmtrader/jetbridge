package runtime

import (
	"reflect"
	"testing"

	"github.com/concourse/concourse/atc"
)

// A task's params come out in name order, so a runtime that numbers them
// numbers them the same way on every composition of the same task.
func TestParamsFromIsInNameOrder(t *testing.T) {
	got := ParamsFrom(atc.TaskEnv{"ZED": "z", "ALPHA": "a", "EMPTY": "", "MID": "$(X)$$"})
	want := []Param{{"ALPHA", "a"}, {"EMPTY", ""}, {"MID", "$(X)$$"}, {"ZED", "z"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParamsFrom = %v, want %v", got, want)
	}
	if ParamsFrom(nil) != nil {
		t.Error("no params is not nil")
	}
}
