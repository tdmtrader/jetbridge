package objectstore

import (
	"strings"
	"testing"
)

func TestNamespacesRefuseCacheAndOutputEqual(t *testing.T) {
	for _, row := range []struct {
		namespaces Namespaces
		ok         bool
	}{
		{Namespaces{Cache: "c", Output: "o"}, true},
		{Namespaces{Cache: "c"}, true},
		{Namespaces{Output: "o"}, true},
		{Namespaces{}, true},
		{Namespaces{Cache: "", Output: "o"}, true},
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
