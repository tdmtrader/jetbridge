package steps

// Publication: what a sealed source becomes, and what the bucket then holds.
//
// The store here is the emulated output bucket the fixture created, read back
// through the same client the daemon uses. That is what makes "holds exactly
// one object" an OUTCOME: there is no request log on the fixture, so dedup can
// only be told from overwrite by seeding the key with a different variant and
// naming which bytes are there afterwards.
//
// Both halves of a receipt assertion are production. The daemon signs with its
// epoch private key and the check verifies with the production verifier under
// the activation-pinned public key — never against a string a test wrote, which
// is the defect step-closing.feature records.
//
// The bodies are stubs until Phase 2 Green adds the output namespace, marker
// and receipt, and Phase 3 Green the publish route.

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/brine-dev/brine-go/pkg/brine"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const publicationPhase = "Phase 2 Green"

// sealAndPublish is the daemon-level publish phrase's whole body.
//
// It is at the DAEMON level and not at `the capture settles` deliberately: the
// settlement is the control plane's Stage 2 transaction and belongs to Phase 5.
// What this does is the node's half -- confirm the seal, publish the sealed
// tree, then answer a stat challenge with a signed receipt -- which is exactly
// what Phase 3 owns.
//
// The receipt comes from a SECOND call against a challenge, not from the
// publish. A challenge names the generation the publish assigned, so it cannot
// exist until the publish returns; a receipt signed inside the publish would be
// bound to no challenge and would answer every later one naming the same facts.
func sealAndPublish(in FinishWitnessed) (CaptureOutcome, error) {
	source := in.Source
	outcome := CaptureOutcome{Source: source}

	sealed := source.Draft.Daemon.capture("begin-seal", "/capture/v1/seal",
		source.Execution, source.sealRequest())
	started, err := decodeControl[hangaroutput.SealStarted](sealed)
	if err != nil {
		return outcome, fmt.Errorf("beginning the seal: %w", err)
	}
	if len(started.DrainSet) != 0 {
		return outcome, fmt.Errorf("the seal captured %d outstanding writer(s); this chain "+
			"admitted none", len(started.DrainSet))
	}
	if err := hangaroutput.VerifyCaptureAcknowledgement(started.Acknowledgement,
		source.Draft.Daemon.ControlPublic); err != nil {
		return outcome, fmt.Errorf("the seal statement does not verify: %w", err)
	}
	if err := source.Draft.Daemon.confirmSeal(source, started); err != nil {
		return outcome, err
	}

	published := source.Draft.Daemon.capture("publish", "/capture/v1/publish",
		source.Execution, source.publicationRequest())
	outcome.Answer = published
	result, err := decodeControl[hangaroutput.PublicationResult](published)
	if err != nil {
		// A refusal is a VALUE. "The capture is refused as collision" reads it.
		return outcome, nil
	}
	outcome.Published = result
	outcome.Settled = true
	outcome.Disposition = hangaroutput.DispositionCapture

	receipt, challenge, err := source.Draft.Daemon.attest(source, result)
	if err != nil {
		return outcome, err
	}
	outcome.Receipt = receipt
	outcome.Challenge = challenge

	return outcome, nil
}

