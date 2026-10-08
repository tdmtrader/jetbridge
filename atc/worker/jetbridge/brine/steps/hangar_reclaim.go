package steps

// The web's two deleting passes, seen from a scenario: the reclaim pass
// against a live read warrant's reader's claim, and the orphan sweep.
//
// Both are atc/hangaroutput/reclaim's production passes over this scenario's
// real PostgreSQL and the fixture's emulated output bucket, deleting through
// the web's own delete client (hangar/gcs.NewDeleteClient behind
// hangar/output/reclaimer). Nothing here classifies an object or decides what
// is reclaimable; the scenarios read back what the passes did.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"cloud.google.com/go/storage"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/reclaim"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
	hangaroutputleaf "github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// ReclaimRace is a bound output with two unspent archive read warrants for
// its exact generation, and what the reclaim pass and the node then answered.
type ReclaimRace struct {
	Bound    BoundOutput
	Warrants [2]mintedRead

	// Registered is whether this generation's lifecycle row is still
	// registered (not stamped reclaimed) after the production pass ran;
	// Reclaimed and Failed are what that pass counted. HoldErr is what
	// holding THIS generation for reclaim directly answered, and LiveReaders
	// how many live readers' claims the database counts on it: the pass's
	// candidate query leaves a protected generation out silently, so the
	// hold and the count are what say why.
	Registered  bool
	Reclaimed   int
	Failed      int
	HoldErr     error
	LiveReaders int

	// FirstRead is the first warrant's archive read: the digest of the tree
	// the node returned, or the refusal.
	FirstDigest hangar.Digest
	FirstErr    error

	// Deleted is the out-of-band delete's outcome; SecondErr is the second
	// warrant's archive read after it, and SecondReturned whether the node
	// answered with an archive at all.
	Deleted        reclaimer.Outcome
	SecondErr      error
	SecondReturned bool

	// LapsedReclaimed and LapsedRegistered are what the pass counted and
	// left once the readers' claims were given back.
	LapsedReclaimed  int
	LapsedRegistered bool
}

// OrphanSweep is the output bucket the sweep runs over: what was planted, and
// what the sweep counted and deleted.
type OrphanSweep struct {
	Daemon    HangarDaemon
	Namespace hangaroutputleaf.OutputNamespace

	Orphan   plantedObject
	Foreign  plantedObject
	Unmarked plantedObject

	Counts  map[string]int
	Deletes []sweptGeneration
}

type plantedObject struct {
	Key        string
	Generation int64
}

type sweptGeneration struct {
	Key        string
	Generation int64
}

