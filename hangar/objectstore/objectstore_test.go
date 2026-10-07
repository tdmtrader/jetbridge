package objectstore

import (
	"strings"
	"testing"
)

func TestNamespacesRefuseAnyTwoEqual(t *testing.T) {
	for _, row := range []struct {
		namespaces Namespaces
		ok         bool
	}{
		{Namespaces{Cache: "c", Input: "i", Output: "o"}, true},
		{Namespaces{Cache: "c"}, true},
		{Namespaces{}, true},
		{Namespaces{Cache: "", Input: "", Output: "o"}, true},
		{Namespaces{Cache: "x", Input: "x", Output: "o"}, false},
		{Namespaces{Cache: "x", Input: "i", Output: "x"}, false},
		{Namespaces{Cache: "c", Input: "x", Output: "x"}, false},
		{Namespaces{Cache: "x", Output: "x"}, false},
	} {
		err := row.namespaces.Validate()
		if (err == nil) != row.ok {
			t.Errorf("%+v: %v", row.namespaces, err)
		}
		if err != nil && !strings.Contains(err.Error(), `"x"`) {
			t.Errorf("%+v: the refusal does not name the shared namespace: %v", row.namespaces, err)
		}
	}
}