// HangarPublicationDefinitions is the publication family.
func HangarPublicationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The two seeding refinements the collision scenarios need.
		//
		// NEITHER NAMES A KEY. The key is server-derived from a namespace this
		// process cannot see and a digest of bytes it did not canonicalize, so
		// a sentence that spelled one would be spelling a key production would
		// never choose -- and the scenario would go green against a collision
		// with nothing.
		//
		// So the fixture LEARNS the key: it runs a whole probe capture of the
		// same bytes, reads back the one key the bucket then holds, removes the
		// object, and seeds that key with the variant the scenario wants. What
		// is left is exactly the state production reaches when some other node
		// on some other day wrote there first.
		brine.DefineMap[FinishWitnessed, FinishWitnessed](
			"the output bucket's key for this tree already holds a different variant",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				// A COMPLETE marker for a different tree, not a partial one. A
				// partial marker is a corrupt object, which is a different
				// answer from a collision, and the scenario is about the
				// collision: the store's copy says something different from the
				// source's.
				return in, in.Source.Draft.Daemon.seedDerivedKey(in.Source,
					"somebody else's bytes", func(existing map[string]string) map[string]string {
						seeded := map[string]string{}
						for key, value := range existing {
							seeded[key] = value
						}
						seeded[hangaroutput.MarkerKeyDigest] =
							"sha256:" + strings.Repeat("cd", 32)

						return seeded
					})
			},
		),

		brine.DefineMap[FinishWitnessed, FinishWitnessed](
			"the output bucket's key for this tree already holds an object with no marker",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (FinishWitnessed, error) {
				return in, in.Source.Draft.Daemon.seedDerivedKey(in.Source,
					"an object nobody's cohort wrote",
					func(map[string]string) map[string]string { return nil })
			},
		),

		// The daemon-level half of that sentence, which is what Phase 3 owns.
		// Phase 5's `the capture settles` will add the control plane's Stage 2
		// transaction on top of exactly this.
		brine.DefineMap[FinishWitnessed, CaptureOutcome](
			"the daemon seals and publishes the source",
			func(in FinishWitnessed, _ brine.Params, _ *brine.Recorder) (CaptureOutcome, error) {
				return sealAndPublish(in)
			},
		),

		// A NEW BUILD of the same step: new execution, new handoff, new source
		// lease. It is the identity rule requirement 6 states, and it is
		// asserted over the DRAFT rather than the outcome because the
		// identities are predeclared at admission -- the only moment both the
		// old and the new one are knowable.
		brine.DefineMap[CaptureOutcome, CaptureDraft](
			"a new build of the same step is admitted",
			func(in CaptureOutcome, _ brine.Params, _ *brine.Recorder) (CaptureDraft, error) {
				previous := in.Source.Admission

				execution := executioncontrol.Identity{
					ExecutionID: executioncontrol.ExecutionID(freshUUID()),
					Fence:       1,
				}
				admitted := in.Source.Draft.Daemon.base("admit", "/execution/v1/admit", execution,
					executioncontrol.Envelope{
						ProtocolVersion: executioncontrol.ProtocolVersion,
						Identity:        execution,
						ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
						NodeUID:         hangarNodeUID,
						Capability:      "opaque-capability",
					})
				if _, err := decodeControl[executioncontrol.ClassifyResult](admitted); err != nil {
					return CaptureDraft{}, fmt.Errorf("admitting the new build: %w", err)
				}

				return CaptureDraft{
					Daemon: in.Source.Draft.Daemon,
					Output: in.Source.Draft.Output,
					Admission: hangaroutput.CaptureAdmission{
						ProtocolVersion: hangaroutput.ProtocolVersion,
						Execution:       execution,
						ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
						HandoffID:       hangaroutput.HandoffID(freshUUID()),
						SourceLeaseID:   hangaroutput.SourceLeaseID(freshUUID()),
						Output:          in.Source.Draft.Output,
						CaptureDeadline: previous.CaptureDeadline,
					},
					PreviousAdmission: previous,
					// The cluster hands back a new Pod for a new build, and a
					// hold binds to it: a replacement Pod is a new incarnation
					// and does not inherit the old one's hold.
					PodUID: executioncontrol.PodUID(freshUUID()),
				}, nil
			},
		),

		// A READ OF THE BUCKET, through the same client the daemon uses. Not a
		// memory of what was published: that is what makes "holds exactly one
		// object" an outcome rather than a call count.
		brine.DefineMap[CaptureOutcome, PublishedTree](
			"the published tree is read back from the output bucket",
			func(in CaptureOutcome, _ brine.Params, _ *brine.Recorder) (PublishedTree, error) {
				tree := PublishedTree{
					Outcome:    in,
					Ref:        in.Published.Ref,
					Attributes: in.Published.Attributes.Foundation(),
				}
				if in.Receipt.Signature != "" {
					tree.Receipts = append(tree.Receipts, in.Receipt)
				}
				keys, err := in.Source.Draft.Daemon.outputObjectKeys()
				if err != nil {
					return tree, err
				}
				tree.BucketKeys = keys
				for _, key := range keys {
					version, err := in.Source.Draft.Daemon.outputMarkerVersion(key)
					if err != nil {
						return tree, err
					}
					tree.MarkerVersions = append(tree.MarkerVersions, version)
				}

				return tree, nil
			},
		),

		// A SECOND capture of the same bytes, from a second admission on the
		// same node. Not a repeat of one publish: repeating a publish cannot
		// tell dedup from overwrite, and a second capture is what production
		// actually does.
		brine.DefineMap[PublishedTree, PublishedTree](
			"the same canonical bytes are captured again",
			func(in PublishedTree, _ brine.Params, _ *brine.Recorder) (PublishedTree, error) {
				second, err := in.Outcome.Source.Draft.Daemon.captureAgain(in.Outcome.Source)
				if err != nil {
					return in, err
				}
				if second.Receipt.Signature != "" {
					in.Receipts = append(in.Receipts, second.Receipt)
				}
				in.Second = second

				keys, err := in.Outcome.Source.Draft.Daemon.outputObjectKeys()
				if err != nil {
					return in, err
				}
				in.BucketKeys = keys
				in.MarkerVersions = nil
				for _, key := range keys {
					version, err := in.Outcome.Source.Draft.Daemon.outputMarkerVersion(key)
					if err != nil {
						return in, err
					}
					in.MarkerVersions = append(in.MarkerVersions, version)
				}

				return in, nil
			},
		),

		// A REPLACEMENT generation, reached the only way production reaches
		// one: the old generation is removed from the bucket and the same
		// canonical bytes are captured again, so the store assigns a new
		// generation at the same key.
		//
		// It is not an overwrite and there is no API for one -- create-if-absent
		// refuses a key that is occupied, which is what the collision scenarios
		// above pin. Req 38's "a caller may recapture and claim a newly
		// published generation" is this sequence, and the old exact ref is a
		// ref to a generation that is gone.
		brine.DefineMap[PublishedTree, PublishedTree](
			"an exact replacement generation is published",
			func(in PublishedTree, _ brine.Params, _ *brine.Recorder) (PublishedTree, error) {
				if in.Ref.Generation == 0 {
					return in, fmt.Errorf("this chain published no generation to replace: %s",
						in.Outcome.Answer.describe())
				}
				if len(in.BucketKeys) != 1 {
					return in, fmt.Errorf("the bucket holds %d objects, so there is no one key "+
						"to replace at: %v", len(in.BucketKeys), in.BucketKeys)
				}
				in.Superseded = in.Ref

				daemon := in.Outcome.Source.Draft.Daemon
				key := in.BucketKeys[0]
				if err := daemon.Client.Bucket(daemon.OutputBucket).Object(key).
					Delete(daemon.Ctx); err != nil {
					return in, fmt.Errorf("removing the superseded object at %q: %w", key, err)
				}

				replacement, err := daemon.captureAgain(in.Outcome.Source)
				if err != nil {
					return in, fmt.Errorf("publishing the replacement generation: %w", err)
				}
				if replacement.Published.Ref.Generation == 0 {
					return in, fmt.Errorf("the replacement capture published nothing: %s",
						replacement.Answer.describe())
				}
				if replacement.Published.Ref.Generation == in.Superseded.Generation {
					return in, fmt.Errorf("the replacement was assigned the SAME generation %d "+
						"as the object it replaced; a store that reuses a generation cannot "+
						"make an exact ref mean one set of bytes",
						replacement.Published.Ref.Generation)
				}

				in.Second = replacement
				in.Ref = replacement.Published.Ref
				in.Attributes = replacement.Published.Attributes.Foundation()
				if replacement.Receipt.Signature != "" {
					in.Receipts = append(in.Receipts, replacement.Receipt)
				}

				return in, nil
			},
		),

		// Checks over the outcome.
		// The receipt is asserted WHOLE, and against the SERVER-DERIVED scope --
		// which is why the sentence cannot name one. The scope is an opaque
		// per-tenant, per-epoch hash; a feature file that could spell it would
		// be a feature file choosing where an object goes.
		CheckThat[CaptureOutcome]("the capture returns a receipt for the server-derived scope at a store-assigned generation",
			func(in CaptureOutcome) error {
				if in.Err != nil {
					return in.Err
				}
				if in.Receipt.Signature == "" {
					return fmt.Errorf("no receipt: %s", in.Answer.describe())
				}
				claims := in.Receipt.Claims
				if claims.Ref != in.Published.Ref {
					return fmt.Errorf("the receipt names %v and the object is %v",
						claims.Ref, in.Published.Ref)
				}
				if claims.Ref.Generation <= 0 {
					return fmt.Errorf("the receipt names generation %d", claims.Ref.Generation)
				}
				if claims.Ref.Scope != in.Published.Ref.Scope {
					return fmt.Errorf("the receipt names scope %q", claims.Ref.Scope)
				}
				if claims.MarkerVersion != hangaroutput.MarkerVersion {
					return fmt.Errorf("the receipt names marker version %q", claims.MarkerVersion)
				}
				if claims.ActivationEpoch != executioncontrol.ActivationEpoch(hangarEpoch) {
					return fmt.Errorf("the receipt names epoch %d", claims.ActivationEpoch)
				}
				if in.Receipt.KeyID != hangarReceiptKeyID {
					return fmt.Errorf("the receipt names key %q", in.Receipt.KeyID)
				}
				if claims.Attributes.Ref != in.Published.Ref {
					return fmt.Errorf("the receipt's attributes name %v", claims.Attributes.Ref)
				}

				return nil
			}),

		CheckContains[CaptureOutcome]("the capture is refused as {string}",
			"the capture's refusal",
			func(in CaptureOutcome) (string, error) {
				// A refusal from the control plane's own guard, when the
				// capture never reached the daemon. It is asked first because
				// a predeclaration cannot produce a daemon answer at all --
				// there is nothing to send.
				if in.Refusal != nil {
					return in.Refusal.Error(), nil
				}
				if in.Answer.Err != nil {
					return "", fmt.Errorf("no answer at all: %w", in.Answer.Err)
				}
				if in.Answer.Status/100 == 2 {
					return "", fmt.Errorf("the capture was not refused: %s", in.Answer.describe())
				}

				return string(in.Answer.Body), nil
			},
			func(in CaptureOutcome) string { return fmt.Sprintf("status %d", in.Answer.Status) }),

		// BOTH HALVES ARE PRODUCTION. The daemon signed with its epoch private
		// key; this verifies with the production verifier under the pinned
		// public half, never against a string a step wrote.
		CheckThat[CaptureOutcome]("the receipt verifies under the activation-pinned public key",
			func(in CaptureOutcome) error {
				if in.Receipt.Signature == "" {
					return fmt.Errorf("no receipt: %s", in.Answer.describe())
				}

				return verifyHangarReceipt(in.Receipt, in.Challenge,
					in.Source.Draft.Daemon.ReceiptPublic)
			}),

		CheckThat[CaptureOutcome]("the receipt does not verify under any other key",
			func(in CaptureOutcome) error {
				if in.Receipt.Signature == "" {
					return fmt.Errorf("no receipt: %s", in.Answer.describe())
				}
				other, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					return err
				}
				if err := verifyHangarReceipt(in.Receipt, in.Challenge, other); err == nil {
					return fmt.Errorf("the receipt verified under a key that did not sign it")
				}

				return nil
			}),

		// Checks over the bucket.
		CheckThat[PublishedTree]("the output bucket holds exactly one object, marked \"hangar-output-v1\"",
			func(in PublishedTree) error {
				if len(in.BucketKeys) != 1 {
					return fmt.Errorf("the output bucket holds %d objects: %v",
						len(in.BucketKeys), in.BucketKeys)
				}
				if len(in.MarkerVersions) != 1 || in.MarkerVersions[0] != hangaroutput.MarkerVersion {
					return fmt.Errorf("the object at %s is marked %v, expected %q",
						in.BucketKeys[0], in.MarkerVersions, hangaroutput.MarkerVersion)
				}

				return nil
			}),

		CheckThat[PublishedTree]("the two captures share one object and carry two distinct receipts",
			func(in PublishedTree) error {
				if len(in.BucketKeys) != 1 {
					return fmt.Errorf("the two captures produced %d objects: %v",
						len(in.BucketKeys), in.BucketKeys)
				}
				if !in.Second.Published.Deduplicated {
					return fmt.Errorf("the second capture did not deduplicate against the first")
				}
				if len(in.Receipts) != 2 {
					return fmt.Errorf("the two captures carry %d receipts", len(in.Receipts))
				}
				if in.Receipts[0].Signature == in.Receipts[1].Signature {
					return fmt.Errorf("the two captures carry the same receipt; a receipt is per " +
						"capture, not per object")
				}
				if in.Receipts[0].Claims.Ref != in.Receipts[1].Claims.Ref {
					return fmt.Errorf("the two receipts name different objects: %v and %v",
						in.Receipts[0].Claims.Ref, in.Receipts[1].Claims.Ref)
				}

				return nil
			}),

		// The DATABASE half of the whole chain, and the last line of it: the
		// lifecycle row a settled capture left behind is about the exact
		// generation the store assigned, not about the logical (scope, digest)
		// the reservation resolved to.
		//
		// It is written against the row rather than against a repository read
		// because what is under test is what the row CONTAINS. A registration
		// that dropped the generation would leave a lifecycle every read still
		// finds -- by scope and digest -- and it would be a lifecycle about
		// whichever bytes are at that key, which is exactly the float Req 28
		// forbids.
		CheckThat[PublishedTree]("the registered exact ref names the published generation",
			func(in PublishedTree) error {
				if in.Outcome.Plane == nil {
					return fmt.Errorf("this capture never settled on a control plane, so " +
						"nothing registered a receipt")
				}
				if in.Ref.Generation == 0 {
					return fmt.Errorf("the capture published no generation: %s",
						in.Outcome.Answer.describe())
				}

				var (
					scope, digest string
					generation    int64
					state         string
				)
				// By GENERATION, not by (scope, digest). A replacement
				// registers a SECOND lifecycle for the same logical pair, and
				// an unqualified QueryRow with no ORDER BY would silently take
				// whichever row the planner handed back -- which is how this
				// check would pass while looking at the superseded row.
				err := in.Outcome.Plane.DB.Conn.QueryRow(`
					SELECT scope, digest, generation, state
					  FROM hangar_exact_lifecycles
					 WHERE scope = $1 AND digest = $2 AND generation = $3`,
					string(in.Ref.Scope), string(in.Ref.Digest), in.Ref.Generation).
					Scan(&scope, &digest, &generation, &state)
				if err != nil {
					return fmt.Errorf("no exact lifecycle for %s/%s at generation %d: %v",
						in.Ref.Scope, in.Ref.Digest, in.Ref.Generation, err)
				}

				registered := hangar.TreeRef{
					Scope:      hangar.Scope(scope),
					Digest:     hangar.Digest(digest),
					Generation: generation,
				}
				if registered != in.Ref {
					return fmt.Errorf("the registered ref is %v and the store published %v; a "+
						"lifecycle that is not about the exact generation is a lifecycle that "+
						"floats to whatever is at the key", registered, in.Ref)
				}
				if state != "registered" {
					return fmt.Errorf("the exact lifecycle is %q, not registered", state)
				}

				return nil
			}),

		// The absence half of the replacement pair. Its positive control is
		// `the registered exact ref names the published generation`, asserted
		// on the line above the replacement in the same scenario, because
		// "the old ref does not resolve" passes against a chain that published
		// nothing at all.
		CheckThat[PublishedTree]("the old ref no longer resolves",
			func(in PublishedTree) error {
				if in.Superseded.Generation == 0 {
					return fmt.Errorf("nothing was superseded in this chain, so there is no " +
						"old ref to fail on")
				}
				if in.Superseded == in.Ref {
					return fmt.Errorf("the replacement carries the same exact ref %v as the "+
						"generation it replaced", in.Ref)
				}

				daemon := in.Outcome.Source.Draft.Daemon
				keys, err := daemon.outputObjectKeys()
				if err != nil {
					return err
				}
				if len(keys) != 1 {
					return fmt.Errorf("the bucket holds %d objects after a replacement: %v",
						len(keys), keys)
				}

				// The exact old generation, asked for by generation. The KEY is
				// occupied -- by the replacement -- so a check that only asked
				// whether the key existed would pass against a store that never
				// replaced anything.
				attrs, err := daemon.Client.Bucket(daemon.OutputBucket).
					Object(keys[0]).Generation(in.Superseded.Generation).Attrs(daemon.Ctx)
				if err == nil {
					return fmt.Errorf("generation %d is still in the bucket at %q (created %v); "+
						"a superseded exact ref must stop resolving",
						in.Superseded.Generation, keys[0], attrs.Created)
				}
				if !errors.Is(err, storage.ErrObjectNotExist) {
					return fmt.Errorf("asking for the superseded generation %d at %q: %w",
						in.Superseded.Generation, keys[0], err)
				}

				return nil
			}),
	}
}

