// brine-adapter-jetbridge hosts the jetbridge behavioral contract as a brine
// adapter. Protocol handling is ported verbatim from brine-adapter-go
// (runners/implementations/go/cmd/brine-adapter-go/main.go); only the registry
// and the resource plane are ours.
// It speaks the Brine adapter protocol: catalog, check, and run subcommands.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "brine-adapter-jetbridge: subcommand required (catalog, check, run)")
		os.Exit(2)
	}

	// Track 0: postgresrunner uses gomega in non-test code; outside a suite
	// the fail handler is ours or gomega panics on the first assertion.
	steps.RegisterGomegaFailHandler()

	// The event stream must own stdout alone; postgresrunner does not know
	// that. See steps.ProtectEventStream.
	events, err := steps.ProtectEventStream()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: %v\n", err)
		os.Exit(2)
	}

	registry := buildAppRegistry()
	emitter := brine.NewEmitter(events)

	// AC5: parse --resources <json> from args and build a seeded pipeline if present.
	resourcesJSON := parseResourcesFlag(os.Args[2:])
	var pipeline *brine.Pipeline
	if resourcesJSON != "" {
		seed, err := brine.BuildSeed(registry, resourcesJSON)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: resources seed error: %v\n", err)
			os.Exit(2)
		}
		if seed != nil {
			pipeline = brine.NewPipelineWithSeed(registry, emitter, seed)
		} else {
			pipeline = brine.NewPipeline(registry, emitter)
		}
	} else {
		pipeline = brine.NewPipeline(registry, emitter)
	}

	// Resource plane (S3a): compose the adapter's ResourceRegistry alongside
	// the step registry. The pipeline owns the scoped lifecycle end to end:
	// eager per-scope acquisition, handles to using-steps, LIFO disposal at
	// scope exits (SIGTERM cancellation included), unknown-resource
	// validation on the check path.
	resourceRegistry, err := buildAppResources()
	if err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: resource registry error: %v\n", err)
		os.Exit(2)
	}
	pipeline = pipeline.WithResources(brine.NewResourceState(resourceRegistry))

	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "catalog":
		runCatalog(pipeline, args)
	case "check":
		runCheck(pipeline, emitter, args)
	case "run":
		runRun(pipeline, args)
	default:
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: unknown subcommand %q (expected catalog, check, or run)\n", sub)
		os.Exit(2)
	}
}

// runCatalog handles the "catalog" subcommand.
// Flags: --registry <path> (accept-and-ignore; statically compiled adapter)
func runCatalog(pipeline *brine.Pipeline, args []string) {
	parseAcceptIgnoreFlags(args)
	if err := pipeline.Catalog(); err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: catalog error: %v\n", err)
		os.Exit(2)
	}
	os.Exit(0)
}

// runCheck handles the "check" subcommand.
// Flags: --features <path>... (multi-token loop), --registry (accept-and-ignore)
// Emits the standard check envelope (check_start / scenario_check / check_end).
func runCheck(pipeline *brine.Pipeline, emitter *brine.JsonlEmitter, args []string) {
	featurePaths, _, _ := parseRunFlags(args)
	features, err := loadFeatures(featurePaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: check error loading features: %v\n", err)
		os.Exit(2)
	}

	checkStart := time.Now()
	results, exitCode, err := pipeline.Check(features)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: check error: %v\n", err)
		os.Exit(2)
	}
	durationMs := time.Since(checkStart).Milliseconds()
	if err := brine.EmitCheckResults(emitter, featurePaths, results, durationMs); err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: check emit error: %v\n", err)
		os.Exit(2)
	}
	os.Exit(exitCode)
}

