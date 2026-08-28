package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/runtime"
)

// WorkerArtifactKeyDefinitions closes the one place worker_test.go was
// stronger than brine: the storage key CreateVolumeForArtifact stamps on the
// volume it returns.
//
// step-integration.feature already asserts a key equals its handle, but on the
// LookupVolume path — a different call site, which the mutation of
// CreateVolumeForArtifact never reaches.
//
// The assertion here is the harm rather than the identity. A constant key
// means every artifact on the worker addresses the same place in the store:
// the second artifact overwrites the first, and a step that asks for its own
// data is handed somebody else's.

// TwoArtifactVolumes is two artifact volumes created back to back by the same
// worker for the same team — the case where a collision would show.
type TwoArtifactVolumes struct {
	Ready  WorkerReady
	First  runtime.Volume
	Second runtime.Volume
	Err    error
}

func WorkerArtifactKeyDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[WorkerReady, TwoArtifactVolumes](
			"the worker creates two volumes for artifacts",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (TwoArtifactVolumes, error) {
				first, _, err := in.Worker.CreateVolumeForArtifact(in.Ctx, in.TeamID)
				if err != nil {
					return TwoArtifactVolumes{Ready: in, Err: err}, nil
				}
				second, _, err := in.Worker.CreateVolumeForArtifact(in.Ctx, in.TeamID)
				return TwoArtifactVolumes{Ready: in, First: first, Second: second, Err: err}, nil
			},
		),

		CheckThat[TwoArtifactVolumes]("each artifact is stored under its own key",
			func(in TwoArtifactVolumes) error {
				if in.Err != nil {
					return fmt.Errorf("creating the artifact volumes failed: %v", in.Err)
				}
				firstKey, err := artifactKeyOf(in.First, "first")
				if err != nil {
					return err
				}
				secondKey, err := artifactKeyOf(in.Second, "second")
				if err != nil {
					return err
				}
				if firstKey == secondKey {
					return fmt.Errorf(
						"both artifact volumes are stored under the key %q — they address the same "+
							"place in the store, so the second overwrites the first and a step that "+
							"asks for its own artifact is handed the other one's", firstKey)
				}
				return nil
			}),

		// The key is the handle, so an artifact can be found again from the
		// database row alone. A key derived from anything else is a key
		// nothing else can reconstruct.
		CheckThat[TwoArtifactVolumes]("each artifact's key is its own volume handle",
			func(in TwoArtifactVolumes) error {
				if in.Err != nil {
					return fmt.Errorf("creating the artifact volumes failed: %v", in.Err)
				}
				for label, vol := range map[string]runtime.Volume{"first": in.First, "second": in.Second} {
					key, err := artifactKeyOf(vol, label)
					if err != nil {
						return err
					}
					if key != vol.Handle() {
						return fmt.Errorf(
							"the %s artifact's key is %q but its volume handle is %q — nothing holding "+
								"the database row can work out where the artifact was put",
							label, key, vol.Handle())
					}
				}
				return nil
			}),
	}
}

func artifactKeyOf(vol runtime.Volume, label string) (string, error) {
	if vol == nil {
		return "", fmt.Errorf("no %s artifact volume came back", label)
	}
	keyed, ok := vol.(interface{ Key() string })
	if !ok {
		return "", fmt.Errorf("the %s artifact volume (%T) carries no storage key", label, vol)
	}
	return keyed.Key(), nil
}
