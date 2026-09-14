// brine-adapter-jetbridge hosts the jetbridge behavioral contract as a brine
// adapter. Protocol handling is ported verbatim from brine-adapter-go
// (runners/implementations/go/cmd/brine-adapter-go/main.go); only the registry
// and the resource plane are ours.
// It speaks the Brine adapter protocol: catalog, check, and run subcommands.
//
// DOCUMENT-NATIVE SINCE CONTRACT 5 (brine lane C5-5, 2026-09-07). `--document`
// is THE ONE FLAG THAT IS BOTH SHAPES:
//
//	run / check   `--document <path>` — the EXECUTION DOCUMENT core hands
//	              this adapter, and the ONLY way to say what runs. The
//	              features come from the document's own embedded AST, one
//	              directive per scenario, and no feature file is opened. It
//	              REPLACED `--features`, `--tags`, `--exclude-tags`,
//	              `--line` and `--resources`, which this binary no longer
//	              parses at all and which brine-dispatch no longer emits.
//
//	catalog       a BARE `--document` — the request that this adapter
//	              declare its REGISTRY DOCUMENT instead of its catalog.
//	              Bare `catalog` is byte-identical to what it always was.
//
//	run           `--hold` — APPARATUS, and the second half of a staged run.
//	              Its PRESENCE is what is read: the `<file:line>` it is
//	              spelled with is a narrowing core already resolved into the
//	              document.
//
// WHY THIS FILE CHANGED AT ALL. brine-dispatch deletes the five legacy
// selection tokens UNCONDITIONALLY — there is no contract-3 compatibility
// branch in dispatch/src/lib.rs — so the previous argv reading received a
// `--document` it did not know and no `--features` at all, which is a run over
// zero scenarios. `Pipeline.Run` also lost its line-filter parameter in the
// same era, so the old call did not compile against the new module either.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

// adapterName is what a manifest reaches this adapter by, and what every
// diagnostic on stderr prefixes itself with. NOT brine.AdapterName: that
// constant is the official Go runner's own name, and a jetbridge refusal that
// signed itself `brine-adapter-go` would send a reader to the wrong repository.
const adapterName = "brine-adapter-jetbridge"

// exitAfterSweep is the only way this adapter leaves.
//
// A daemon fixture makes directories outside the tree -- a built binary, a node
// storage root -- and `os.Exit` runs no defers, so every exit path has to sweep
// explicitly or the bytes stay. 571 of them, 42 GB, were found in one user's
// temp directory before this existed.
//
// A LEAK FAILS THE RUN. Reporting it and exiting 0 is how a fixture leak
// survives for five days: nobody reads a warning on a green run, and the cost
// lands on whoever's disk fills up next. The report names the directory and its
// size, and the bytes are already gone by then -- leaving them behind to prove
// they were there would be the leak all over again.
func exitAfterSweep(code int) {
	leaks := steps.SweepAdapterDaemonRoots()
	for _, leak := range leaks {
		fmt.Fprintln(os.Stderr, "brine-adapter-jetbridge: temp leak:", leak)
	}
	if len(leaks) != 0 && code == 0 {
		code = 1
	}
	os.Exit(code)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "%s: subcommand required (catalog, check, run)\n", adapterName)
		exitAfterSweep(2)
	}

	// Track 0: postgresrunner uses gomega in non-test code; outside a suite
	// the fail handler is ours or gomega panics on the first assertion.
	steps.RegisterGomegaFailHandler()

	// The event stream must own stdout alone; postgresrunner does not know
	// that. See steps.ProtectEventStream.
	events, err := steps.ProtectEventStream()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", adapterName, err)
		exitAfterSweep(2)
	}

	registry := buildAppRegistry()
	emitter := brine.NewEmitter(events)

	sub := os.Args[1]
	args := os.Args[2:]

	// THE DOCUMENT, read once for both verbs, and the ONLY thing on the argv
	// that says what runs.
	var document brine.ExecutionDocument
	if sub == "run" || sub == "check" {
		switch state, path := brine.DocumentArg(args); state {
		case brine.DocumentAbsent:
			// A VERB WITH NO DOCUMENT HAS NO SELECTION. There is no legacy
			// path left to fall through to, and a run that shrugged here would
			// announce a run_start over zero features and exit 0 — the quiet
			// success over nothing that this pipeline's own `brine run`
			// comment exists to prevent.
			refuse(&brine.DocumentError{Message: brine.AbsentDocumentMessage})
		case brine.DocumentBare:
			// THREE STATES, NOT TWO: a `--document` riding with nothing after
			// it is not the same as a flag that was never there, and neither
			// is a selection.
			refuse(&brine.DocumentError{Message: brine.PathlessDocumentMessage})
		case brine.DocumentPath:
			read, err := brine.ReadDocument(path)
			if err != nil {
				refuse(err)
			}
			document = read
		}
	}

	// AC5: the seed, and `manifest.resources` is its ONE source. The
	// `--resources` token that used to be the other one is gone from
	// dispatch's argv and gone from this binary's reading, so there is no
	// "otherwise" left to write.
	resourcesJSON := ""
	if document != nil {
		if seed, ok := document.DocumentSeedJSON(); ok {
			resourcesJSON = seed
		}
	}
	var pipeline *brine.Pipeline
	if resourcesJSON != "" {
		seed, err := brine.BuildSeed(registry, resourcesJSON)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: resources seed error: %v\n", adapterName, err)
			exitAfterSweep(2)
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
		fmt.Fprintf(os.Stderr, "%s: resource registry error: %v\n", adapterName, err)
		exitAfterSweep(2)
	}
	pipeline = pipeline.WithResources(brine.NewResourceState(resourceRegistry))

	switch sub {
	case "catalog":
		runCatalog(pipeline, registry, resourceRegistry, args)
	case "check":
		runCheck(pipeline, emitter, document)
	case "run":
		runRun(pipeline, document, args)
	default:
		fmt.Fprintf(os.Stderr, "%s: unknown subcommand %q (expected catalog, check, or run)\n", adapterName, sub)
		exitAfterSweep(2)
	}
}

