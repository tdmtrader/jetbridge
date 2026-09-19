package reclaimer_test

// The reclaimer's own refusals: the ones decided from the ref and the
// precondition before any delete is issued. The six typed outcomes of a
// delete the store answered are proved in the conformance suite; this file is
// about the one way in, and the recorder is what makes "no delete was issued"
// a fact rather than a reading of the code.

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
	testsupport "github.com/concourse/concourse/hangar/output/testsupport"
)

const (
	bucket = "reclaimer-spec"
	epoch  = executioncontrol.ActivationEpoch(7)
)

func namespaceFor(t *testing.T) output.OutputNamespace {
	return testsupport.Namespace(t, bucket, "tenant-a", epoch)
}

// role builds a reclaimer over a recorded tier-1 store. The memory comes back
// too, for the one case that seeds an object to delete.
func role(t *testing.T, namespace output.OutputNamespace) (*reclaimer.Reclaimer, *gcstest.Memory, *gcstest.Recorder) {
	t.Helper()

	memory, recorder := testsupport.RecordedMemory(bucket)
	built, err := reclaimer.New(namespace, reclaimer.Restrict(recorder.RecordDeletes(memory)))
	if err != nil {
		t.Fatalf("building the reclaimer: %v", err)
	}

	return built, memory, recorder
}

func TestNewRefusesAnIncompleteRole(t *testing.T) {
	namespace := namespaceFor(t)
	store := reclaimer.Restrict(gcstest.NewMemory())

	for name, build := range map[string]func() (*reclaimer.Reclaimer, error){
		"a zero namespace": func() (*reclaimer.Reclaimer, error) {
			return reclaimer.New(output.OutputNamespace{}, store)
		},
		"no store": func() (*reclaimer.Reclaimer, error) {
			return reclaimer.New(namespace, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			built, err := build()
			if !errors.Is(err, output.ErrIncomplete) {
				t.Fatalf("expected ErrIncomplete, got %v", err)
			}
			if built != nil {
				t.Error("a refused constructor still returned a reclaimer")
			}
		})
	}

	if _, err := reclaimer.New(namespace, store); err != nil {
		t.Fatalf("the complete shape was refused: %v", err)
	}
}

func TestDeleteExactGenerationRefusesBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t)
	ref := namespace.Ref(testsupport.Digest("ab"), 3)

	// A precondition about a generation other than the one the ref names is
	// an unconditional delete with extra steps: it would remove whatever is
	// at the key so long as it is not the thing the caller was asked about.
	t.Run("a precondition naming another generation", func(t *testing.T) {
		built, _, recorder := role(t, namespace)
		outcome, err := built.DeleteExactGeneration(ctx, ref, output.DeletePrecondition{
			Generation:     ref.Generation + 1,
			Metageneration: 1,
		})
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		if outcome != output.DeleteInfrastructure {
			t.Errorf("outcome %q; a refused delete is not any kind of reclamation", outcome)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a precondition with no generation", func(t *testing.T) {
		built, _, recorder := role(t, namespace)
		outcome, err := built.DeleteExactGeneration(ctx, ref, output.DeletePrecondition{Metageneration: 1})
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		if outcome != output.DeleteInfrastructure {
			t.Errorf("outcome %q; a refused delete is not any kind of reclamation", outcome)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a ref that does not validate", func(t *testing.T) {
		built, _, recorder := role(t, namespace)
		outcome, err := built.DeleteExactGeneration(ctx, hangar.TreeRef{Scope: namespace.Scope()},
			output.DeletePrecondition{Generation: 3, Metageneration: 1})
		if err == nil {
			t.Error("an incomplete ref was accepted")
		}
		if outcome != output.DeleteInfrastructure {
			t.Errorf("outcome %q; a refused delete is not any kind of reclamation", outcome)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})
}

func TestTheOneDeleteIsPinnedAndConditioned(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t)
	digest := testsupport.Digest("cd")
	built, memory, recorder := role(t, namespace)

	key, err := hangar.TreeKey(namespace.Prefix(), namespace.Scope(), digest)
	if err != nil {
		t.Fatalf("deriving the key: %v", err)
	}
	seeded := memory.Seed(bucket, key, []byte("tree"), nil)

	// The precondition is the generation and only the generation: a
	// metageneration the registration recorded is evidence about the object,
	// not a condition on removing it.
	outcome, err := built.DeleteExactGeneration(ctx, namespace.Ref(digest, seeded.Generation),
		output.DeletePrecondition{Generation: seeded.Generation, Metageneration: seeded.Metageneration + 5})
	if err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if outcome != output.DeleteConfirmed {
		t.Errorf("outcome %q, expected %q", outcome, output.DeleteConfirmed)
	}
	if kinds := recorder.Kinds(); len(kinds) != 1 || kinds[0] != "objects.delete" {
		t.Errorf("the delete issued %v; it is one conditional delete and nothing else", kinds)
	}
	if _, present := memory.Body(bucket, key); present {
		t.Error("the object is still there after a confirmed delete")
	}

	// And a second delete of the same exact generation is absence, which
	// this method reports and does not interpret.
	outcome, err = built.DeleteExactGeneration(ctx, namespace.Ref(digest, seeded.Generation),
		output.DeletePrecondition{Generation: seeded.Generation, Metageneration: 1})
	if err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if outcome != output.DeleteAlreadyAbsent {
		t.Errorf("outcome %q, expected %q", outcome, output.DeleteAlreadyAbsent)
	}
}
