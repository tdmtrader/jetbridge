package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The chart supplies the connection string in the ENVIRONMENT, not in argv, so
// the fallback below is the only thing that reads it in a deployed Pod. A
// broken fallback is a controller that starts with an empty DSN.
//
// The variable name is read out of the template rather than written twice: two
// spellings of it is a Pod whose command finds nothing.
func TestTheDatabaseCredentialIsReadFromTheEnvironmentTheChartSets(t *testing.T) {
	t.Setenv(dsnEnvironmentVariable, "postgres://from-the-environment")

	if got := resolveDSN(""); got != "postgres://from-the-environment" {
		t.Errorf("resolveDSN of an empty flag is %q; the chart passes no --database and "+
			"this is the only path left", got)
	}
	if got := resolveDSN("postgres://explicit"); got != "postgres://explicit" {
		t.Errorf("resolveDSN does not prefer an explicit flag: %q", got)
	}

	template, err := os.ReadFile(filepath.Join("..", "..", "deploy", "chart", "templates", "hangar-output-activation-job.yaml"))
	if err != nil {
		t.Fatalf("reading the chart template: %v", err)
	}
	if !strings.Contains(string(template), "- name: "+dsnEnvironmentVariable) {
		t.Errorf("the chart template does not set %q; the command would read an unset "+
			"variable and start with no connection string", dsnEnvironmentVariable)
	}
	if strings.Contains(string(template), "--database=$("+dsnEnvironmentVariable+")") {
		t.Error("the chart still expands the DSN into argv, where /proc/<pid>/cmdline " +
			"exposes it to anything in the container")
	}
}
