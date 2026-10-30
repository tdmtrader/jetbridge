package jetbridge

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

func TestSidecarLogWriters(t *testing.T) {
	sidecars := []atc.SidecarConfig{{Name: "helper", Image: "busybox"}, {Name: "db", Image: "postgres"}}

	t.Run("one writer per declared sidecar", func(t *testing.T) {
		helper, db := new(bytes.Buffer), new(bytes.Buffer)
		writers, err := sidecarLogWriters(sidecars, runtime.ProcessIO{
			SidecarWriters: map[string]io.Writer{"helper": helper, "db": db},
		})
		if err != nil {
			t.Fatalf("sidecarLogWriters: %v", err)
		}
		if len(writers) != 2 || writers["helper"] != helper || writers["db"] != db {
			t.Fatalf("writers = %#v, want the two the engine handed over", writers)
		}
	})

	t.Run("a declared sidecar without a writer is an error naming it", func(t *testing.T) {
		_, err := sidecarLogWriters(sidecars, runtime.ProcessIO{
			SidecarWriters: map[string]io.Writer{"helper": new(bytes.Buffer)},
		})
		if err == nil || !strings.Contains(err.Error(), `sidecar "db" has no log writer`) {
			t.Fatalf("err = %v, want the missing sidecar named", err)
		}
	})

	t.Run("no sidecars need no writers", func(t *testing.T) {
		writers, err := sidecarLogWriters(nil, runtime.ProcessIO{})
		if err != nil || writers != nil {
			t.Fatalf("writers, err = %v, %v; want nil, nil", writers, err)
		}
	})
}
