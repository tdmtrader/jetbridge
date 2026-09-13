package main

// What this binary is: flags, a derived namespace, one role, one runner.
//
// 900 lines of `package main` across the three controllers had no test at all,
// at a checkpoint whose subject is controller isolation. Most of the composition
// has moved into atc/hangaroutput/inventorypass, where atc/db's specs drive it
// against real PostgreSQL and a real store; what is left here is the part a main
// is supposed to be, and this is what covers it.
//
// Lease acquisition and the bounded unit are NOT re-covered here. They are
// proved through the production controller.Runner in
// atc/db/hangar_output_liveness_test.go and through the production pass in
// atc/db/hangar_output_controller_pass_test.go, both against real PostgreSQL. A
// second description of them driven by a fake would be a second thing to keep in
// step, and the one nothing runs is the one that drifts.

import (
	"context"
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
)

func TestTheInventoryFlagsDefaultToTheFrozenValues(t *testing.T) {
	config := controllerConfig{}
	flags := flag.NewFlagSet("inventory", flag.ContinueOnError)
	config.bind(flags)

	if err := flags.Parse(nil); err != nil {
		t.Fatalf("parsing no arguments: %v", err)
	}

	// Each of these is a frozen default and not a convenience. A zero interval
	// is a worker one missed notification silences until a restart; a grace
	// below the capture deadline plus an hour lets adoption race a capture that
	// is still legitimately retrying; and only the strict native GCS profile is
	// admitted at all.
	if config.Interval != output.WorkerFallbackInterval {
		t.Errorf("the default interval is %s, expected %s",
			config.Interval, output.WorkerFallbackInterval)
	}
	if config.Grace != output.DefaultPublicationGrace {
		t.Errorf("the default publication grace is %s, expected %s",
			config.Grace, output.DefaultPublicationGrace)
	}
	if config.Store != output.StoreGCS {
		t.Errorf("the default store profile is %q, expected %q", config.Store, output.StoreGCS)
	}
}

func TestTheInventoryFlagsAreRead(t *testing.T) {
	config := controllerConfig{}
	flags := flag.NewFlagSet("inventory", flag.ContinueOnError)
	config.bind(flags)

	if err := flags.Parse([]string{
		"-database", "postgres://example",
		"-output-bucket", "output-bucket",
		"-output-prefix", "deployments/blue",
		"-output-tenant", "tenant-a",
		"-activation-epoch", "7",
		"-interval", "20s",
		"-publication-grace", "200h",
	}); err != nil {
		t.Fatalf("parsing: %v", err)
	}

	for _, field := range []struct {
		name       string
		got, wants any
	}{
		{"database", config.DSN, "postgres://example"},
		{"output-bucket", config.Bucket, "output-bucket"},
		{"output-prefix", config.Prefix, "deployments/blue"},
		{"output-tenant", config.Tenant, "tenant-a"},
		{"activation-epoch", config.ActivationEpoch, int64(7)},
		{"interval", config.Interval, 20 * time.Second},
		{"publication-grace", config.Grace, 200 * time.Hour},
	} {
		if field.got != field.wants {
			t.Errorf("-%s parsed as %v, expected %v", field.name, field.got, field.wants)
		}
	}
}

// The namespace is DERIVED and never a request field: the bucket, the prefix
// and the tenant are authenticated server configuration, and no caller anywhere
// in this plane can choose one. A binary started without them must refuse
// rather than derive something.
func TestTheInventoryNamespaceIsRefusedWhenItIsNotFullyConfigured(t *testing.T) {
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
		{"prefix", func(config *controllerConfig) { config.Prefix = "../escape" }},
		{"tenant", func(config *controllerConfig) { config.Tenant = "" }},
		{"activation epoch", func(config *controllerConfig) { config.ActivationEpoch = 0 }},
		{"store profile", func(config *controllerConfig) { config.Store = "s3" }},
	} {
		// An EMPTY deployment prefix is not in this list and that is not an
		// oversight: it means the bucket root, which a dedicated bucket
		// legitimately has. What is refused is a prefix that is not a prefix.
		config := complete
		missing.mutate(&config)
		if _, err := config.namespace(); err == nil {
			t.Errorf("a namespace was derived with no %s", missing.name)
		}
	}
}

// The role this binary links, built the way run() builds it: list and stat, and
// no writer and no delete, because those calls do not exist to make.
func TestTheInventoryRoleIsBuiltOverItsOwnNamespace(t *testing.T) {
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

	clock := output.ClockFunc(func() time.Time { return time.Now().UTC() })
	if _, err := inventory.New(namespace, nil, clock); err == nil {
		t.Error("an inventory was built with no store at all")
	}
	if _, err := inventory.New(output.OutputNamespace{}, inventory.Restrict(nil), clock); err == nil {
		t.Error("an inventory was built with no derived namespace; the prefix it lists under " +
			"comes from there and from nowhere a caller can reach")
	}
}

// Startup refuses a grace that could race a capture, BEFORE it opens anything.
//
// run() validates in that order on purpose: a deployment misconfigured this way
// must fail to start rather than start and adopt objects whose captures are
// still retrying.
func TestTheInventoryRefusesAGraceThatCouldRaceACaptureBeforeItOpensAnything(t *testing.T) {
	config := controllerConfig{
		DSN:             "postgres://unreachable.invalid/none",
		Store:           output.StoreGCS,
		Bucket:          "output-bucket",
		Prefix:          "deployments/blue",
		Tenant:          "tenant-a",
		ActivationEpoch: 7,
		Grace:           time.Hour,
	}

	err := run(context.Background(), config)
	if err == nil {
		t.Fatal("a publication grace of one hour was accepted")
	}
	if !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("the refusal is %v; a grace below the floor is an incomplete configuration "+
			"and the message has to say which bound it missed", err)
	}
}