// confirmSeal proves the drain the ATC is responsible for.
//
// In production the ATC terminates the admitted writers and offers the
// evidence; here nothing was admitted, so the evidence is empty and the daemon
// still checks it against the set IT captured. That is the honest shape: the
// daemon replaces whatever set a caller offers with its own before validating.
func (s HangarDaemon) confirmSeal(source HeldSource, started hangaroutput.SealStarted) error {
	answer := s.capture("confirm-seal", "/capture/v1/seal/confirm", source.Execution,
		map[string]any{
			"execution":     source.Execution,
			"started":       started,
			"drained":       []any{},
			"capture_fence": source.Execution.Fence,
			"observed_at":   hangaroutput.NewTimestamp(time.Now().UTC()),
		})
	if _, err := decodeControl[hangaroutput.CaptureAcknowledgement](answer); err != nil {
		return fmt.Errorf("confirming the seal: %w", err)
	}

	return nil
}

// attest asks for the receipt against a one-use stat challenge.
//
// The challenge names the generation the publish assigned, which is why it
// cannot be minted a call earlier. The control plane owns it in production; the
// fixture plays that part, and the daemon still checks it against what it finds
// in the bucket.
func (s HangarDaemon) attest(source HeldSource,
	published hangaroutput.PublicationResult) (hangaroutput.Receipt, hangaroutput.StatChallenge, error) {
	issuedAt := time.Now().UTC()
	challenge := hangaroutput.StatChallenge{
		Nonce:           "brine-challenge-" + freshUUID(),
		HandoffID:       source.Admission.HandoffID,
		ReservationID:   hangaroutput.ReservationID(source.ReservationID),
		ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
		Ref:             published.Ref,
		CaptureFence:    hangaroutput.CaptureFence(source.Execution.Fence),
		IssuedAt:        hangaroutput.NewTimestamp(issuedAt),
		NotAfter:        hangaroutput.NewTimestamp(issuedAt.Add(5 * time.Minute)),
	}

	answer := s.capture("stat", "/capture/v1/stat", source.Execution, map[string]any{
		"execution": source.Execution,
		"challenge": challenge,
		"claims": hangaroutput.ReceiptClaims{
			Execution:            source.Execution,
			ProducerCheckpointID: hangaroutput.OpaqueID("brine-checkpoint-" + freshUUID()),
			Incarnation:          source.Incarnation,
			Output:               source.Admission.Output,
			WriterFence:          1,
		},
	})

	receipt, err := decodeControl[hangaroutput.Receipt](answer)
	if err != nil {
		return hangaroutput.Receipt{}, hangaroutput.StatChallenge{},
			fmt.Errorf("attesting the publication: %w", err)
	}

	return receipt, challenge, nil
}

