package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/snapshot"
)

// recordSchemaRevision is one frozen contract-identity document for a record
// type, together with the revision number that fixes its place in that type's
// history.
//
// The descriptor STRING is the source of truth; its digest is computed. Nobody
// hand-writes a digest here. Two reasons:
//
//  1. A hand-written digest is unverifiable. A typo produces a digest that
//     matches nothing, so the type silently stops accepting the corpus it was
//     supposed to keep accepting, and the failure surfaces as ErrCorruptSnapshot
//     on some later read.
//  2. The history has to stay auditable. Keeping every revision's descriptor
//     text means a reader can see what actually changed between revision N and
//     N+1, which a list of opaque hex strings cannot show.
type recordSchemaRevision struct {
	revision   int
	descriptor string
}

// recordSchemaHistory is the append-only revision history of one record type's
// contract identity.
//
// There is deliberately no "which end of the list is current" question to get
// wrong: `current` is named, and `superseded` is an unordered bag whose order is
// derived from each entry's declared revision number.
//
// To bump a descriptor:
//
//	current:    revision N+1 with the NEW descriptor bytes
//	superseded: the previous `current` value, moved verbatim, plus everything
//	            that was already there (order does not matter)
//
// Never edit or delete a superseded entry. Every record already sealed under it
// is re-validated against this table on every read (Record.RevalidateSealed,
// reached from agent/publisher/gateway.go, agent/projection/review.go,
// agent/projection/repository_change.go and agent/functions/repositorymerge), so
// removing a digest retroactively invalidates stored data and reports it as
// corruption rather than as a versioning event. buildRecordSchemaIndex refuses
// histories whose revision numbers are not the contiguous range 1..N, which makes
// a deletion or a reordering fail at package initialisation instead of at read
// time.
//
// Appending a revision is safe in the other direction too: the seal path
// (Record.AdmitForSeal) admits only `current`, so the moment a revision is
// appended, newly sealed records pin it and nothing can still author the old one.
type recordSchemaHistory struct {
	current    recordSchemaRevision
	superseded []recordSchemaRevision
}

// recordSchemaHistories holds the frozen contract-identity documents. Their
// digests are carried by record values so changing a built-in validator cannot
// silently preserve the same advertised contract identity.
//
// Two descriptor FORMS coexist here, and they are trivially distinguishable
// because a revision-1 stamp begins with '{' and a framed canonical
// serialization with 's':
//
//   - Revision 1 is the pre-dialect one-line stamp, written out as a literal. It
//     is immutable and unparsed forever. Every record already sealed under it is
//     re-validated against this table on every read, so editing a byte of it is
//     retroactive corruption of stored data, reported as ErrCorruptSnapshot
//     rather than as a versioning event.
//   - Revision 2 is the canonical serialization of that type's embedded schema
//     document (§10.2 of the dialect), DERIVED from the document rather than
//     pasted. Pasting the bytes would put a second copy of the contract in the
//     tree with nothing comparing the two, which is the failure the derivation
//     exists to make impossible: the document is the contract, and this is the
//     one expression of it.
//
// mustCanonicalSchemaDescriptorFor reads recordSchemaDocumentRevisions, which the
// PARSE phase of the loader (schema_document_load.go) populates alongside
// recordSchemaDocuments. Both properties of that lookup are load-bearing rather
// than incidental:
//
//   - It is the PARSE phase, so the initialisation graph stays acyclic. Parsing
//     references nothing that depends on this map. Reading from anything that
//     validates registration instead — the phase that cross-checks documents
//     against these histories — is an `initialization cycle for
//     recordSchemaHistories` compile error, not a subtlety.
//   - It is keyed by (type, revision), not by newest-document-for-type. The
//     loader deliberately permits a STAGED document at current+1 to sit in the
//     embed tree while its bytes are under review; resolving by type would let
//     that staged file silently redefine the bytes an already-adopted entry
//     hashes.
//
// A semantic validator change must append a new revision here, as a new document
// FILE, and, after external release, introduce a new TypeRef version.
var recordSchemaHistories = map[snapshot.TypeRef]recordSchemaHistory{
	"review/v1": {
		current: recordSchemaRevision{
			revision:   2,
			descriptor: mustCanonicalSchemaDescriptorFor("review/v1", 2),
		},
		superseded: []recordSchemaRevision{{
			revision:   1,
			descriptor: `{"contract":"review/v1","envelope":"record/v1","revision":1}`,
		}},
	},
	"diagnosis/v1": {
		current: recordSchemaRevision{
			revision:   2,
			descriptor: mustCanonicalSchemaDescriptorFor("diagnosis/v1", 2),
		},
		superseded: []recordSchemaRevision{{
			revision:   1,
			descriptor: `{"contract":"diagnosis/v1","envelope":"record/v1","revision":1}`,
		}},
	},
	"validation/v1": {
		current: recordSchemaRevision{
			revision:   3,
			descriptor: mustCanonicalSchemaDescriptorFor("validation/v1", 3),
		},
		superseded: []recordSchemaRevision{
			{revision: 2, descriptor: mustCanonicalSchemaDescriptorFor("validation/v1", 2)},
			{revision: 1, descriptor: `{"contract":"validation/v1","envelope":"record/v1","revision":1}`},
		},
	},
	"repository-change/v1": {
		current: recordSchemaRevision{
			revision:   2,
			descriptor: mustCanonicalSchemaDescriptorFor("repository-change/v1", 2),
		},
		superseded: []recordSchemaRevision{{
			revision:   1,
			descriptor: `{"contract":"repository-change/v1","envelope":"record/v1","revision":1}`,
		}},
	},
	"measurements/v1": {
		current: recordSchemaRevision{
			revision:   2,
			descriptor: mustCanonicalSchemaDescriptorFor("measurements/v1", 2),
		},
		superseded: []recordSchemaRevision{{
			revision:   1,
			descriptor: `{"contract":"measurements/v1","envelope":"record/v1","revision":1}`,
		}},
	},
}