// HangarReclaimDefinitions is the reclaim-and-sweep family.
func HangarReclaimDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Two warrants, minted while the consumer's claim is open (a warrant
		// is issued only under an active claim), for two input volumes so
		// each is its own single-use read.
		brine.DefineMap[BoundOutput, ReclaimRace](
			"two archive read warrants are minted for the published generation",
			func(in BoundOutput, _ brine.Params, _ *brine.Recorder) (ReclaimRace, error) {
				race := ReclaimRace{Bound: in}
				if in.Err != nil {
					return race, fmt.Errorf("the consumer never bound the output: %w", in.Err)
				}
				for i, volume := range []string{"input-0", "input-1"} {
					warrant, err := managedReadWarrantInto(in, volume)
					if err != nil {
						return race, fmt.Errorf("minting warrant %d: %w", i+1, err)
					}
					race.Warrants[i] = warrant
				}

				return race, nil
			},
		),

		// Every consumer's claim goes: the consumer's binding and the capture's
		// own. What is left protecting the generation is the two readers'
		// claims the warrants were minted over, and nothing else, which is
		// what makes the deferral below about them.
		brine.DefineMap[ReclaimRace, ReclaimRace](
			"every consumer's claim on the published generation is released",
			func(in ReclaimRace, _ brine.Params, _ *brine.Recorder) (ReclaimRace, error) {
				bound := in.Bound
				if err := bound.Consumer.release(bound.Binding, bound.Acquisition.ClaimID,
					bound.Tree.Ref); err != nil {
					return in, fmt.Errorf("releasing the consumer's claim: %w", err)
				}
				own := bound.Tree.Outcome.Capture.Key.ClaimID()
				if err := releaseClaim(bound, own); err != nil {
					return in, fmt.Errorf("releasing the capture's own claim: %w", err)
				}

				return in, nil
			},
		),

		// The production pass, then the same generation held for reclaim
		// directly: the pass's candidate query filters a protected generation
		// out silently, so the direct hold is what says why, and the count of
		// live readers' claims is what says it was the readers.
		brine.DefineMap[ReclaimRace, ReclaimRace](
			"the reclaim pass runs over the published generation",
			func(in ReclaimRace, _ brine.Params, _ *brine.Recorder) (ReclaimRace, error) {
				var err error
				in.Reclaimed, _, in.Failed, err = runReclaimPass(in.Bound)
				if err != nil {
					return in, fmt.Errorf("the reclaim pass: %w", err)
				}
				if in.Registered, err = lifecycleRegistered(in.Bound); err != nil {
					return in, err
				}

				if in.LiveReaders, err = liveReadersClaims(in.Bound); err != nil {
					return in, err
				}

				// The hold's transaction is rolled back as soon as it answered:
				// it holds the tree and lifecycle locks, and the fixture's
				// connection, and nothing after it may wait on either.
				plane := in.Bound.Tree.Outcome.Plane
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				tx, err := plane.DB.Conn.Begin()
				if err != nil {
					return in, err
				}
				in.HoldErr = plane.Repository.HoldForReclaim(ctx, brineHangarTx(tx), in.Bound.Tree.Ref,
					brineReclaimGrace)
				_ = tx.Rollback()

				return in, nil
			},
		),

		brine.DefineMap[ReclaimRace, ReclaimRace](
			"the archive read under the first warrant is made",
			func(in ReclaimRace, _ brine.Params, _ *brine.Recorder) (ReclaimRace, error) {
				in.FirstDigest, in.FirstErr = readArchiveDigest(in.Bound, in.Warrants[0])

				return in, nil
			},
		),

		// Out of band: the web's own delete role removes the EXACT generation
		// while the second warrant is still unspent -- the state a reclaim
		// would leave if the reader's claim did not defer it, and the state
		// any out-of-band lifetime violation leaves.
		brine.DefineMap[ReclaimRace, ReclaimRace](
			"the published generation is deleted out of band",
			func(in ReclaimRace, _ brine.Params, _ *brine.Recorder) (ReclaimRace, error) {
				daemon := in.Bound.Tree.Outcome.Source.Draft.Daemon
				deletes, _, closeDeletes, err := webDeleteRole(daemon)
				if err != nil {
					return in, err
				}
				defer func() { _ = closeDeletes() }()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				in.Deleted, err = deletes.DeleteExactGeneration(ctx, in.Bound.Tree.Ref,
					hangaroutputleaf.DeletePrecondition{Generation: in.Bound.Tree.Ref.Generation})
				if in.Deleted != reclaimer.Deleted {
					return in, fmt.Errorf("the out-of-band delete answered %v: %v", in.Deleted, err)
				}

				node := jetbridge.NewOutputControlClient(daemon.Output.URL, daemon.HTTP,
					daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
				archive, _, readErr := node.OpenManagedOutput(ctx, managedReadOf(in.Bound, in.Warrants[1]),
					16<<20)
				if archive != nil {
					in.SecondReturned = true
					_ = archive.Close()
				}
				in.SecondErr = readErr

				return in, nil
			},
		),

		CheckThat[ReclaimRace]("the reclaim pass defers the generation while the readers' claims are live",
			func(in ReclaimRace) error {
				if !in.Registered {
					return fmt.Errorf("the reclaim pass stamped the generation reclaimed with two "+
						"live readers' claims on it (reclaimed %d, failed %d)", in.Reclaimed, in.Failed)
				}
				if in.Reclaimed != 0 || in.Failed != 0 {
					return fmt.Errorf("the reclaim pass reclaimed %d and failed %d generation(s); "+
						"the only registered one is protected", in.Reclaimed, in.Failed)
				}
				if in.HoldErr == nil {
					return fmt.Errorf("holding %v for reclaim succeeded under two live readers' claims",
						in.Bound.Tree.Ref)
				}
				if !errors.Is(in.HoldErr, hangaroutputleaf.ErrConflict) {
					return fmt.Errorf("the hold was refused, but not as a lifecycle conflict: %v",
						in.HoldErr)
				}
				if in.LiveReaders != 2 {
					return fmt.Errorf("the database counts %d live readers' claim(s) on the "+
						"generation; the scenario released every consumer's claim and holds two "+
						"warrants, so two readers' claims must be what deferred it: %v",
						in.LiveReaders, in.HoldErr)
				}

				return nil
			}),

		CheckThat[ReclaimRace]("the first warrant's archive read returned the exact published tree",
			func(in ReclaimRace) error {
				if in.FirstErr != nil {
					return fmt.Errorf("the archive read under a live warrant failed: %w", in.FirstErr)
				}
				if in.FirstDigest != in.Bound.Tree.Ref.Digest {
					return fmt.Errorf("the archive read returned digest %s; the published tree "+
						"is %s", in.FirstDigest, in.Bound.Tree.Ref.Digest)
				}

				return nil
			}),

		CheckString[ReclaimRace]("the second warrant's archive read fails closed as {string}",
			"the archive read's refusal",
			func(in ReclaimRace) (string, error) {
				if in.SecondReturned {
					return "", fmt.Errorf("the node answered with an archive for a generation " +
						"that is gone")
				}
				word, refused := refusalWord(in.SecondErr)
				if !refused {
					return "", fmt.Errorf("the read failed, but not with a typed outcome: %v",
						in.SecondErr)
				}

				return word, nil
			},
			func(in ReclaimRace) string { return fmt.Sprintf("error: %v", in.SecondErr) }),

		// The readers' claims given back, the generation is nothing's: the
		// pass holds it, asks the store to delete it -- already absent, after
		// the out-of-band delete -- and stamps it reclaimed in the same
		// transaction.
		brine.DefineMap[ReclaimRace, ReclaimRace](
			"the readers' claims are released and the reclaim pass runs again",
			func(in ReclaimRace, _ brine.Params, _ *brine.Recorder) (ReclaimRace, error) {
				for i, warrant := range in.Warrants {
					if err := releaseClaim(in.Bound, warrant.Claim.ClaimID); err != nil {
						return in, fmt.Errorf("releasing reader's claim %d: %w", i+1, err)
					}
				}
				var err error
				in.LapsedReclaimed, _, _, err = runReclaimPass(in.Bound)
				if err != nil {
					return in, fmt.Errorf("the reclaim pass: %w", err)
				}
				if in.LapsedRegistered, err = lifecycleRegistered(in.Bound); err != nil {
					return in, err
				}

				return in, nil
			},
		),

		CheckThat[ReclaimRace]("the generation is stamped reclaimed",
			func(in ReclaimRace) error {
				if in.LapsedReclaimed != 1 {
					return fmt.Errorf("the reclaim pass reclaimed %d generation(s) once the readers' "+
						"claims were released; want exactly this one", in.LapsedReclaimed)
				}
				if in.LapsedRegistered {
					return fmt.Errorf("the reclaim pass counted %v reclaimed and left its lifecycle "+
						"registered", in.Bound.Tree.Ref)
				}

				return nil
			}),

		// The orphan sweep's bucket. Every key is the one the namespace derives
		// for its digest, so all three are listed; what tells them apart is
		// only the marker.
		brine.DefineMap[HangarDaemon, OrphanSweep](
			"the output bucket holds an orphan marked for this store, one marked for another store, and one with no marker",
			func(in HangarDaemon, _ brine.Params, _ *brine.Recorder) (OrphanSweep, error) {
				namespace, err := brineOutputNamespace(in)
				if err != nil {
					return OrphanSweep{}, err
				}
				sweep := OrphanSweep{Daemon: in, Namespace: namespace}
				now := hangaroutputleaf.NewTimestamp(time.Now().UTC())

				ours := namespace.MarkerFor(hangaroutputleaf.ReservationID(freshUUID()),
					sweepDigest('a'), now)
				if sweep.Orphan, err = plantOutputObject(in, namespace, sweepDigest('a'),
					ours.Metadata()); err != nil {
					return sweep, err
				}
				foreign := namespace.MarkerFor(hangaroutputleaf.ReservationID(freshUUID()),
					sweepDigest('c'), now)
				foreign.Store = "gs://another-deployment"
				if sweep.Foreign, err = plantOutputObject(in, namespace, sweepDigest('c'),
					foreign.Metadata()); err != nil {
					return sweep, err
				}
				if sweep.Unmarked, err = plantOutputObject(in, namespace, sweepDigest('d'),
					nil); err != nil {
					return sweep, err
				}

				return sweep, nil
			},
		),

		// One production sweep, its age threshold set so that every listed
		// object counts as past twice the capture deadline. The threshold is
		// set NEGATIVE rather than tiny on purpose: the sweep compares the
		// emulator's creation time against PostgreSQL's clock, and in CI those
		// are two machines.
		brine.DefineMapUsing[OrphanSweep, OrphanSweep](
			"the orphan sweep runs with every object past twice the capture deadline",
			[]string{"jetbridge-db"},
			func(in OrphanSweep, _ brine.Params, _ *brine.Recorder,
				res brine.Resources) (OrphanSweep, error) {
				plane, err := newSettlementPlane(in.Daemon, res)
				if err != nil {
					return in, err
				}
				deletes, recorder, closeDeletes, err := webDeleteRole(in.Daemon)
				if err != nil {
					return in, err
				}
				defer func() { _ = closeDeletes() }()
				lister, closeLister, err := hangargcs.NewClient(in.Daemon.Ctx, in.Daemon.Endpoint)
				if err != nil {
					return in, err
				}
				defer func() { _ = closeLister() }()

				sweep := &reclaim.Sweep{
					Transactor:      brineTransactor{conn: plane.DB.Conn},
					Repository:      plane.Repository,
					Namespace:       in.Namespace,
					Lister:          lister,
					Reclaimer:       deletes,
					CaptureDeadline: -time.Hour,
					PageSize:        2,
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				in.Counts, err = sweep.Once(ctx)
				if err != nil {
					return in, fmt.Errorf("the orphan sweep: %w", err)
				}
				in.Deletes = recorder.deletes

				return in, nil
			},
		),

		CheckThat[OrphanSweep](
			"the sweep deleted exactly the orphan marked for this store, by its listed generation",
			func(in OrphanSweep) error {
				if in.Counts[reclaim.SweepDeleted] != 1 {
					return fmt.Errorf("the sweep counted %d deleted: %v",
						in.Counts[reclaim.SweepDeleted], in.Counts)
				}
				want := sweptGeneration{in.Orphan.Key, in.Orphan.Generation}
				if len(in.Deletes) != 1 || in.Deletes[0] != want {
					return fmt.Errorf("the sweep asked to delete %v; want exactly %v",
						in.Deletes, want)
				}
				present, err := in.Daemon.outputGenerationPresent(in.Orphan)
				if err != nil {
					return err
				}
				if present {
					return fmt.Errorf("generation %d of %s is still in the bucket",
						in.Orphan.Generation, in.Orphan.Key)
				}

				return nil
			}),

		CheckThat[OrphanSweep](
			"the object marked for another store survives the sweep, counted as foreign",
			func(in OrphanSweep) error {
				return survivedAs(in, in.Foreign, reclaim.SweepForeign)
			}),

		CheckThat[OrphanSweep](
			"the object with no marker survives the sweep, counted as unmarked",
			func(in OrphanSweep) error {
				return survivedAs(in, in.Unmarked, reclaim.SweepUnmarked)
			}),
	}
}

