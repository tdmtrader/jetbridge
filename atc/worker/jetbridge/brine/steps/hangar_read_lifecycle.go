package steps

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type ReadWorkOutcome struct {
	Err, WorkContext, Deadline error
}

// The lease, signer, HTTP handler/client and subprocess are real. Releasing a
// committed lease makes the production renewal path refuse it; no response or
// profile is substituted. The parent deadline only bounds a broken renewal loop.
func HangarReadLifecycleDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[BoundOutput, ReadWorkOutcome]("a live read lease is released while its work waits",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (ReadWorkOutcome, error) {
				warrant, err := managedReadWarrant(in)
				if err != nil {
					return ReadWorkOutcome{}, err
				}
				public, private, err := ed25519.GenerateKey(nil)
				if err != nil {
					return ReadWorkOutcome{}, err
				}
				signer, err := output.NewCaptureStatementSigner(private)
				if err != nil {
					return ReadWorkOutcome{}, err
				}
				clock := output.ClockFunc(func() time.Time { return time.Now().UTC() })
				verifier, err := output.NewReadWarrantVerifier(brineReadWarrantKey, clock)
				if err != nil {
					return ReadWorkOutcome{}, err
				}
				minter, err := output.NewReadWarrantSigner(brineReadWarrantKey)
				if err != nil {
					return ReadWorkOutcome{}, err
				}
				const node executioncontrol.NodeUID = "read-node"
				const keyID = "read-node-key"
				plane := in.Tree.Outcome.Plane
				control := &hangaroutput.LeaseControl{
					Transactor: brineTransactor{conn: plane.DB.Conn}, Leases: plane.Repository,
					Warrants: verifier, Minter: minter, Clock: clock,
					Keys: hangaroutput.NodeKeysFunc(func(got executioncontrol.NodeUID, key string) (ed25519.PublicKey, error) {
						if got != node || key != keyID {
							return nil, errors.New("unknown node signing key")
						}
						return public, nil
					}),
				}
				server := httptest.NewServer(control.Handler())
				defer server.Close()
				client := &output.LeaseControlClient{
					BaseURL: server.URL, HTTP: server.Client(), NodeUID: node,
					KeyID: keyID, Signer: signer, Clock: clock,
				}
				profile, err := output.NewLeaseReadProfile(client, warrant.Token, 10*time.Minute)
				if err != nil {
					return ReadWorkOutcome{}, err
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, err := profile.Admit(ctx, in.Tree.Ref, "consumer", "input-0"); err != nil {
					return ReadWorkOutcome{}, fmt.Errorf("live read admission: %w", err)
				}
				// Positive control: this same profile can renew before losing its lease.
				if err := profile.Renew(ctx); err != nil {
					return ReadWorkOutcome{}, fmt.Errorf("live lease renewal: %w", err)
				}
				outcome := ReadWorkOutcome{}
				outcome.Err = hangar.RenewWhile(ctx, profile, 10*time.Millisecond, func(work context.Context) error {
					if err := profile.Release(ctx, nil); err != nil {
						return fmt.Errorf("release the lease: %w", err)
					}
					err := exec.CommandContext(work, "sleep", "60").Run()
					outcome.WorkContext = work.Err()
					return err
				})
				outcome.Deadline = ctx.Err()
				return outcome, nil
			}),
		CheckThat[ReadWorkOutcome]("the read work stops on renewal refusal without waiting for its own timeout",
			func(in ReadWorkOutcome) error {
				if in.Deadline != nil || !errors.Is(in.WorkContext, context.Canceled) || !errors.Is(in.Err, output.ErrUnauthorized) {
					return fmt.Errorf("renewal must stop the work: work context=%v, parent deadline=%v, result=%v",
						in.WorkContext, in.Deadline, in.Err)
				}
				return nil
			}),
	}
}
