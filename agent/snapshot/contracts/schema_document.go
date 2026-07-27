package contracts

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/snapshot"
)

// schemaDocumentSources holds one type's contract identity per file, expressed as
// data.
//
// go:embed cannot escape its own package directory, which is one of the four
// constraints that put the documents here beside the loader rather than in
// agent/schema — that module is standalone and must never import the main
// module, so dragging the record contracts into it would move the boundary in
// the wrong direction.
//
// A revision is a NEW FILE. The filename carries the revision, so revision 3 is
// schemas/<name>.<version>.rev3.json and the rev2 file is never edited: editing a
// file whose bytes are already a frozen descriptor is the same defect as editing
// a superseded descriptor string.
//
//go:embed schemas/*.json
var schemaDocumentSources embed.FS

const schemaDocumentDirectory = "schemas"

// The envelope shape every record type shares. Its field set is fixed by the
// dialect rather than by any document, so a change to it is a record/v2 envelope
// and not an edit.
const recordEnvelopeName = "record/v1"

// recordSchemaDialectName is the dialect version every document declares, in its
// own frozen bytes, as its first key.
//
// It is inside the digest because the dialect contributes structure the document
// does not spell out: a `score`, `blob` or `anchor` declaration expands to a fixed
// leaf subtree that appears NOWHERE in the file. Which leaves those are, what
// presence each gets, and how `forbidden` is derived are all decided by the
// dialect version in force at load time. Without this marker the frozen bytes are
// ambiguous the moment the expansions change — the descriptor would name a subtree
// nobody can reconstruct — and there is no later opportunity to add it, because a
// key added after the bump changes every descriptor.
//
// A new expansion, a new kind, or any change to a derived presence is
// record-schema-dialect/2 and is a visibly different thing, exactly as a
// canonicalization change is snapshot-canonical-json/2.
const recordSchemaDialectName = "record-schema-dialect/1"

// SchemaKind is one entry of the enforced core. The set is closed: an extension
// point inside a frozen digest is a second vocabulary waiting to happen.
type SchemaKind string

const (
	KindString     SchemaKind = "string"
	KindMarkdown   SchemaKind = "markdown"
	KindIdentifier SchemaKind = "identifier"
	KindInt        SchemaKind = "int"
	KindFloat      SchemaKind = "float"
	KindBool       SchemaKind = "bool"
	KindTimestamp  SchemaKind = "timestamp"
	KindDuration   SchemaKind = "duration"
	KindEnum       SchemaKind = "enum"
	KindScore      SchemaKind = "score"
	KindBlob       SchemaKind = "blob"
	KindEntitySet  SchemaKind = "entity-set"
	KindArray      SchemaKind = "array"
	KindObject     SchemaKind = "object"
	KindAnchor     SchemaKind = "anchor"
	KindRecordRef  SchemaKind = "record-ref"
)

// SchemaPresence is derived from what the Go validator actually tolerates, never
// authored. Declaring `required` on a field the validator tolerates as absent
// retroactively corrupts every stored record that omitted it, and the failure
// surfaces as corruption on some later read rather than at seal time.
type SchemaPresence string

const (
	PresenceRequired  SchemaPresence = "required"
	PresenceOptional  SchemaPresence = "optional"
	PresenceForbidden SchemaPresence = "forbidden"
)

// AbsentImage is the one place a field's Go representation shows through, and the
// difference is load-bearing: an explicit 0 in a *float64 is distinguishable from
// absence and can be rejected on its own, while an explicit 0 in a float64 cannot
// be distinguished from absence at all.
type AbsentImage string

const (
	AbsentImageZero AbsentImage = "zero"
	AbsentImageNull AbsentImage = "null"
)

// Subject-set port sourcing. Where the candidate-port set comes from is a Go rule
// and cannot be a schema rule: at seal time it is the server's compiled port
// declarations, and at read time the sealed candidate-role subjects that
// seal-time admission already certified.
const (
	PortsAnyExposedInput       = "any-exposed-input"
	PortsCandidatePortsExactly = "candidate-ports-exactly"
)

// The open value written explicitly for a score's scale and direction. An omitted
// key meaning "open" would be a second spelling.
const schemaOpenValue = "open"

type EnumValue struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

type GoOnlyRule struct {
	ID   string `json:"id"`
	Rule string `json:"rule"`
}

type RoleBounds struct {
	Minimum int  `json:"minimum"`
	Maximum *int `json:"maximum"`
}

// SubjectShape is the one envelope constraint a document declares, because it is
// the one that genuinely differs per type. A role absent from Roles is forbidden.
type SubjectShape struct {
	Minimum            int                        `json:"minimum"`
	Maximum            *int                       `json:"maximum"`
	Roles              map[SubjectRole]RoleBounds `json:"roles"`
	SubjectType        *snapshot.TypeRef          `json:"subject_type"`
	UniformSubjectType bool                       `json:"uniform_subject_type"`
	Ports              string                     `json:"ports"`
}

