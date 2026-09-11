package main

// The only binary in this system that deletes a published object, at the level
// a main is responsible for: flags, a derived namespace, one narrowed role, and
// the two runners it wires.
//
// The delete BOUNDARY is not argued here. It is measured by
// TestOnlyTheReclaimerBinaryCanInvokeAnOutputDelete against the real build
// graph: this binary is the only main that links hangar/gcsdelete, which is the
// only package that can construct a deleter over a real cloud client, and
// objectstore.Handle carries no Delete at all. Lease algebra and the bounded
// units are proved in atc/db against real PostgreSQL through the production
// passes.

import (
	"context"
	"errors"
	"flag"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

func TestTheReclaimerFlagsDefaultToTheFrozenValues(t *testing.T) {
	config := controllerConfig{}
	flags := flag.NewFlagSet("reclaimer", flag.ContinueOnError)
	config.bind(flags)

	if err := flags.Parse(nil); err != nil {
		t.Fatalf("parsing no arguments: %v", err)
	}

	if config.Interval != output.WorkerFallbackInterval {
		t.Errorf("the default interval is %s, expected %s",
			config.Interval, output.WorkerFallbackInterval)
	}
	if config.Grace != output.DefaultPublicationGrace {
		t.Errorf("the default publication grace is %s, expected %s",
			config.Grace, output.DefaultPublicationGrace)
	}
	if config.Batch <= 0 {
		t.Errorf("the default batch is %d; a pass that drained its whole backlog would hold "+
			"every other operation behind its slowest item", config.Batch)
	}

	// The lease term is DERIVED from the delete timeout rather than configured
	// beside it, so a deployment cannot be configured into a state where a
	// reclaimer's lease can expire mid-delete.
	term := output.LeaseTermFor(config.DeleteTimeout)
	if term < config.DeleteTimeout+output.LeaseTermMargin {
		t.Errorf("a delete timeout of %s derives a lease term of %s, which does not cover it "+
			"plus the %s margin", config.DeleteTimeout, term, output.LeaseTermMargin)
	}
	if !output.MayStartWork(term, config.DeleteTimeout) {
		t.Errorf("work could not begin on a freshly claimed lease of %s for a delete of %s",
			term, config.DeleteTimeout)
	}
}

func TestTheReclaimerFlagsAreRead(t *testing.T) {
	config := controllerConfig{}
	flags := flag.NewFlagSet("reclaimer", flag.ContinueOnError)
	config.bind(flags)

	if err := flags.Parse([]string{
		"-database", "postgres://example",
		"-output-bucket", "output-bucket",
		"-activation-epoch", "7",
		"-delete-timeout", "90s",
		"-batch", "3",
	}); err != nil {
		t.Fatalf("parsing: %v", err)
	}

	for _, field := range []struct {
		name       string
		got, wants any
	}{
		{"database", config.DSN, "postgres://example"},
		{"output-bucket", config.Bucket, "output-bucket"},
		{"activation-epoch", config.ActivationEpoch, int64(7)},
		{"delete-timeout", config.DeleteTimeout, 90 * time.Second},
		{"batch", config.Batch, 3},
	} {
		if field.got != field.wants {
			t.Errorf("-%s parsed as %v, expected %v", field.name, field.got, field.wants)
		}
	}
}

func TestTheReclaimerNamespaceIsRefusedWhenItIsNotFullyConfigured(t *testing.T) {
	complete := controllerConfig{
		Store:           output.StoreGCS,
		Bucket:          "output-bucket",
		Prefix:          "deployments/blue",
		Tenant:          "tenant-a",
		ActivationEpoch: 7,
	}
	if _, err := complete.namespace(); err != nil {
		t.Fatalf("a fully configured namespace was refused: %v", err)
	}

	for _, missing := range []struct {
		name   string
		mutate func(*controllerConfig)
	}{
		{"bucket", func(config *controllerConfig) { config.Bucket = "" }},
		{"tenant", func(config *controllerConfig) { config.Tenant = "" }},
		{"activation epoch", func(config *controllerConfig) { config.ActivationEpoch = 0 }},
		{"store profile", func(config *controllerConfig) { config.Store = "s3" }},
	} {
		config := complete
		missing.mutate(&config)
		if _, err := config.namespace(); err == nil {
			t.Errorf("a namespace was derived with no %s", missing.name)
		}
	}
}

func TestTheReclaimerRoleNeedsBothANamespaceAndAStore(t *testing.T) {
	config := controllerConfig{
		Store:           output.StoreGCS,
		Bucket:          "output-bucket",
		Prefix:          "deployments/blue",
		Tenant:          "tenant-a",
		ActivationEpoch: 7,
	}
	namespace, err := config.namespace()
	if err != nil {
		t.Fatalf("deriving the namespace: %v", err)
	}

	if _, err := reclaimer.New(namespace, nil); err == nil {
		t.Error("a reclaimer was built with no store at all")
	}
	if _, err := reclaimer.New(output.OutputNamespace{}, reclaimer.Restrict(nil)); err == nil {
		t.Error("a reclaimer was built with no derived namespace; the key it deletes is composed " +
			"from the namespace prefix and an exact registered ref, and from nothing a caller " +
			"supplies")
	}
}

// Startup refuses a grace that could race a capture, before it opens anything.
//
// Elapsed grace is one of reclaim admission's seven preconditions, so a
// reclaimer configured below the floor is a reclaimer that would delete objects
// whose captures could still legitimately be retrying.
func TestTheReclaimerRefusesAGraceThatCouldRaceACaptureBeforeItOpensAnything(t *testing.T) {
	config := controllerConfig{
		DSN:             "postgres://unreachable.invalid/none",
		Store:           output.StoreGCS,
		Bucket:          "output-bucket",
		Prefix:          "deployments/blue",
		Tenant:          "tenant-a",
		ActivationEpoch: 7,
		Grace:           time.Hour,
		DeleteTimeout:   2 * time.Minute,
	}

	err := run(context.Background(), config)
	if err == nil {
		t.Fatal("a publication grace of one hour was accepted")
	}
	if !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("the refusal is %v; a grace below the floor is an incomplete configuration", err)
	}
}

// The acceleration never takes the process down with it.
//
// Notification accelerates work and is never how work is found -- every worker
// in this plane has a nonzero periodic wake beside it -- so a listener that
// cannot be opened costs latency and nothing else. That is worth a test because
// the listener's own constructor PANICS when it cannot acquire a connection,
// and a pool is lazy: without a reachability check the reclaimer would crash on
// a database blip rather than run on its ticker.
func TestTheReclaimerRunsWithoutItsAccelerationRatherThanCrashing(t *testing.T) {
	ctx := lagerctx.NewContext(context.Background(), lagertest.NewTestLogger("reclaimer"))

	var accelerated controller.Acceleration
	var closeBus func()
	if r := func() (recovered any) {
		defer func() { recovered = recover() }()
		accelerated, closeBus = listenForAdmissions(ctx, "postgres://127.0.0.1:1/none", nil)

		return nil
	}(); r != nil {
		t.Fatalf("an unreachable listener panicked instead of being run past: %v", r)
	}
	defer closeBus()

	if accelerated != nil {
		t.Error("an unreachable listener came back as a working acceleration")
	}
}