// captureAgain runs a whole second capture of the same bytes: a new admission,
// a new hold, a new source with the same content, sealed and published.
//
// It is a second CAPTURE and not a second publish, because repeating a publish
// cannot tell dedup from overwrite. Two captures writing identical bytes is
// what production does, and the assertion is that they share one object.
func (s HangarDaemon) captureAgain(first HeldSource) (CaptureOutcome, error) {
	draft := CaptureDraft{
		Daemon: s,
		Output: first.Admission.Output,
		Admission: hangaroutput.CaptureAdmission{
			ProtocolVersion: hangaroutput.ProtocolVersion,
			Execution: executioncontrol.Identity{
				ExecutionID: executioncontrol.ExecutionID(freshUUID()),
				Fence:       1,
			},
			ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
			HandoffID:       hangaroutput.HandoffID(freshUUID()),
			SourceLeaseID:   hangaroutput.SourceLeaseID(freshUUID()),
			Output:          first.Admission.Output,
			CaptureDeadline: hangaroutput.NewTimestamp(time.Now().UTC().Add(24 * time.Hour)),
		},
		PodUID: executioncontrol.PodUID(freshUUID()),
	}

	admitted := s.base("admit", "/execution/v1/admit", draft.Admission.Execution,
		executioncontrol.Envelope{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        draft.Admission.Execution,
			ActivationEpoch: draft.Admission.ActivationEpoch,
			NodeUID:         hangarNodeUID,
			Capability:      "opaque-admission-capability",
		})
	if _, err := decodeControl[executioncontrol.ClassifyResult](admitted); err != nil {
		return CaptureOutcome{}, fmt.Errorf("admitting the second capture: %w", err)
	}

	// The second capture reserves its own incarnation, as the first did: a
	// reservation is per handoff, and two captures of the same bytes are two
	// locations on this node.
	reserving := s.capture("reserve-incarnation", "/capture/v1/reserve-incarnation",
		draft.Admission.Execution, draft.Admission)
	reserved, err := decodeControl[hangaroutput.ReservedIncarnation](reserving)
	if err != nil {
		return CaptureOutcome{}, fmt.Errorf("reserving the second incarnation: %w", err)
	}
	draft.Reserved = reserved

	answer := s.capture("hold", "/capture/v1/hold", draft.Admission.Execution,
		holdBody(draft.Admission, reserved.Incarnation, draft.PodUID))
	ack, err := decodeControl[hangaroutput.CaptureAcknowledgement](answer)
	if err != nil {
		return CaptureOutcome{}, fmt.Errorf("holding the second source: %w", err)
	}
	second := heldFrom(draft, ack, answer)

	// The same bytes, written into the second incarnation.
	if err := copySourceTree(first.incarnationRoot(), second.incarnationRoot()); err != nil {
		return CaptureOutcome{}, err
	}

	witnessed, err := second.witness(executioncontrol.AcknowledgementFinish,
		executioncontrol.ExitOutcome{ExitCode: 0})
	if err != nil {
		return CaptureOutcome{}, err
	}

	return sealAndPublish(witnessed)
}

