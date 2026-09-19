package steps

// The consumer's half: what the ATC binds in PostgreSQL, and what the consuming
// Pod says.
//
// Every check over BoundOutput is a PRODUCTION read, never a raw SQL select —
// a repository that writes the right row through the wrong API has to fail
// these. The claim ledger comes back through ClaimRepository.ReadClaims, which
// exists for exactly this reason; the consumer's own binding comes back through
// the neutral consumer's own reader, because a consumer's table is the
// consumer's and Hangar may not read it.
//
// The database is the real scenario-scoped PostgreSQL the estate already runs
// (steps/resources.go), reached by the fixture rather than by a phrase, and the
// repository is atc/db's own.
//
// The consumer's Pod reuses the existing PodCreated state, so the existing mount
// and volume checks compose with the one Go test this family adopts from
// storage_daemonset_test.go: the one-read-only-mount one. The init-script tests
// do NOT move — they run the generated shell with a fake wget on PATH, and
// script text is not spec.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	hangaroutputleaf "github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// The output namespace inputs this family's daemon was started with. They are
// constants here and flags there, and the pair is what lets a step derive the
// same namespace the daemon derived without asking the daemon -- which is what
// makes a stat of the published object a real stat rather than a repetition of
// an answer.
const (
	brineOutputPrefix = "brine/deployments/one"
	brineOutputTenant = "brine-tenant"
)

// brineReadWarrantKey is the output plane's materialization key for this fixture.
// It is exactly 32 raw bytes, which is what the signer requires and what makes
// "an Ed25519 receipt key cannot sign a read warrant" true by construction.
var brineReadWarrantKey = []byte("0123456789abcdef0123456789abcdef")

// freshReader is the randomness a nonce comes from. It is the real one: a
// deterministic reader would make two scenarios' nonces collide, and a nonce
// that repeats is a warrant that replays.
func freshReader() io.Reader { return rand.Reader }

// neutralConsumer is the product-neutral test consumer: a table of opaque
// bindings and the four things a consumer does with one.
//
// It is deliberately not a Run, a build, a ticket or a workflow. Hangar stores
// its binding id, compares it and hands it back, and the moment Hangar could
// read one it would know what a Run is. What the consumer keeps for itself is
// the only thing Hangar must never see: whether the binding is VISIBLE.
type neutralConsumer struct {
	Conn       db.DbConn
	Repository *db.HangarOutputRepository
}

// prepare creates the consumer's own table. It is the consumer's, not Hangar's,
// which is the point of it existing at all.
func (consumer neutralConsumer) prepare() error {
	_, err := consumer.Conn.Exec(`
		CREATE TABLE IF NOT EXISTS opaque_consumer_bindings (
			binding_id text PRIMARY KEY,
			visibility text NOT NULL,
			claim_id   uuid
		)`)

	return err
}

// bind writes the consumer's binding and acquires Hangar's claim in ONE
// transaction, which is requirement 30's whole shape.
func (consumer neutralConsumer) bind(binding string, claimID hangaroutputleaf.ClaimID, ref hangar.TreeRef, commit bool) error {
	tx, err := consumer.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)

	if _, err := tx.Exec(`
		INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
		VALUES ($1, 'hidden', $2)`, binding, string(claimID)); err != nil {
		return err
	}
	if err := consumer.Repository.AcquireClaim(context.Background(), tx,
		hangaroutputleaf.ClaimAcquisition{
			ProtocolVersion:   hangaroutputleaf.ProtocolVersion,
			ClaimID:           claimID,
			Ref:               ref,
			ConsumerBindingID: hangaroutputleaf.OpaqueID(binding),
			RequestedAt:       hangaroutputleaf.NewTimestamp(time.Now()),
		}); err != nil {
		return err
	}
	if !commit {
		// The rollback half. Both writes are in the transaction and neither
		// survives, which is the outcome the scenario names.
		return tx.Rollback()
	}

	return tx.Commit()
}

// acquire is a bare claim acquisition, for the conflict twin.
func (consumer neutralConsumer) acquire(binding string, claimID hangaroutputleaf.ClaimID, ref hangar.TreeRef) error {
	tx, err := consumer.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)

	if err := consumer.Repository.AcquireClaim(context.Background(), tx,
		hangaroutputleaf.ClaimAcquisition{
			ProtocolVersion:   hangaroutputleaf.ProtocolVersion,
			ClaimID:           claimID,
			Ref:               ref,
			ConsumerBindingID: hangaroutputleaf.OpaqueID(binding),
			RequestedAt:       hangaroutputleaf.NewTimestamp(time.Now()),
		}); err != nil {
		return err
	}

	return tx.Commit()
}