// recordSchemaIndex maps each record type to its accepted schema digests,
// newest first. Index 0 is the digest newly authored records pin.
type recordSchemaIndex map[snapshot.TypeRef][]snapshot.Digest

var acceptedRecordSchemaDigests = mustBuildRecordSchemaIndex(recordSchemaHistories)

func schemaDescriptorDigest(descriptor string) snapshot.Digest {
	sum := sha256.Sum256([]byte(descriptor))
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func mustBuildRecordSchemaIndex(histories map[snapshot.TypeRef]recordSchemaHistory) recordSchemaIndex {
	index, err := buildRecordSchemaIndex(histories)
	if err != nil {
		panic("snapshot contracts: invalid record schema history: " + err.Error())
	}
	return index
}

// buildRecordSchemaIndex derives the accepted-digest index from the declared
// histories and enforces the append-only invariants structurally: revision
// numbers must form the contiguous range 1..N, `current` must be the newest of
// them, and no two revisions anywhere may share descriptor bytes.
func buildRecordSchemaIndex(histories map[snapshot.TypeRef]recordSchemaHistory) (recordSchemaIndex, error) {
	index := make(recordSchemaIndex, len(histories))
	owners := make(map[snapshot.Digest]snapshot.TypeRef)
	refs := make([]snapshot.TypeRef, 0, len(histories))
	for ref := range histories {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i] < refs[j] })
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("record schema history key: %w", err)
		}
		history := histories[ref]
		revisions := make([]recordSchemaRevision, 0, len(history.superseded)+1)
		revisions = append(revisions, history.current)
		revisions = append(revisions, history.superseded...)
		sort.SliceStable(revisions, func(i, j int) bool {
			return revisions[i].revision > revisions[j].revision
		})
		if revisions[0].revision != history.current.revision {
			return nil, fmt.Errorf(
				"%q: current revision %d is not the newest declared revision (%d is); a bump appends a higher revision and moves the old current into superseded",
				ref, history.current.revision, revisions[0].revision,
			)
		}
		digests := make([]snapshot.Digest, 0, len(revisions))
		for position, revision := range revisions {
			want := len(revisions) - position
			if revision.revision != want {
				return nil, fmt.Errorf(
					"%q: schema revisions must be the contiguous range 1..%d, newest first, with no gaps, duplicates, or removals; position %d declares revision %d, want %d",
					ref, len(revisions), position, revision.revision, want,
				)
			}
			if revision.descriptor == "" {
				return nil, fmt.Errorf("%q: revision %d descriptor is empty", ref, revision.revision)
			}
			digest := schemaDescriptorDigest(revision.descriptor)
			if owner, found := owners[digest]; found {
				return nil, fmt.Errorf(
					"%q revision %d has the same descriptor bytes as %q, so it would compute the same digest; every schema revision must have distinct bytes",
					ref, revision.revision, owner,
				)
			}
			owners[digest] = ref
			digests = append(digests, digest)
		}
		index[ref] = digests
	}
	return index, nil
}

