package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/hangaroutput/activation"
)

// The activation command's configuration, and what it refuses.
//
// stdlib testing, matching cmd/artifact-daemon (0 Ginkgo). 927 lines of command
// code with no tests was a Phase 7 finding; this command is small, but every
// refusal below is one an operator meets at three in the morning while a plane
// is half-enabled, and a refusal that does not say what is missing is one they
// will work around.

func parse(t *testing.T, arguments ...string) (Config, error) {
	t.Helper()

	config := Config{}
	flags := flag.NewFlagSet("hangar-output-activate", flag.ContinueOnError)
	flags.SetOutput(&strings.Builder{})
	BindFlags(flags, &config)
	if err := flags.Parse(arguments); err != nil {
		return Config{}, err
	}

	return config, config.Validate()
}

// The control first: a complete invocation of each mode is accepted, so the
// table of refusals below is not a table that refuses everything.
func TestEachModeIsAcceptedWhenItIsComplete(t *testing.T) {
	for name, arguments := range map[string][]string{
		"begin": {"--mode=begin", "--epoch=7", "--database=postgres://x"},
		"enable": {"--mode=enable", "--facet=base", "--epoch=7",
			"--database=postgres://x"},
		"drain": {"--mode=drain", "--facet=all", "--epoch=7",
			"--database=postgres://x"},
		"attest": {"--mode=attest", "--facet=output", "--epoch=7",
			"--database=postgres://x", "--namespace=cicd"},
	} {
		if _, err := parse(t, arguments...); err != nil {
			t.Errorf("a complete %s invocation was refused: %v", name, err)
		}
	}
}

func TestAnIncompleteInvocationIsRefusedAndSaysWhat(t *testing.T) {
	for name, expected := range map[string]struct {
		arguments []string
		says      string
	}{
		"no database": {
			[]string{"--mode=begin", "--epoch=7"}, "--database",
		},
		"no epoch": {
			[]string{"--mode=begin", "--database=postgres://x"}, "--epoch",
		},
		"a zero epoch": {
			[]string{"--mode=begin", "--epoch=0", "--database=postgres://x"},
			"zero is the absence",
		},
		"no mode": {
			[]string{"--epoch=7", "--database=postgres://x"}, "--mode",
		},
		"no facet for enable": {
			[]string{"--mode=enable", "--epoch=7", "--database=postgres://x"}, "--facet",
		},
		"attest with no namespace": {
			[]string{"--mode=attest", "--facet=base", "--epoch=7",
				"--database=postgres://x"},
			"Kubernetes API",
		},
		"attest with half a client certificate": {
			[]string{"--mode=attest", "--facet=base", "--epoch=7",
				"--database=postgres://x", "--namespace=cicd", "--tls-cert=/c"},
			"partially configured",
		},
	} {
		_, err := parse(t, expected.arguments...)
		if err == nil {
			t.Errorf("%s was accepted", name)

			continue
		}
		if !strings.Contains(err.Error(), expected.says) {
			t.Errorf("%s was refused without saying %q: %v", name, expected.says, err)
		}
	}
}

// `--facet=all` is only meaningful for a drain.
//
// Attesting or enabling "both facets" in one step would hide the ordering the
// whole protocol IS: output can never be ready without base, and a command that
// let an operator skip past that would be a command that made the epoch row's
// constraints the only thing enforcing it.
func TestFacetAllIsOnlyMeaningfulForADrain(t *testing.T) {
	for _, mode := range []string{"attest", "enable"} {
		_, err := parse(t, "--mode="+mode, "--facet=all", "--epoch=7",
			"--database=postgres://x", "--namespace=cicd")
		if err == nil {
			t.Errorf("--facet=all was accepted for --mode=%s", mode)

			continue
		}
		if !strings.Contains(err.Error(), "output can never be ready without base") {
			t.Errorf("the refusal for --mode=%s does not say why: %v", mode, err)
		}
	}
}

// `begin` names no facet, because there is one row and both facets start in
// `initial`. A --facet there would read as "begin this facet", which is not a
// thing.
func TestBeginNamesNoFacet(t *testing.T) {
	config, err := parse(t, "--mode=begin", "--epoch=7", "--database=postgres://x")
	if err != nil {
		t.Fatalf("begin without a facet was refused: %v", err)
	}
	if config.Facet != "" {
		t.Errorf("begin carried the facet %q", config.Facet)
	}
}

// An unknown mode or facet is refused by the FLAG, before anything is built.
func TestAnUnknownModeOrFacetIsRefusedAtParse(t *testing.T) {
	if _, err := parse(t, "--mode=turn-it-on", "--epoch=7", "--database=x"); err == nil {
		t.Error("an unknown mode was accepted")
	}
	if _, err := parse(t, "--mode=enable", "--facet=everything", "--epoch=7",
		"--database=x"); err == nil {
		t.Error("an unknown facet was accepted")
	}
}

// --facet=all sets the flag AND leaves a concrete facet behind it, so a drain
// that loops over both has something to name in a message.
func TestFacetAllCarriesBothTheFlagAndAFacet(t *testing.T) {
	config, err := parse(t, "--mode=drain", "--facet=all", "--epoch=7", "--database=x")
	if err != nil {
		t.Fatalf("drain --facet=all was refused: %v", err)
	}
	if !config.All {
		t.Error("--facet=all did not set All")
	}
	if config.Facet != activation.FacetOutput {
		t.Errorf("--facet=all left the facet %q; the loop takes OUTPUT down first, because "+
			"base is what settles a capture and a plane that lost it mid-capture could finish "+
			"nothing", config.Facet)
	}
}

func TestIntegrityReconciliationRequiresOneRuntimeFinding(t *testing.T) {
	valid := Config{DSN: "postgres://localhost/test", Epoch: 1, Mode: ModeReconcileIntegrity,
		IntegrityViolation: "out_of_band_absence", IntegritySubject: "gs://bucket/key@generation"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Epoch = 0 },
		func(c *Config) { c.IntegrityViolation = "lifecycle_delete_rule" },
		func(c *Config) { c.IntegrityViolation = "" },
		func(c *Config) { c.IntegritySubject = "" },
		func(c *Config) { c.Mode = ModeBegin },
	} {
		invalid := valid
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Errorf("accepted unscoped reconciliation: %+v", invalid)
		}
	}
}
