package hangaroutput_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// A6. An output-plane controller started before its epoch's row exists waits
// as a non-owner, and claims once the row is there.
//
// The lease row's activation_epoch is a foreign key to the epoch row, so the
// claim's INSERT is refused while there is none. That refusal has to read as
// "not the owner this minute" rather than as a failure: the chart starts the
// inventory and reclaimer controllers in the same sync whose PostSync walk
// begins the row, and a controller that exited on it would hold the sync
// unhealthy so that the walk never ran.
func TestAControllerWaitsAsANonOwnerUntilItsEpochHasARow(t *testing.T) {
	const epoch = executioncontrol.ActivationEpoch(111)

	for _, kind := range []output.OperationKind{
		output.OperationInventory,
		output.OperationReclaimAdmission,
		output.OperationReclaimDelete,
	} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			epochs, conn := activationFixture(t)

			consumer, err := db.HangarConsumerPrefixHeld("controller-epoch-spec")
			if err != nil {
				t.Fatal(err)
			}
			var classes []string
			passes := 0
			runner := &controller.Runner{
				Kind:            kind,
				ActivationEpoch: int64(epoch),
				OwnerID:         uuid.NewString(),
				Term:            output.MinLeaseTerm,
				Transactor:      controller.SQLTransactor{DB: conn, CommitError: db.HangarCommitError},
				Leases:          db.NewHangarOutputRepository(consumer),
				Pass: controller.PassFunc(func(context.Context, output.OperationLease) (int, error) {
					passes++

					return 0, nil
				}),
				Reporter: controller.ReporterFunc(func(_ context.Context, _ output.OperationKind, _ int, class string) {
					classes = append(classes, class)
				}),
			}

			for wake := 0; wake < 2; wake++ {
				if err := runner.Run(ctx); err != nil {
					t.Fatalf("wake %d with no epoch row returned an error, which the "+
						"controller's loop would exit on: %v", wake, err)
				}
			}
			if runner.Holds() || passes != 0 {
				t.Errorf("with no epoch row the runner holds=%v and ran %d pass(es)",
					runner.Holds(), passes)
			}
			for _, class := range classes {
				if class != "not_owner" {
					t.Errorf("with no epoch row a wake reported %q, not not_owner", class)
				}
			}

			mustBegin(t, epochs, epoch)
			if err := runner.Run(ctx); err != nil {
				t.Fatalf("the first wake after begin: %v", err)
			}
			if !runner.Holds() || passes != 1 {
				t.Errorf("the first wake after begin holds=%v and ran %d pass(es); it should "+
					"claim the lease and run", runner.Holds(), passes)
			}
		})
	}
}