// SchemaDigestFor returns the schema digest newly authored records of ref must
// pin: the newest entry in ref's accepted digest history.
func SchemaDigestFor(ref snapshot.TypeRef) (snapshot.Digest, bool) {
	accepted, found := acceptedRecordSchemaDigests[ref]
	if !found {
		return "", false
	}
	return accepted[0], true
}

// AcceptedSchemaDigests returns every schema digest ref's stored records may
// carry, newest first. Index 0 is what writes pin and the only digest seal-time
// admission accepts. Later entries are superseded revisions that remain valid at
// the read-time gate forever.
func AcceptedSchemaDigests(ref snapshot.TypeRef) ([]snapshot.Digest, bool) {
	accepted, found := acceptedRecordSchemaDigests[ref]
	if !found {
		return nil, false
	}
	return append([]snapshot.Digest(nil), accepted...), true
}

// SchemaRevisionFor maps a schema digest a stored record carries to the revision
// number that digest identifies. It is the only correct way to get from the one
// contract-identity fact a stored record has to anything keyed by revision, such
// as SchemaDigestForRevision's inverse lookup or a revision-gated read rule.
//
// The mapping is deliberately NOT the digest's position in the accepted history.
// That history is newest-first and revisions are numbered from 1, so a type with
// N accepted revisions carries revision R at position N-R: position 0 is the
// HIGHEST revision, and no position equals its own revision number once N > 2.
// Reading position as revision is inverted and off by one, which is why this
// function exists instead of leaving the arithmetic to every caller.
func SchemaRevisionFor(ref snapshot.TypeRef, digest snapshot.Digest) (int, bool) {
	accepted, found := acceptedRecordSchemaDigests[ref]
	if !found {
		return 0, false
	}
	for position, candidate := range accepted {
		if candidate == digest {
			return len(accepted) - position, true
		}
	}
	return 0, false
}

// SchemaDigestForRevision is the inverse of SchemaRevisionFor. Revisions are
// numbered from 1; revision 0 exists for no type.
func SchemaDigestForRevision(ref snapshot.TypeRef, revision int) (snapshot.Digest, bool) {
	accepted, found := acceptedRecordSchemaDigests[ref]
	if !found || revision < 1 || revision > len(accepted) {
		return "", false
	}
	return accepted[len(accepted)-revision], true
}

// CurrentSchemaRevisionFor returns the revision newly authored records pin, which
// is the revision of the digest SchemaDigestFor hands out.
func CurrentSchemaRevisionFor(ref snapshot.TypeRef) (int, bool) {
	accepted, found := acceptedRecordSchemaDigests[ref]
	if !found {
		return 0, false
	}
	return len(accepted), true
}

// IsAcceptedSchemaDigest reports whether digest is any revision of ref's schema
// history. This is the READ-TIME rule and nothing else: seal-time admission uses
// SchemaDigestFor and requires exact equality with the newest revision, so a
// producer cannot pick which contract identity its own output advertises. Calling
// this from a write path is the defect the two gates exist to prevent.
func IsAcceptedSchemaDigest(ref snapshot.TypeRef, digest snapshot.Digest) bool {
	for _, accepted := range acceptedRecordSchemaDigests[ref] {
		if accepted == digest {
			return true
		}
	}
	return false
}

func IsRecordType(ref snapshot.TypeRef) bool {
	_, found := acceptedRecordSchemaDigests[ref]
	return found
}