func survivedAs(in OrphanSweep, object plantedObject, class string) error {
	if in.Counts[class] != 1 {
		return fmt.Errorf("the sweep counted %d %s object(s); want 1 (all: %v)",
			in.Counts[class], class, in.Counts)
	}
	for _, deleted := range in.Deletes {
		if deleted.Key == object.Key {
			return fmt.Errorf("the sweep asked to delete the %s object %s", class, object.Key)
		}
	}
	present, err := in.Daemon.outputGenerationPresent(object)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("the %s object %s at generation %d is gone", class, object.Key,
			object.Generation)
	}

	return nil
}

// brineHangarTx wraps a scenario transaction the way production does, so the
// deferred triggers refuse at COMMIT exactly as they would deployed.
func brineHangarTx(tx db.Tx) db.HangarOutputTx { return db.HangarOutputTx{Tx: tx} }

// releaseClaim releases one claim on the bound tree through the repository.
func releaseClaim(bound BoundOutput, id hangaroutputleaf.ClaimID) error {
	plane := bound.Tree.Outcome.Plane
	tx, err := plane.DB.Conn.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := plane.Repository.ReleaseClaim(context.Background(), brineHangarTx(tx),
		hangaroutputleaf.ClaimRelease{
			ProtocolVersion: hangaroutputleaf.ProtocolVersion,
			ClaimID:         id,
			Ref:             bound.Tree.Ref,
			RequestedAt:     hangaroutputleaf.NewTimestamp(time.Now()),
		}); err != nil {
		return err
	}

	return tx.Commit()
}