// copySourceTree reproduces a source's content under a second incarnation. It
// is the fixture standing in for two producers that wrote the same thing.
func copySourceTree(from, to string) error {
	return filepath.WalkDir(from, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, name)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		body, err := os.ReadFile(name)
		if err != nil {
			return err
		}

		return os.WriteFile(target, body, 0o600)
	})
}

// verifyHangarReceipt checks a receipt with the PRODUCTION verifier under a
// pinned key ring, never against a string a step wrote.
func verifyHangarReceipt(receipt hangaroutput.Receipt, challenge hangaroutput.StatChallenge,
	public ed25519.PublicKey) error {
	ring, err := hangaroutput.NewReceiptKeyRing(hangaroutput.EpochKey{
		KeyID:      hangarReceiptKeyID,
		Epoch:      executioncontrol.ActivationEpoch(hangarEpoch),
		PublicKey:  public,
		ValidFrom:  hangaroutput.NewTimestamp(time.Now().UTC().Add(-time.Hour)),
		ValidUntil: hangaroutput.NewTimestamp(time.Now().UTC().Add(time.Hour)),
	})
	if err != nil {
		return err
	}
	verifier, err := hangaroutput.NewReceiptSignatureVerifier(ring,
		hangaroutput.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		return err
	}

	// The FULL production verification: ring, window, signature, the challenge
	// binding and the one-use consume. A verifier per call, so the nonce one
	// row spends is not one the next row needs.
	return verifier.Verify(receipt, challenge)
}

