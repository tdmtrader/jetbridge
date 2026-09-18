// brine-adapter-jetbridge hosts JetBridge's registry and resources using
// Brine's document-native execution machinery.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

func main() {
	if len(os.Args) < 2 {
		fail(2, "subcommand required (catalog, check, run)")
	}
	sub, args := os.Args[1], os.Args[2:]
	if sub != "catalog" && sub != "check" && sub != "run" {
		fail(2, "unknown subcommand %q (expected catalog, check, or run)", sub)
	}

	// Reject invalid requests before creating resources or emitting events.
	var document brine.ExecutionDocument
	if sub != "catalog" {
		switch state, path := brine.DocumentArg(args); state {
		case brine.DocumentAbsent:
			fail(2, "%s", brine.AbsentDocumentMessage)
		case brine.DocumentBare:
			fail(2, "%s", brine.PathlessDocumentMessage)
		case brine.DocumentPath:
			var err error
			document, err = brine.ReadDocument(path)
			if err != nil {
				refuse(err)
			}
		}
	}

	steps.RegisterGomegaFailHandler()
	events, err := steps.ProtectEventStream()
	if err != nil {
		fail(2, "protect event stream: %v", err)
	}
	registry := buildAppRegistry()
	resources, err := buildAppResources()
	if err != nil {
		fail(2, "resource registry: %v", err)
	}
	emitter := brine.NewEmitter(events)
	pipeline := brine.NewPipeline(registry, emitter)
	if document != nil {
		if encoded, present := document.DocumentSeedJSON(); present {
			seed, err := brine.BuildSeed(registry, encoded)
			if err != nil {
				fail(2, "resource seed: %v", err)
			}
			if seed != nil {
				pipeline = brine.NewPipelineWithSeed(registry, emitter, seed)
			}
		}
	}
	pipeline = pipeline.WithResources(brine.NewResourceState(resources))

	switch sub {
	case "catalog":
		if brine.HasFlag(args, "--document") {
			line, err := brine.BuildRegistryDocument("jetbridge", registry, resources, nil)
			if err != nil {
				fail(2, "registry document: %v", err)
			}
			// fd 1 now belongs to diagnostics. The protocol's saved writer
			// owns catalog documents just as it owns the event stream.
			if _, err := fmt.Fprintln(events, line); err != nil {
				fail(2, "write registry document: %v", err)
			}
		} else if err := pipeline.Catalog(); err != nil {
			fail(2, "catalog: %v", err)
		}
	case "check":
		inputs, err := document.DocumentCheckInputs()
		if err != nil {
			refuse(err)
		}
		start := time.Now()
		results, code, err := pipeline.Check(inputs)
		if err != nil {
			fail(2, "check: %v", err)
		}
		if err := brine.EmitCheckResultsWithRoster(emitter, inputs, results,
			time.Since(start).Milliseconds(), document.DocumentCheckStartRoster()); err != nil {
			fail(2, "emit check results: %v", err)
		}
		os.Exit(code)
	case "run":
		brine.InstallSigtermDrain()
		if line, requested := document.DocumentRequestedLine(); requested {
			empty, err := document.DocumentNamesNoScenario()
			if err != nil {
				refuse(err)
			}
			if empty {
				fail(1, "No scenario found at line %d", line)
			}
		}
		plan, err := document.DocumentPlanWithHold(brine.HasFlag(args, "--hold"))
		if err != nil {
			refuse(err)
		}
		_, code, err := pipeline.RunPlan(plan)
		if err != nil {
			fail(2, "run: %v", err)
		}
		os.Exit(code)
	}
}

func refuse(err error) {
	if !brine.IsDocumentError(err) {
		fail(1, "internal document error: %v", err)
	}
	fail(2, "%v", err)
}

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "brine-adapter-jetbridge: "+format+"\n", args...)
	os.Exit(code)
}