// FieldDeclaration is one field's declared shape. Every declaration has Kind and
// Presence; the remaining attributes are per-kind and are rejected where they do
// not apply.
type FieldDeclaration struct {
	Kind        SchemaKind     `json:"kind"`
	Presence    SchemaPresence `json:"presence"`
	Label       string         `json:"label,omitempty"`
	Description string         `json:"description,omitempty"`
	GoRules     []string       `json:"go_rules,omitempty"`

	AbsentImage AbsentImage     `json:"absent_image,omitempty"`
	Values      []EnumValue     `json:"values,omitempty"`
	Minimum     json.RawMessage `json:"minimum,omitempty"`
	Maximum     json.RawMessage `json:"maximum,omitempty"`
	IDField     string          `json:"id_field,omitempty"`
	Element     SchemaKind      `json:"element,omitempty"`
	Unique      bool            `json:"unique,omitempty"`
	Sorted      bool            `json:"sorted,omitempty"`
	Scale       string          `json:"scale,omitempty"`
	Direction   string          `json:"direction,omitempty"`
	PathPrefix  string          `json:"path_prefix,omitempty"`
	MediaTypes  json.RawMessage `json:"media_types,omitempty"`
	RecordType  string          `json:"record_type,omitempty"`
	Resolution  string          `json:"resolution,omitempty"`

	// resolvesSubject marks the synthesized `subject` leaf of an anchor: the one
	// declared field whose value must name a subject this record declares. It is
	// set by the loader when it expands a composite kind, never by a document —
	// "resolves through a declared subject" is fixed by the dialect and is not a
	// per-site choice.
	resolvesSubject bool
}

// SchemaDocument is one record type's contract identity as data. Its canonical
// serialization is the descriptor string the digest-history mechanism hashes, so
// this is not documentation about the contract — it is the contract.
type SchemaDocument struct {
	// Dialect is the first declaration in the file: the dialect version whose
	// composite-kind expansions this document's bytes are to be read under.
	Dialect      string                      `json:"dialect"`
	Contract     snapshot.TypeRef            `json:"contract"`
	Envelope     string                      `json:"envelope"`
	Revision     int                         `json:"revision"`
	Supersedes   int                         `json:"supersedes"`
	Description  string                      `json:"description"`
	SubjectShape SubjectShape                `json:"subject_shape"`
	Fields       map[string]FieldDeclaration `json:"fields"`
	GoOnlyRules  []GoOnlyRule                `json:"go_only_rules"`

	// fileName is the embedded file this document came from. It is not part of the
	// canonical serialization; the digest is computed over the document's own
	// bytes, and the filename is cross-checked against the header instead.
	fileName string

	// source is the file's exact bytes. The descriptor is the CANONICAL
	// serialization of these, never the file bytes themselves — which is the only
	// arrangement in which the auditability claim pays off, because a
	// canonicalized one-line file is undiffable.
	source []byte

	// resolved is Fields plus the leaves that the composite kinds imply — a
	// score's six, a blob's three, an anchor's seven. Their presence is derived
	// from the declaration rather than authored, which is the only reason
	// `forbidden` exists at all.
	resolved map[string]FieldDeclaration
}

// impliedLeafPaths is the leaf set the document implies: every declared leaf plus
// the expansion of every composite kind. Containers are not leaves — a container
// carries no claim — which is why this set is exactly what the epistemic table
// keys on.
func (document SchemaDocument) impliedLeafPaths() []string {
	paths := make([]string, 0, len(document.resolved))
	for path, declaration := range document.resolved {
		if isLeafKind(declaration) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

// Descriptor is the string this revision contributes to the type's digest
// history: the canonical serialization of the document's own bytes. Its digest is
// what a recordSchemaHistories entry carries.
func (document SchemaDocument) Descriptor() (string, error) {
	framed, err := canonicalJSONSerialization(document.source)
	if err != nil {
		return "", fmt.Errorf("snapshot contracts: canonical descriptor for %s: %w", document.fileName, err)
	}
	return string(framed), nil
}

// mustCanonicalSchemaDescriptor is the form a recordSchemaHistories entry uses
// once a document is adopted as a revision. It panics rather than returning an
// error because a descriptor that cannot be computed is not a runtime condition:
// the document is embedded, so the failure is a build-time defect.
func mustCanonicalSchemaDescriptor(document SchemaDocument) string {
	descriptor, err := document.Descriptor()
	if err != nil {
		panic(err.Error())
	}
	return descriptor
}

// mustCanonicalSchemaDescriptorFor resolves the document at exactly (ref,
// revision) — deliberately NOT the newest document for the type. The loader
// permits a STAGED document at current+1 to sit in the embed tree while its
// bytes are under review; deriving an adopted history entry from the
// newest-document map would let that staged file silently redefine the bytes
// the entry hashes and swap which declaration the core validator enforces.
// Pinning the lookup by revision makes that impossible by construction.
func mustCanonicalSchemaDescriptorFor(ref snapshot.TypeRef, revision int) string {
	document, found := recordSchemaDocumentRevisions[epistemicKey{ref: ref, revision: revision}]
	if !found {
		panic(fmt.Sprintf("snapshot contracts: no embedded schema document for %q revision %d", ref, revision))
	}
	return mustCanonicalSchemaDescriptor(document)
}

// SchemaDocumentFor returns the declared schema of one record type.
func SchemaDocumentFor(ref snapshot.TypeRef) (SchemaDocument, bool) {
	document, found := recordSchemaDocuments[ref]
	if !found {
		return SchemaDocument{}, false
	}
	return document.clone(), true
}

func (document SchemaDocument) clone() SchemaDocument {
	fields := make(map[string]FieldDeclaration, len(document.Fields))
	for path, declaration := range document.Fields {
		fields[path] = declaration
	}
	resolved := make(map[string]FieldDeclaration, len(document.resolved))
	for path, declaration := range document.resolved {
		resolved[path] = declaration
	}
	document.Fields = fields
	document.resolved = resolved
	document.GoOnlyRules = append([]GoOnlyRule(nil), document.GoOnlyRules...)
	roles := make(map[SubjectRole]RoleBounds, len(document.SubjectShape.Roles))
	for role, bounds := range document.SubjectShape.Roles {
		roles[role] = bounds
	}
	document.SubjectShape.Roles = roles
	return document
}