// The output bucket, read through the same client the daemon uses. It is the
// OUTPUT bucket and not the artifact daemon's: they are never the same one.
func (s HangarDaemon) outputObjectKeys() ([]string, error) {
	var keys []string
	it := s.Client.Bucket(s.OutputBucket).Objects(s.Ctx, nil)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list the Hangar output bucket %q: %w", s.OutputBucket, err)
		}
		keys = append(keys, attrs.Name)
	}

	return keys, nil
}

func (s HangarDaemon) outputMarkerVersion(key string) (string, error) {
	attrs, err := s.Client.Bucket(s.OutputBucket).Object(key).Attrs(s.Ctx)
	if err != nil {
		return "", fmt.Errorf("stat %q in the Hangar output bucket: %w", key, err)
	}

	return attrs.Metadata[hangaroutput.MarkerKeyVersion], nil
}

// seedDerivedKey puts a chosen variant at the key this capture's bytes will
// derive.
//
// The probe is a whole second capture of the same content, published and then
// removed. That is the only honest way to learn the key: it is a hash of bytes
// this process did not canonicalize, under a scope derived from configuration
// this process cannot read. Anything shorter would be the fixture asserting
// against a key production would never choose.
func (s HangarDaemon) seedDerivedKey(source HeldSource, body string,
	metadata func(existing map[string]string) map[string]string) error {
	probe, err := s.captureAgain(source)
	if err != nil {
		return fmt.Errorf("probing for the derived key: %w", err)
	}
	if probe.Published.Ref.Generation == 0 {
		return fmt.Errorf("the probe capture published nothing: %s", probe.Answer.describe())
	}

	keys, err := s.outputObjectKeys()
	if err != nil {
		return err
	}
	if len(keys) != 1 {
		return fmt.Errorf("the probe capture left %d objects in the bucket: %v", len(keys), keys)
	}
	key := keys[0]

	// The probe's own metadata is read before it goes, so a seeded variant can
	// be a REAL marker with one fact changed rather than a fragment that would
	// be reported as corruption instead of as a collision.
	attrs, err := s.Client.Bucket(s.OutputBucket).Object(key).Attrs(s.Ctx)
	if err != nil {
		return fmt.Errorf("reading the probe object's marker at %q: %w", key, err)
	}

	if err := s.Client.Bucket(s.OutputBucket).Object(key).Delete(s.Ctx); err != nil {
		return fmt.Errorf("removing the probe object at %q: %w", key, err)
	}

	writer := s.Client.Bucket(s.OutputBucket).Object(key).NewWriter(s.Ctx)
	writer.Metadata = metadata(attrs.Metadata)
	if _, err := io.WriteString(writer, body); err != nil {
		_ = writer.Close()

		return fmt.Errorf("seeding %q in the Hangar output bucket: %w", key, err)
	}

	return writer.Close()
}