// release makes the consumer's binding unusable and gives up protection in one
// transaction, which is requirement 31's shape.
func (consumer neutralConsumer) release(binding string, claimID hangaroutputleaf.ClaimID, ref hangar.TreeRef) error {
	tx, err := consumer.Conn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)

	if _, err := tx.Exec(`
		UPDATE opaque_consumer_bindings SET visibility = 'unusable' WHERE binding_id = $1`,
		binding); err != nil {
		return err
	}
	if err := consumer.Repository.ReleaseClaim(context.Background(), tx,
		hangaroutputleaf.ClaimRelease{
			ProtocolVersion: hangaroutputleaf.ProtocolVersion,
			ClaimID:         claimID,
			Ref:             ref,
			RequestedAt:     hangaroutputleaf.NewTimestamp(time.Now()),
		}); err != nil {
		return err
	}

	return tx.Commit()
}

// publish moves the binding from hidden to published WITHOUT a second acquire.
// This method would not compile if the API required one, which is T6's rule
// stated as code rather than as a comment.
func (consumer neutralConsumer) publish(binding string) error {
	_, err := consumer.Conn.Exec(`
		UPDATE opaque_consumer_bindings SET visibility = 'published' WHERE binding_id = $1`,
		binding)

	return err
}

// visible is the consumer's own read. A binding is visible once the consumer
// has published it; Hangar is never asked and never told.
func (consumer neutralConsumer) visible(binding string) (bool, error) {
	var visibility string
	if err := consumer.Conn.QueryRow(`
		SELECT visibility FROM opaque_consumer_bindings WHERE binding_id = $1`,
		binding).Scan(&visibility); err != nil {
		return false, err
	}

	return visibility == "published", nil
}

func (consumer neutralConsumer) claimFor(binding string) (hangaroutputleaf.ClaimID, error) {
	var id string
	if err := consumer.Conn.QueryRow(`
		SELECT coalesce(claim_id::text, '') FROM opaque_consumer_bindings WHERE binding_id = $1`,
		binding).Scan(&id); err != nil {
		return "", err
	}

	return hangaroutputleaf.ClaimID(id), nil
}

// ledger reads every claim on a ref back through the repository.
func (consumer neutralConsumer) ledger(ref hangar.TreeRef) ([]hangaroutputleaf.ClaimRecord, error) {
	tx, err := consumer.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback(tx)

	return consumer.Repository.ReadClaims(context.Background(), tx, ref)
}

// consumerFor builds the neutral consumer over the plane a capture settled on.
func consumerFor(tree PublishedTree) (neutralConsumer, error) {
	plane := tree.Outcome.Plane
	if plane == nil {
		return neutralConsumer{}, fmt.Errorf(
			"this chain never settled a capture, so there is no plane to bind on")
	}
	consumer := neutralConsumer{Conn: plane.DB.Conn, Repository: plane.Repository}

	return consumer, consumer.prepare()
}

// outputStat is the REAL publisher's exact-generation stat against the bucket
// this scenario's output daemon published into.
//
// It goes through the same derived namespace the daemon derived, from the same
// authenticated inputs the fixture started it with. A stand-in would have been
// asserting the fixture's opinion of the object; requirement 35 is about the
// object.
func outputStat(daemon HangarDaemon) (hangaroutput.ExactStat, func() error, error) {
	namespace, err := hangaroutputleaf.DeriveNamespace(hangaroutputleaf.NamespaceConfig{
		Store:            hangaroutputleaf.StoreGCS,
		Bucket:           daemon.OutputBucket,
		DeploymentPrefix: brineOutputPrefix,
		TenantID:         brineOutputTenant,
		ActivationEpoch:  executioncontrol.ActivationEpoch(hangarEpoch),
	})
	if err != nil {
		return nil, nil, err
	}

	objects, closeObjects, err := hangargcs.NewObjectClient(daemon.Ctx, daemon.Endpoint)
	if err != nil {
		return nil, nil, err
	}

	stat, err := publisher.New(namespace, publisher.Restrict(objects), 30*time.Second)
	if err != nil {
		_ = closeObjects()
		return nil, nil, err
	}

	return stat, closeObjects, nil
}