func managedReadOf(bound BoundOutput, warrant mintedRead) hangaroutputleaf.ManagedReadRequest {
	return hangaroutputleaf.ManagedReadRequest{Ref: bound.Tree.Ref,
		Destination: warrant.Destination, Warrant: warrant.Token}
}

// brineReclaimGrace is the publication grace a scenario's reclaim pass runs
// with. The pass rounds it to its second floor, so it has elapsed the moment
// the registration committed; the scenarios are about claims, not grace.
const brineReclaimGrace = time.Millisecond

// runReclaimPass is one production reclaim pass over the plane a capture
// settled on, deleting through the web's delete role, and reports what it
// counted: reclaimed, deferred, failed.
func runReclaimPass(bound BoundOutput) (int, int, int, error) {
	plane := bound.Tree.Outcome.Plane
	if plane == nil {
		return 0, 0, 0, fmt.Errorf("this chain never settled a capture")
	}
	deletes, _, closeDeletes, err := webDeleteRole(bound.Tree.Outcome.Source.Draft.Daemon)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = closeDeletes() }()
	pass := &reclaim.Pass{
		Transactor: brineTransactor{conn: plane.DB.Conn},
		Repository: plane.Repository,
		Reclaimer:  deletes,
		Grace:      brineReclaimGrace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return pass.Reclaim(ctx)
}

