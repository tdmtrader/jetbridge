package resource_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/pullrequest/resource"
)

func TestForgePROutFailsBeforeResolvingDependencies(t *testing.T) {
	err := resource.Out(context.Background(), t.TempDir(), bytes.NewReader([]byte(`{"source":{}}`)), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{ObserverFactory: func(resource.Source) (pullrequest.Observer, error) {
		t.Fatal("must not resolve provider")
		return nil, nil
	}})
	if err == nil {
		t.Fatal("expected read-only resource error")
	}
}