// refuse answers a document that cannot be honoured: exit 2, the reason on
// stderr, and NOTHING on stdout — a run that was refused never started.
//
// ONE PLACE FOR EVERY DOCUMENT REFUSAL, and one exit code. A refusal raised
// while READING the document and one raised while PROJECTING it are the same
// answer — the invocation is not the invocation anybody asked for — and must
// not be able to acquire two different exit codes.
//
// The non-document arm exits 1 rather than 2 on purpose: exit 2 with an empty
// stdout is the specific claim "the document was not honourable", and an
// adapter defect must not be able to borrow it.
func refuse(err error) {
	if !brine.IsDocumentError(err) {
		fmt.Fprintf(os.Stderr,
			"%s: internal error on the document path (not a refusal the document earned): %v\n",
			adapterName, err)
		exitAfterSweep(1)
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", adapterName, err)
	exitAfterSweep(2)
}

// runCatalog handles the "catalog" subcommand.
// Flags: --registry <path> (accept-and-ignore; statically compiled adapter),
// a BARE --document (declare the registry document instead of the catalog).
func runCatalog(
	pipeline *brine.Pipeline,
	registry brine.StepRegistry,
	resources *brine.ResourceRegistry,
	args []string,
) {
	if brine.HasFlag(args, "--document") {
		// The registry document, and NOTHING ELSE on stdout: it is exactly one
		// line, because every other adapter channel in brine is line-framed
		// and dispatch's transport is a line callback.
		//
		// `seedState` is permanently absent here: its only source was
		// `--resources` on the CATALOG argv, and no invocation supplies one.
		line, err := brine.BuildRegistryDocument(adapterName, registry, resources, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot declare the registry: %v\n", adapterName, err)
			exitAfterSweep(2)
		}
		fmt.Println(line)
		exitAfterSweep(0)
	}
	if err := pipeline.Catalog(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: catalog error: %v\n", adapterName, err)
		exitAfterSweep(2)
	}
	exitAfterSweep(0)
}

// runCheck handles the "check" subcommand.
// Flags: --registry (accept-and-ignore), --document <path> (the execution
// document, which is the whole selection).
// Emits the standard check envelope (check_start / scenario_check / check_end).
//
// `document` is never nil here: main refuses an absent or pathless
// `--document` before this is reached.
func runCheck(pipeline *brine.Pipeline, emitter *brine.JsonlEmitter, document brine.ExecutionDocument) {
	// NO FEATURE FILE IS OPENED. The document IS the text.
	features, err := document.DocumentCheckInputs()
	if err != nil {
		refuse(err)
	}
	// ECHOED, never computed. A runner that echoes `check_start.features`
	// cannot spell the separator wrong, cannot count files where the contract
	// counts Features, and cannot derive a path core canonicalized.
	roster := document.DocumentCheckStartRoster()

	checkStart := time.Now()
	results, exitCode, err := pipeline.Check(features)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: check error: %v\n", adapterName, err)
		exitAfterSweep(2)
	}
	durationMs := time.Since(checkStart).Milliseconds()
	if err := brine.EmitCheckResultsWithRoster(emitter, features, results, durationMs, roster); err != nil {
		fmt.Fprintf(os.Stderr, "%s: check emit error: %v\n", adapterName, err)
		exitAfterSweep(2)
	}
	exitAfterSweep(exitCode)
}

// runRun handles the "run" subcommand.
// Flags: --registry, --binary (accept-and-ignore), --document <path> (the
// execution document, which IS the selection), --hold (apparatus).
//
// `document` is never nil here: main refuses an absent or pathless
// `--document` before this is reached.
func runRun(pipeline *brine.Pipeline, document brine.ExecutionDocument, args []string) {
	// R3 cancellation drain: catch SIGTERM, drain the in-flight scenario's
	// disposers, emit the drain event pair, exit 143 — instead of dying by
	// default signal disposition and losing every registered disposer.
	brine.InstallSigtermDrain()

	// THE LINE REFUSAL. Read FOR THE REFUSAL ONLY, never as a narrowing: core
	// resolved the line before this document existed, and re-selecting on it
	// would be the dual-path violation the plan type forbids structurally.
	// Under a document there is no file to compute the emptiness from, so the
	// document remembers what was ASKED and the refusal names the number the
	// caller typed instead of degrading into a silent exit 0.
	if line, ok := document.DocumentRequestedLine(); ok {
		empty, err := document.DocumentNamesNoScenario()
		if err != nil {
			refuse(err)
		}
		if empty {
			fmt.Fprintf(os.Stderr, "No scenario found at line %d\n", line)
			exitAfterSweep(1)
		}
	}

	// `--hold` IS APPARATUS. The flag says the run HOLDS; the document says
	// what RUNS; neither can say the other's half. Its VALUE is not read —
	// `--hold <file:line>` is a narrowing spelling core has already resolved
	// into the document, so re-reading it here would be the dual-path
	// violation. Its PRESENCE is the intent.
	plan, err := document.DocumentPlanWithHold(brine.HasFlag(args, "--hold"))
	if err != nil {
		refuse(err)
	}

	_, exitCode, err := pipeline.RunPlan(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: run error: %v\n", adapterName, err)
		exitAfterSweep(2)
	}
	exitAfterSweep(exitCode)
}