// lifecycleRegistered reads whether the bound generation's lifecycle row is
// still registered: not stamped reclaimed.
func lifecycleRegistered(bound BoundOutput) (bool, error) {
	ref := bound.Tree.Ref
	var registered bool
	if err := bound.Tree.Outcome.Plane.DB.Conn.QueryRow(`
		SELECT reclaimed_at IS NULL FROM hangar_exact_lifecycles
		 WHERE scope = $1 AND digest = $2 AND generation = $3`,
		string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&registered); err != nil {
		return false, fmt.Errorf("reading the lifecycle of %v: %w", ref, err)
	}

	return registered, nil
}

// liveReadersClaims counts the live readers' claims on the bound generation
// the way the reclaim pass does: unreleased, with an expiry still ahead on
// the database clock.
func liveReadersClaims(bound BoundOutput) (int, error) {
	ref := bound.Tree.Ref
	var live int
	if err := bound.Tree.Outcome.Plane.DB.Conn.QueryRow(`
		SELECT count(*) FROM hangar_claims c
		  JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
		 WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3
		   AND c.released_at IS NULL AND c.expires_at IS NOT NULL AND c.expires_at > now()`,
		string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&live); err != nil {
		return 0, fmt.Errorf("counting the readers' claims on %v: %w", ref, err)
	}

	return live, nil
}