// managedRead admits one read through the production admission and reports what
// happened.
func managedRead(in BoundOutput) (hangaroutputleaf.ReadLease, error) {
	plane := in.Tree.Outcome.Plane
	if plane == nil {
		return hangaroutputleaf.ReadLease{}, fmt.Errorf("this chain never settled a capture")
	}

	stat, closeStat, err := outputStat(in.Tree.Outcome.Source.Draft.Daemon)
	if err != nil {
		return hangaroutputleaf.ReadLease{}, err
	}
	defer func() { _ = closeStat() }()
	signer, err := hangaroutputleaf.NewReadWarrantSigner(brineReadWarrantKey)
	if err != nil {
		return hangaroutputleaf.ReadLease{}, err
	}
	nonce, err := hangaroutputleaf.NewReadWarrantNonce(freshReader())
	if err != nil {
		return hangaroutputleaf.ReadLease{}, err
	}

	admission := &hangaroutput.ReadAdmission{
		Transactor: brineTransactor{conn: plane.DB.Conn},
		Leases:     plane.Repository,
		Stat:       stat,
		Minter:     signer,
		Clock:      hangaroutputleaf.ClockFunc(func() time.Time { return time.Now().UTC() }),
	}

	warrant, err := admission.Admit(context.Background(), hangaroutput.ReadRequest{
		ReadLeaseID:            hangaroutputleaf.ReadLeaseID(freshUUID()),
		WarrantNonce:           nonce,
		ClaimID:                in.Acquisition.ClaimID,
		Ref:                    in.Tree.Ref,
		Destination:            hangaroutputleaf.ReadDestination{Handle: "consumer", Volume: "input-0"},
		ActivationEpoch:        executioncontrol.ActivationEpoch(hangarEpoch),
		MaterializationTimeout: 10 * time.Minute,
	})
	if err != nil {
		return hangaroutputleaf.ReadLease{}, err
	}

	return warrant.Lease, nil
}

// refusalWords is the closed vocabulary, in one place, so that a phrase taking
// a refusal by name can refuse a word this plane does not have. A check that
// accepted any string would be a check whose failing case is unreachable.
func refusalWords() []string {
	return []string{"not found", "lifecycle conflict", "unauthorized", "at risk", "expired"}
}

func knownRefusalWord(word string) bool {
	for _, member := range refusalWords() {
		if member == word {
			return true
		}
	}

	return false
}

// refusalWord maps a typed sentinel onto the word a scenario says.
//
// It is a closed mapping and not a substring search: "refused as X" has to name
// which refusal, and a check that matched on message text would pass for any
// error whose sentence happened to contain the word.
func refusalWord(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, hangaroutputleaf.ErrNotFound):
		return "not found", true
	case errors.Is(err, hangaroutputleaf.ErrConflict):
		return "lifecycle conflict", true
	case errors.Is(err, hangaroutputleaf.ErrUnauthorized):
		return "unauthorized", true
	case errors.Is(err, hangaroutputleaf.ErrAtRisk):
		return "at risk", true
	case errors.Is(err, hangaroutputleaf.ErrTimeout):
		return "expired", true
	}

	return "", false
}

