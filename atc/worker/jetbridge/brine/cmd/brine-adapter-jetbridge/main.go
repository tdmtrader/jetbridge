// brine-adapter-jetbridge hosts JetBridge's registry and resources using
// Brine's document-native execution machinery (contract 5).
//
// `--document <path>` is the only selection input for check and run; a bare
// `--document` on catalog asks for the registry document instead of the
// catalog; `--hold` on run is apparatus whose presence, not value, is read.
// The flags-era tokens (--features, --tags, --exclude-tags, --line,
// --resources) are not parsed: brine-dispatch stopped emitting them at the
// contract-5 mint.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

// adapterName is what a manifest reaches this adapter by, and what every
// diagnostic on stderr prefixes itself with. NOT brine.AdapterName: that
// constant is the official Go runner's own name, and a jetbridge refusal that
// signed itself `brine-adapter-go` would send a reader to the wrong repository.
const adapterName = "brine-adapter-jetbridge"

func main() {
	// Every exit below lands in exitAfterSweep, which disposes and sweeps.
	// A panic is the one path that would not, so it is given one here.
	defer exitAfterPanic()
	run()
}

func run() {
	// After ProtectEventStream, fd 1 is stderr, so every diagnostic this
	// process writes is an fd-2 write. Go turns EPIPE on fd 1 or 2 into
	// SIGPIPE: death with no defers, before exitAfterSweep. That is exactly
	// the moment the CLI or engine has died and the pipe is gone -- the one
	// case where nobody else will signal the daemons this process started.
	// Ignored, the write just returns an error and the sweep still runs.
	signal.Ignore(syscall.SIGPIPE)

	// Before anything can start a resource. The drain is installed for every
	// subcommand: catalog and check acquire nothing today (neither
	// Pipeline.Catalog nor Pipeline.Check calls Require), and an adapter whose
	// signal handling depends on that staying true is one step away from a
	// leak nobody connects to the step that caused it.
	installResourceDrain()

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
	emitter := brine.NewEmitter(steps.WithDisposalVerdict(events))
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
	state := trackResources(brine.NewResourceState(resources))
	pipeline = pipeline.WithResources(state)

	switch sub {
	case "catalog":
		if brine.HasFlag(args, "--document") {
			line, err := brine.BuildRegistryDocument(adapterName, registry, resources, nil)
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
		exitAfterSweep(0)
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
		exitAfterSweep(code)
	case "run":
		// brine.InstallSigtermDrain is NOT called here. SIGTERM belongs to
		// installResourceDrain above, which runs this adapter's whole exit
		// chain and then installs the library's handler and re-raises, so the
		// cancellation contract still owns its events and its exit code.
		// lifecycle.go says why two handlers on one signal cannot be ordered.
		//
		// Read for the refusal only, never as a narrowing: core resolved the
		// line into the document before this process existed.
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
		if err := prepareRun(plan, state, steps.StartRealCluster); err != nil {
			fail(1, "prepare run: %v", err)
		}
		_, code, err := pipeline.RunPlan(plan)
		if err != nil {
			fail(2, "run: %v", err)
		}
		exitAfterSweep(code)
	}
}

// refuse answers a document that cannot be honoured: exit 2, the reason on
// stderr, and nothing on stdout. A non-document error on the document path is
// an adapter defect and exits 1 so it cannot borrow the refusal's exit code.
func refuse(err error) {
	if !brine.IsDocumentError(err) {
		fail(1, "internal document error: %v", err)
	}
	fail(2, "%v", err)
}

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, adapterName+": "+format+"\n", args...)
	exitAfterSweep(code)
}
