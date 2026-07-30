package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/pullrequest/resource"
)

func TestForgePRResourceDispatchRejectsWrongArguments(t *testing.T) {
	for _, test := range []struct {
		executable string
		args       []string
	}{{"/opt/resource/check", []string{"unexpected"}}, {"/opt/resource/in", nil}, {"/opt/resource/out", nil}, {"/opt/resource/nope", nil}} {
		if err := Run(context.Background(), test.executable, test.args, &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{}); err == nil {
			t.Fatalf("%s accepted %#v", test.executable, test.args)
		}
	}
}

func TestForgePRResourceOutIsReadOnly(t *testing.T) {
	err := Run(context.Background(), "/opt/resource/out", []string{"/tmp/sources"}, strings.NewReader(`{"source":{}}`), &bytes.Buffer{}, &bytes.Buffer{}, resource.Dependencies{})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("out error = %v", err)
	}
}