// readArchiveDigest reads the archive route under one warrant and
// canonicalizes what came back, so the answer is the digest of the bytes the
// node sent rather than the header it claimed.
func readArchiveDigest(bound BoundOutput, warrant mintedRead) (hangar.Digest, error) {
	daemon := bound.Tree.Outcome.Source.Draft.Daemon
	node := jetbridge.NewOutputControlClient(daemon.Output.URL, daemon.HTTP, daemon.Minter,
		executioncontrol.ActivationEpoch(hangarEpoch))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	archive, attributes, err := node.OpenManagedOutput(ctx, managedReadOf(bound, warrant), 16<<20)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	if attributes.Ref != bound.Tree.Ref {
		return "", fmt.Errorf("the node answered for %v, not %v", attributes.Ref, bound.Tree.Ref)
	}
	scratch, err := AttributedTempDir("brine-reclaim-read-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	tree, err := (hangar.Canonicalizer{TempDir: scratch}).Capture(ctx, io.LimitReader(archive, 16<<20))
	if err != nil {
		return "", err
	}
	defer tree.Close()

	return tree.Digest, nil
}

// webDeleteRole is the web's delete role over the fixture's output bucket:
// the delete client only the web constructs, behind the reclaimer, recording
// every exact generation it is asked to remove.
func webDeleteRole(daemon HangarDaemon) (*reclaimer.Reclaimer, *recordingDeleteClient, func() error, error) {
	namespace, err := brineOutputNamespace(daemon)
	if err != nil {
		return nil, nil, nil, err
	}
	deleter, closeDeleter, err := hangargcs.NewDeleteClient(daemon.Ctx, daemon.Endpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	recorder := &recordingDeleteClient{inner: deleter}
	deletes, err := reclaimer.New(namespace, reclaimer.Restrict(recorder))
	if err != nil {
		_ = closeDeleter()
		return nil, nil, nil, err
	}

	return deletes, recorder, closeDeleter, nil
}

// recordingDeleteClient records what the delete role was asked. It is the
// production client underneath; the record is what lets "by its listed
// generation" be an outcome.
type recordingDeleteClient struct {
	inner   objectstore.DeleteClient
	deletes []sweptGeneration
}

func (r *recordingDeleteClient) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	return r.inner.StatExact(ctx, bucket, key, generation)
}

func (r *recordingDeleteClient) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	r.deletes = append(r.deletes, sweptGeneration{key, generation})
	return r.inner.DeleteExact(ctx, bucket, key, generation)
}

func sweepDigest(fill byte) hangar.Digest {
	hex := make([]byte, 64)
	for i := range hex {
		hex[i] = fill
	}

	return hangar.Digest("sha256:" + string(hex))
}

// plantOutputObject writes an object at the key the namespace derives for a
// digest, with the given metadata, and reports the generation the bucket gave
// it.
func plantOutputObject(daemon HangarDaemon, namespace hangaroutputleaf.OutputNamespace,
	digest hangar.Digest, metadata map[string]string) (plantedObject, error) {
	key, err := namespace.ObjectKey(digest)
	if err != nil {
		return plantedObject{}, err
	}
	writer := daemon.Client.Bucket(daemon.OutputBucket).Object(key).NewWriter(daemon.Ctx)
	writer.Metadata = metadata
	if _, err := io.WriteString(writer, "planted "+key); err != nil {
		_ = writer.Close()
		return plantedObject{}, fmt.Errorf("planting %q: %w", key, err)
	}
	if err := writer.Close(); err != nil {
		return plantedObject{}, fmt.Errorf("planting %q: %w", key, err)
	}

	return plantedObject{Key: key, Generation: writer.Attrs().Generation}, nil
}

// outputGenerationPresent asks the bucket for one exact generation.
func (s HangarDaemon) outputGenerationPresent(object plantedObject) (bool, error) {
	_, err := s.Client.Bucket(s.OutputBucket).Object(object.Key).
		Generation(object.Generation).Attrs(s.Ctx)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, storage.ErrObjectNotExist):
		return false, nil
	}

	return false, fmt.Errorf("asking for %s at generation %d: %w", object.Key, object.Generation, err)
}