// runRun handles the "run" subcommand.
// Flags: --features <path>... (multi-token loop), --tags, --exclude-tags, --line, --registry, --binary (accept-and-ignore)
func runRun(pipeline *brine.Pipeline, args []string) {
	// R3 cancellation drain: catch SIGTERM, drain the in-flight scenario's
	// disposers, emit the drain event pair, exit 143 — instead of dying by
	// default signal disposition and losing every registered disposer.
	brine.InstallSigtermDrain()

	featurePaths, tagArgs, excludeTagArgs := parseRunFlags(args)
	lineFilter := parseLineFilter(args)

	features, err := loadFeatures(featurePaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: run error loading features: %v\n", err)
		os.Exit(2)
	}

	filter := brine.TagFilter{Include: tagArgs, Exclude: excludeTagArgs}
	_, exitCode, err := pipeline.Run(features, filter, lineFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: run error: %v\n", err)
		os.Exit(2)
	}
	os.Exit(exitCode)
}

// parseRunFlags parses --features, --tags, --exclude-tags from args.
// --registry and --binary are accepted and ignored (statically compiled adapter).
// Multi-token loop: all non-flag tokens after --features are consumed.
//
// TAG VALUES ARE COMMA-JOINED BY THE CALLER. brine-dispatch builds the
// adapter's argv by pushing "--tags" and then `tags.join(",")` — ONE token —
// and every runner splits it back apart itself (rust-v3 adapter.rs,
// node-cli-v3.ts, haskell Main.hs, swift main.swift, python adapter.py).
// Go did not, until 2026-08-22: it narrowed on the literal tag "@a,@b", which
// no scenario carries, so under ALL-include semantics `--tags "@a,@b"`
// selected NOTHING — including a scenario carrying both — and
// `--exclude-tags "@a,@b"` excluded nothing at all. The multi-token form is
// still accepted, so both spellings work.
func parseRunFlags(args []string) (features, tags, excludeTags []string) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--features":
			i++
			// Multi-token loop: consume all non-flag tokens.
			for i < len(args) && !isFlag(args[i]) {
				features = append(features, args[i])
				i++
			}
		case "--tags":
			i++
			for i < len(args) && !isFlag(args[i]) {
				tags = append(tags, splitTagValue(args[i])...)
				i++
			}
		case "--exclude-tags":
			i++
			for i < len(args) && !isFlag(args[i]) {
				excludeTags = append(excludeTags, splitTagValue(args[i])...)
				i++
			}
		case "--registry", "--binary", "--resources":
			// Accept and ignore (statically compiled adapter invariant; --resources
			// is consumed globally in main() before subcommand dispatch).
			i++
			if i < len(args) && !isFlag(args[i]) {
				i++ // skip the value
			}
		case "--line":
			// Parsed separately; skip here.
			i++
			if i < len(args) && !isFlag(args[i]) {
				i++
			}
		default:
			i++
		}
	}
	return
}

// splitTagValue splits one --tags/--exclude-tags token on commas, dropping
// empties so a trailing or doubled comma cannot become a tag that matches
// nothing. A token with no comma comes back as itself.
func splitTagValue(token string) []string {
	var tags []string
	for _, tag := range strings.Split(token, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// parseLineFilter extracts the --line value from args, returning a *int or nil.
func parseLineFilter(args []string) *int {
	for i, arg := range args {
		if arg == "--line" && i+1 < len(args) {
			n, err := strconv.Atoi(args[i+1])
			if err == nil {
				return &n
			}
		}
	}
	return nil
}

// parseAcceptIgnoreFlags consumes args without acting on them.
// Used for subcommands that accept --registry but do nothing with it.
func parseAcceptIgnoreFlags(_ []string) {}

// parseResourcesFlag extracts the --resources JSON string from args.
// Returns "" if --resources is not present. Used in main() to build a seeded pipeline (AC5).
func parseResourcesFlag(args []string) string {
	for i, arg := range args {
		if arg == "--resources" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// isFlag returns true if the token starts with "--".
func isFlag(s string) bool {
	return len(s) >= 2 && s[0] == '-' && s[1] == '-'
}

// loadFeatures parses each feature file path and returns the AST slice.
func loadFeatures(paths []string) ([]*brine.ParsedFeature, error) {
	features := make([]*brine.ParsedFeature, 0, len(paths))
	for _, path := range paths {
		pf, err := brine.ParseFeatureFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", path, err)
		}
		features = append(features, pf)
	}
	return features, nil
}