// HangarBindingDefinitions is the binding and consumer-pod family.
func HangarBindingDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[PublishedTree, ConsumerDraft](
			"a later step {string} takes the published output {string} at {string}",
			[]string{"jetbridge-db", "real-cluster"},
			func(in PublishedTree, p brine.Params, rec *brine.Recorder, res brine.Resources) (ConsumerDraft, error) {
				const pattern = "a later step {string} takes the published output {string} at {string}"
				name, err := paramAt(pattern, p, 0)
				if err != nil {
					return ConsumerDraft{}, err
				}
				output, err := paramAt(pattern, p, 1)
				if err != nil {
					return ConsumerDraft{}, err
				}
				destination, err := paramAt(pattern, p, 2)
				if err != nil {
					return ConsumerDraft{}, err
				}

				// The consuming step runs on a worker with the output plane on
				// and a materialization signer, because a Hangar tree input is
				// refused outright without both -- which is itself a rule the
				// pod-shape family already covers.
				cluster, err := newHangarConsumerWorker(res, rec)
				if err != nil {
					return ConsumerDraft{}, err
				}

				return ConsumerDraft{
					Tree:        in,
					Cluster:     cluster,
					StepName:    name,
					Output:      hangaroutputleaf.OutputName(output),
					Destination: destination,
				}, nil
			},
		),

		brine.DefineMap[ConsumerDraft, PodCreated](
			"the consumer's pod is built",
			func(in ConsumerDraft, _ brine.Params, _ *brine.Recorder) (PodCreated, error) {
				return buildConsumerPod(in)
			},
		),

		brine.DefineMap[PublishedTree, BoundOutput](
			"the consumer binds the output inside its own transaction",
			func(in PublishedTree, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				consumer, err := consumerFor(in)
				if err != nil {
					return BoundOutput{}, err
				}

				bound := BoundOutput{Tree: in, Consumer: consumer, Binding: "binding-" + freshUUID()}
				bound.Acquisition.ClaimID = hangaroutputleaf.ClaimID(freshUUID())
				bound.Acquisition.Ref = in.Ref

				if in.UnregisteredRef {
					unregistered := in.Ref
					unregistered.Generation++
					bound.Acquisition.Ref = unregistered
				}

				bound.Err = consumer.bind(bound.Binding, bound.Acquisition.ClaimID,
					bound.Acquisition.Ref, true)
				if bound.Err == nil {
					bound.Claims, err = consumer.ledger(in.Ref)
					if err != nil {
						return bound, err
					}
				}

				return bound, nil
			},
		),

		// A SECOND bind, in a transaction that is rolled back. The committed one
		// above is still there, which is what makes "no claim is left behind" a
		// statement about this attempt rather than about a repository that never
		// writes a claim at all.
		brine.DefineMap[BoundOutput, BoundOutput](
			"the consumer's transaction is rolled back",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				in.RolledBackClaimID = hangaroutputleaf.ClaimID(freshUUID())
				in.RolledBackBinding = "binding-" + freshUUID()

				if err := in.Consumer.bind(in.RolledBackBinding, in.RolledBackClaimID,
					in.Tree.Ref, false); err != nil {
					return in, err
				}

				claims, err := in.Consumer.ledger(in.Tree.Ref)
				if err != nil {
					return in, err
				}
				in.Claims = claims

				return in, nil
			},
		),

		brine.DefineMap[BoundOutput, BoundOutput](
			"the binding is released",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				return releaseBinding(in)
			},
		),

		brine.DefineMap[BoundOutput, BoundOutput](
			"the binding is released again",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				return releaseBinding(in)
			},
		),

		// The conflict twin: "release is idempotent" passes for a repository
		// that ignores the claim ID entirely.
		brine.DefineMap[BoundOutput, BoundOutput](
			"the released claim ID is acquired again",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				in.Err = in.Consumer.acquire("binding-"+freshUUID(), in.Acquisition.ClaimID,
					in.Tree.Ref)

				return in, nil
			},
		),

		brine.DefineMap[BoundOutput, BoundOutput](
			"a fresh claim ID is acquired",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				fresh := hangaroutputleaf.ClaimID(freshUUID())
				in.Err = in.Consumer.acquire("binding-"+freshUUID(), fresh, in.Tree.Ref)
				if in.Err != nil {
					return in, nil
				}
				in.Acquisition.ClaimID = fresh

				claims, err := in.Consumer.ledger(in.Tree.Ref)
				if err != nil {
					return in, err
				}
				in.Claims = claims

				return in, nil
			},
		),

		brine.DefineMap[BoundOutput, BoundOutput](
			"the binding is verified",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				if err := in.Consumer.publish(in.Binding); err != nil {
					return in, err
				}
				visible, err := in.Consumer.visible(in.Binding)
				if err != nil {
					return in, err
				}
				in.Visible = visible

				return in, nil
			},
		),

		// The hidden-to-published transition, with NO second acquire.
		brine.DefineMap[BoundOutput, BoundOutput](
			"the ref moves from hidden to published",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				if err := in.Consumer.publish(in.Binding); err != nil {
					return in, err
				}
				held, err := in.Consumer.claimFor(in.Binding)
				if err != nil {
					return in, err
				}
				in.HeldClaimID = held

				claims, err := in.Consumer.ledger(in.Tree.Ref)
				if err != nil {
					return in, err
				}
				in.Claims = claims

				return in, nil
			},
		),

		// A ref the lifecycle never registered. It is a refinement on the TREE
		// rather than a phrase that builds one, so there is no sentence that
		// invents a ref: the generation is the published one, moved.
		brine.DefineMap[BoundOutput, BoundOutput](
			"the consumer names an unregistered exact ref",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				unregistered := in.Tree.Ref
				unregistered.Generation++

				in.Err = in.Consumer.acquire("binding-"+freshUUID(),
					hangaroutputleaf.ClaimID(freshUUID()), unregistered)

				return in, nil
			},
		),

		brine.DefineMap[BoundOutput, BoundOutput](
			"the consumer holds no active claim",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (BoundOutput, error) {
				if err := in.Consumer.release(in.Binding, in.Acquisition.ClaimID,
					in.Tree.Ref); err != nil {
					return in, err
				}

				lease, err := managedRead(in)
				in.Lease, in.Err = lease, err

				return in, nil
			},
		),

		// Checks over the binding, all through production reads.
		CheckThat[BoundOutput]("the claim protects the published generation",
			func(in BoundOutput) error {
				if in.Err != nil {
					return in.Err
				}
				for _, claim := range in.Claims {
					if claim.ClaimID != in.Acquisition.ClaimID {
						continue
					}
					if claim.Ref != in.Tree.Ref {
						return fmt.Errorf("the claim protects %s/%s/%d and the capture published "+
							"%s/%s/%d", claim.Ref.Scope, claim.Ref.Digest, claim.Ref.Generation,
							in.Tree.Ref.Scope, in.Tree.Ref.Digest, in.Tree.Ref.Generation)
					}

					return nil
				}

				return fmt.Errorf("no claim %s is recorded for %s/%s/%d", in.Acquisition.ClaimID,
					in.Tree.Ref.Scope, in.Tree.Ref.Digest, in.Tree.Ref.Generation)
			}),

		check[BoundOutput]("exactly {int} claim is recorded",
			func(in BoundOutput, p brine.Params) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a claim count parameter")
				}
				if in.Err != nil {
					return in.Err
				}

				active := 0
				for _, claim := range in.Claims {
					if claim.Active() {
						active++
					}
				}
				if active != want {
					return fmt.Errorf("%d active claim(s) protect %s/%s/%d, not %d",
						active, in.Tree.Ref.Scope, in.Tree.Ref.Digest, in.Tree.Ref.Generation, want)
				}

				return nil
			}),

		CheckThat[BoundOutput]("no claim is left behind",
			func(in BoundOutput) error {
				if in.RolledBackClaimID == "" {
					return fmt.Errorf("nothing was rolled back, so this check says nothing")
				}
				for _, claim := range in.Claims {
					if claim.ClaimID == in.RolledBackClaimID {
						return fmt.Errorf("claim %s survived a rolled-back transaction",
							claim.ClaimID)
					}
				}

				// And the committed one is still there, so the absence above is
				// about the rollback rather than about an empty ledger.
				for _, claim := range in.Claims {
					if claim.ClaimID == in.Acquisition.ClaimID && claim.Active() {
						return nil
					}
				}

				return fmt.Errorf("the committed claim %s is not active either, so the absence "+
					"of the rolled-back one proves nothing", in.Acquisition.ClaimID)
			}),

		CheckThat[BoundOutput]("the tombstone is permanent",
			func(in BoundOutput) error {
				if in.Err != nil {
					return in.Err
				}
				for _, claim := range in.Claims {
					if claim.ClaimID != in.Acquisition.ClaimID {
						continue
					}
					if claim.Active() {
						return fmt.Errorf("claim %s is still active after two releases",
							claim.ClaimID)
					}

					return nil
				}

				return fmt.Errorf("claim %s is gone from the ledger; a released identity stays "+
					"tombstoned for the lifetime of the exact-ref lifecycle record, and a row "+
					"that was deleted cannot stop it being reacquired", in.Acquisition.ClaimID)
			}),

		CheckThat[BoundOutput]("the candidate claim ID is unchanged",
			func(in BoundOutput) error {
				if in.HeldClaimID == "" {
					return fmt.Errorf("the consumer's binding names no claim after the transition")
				}
				if in.HeldClaimID != in.Acquisition.ClaimID {
					return fmt.Errorf("the binding names claim %s and it was bound with %s",
						in.HeldClaimID, in.Acquisition.ClaimID)
				}

				active := 0
				for _, claim := range in.Claims {
					if claim.Active() {
						active++
					}
				}
				if active != 1 {
					return fmt.Errorf("%d active claim(s) after a transition that acquires none",
						active)
				}

				return nil
			}),

		CheckThat[BoundOutput]("the binding is not visible",
			func(in BoundOutput) error {
				if in.Err != nil {
					return in.Err
				}
				visible, err := in.Consumer.visible(in.Binding)
				if err != nil {
					return err
				}
				if visible {
					return fmt.Errorf("binding %s is visible before it was verified", in.Binding)
				}

				return nil
			}),

		CheckThat[BoundOutput]("the binding is visible",
			func(in BoundOutput) error {
				if !in.Visible {
					return fmt.Errorf("binding %s is not visible after it was verified", in.Binding)
				}

				return nil
			}),

		check[BoundOutput]("the binding is refused as {string}",
			func(in BoundOutput, p brine.Params) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a refusal parameter")
				}
				got, refused := refusalWord(in.Err)
				if !refused {
					return fmt.Errorf("the binding was not refused: %v", in.Err)
				}
				if got != want {
					return fmt.Errorf("the binding was refused as %q, not %q: %v", got, want, in.Err)
				}

				return nil
			}),

		CheckThat[BoundOutput]("the managed read is granted",
			func(in BoundOutput) error {
				lease, err := managedRead(in)
				if err != nil {
					return fmt.Errorf("a read against a claimed registered generation was "+
						"refused: %w", err)
				}
				if lease.Ref != in.Tree.Ref {
					return fmt.Errorf("the lease reads %s/%s/%d and the capture published "+
						"%s/%s/%d", lease.Ref.Scope, lease.Ref.Digest, lease.Ref.Generation,
						in.Tree.Ref.Scope, in.Tree.Ref.Digest, in.Tree.Ref.Generation)
				}

				return nil
			}),

		// Through refusalWord, like the binding phrase beside it, so the map is
		// CLOSED. The first version knew one word and let every other one pass
		// on any error at all: a scenario could have said `refused as "sausage"`
		// and been green on a nil-pointer panic recovered into an error.
		check[BoundOutput]("the managed read is refused as {string}",
			func(in BoundOutput, p brine.Params) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a refusal parameter")
				}
				if !knownRefusalWord(want) {
					return fmt.Errorf("%q is not a refusal this plane has; the vocabulary is %v",
						want, refusalWords())
				}
				if in.Err == nil {
					return fmt.Errorf("the read was granted: %+v", in.Lease.ReadLeaseID)
				}
				got, refused := refusalWord(in.Err)
				if !refused {
					return fmt.Errorf("the read was not refused with a typed outcome: %v", in.Err)
				}
				if got != want {
					return fmt.Errorf("the read was refused as %q, not %q: %v", got, want, in.Err)
				}

				return nil
			}),

		// Checks over the consuming Pod. PodCreated is reused deliberately: a
		// consumer's pod is a pod, and every existing mount and volume check
		// already reads it.
		CheckThat[PodCreated]("the consumer's Hangar init verifies exactly the receipt for its tree",
			verifiesExactlyTheReceipt),

		check[PodCreated]("the consumer's pod declares exactly {int} read-only verification mount per tree",
			declaresOneReadOnlyVerificationMountPerTree),

		CheckThat[PodCreated]("no user-controlled destination enters the verification command",
			noUserDestinationInTheVerificationCommand),

		CheckThat[PodCreated]("the consumer's pod asks for exactly the receipt's tree",
			asksForExactlyTheReceiptsTree),
	}
}

// releaseBinding is the body both release phrases share. They are two phrases
// and one body because "released" and "released again" differ only in how many
// times the scenario has said it, and a second body would be a second answer to
// the same question.
func releaseBinding(in BoundOutput) (BoundOutput, error) {
	in.Err = in.Consumer.release(in.Binding, in.Acquisition.ClaimID, in.Tree.Ref)
	if in.Err != nil {
		return in, nil
	}

	claims, err := in.Consumer.ledger(in.Tree.Ref)
	if err != nil {
		return in, err
	}
	in.Claims = claims

	return in, nil
}
