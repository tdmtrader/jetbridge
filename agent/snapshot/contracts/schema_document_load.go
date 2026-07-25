package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// Loading is all-or-nothing at package initialisation, following the same
// mustBuild pattern as the digest history: a document that does not describe the
// Go types beside it is a defect that must not be discoverable at read time.
//
// It runs in TWO PHASES, and the split is not stylistic — it is what makes a
// descriptor bump expressible at all. recordSchemaHistories now carries
// `descriptor: mustCanonicalSchemaDescriptor(<a loaded document>)` for every
// type, so the histories depend on the documents; if loading also depended on the
// histories, Go's initialisation-order analysis would report `initialization
// cycle for recordSchemaHistories` and the package would not compile. So:
//
//	PHASE 1 (here) parses. It reads the embedded filesystem and judges everything
//	judgeable from a document alone, and it references NOTHING that depends on
//	recordSchemaHistories. This is the phase the bump's descriptor expression
//	reads from.
//
//	PHASE 2 (registerSchemaDocuments) cross-checks each document against the rest
//	of the package: the digest history, the Go record types, and the epistemic
//	table. It is a blank package-level var, so Go schedules it after everything it
//	transitively references — including the histories and phase 1 — and any
//	failure is still a package-initialisation panic and never a read-time report.
//
// recordSchemaDocuments is the newest document per record type;
// recordSchemaDocumentRevisions holds every embedded revision, because a revision
// is an append-only FILE and the superseded ones stay in the tree forever.
var recordSchemaDocuments, recordSchemaDocumentRevisions = mustParseSchemaDocuments()

// Phase 2. The blank name is load-bearing: it is a package-level variable, so its
// initialiser is ordered by the same dependency analysis as every other one, and
// declaring it here rather than in an init() function is what puts it AFTER
// recordSchemaHistories instead of merely after phase 1.
var _ = mustRegisterSchemaDocuments()

func mustParseSchemaDocuments() (map[snapshot.TypeRef]SchemaDocument, map[epistemicKey]SchemaDocument) {
	newest, revisions, err := parseSchemaDocuments()
	if err != nil {
		panic("snapshot contracts: invalid record schema document: " + err.Error())
	}
	return newest, revisions
}

func mustRegisterSchemaDocuments() struct{} {
	if err := registerSchemaDocuments(); err != nil {
		panic("snapshot contracts: invalid record schema document: " + err.Error())
	}
	return struct{}{}
}

// parseSchemaDocuments is phase 1: embedded bytes in, parsed documents out. It
// must not reference recordSchemaHistories, acceptedRecordSchemaDigests or
// anything derived from them, directly or through a called function — that
// reference is the initialisation cycle the two phases exist to avoid, and the
// compiler is what enforces it.
func parseSchemaDocuments() (map[snapshot.TypeRef]SchemaDocument, map[epistemicKey]SchemaDocument, error) {
	names, err := embeddedSchemaDocumentNames()
	if err != nil {
		return nil, nil, err
	}
	newest := make(map[snapshot.TypeRef]SchemaDocument, len(names))
	revisions := make(map[epistemicKey]SchemaDocument, len(names))
	for _, name := range names {
		source, err := schemaDocumentSources.ReadFile(schemaDocumentDirectory + "/" + name)
		if err != nil {
			return nil, nil, fmt.Errorf("read embedded schema document %q: %w", name, err)
		}
		document, err := parseSchemaDocument(name, source)
		if err != nil {
			return nil, nil, err
		}
		key := epistemicKey{ref: document.Contract, revision: document.Revision}
		if existing, found := revisions[key]; found {
			return nil, nil, fmt.Errorf(
				"%q revision %d is declared by both %s and %s; a revision is one file",
				document.Contract, document.Revision, existing.fileName, document.fileName,
			)
		}
		revisions[key] = document
		if current, found := newest[document.Contract]; !found || document.Revision > current.Revision {
			newest[document.Contract] = document
		}
	}
	return newest, revisions, nil
}

// registerSchemaDocuments is phase 2: every check that needs the rest of the
// package. Documents are visited in file-name order so a failing tree always
// reports the same one first.
func registerSchemaDocuments() error {
	names, err := embeddedSchemaDocumentNames()
	if err != nil {
		return err
	}
	byFileName := make(map[string]SchemaDocument, len(recordSchemaDocumentRevisions))
	for _, document := range recordSchemaDocumentRevisions {
		byFileName[document.fileName] = document
	}
	for _, name := range names {
		document, found := byFileName[name]
		if !found {
			return fmt.Errorf("embedded schema document %q was not parsed", name)
		}
		if err := validateSchemaDocumentRegistration(document); err != nil {
			return err
		}
	}
	for ref := range recordSchemaHistories {
		if _, found := recordSchemaDocuments[ref]; !found {
			return fmt.Errorf("record type %q has no schema document", ref)
		}
	}
	return nil
}

func embeddedSchemaDocumentNames() ([]string, error) {
	entries, err := schemaDocumentSources.ReadDir(schemaDocumentDirectory)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema documents: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("embedded schema directory contains subdirectory %q", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

var schemaDocumentKeys = []string{
	"dialect", "contract", "envelope", "revision", "supersedes",
	"description", "subject_shape", "fields", "go_only_rules",
}

var subjectShapeKeys = []string{
	"minimum", "maximum", "roles", "subject_type", "uniform_subject_type", "ports",
}

var roleBoundsKeys = []string{"minimum", "maximum"}

// parseSchemaDocument validates everything about one document that can be judged
// from the document alone. What it deliberately does NOT judge is registration:
// whether the contract is a record type, whether the revision links to that
// type's digest history, and whether the declaration matches the Go struct. Those
// are validateSchemaDocumentRegistration's job, so that a document can be
// well-formedness-tested without dragging six real types in with it.
func parseSchemaDocument(fileName string, source []byte) (SchemaDocument, error) {
	label := fmt.Sprintf("schema document %s", fileName)
	raw, err := requireExactObjectKeys(label, source, schemaDocumentKeys)
	if err != nil {
		return SchemaDocument{}, err
	}
	if err := rejectEpistemicKeys(label, source); err != nil {
		return SchemaDocument{}, err
	}
	var document SchemaDocument
	if err := decodeStrictJSON(source, &document); err != nil {
		return SchemaDocument{}, fmt.Errorf("%s: %w", label, err)
	}
	document.fileName = fileName
	document.source = append([]byte(nil), source...)

	// The dialect version is checked FIRST, before anything else about the
	// document's content is judged. Every later rule — which leaves a composite kind
	// implies, where `forbidden` comes from, what an attribute means — is a rule OF a
	// dialect version, so reading a document whose dialect this loader does not
	// implement is guessing. A missing key is caught by requireExactObjectKeys above,
	// which is why a document cannot be silently read as dialect 1 by default.
	if document.Dialect != recordSchemaDialectName {
		return SchemaDocument{}, fmt.Errorf(
			"%s: dialect is %q, want %q; the composite-kind expansions are fixed by the dialect and appear nowhere in the document's own bytes, so a document this loader cannot read under its declared dialect is refused rather than guessed at",
			label, document.Dialect, recordSchemaDialectName,
		)
	}
	if err := document.Contract.Validate(); err != nil {
		return SchemaDocument{}, fmt.Errorf("%s: contract: %w", label, err)
	}
	if document.Envelope != recordEnvelopeName {
		return SchemaDocument{}, fmt.Errorf(
			"%s: envelope is %q, want %q; a change to the envelope's fixed field set is a record/v2 envelope and not an edit",
			label, document.Envelope, recordEnvelopeName,
		)
	}
	if document.Revision < 2 {
		return SchemaDocument{}, fmt.Errorf(
			"%s: revision is %d; revision 1 is the pre-dialect one-line stamp and stays immutable and unparsed forever, so a document is at least revision 2",
			label, document.Revision,
		)
	}
	if document.Supersedes != document.Revision-1 {
		return SchemaDocument{}, fmt.Errorf(
			"%s: revision %d supersedes %d, want %d; the linkage must be readable from the descriptor bytes alone",
			label, document.Revision, document.Supersedes, document.Revision-1,
		)
	}
	if strings.TrimSpace(document.Description) == "" {
		return SchemaDocument{}, fmt.Errorf("%s: description is required", label)
	}
	if want := schemaDocumentFileName(document.Contract, document.Revision); fileName != want {
		return SchemaDocument{}, fmt.Errorf(
			"%s: file name disagrees with the header; %q revision %d belongs in %q",
			label, document.Contract, document.Revision, want,
		)
	}
	if err := validateSubjectShape(label, raw["subject_shape"], document.SubjectShape); err != nil {
		return SchemaDocument{}, err
	}
	if err := document.validateGoOnlyRules(label); err != nil {
		return SchemaDocument{}, err
	}
	if err := document.validateFields(label); err != nil {
		return SchemaDocument{}, err
	}
	resolved, err := document.resolveDeclarations(label)
	if err != nil {
		return SchemaDocument{}, err
	}
	document.resolved = resolved
	return document, nil
}

func schemaDocumentFileName(contract snapshot.TypeRef, revision int) string {
	return fmt.Sprintf("%s.rev%d.json", strings.Replace(contract.String(), "/", ".", 1), revision)
}

func (document SchemaDocument) validateGoOnlyRules(label string) error {
	ids := make(map[string]struct{}, len(document.GoOnlyRules))
	for _, rule := range document.GoOnlyRules {
		if err := ValidateIdentifier(fmt.Sprintf("%s: go_only_rules id %q", label, rule.ID), rule.ID); err != nil {
			return err
		}
		if _, found := ids[rule.ID]; found {
			return fmt.Errorf("%s: go_only_rules id %q is duplicate; ids are unique within a document because field declarations reference them", label, rule.ID)
		}
		ids[rule.ID] = struct{}{}
		if strings.TrimSpace(rule.Rule) == "" {
			return fmt.Errorf("%s: go_only_rules %q has no rule text", label, rule.ID)
		}
	}
	for _, path := range sortedFieldPaths(document.Fields) {
		for _, reference := range document.Fields[path].GoRules {
			if _, found := ids[reference]; !found {
				return fmt.Errorf("%s: field %q references go rule %q, which this document does not declare", label, path, reference)
			}
		}
	}
	return nil
}

func (document SchemaDocument) validateFields(label string) error {
	if len(document.Fields) == 0 {
		return fmt.Errorf("%s: fields declares nothing", label)
	}
	body, found := document.Fields[bodyFieldPath]
	if !found {
		return fmt.Errorf("%s: fields must declare %q, the root of the body subtree", label, bodyFieldPath)
	}
	if body.Kind != KindObject {
		return fmt.Errorf("%s: %q must be an object, not %q", label, bodyFieldPath, body.Kind)
	}
	for _, path := range sortedFieldPaths(document.Fields) {
		declaration := document.Fields[path]
		if err := ValidateFieldDeclarationPath(path); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		segments := strings.Split(path, fieldPathSeparator)
		if segments[0] != bodyFieldPath {
			return fmt.Errorf(
				"%s: declaration path %q is outside the %q subtree; the envelope's own fields are fixed by the record/v1 envelope and are never re-declared per type",
				label, path, bodyFieldPath,
			)
		}
		if segments[len(segments)-1] == fieldPathWildcard {
			return fmt.Errorf("%s: declaration path %q ends in %q, which addresses no field", label, path, fieldPathWildcard)
		}
		if err := document.validateFieldParent(label, path); err != nil {
			return err
		}
		if err := validateFieldDeclaration(label, path, declaration); err != nil {
			return err
		}
		if declaration.Kind == KindEntitySet {
			idPath := path + fieldPathSeparator + fieldPathWildcard + fieldPathSeparator + declaration.IDField
			id, found := document.Fields[idPath]
			if !found || id.Kind != KindIdentifier || id.Presence != PresenceRequired {
				return fmt.Errorf(
					"%s: entity-set %q declares id_field %q, so %q must be declared as a required identifier",
					label, path, declaration.IDField, idPath,
				)
			}
		}
	}
	return nil
}

// validateFieldParent is the structural closure rule: a declaration is reachable
// from `body` through declared containers, so a typo cannot introduce a field
// nothing knows the shape of.
func (document SchemaDocument) validateFieldParent(label, path string) error {
	cut := strings.LastIndex(path, fieldPathSeparator)
	if cut < 0 {
		if path != bodyFieldPath {
			return fmt.Errorf("%s: declaration path %q has no declared parent", label, path)
		}
		return nil
	}
	parent := path[:cut]
	if strings.HasSuffix(parent, fieldPathSeparator+fieldPathWildcard) {
		container := strings.TrimSuffix(parent, fieldPathSeparator+fieldPathWildcard)
		declaration, found := document.Fields[container]
		if !found {
			return fmt.Errorf("%s: declaration path %q has no declared parent %q", label, path, container)
		}
		if declaration.Kind == KindEntitySet {
			return nil
		}
		if declaration.Kind == KindArray && declaration.Element == KindObject {
			return nil
		}
		return fmt.Errorf(
			"%s: declaration path %q declares an element field, but its parent %q is a %q and has no object elements",
			label, path, container, declaration.Kind,
		)
	}
	declaration, found := document.Fields[parent]
	if !found {
		return fmt.Errorf("%s: declaration path %q has no declared parent %q", label, path, parent)
	}
	if declaration.Kind != KindObject {
		return fmt.Errorf("%s: declaration path %q has parent %q, which is a %q and not an object", label, path, parent, declaration.Kind)
	}
	return nil
}

const bodyFieldPath = "body"

// isBodyFieldPath is exact-SEGMENT matching, not a string prefix. The envelope
// and the body share one path namespace, and the frozen grammar separates
// segments with "/", so a future envelope field whose name merely STARTS with
// "body" — `bodyX`, say — is an envelope field and not a body leaf. Classifying
// it as one would demand a body declaration for an envelope field and panic the
// package at initialisation, which is the loudest possible way to be wrong about
// a field nobody has written yet.
func isBodyFieldPath(path string) bool {
	return path == bodyFieldPath || strings.HasPrefix(path, bodyFieldPath+fieldPathSeparator)
}

// requiredAttributes and allowedAttributes together close the vocabulary per
// kind. An attribute that does not apply is an error rather than ignored: a
// silently ignored `minimum` on a float would read as a bound that nothing
// enforces, which is the failure mode a declared schema exists to remove.
var requiredAttributes = map[SchemaKind][]string{
	KindInt:       {"absent_image"},
	KindFloat:     {"absent_image"},
	KindBool:      {"absent_image"},
	KindEnum:      {"values"},
	KindScore:     {"scale", "direction"},
	KindBlob:      {"path_prefix", "media_types"},
	KindEntitySet: {"id_field"},
	KindArray:     {"element"},
	KindRecordRef: {"record_type", "resolution"},
}

var allowedAttributes = map[SchemaKind][]string{
	KindInt:       {"absent_image", "minimum", "maximum"},
	KindFloat:     {"absent_image"},
	KindBool:      {"absent_image"},
	KindDuration:  {"minimum", "maximum"},
	KindEnum:      {"values"},
	KindScore:     {"scale", "direction"},
	KindBlob:      {"path_prefix", "media_types"},
	KindEntitySet: {"id_field"},
	KindArray:     {"element", "unique", "sorted"},
	KindRecordRef: {"record_type", "resolution"},
}

func validateFieldDeclaration(label, path string, declaration FieldDeclaration) error {
	switch declaration.Presence {
	case PresenceRequired, PresenceOptional:
	case PresenceForbidden:
		return fmt.Errorf(
			"%s: field %q authors presence %q; forbidden is derived from a pinning elsewhere in the same declaration and is emitted by the loader, never written by hand",
			label, path, declaration.Presence,
		)
	default:
		return fmt.Errorf("%s: field %q presence %q must be required or optional", label, path, declaration.Presence)
	}
	if !isDeclarableKind(declaration.Kind) {
		return fmt.Errorf(
			"%s: field %q kind %q is not one of the enforced core kinds; the set is closed, because an extension point inside a frozen digest is a second vocabulary waiting to happen",
			label, path, declaration.Kind,
		)
	}
	set := declaredAttributes(declaration)
	allowed := make(map[string]struct{}, len(allowedAttributes[declaration.Kind]))
	for _, name := range allowedAttributes[declaration.Kind] {
		allowed[name] = struct{}{}
	}
	for _, name := range set {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s: field %q is a %q and cannot declare %q", label, path, declaration.Kind, name)
		}
	}
	present := make(map[string]struct{}, len(set))
	for _, name := range set {
		present[name] = struct{}{}
	}
	for _, name := range requiredAttributes[declaration.Kind] {
		if _, ok := present[name]; !ok {
			return fmt.Errorf("%s: field %q is a %q and must declare %q", label, path, declaration.Kind, name)
		}
	}
	return validateFieldAttributes(label, path, declaration)
}

func validateFieldAttributes(label, path string, declaration FieldDeclaration) error {
	if declaration.AbsentImage != "" {
		switch declaration.AbsentImage {
		case AbsentImageZero, AbsentImageNull:
		default:
			return fmt.Errorf("%s: field %q absent_image %q must be zero or null", label, path, declaration.AbsentImage)
		}
	}
	if declaration.Kind == KindEnum {
		if len(declaration.Values) == 0 {
			return fmt.Errorf("%s: field %q is an enum and must declare a non-empty values array", label, path)
		}
		seen := make(map[string]struct{}, len(declaration.Values))
		for _, value := range declaration.Values {
			if strings.TrimSpace(value.Value) == "" {
				return fmt.Errorf("%s: field %q declares a blank enum value", label, path)
			}
			if _, found := seen[value.Value]; found {
				return fmt.Errorf("%s: field %q enum value %q is duplicate", label, path, value.Value)
			}
			seen[value.Value] = struct{}{}
		}
	}
	if declaration.Kind == KindInt {
		minimum, err := integerAttribute(label, path, "minimum", declaration.Minimum)
		if err != nil {
			return err
		}
		maximum, err := integerAttribute(label, path, "maximum", declaration.Maximum)
		if err != nil {
			return err
		}
		if minimum != nil && maximum != nil && *minimum > *maximum {
			return fmt.Errorf("%s: field %q declares minimum %d above maximum %d", label, path, *minimum, *maximum)
		}
	}
	if declaration.Kind == KindDuration {
		minimum, err := durationAttribute(label, path, "minimum", declaration.Minimum)
		if err != nil {
			return err
		}
		maximum, err := durationAttribute(label, path, "maximum", declaration.Maximum)
		if err != nil {
			return err
		}
		if minimum != nil && maximum != nil && *minimum > *maximum {
			return fmt.Errorf("%s: field %q declares minimum above maximum", label, path)
		}
	}
	if declaration.Kind == KindArray {
		if !isScalarKind(declaration.Element) && declaration.Element != KindObject && declaration.Element != KindAnchor {
			return fmt.Errorf("%s: field %q element %q must be a scalar kind, object, or anchor", label, path, declaration.Element)
		}
		if (declaration.Unique || declaration.Sorted) && !isScalarKind(declaration.Element) {
			return fmt.Errorf("%s: field %q declares unique or sorted, which apply only when element is a scalar kind", label, path)
		}
	}
	if declaration.Kind == KindEntitySet {
		if err := ValidateIdentifier(fmt.Sprintf("%s: field %q id_field", label, path), declaration.IDField); err != nil {
			return err
		}
	}
	if declaration.Kind == KindScore {
		switch declaration.Scale {
		case schemaOpenValue, "unit-interval", "bounded", "unbounded":
		default:
			return fmt.Errorf("%s: field %q scale %q must be open, unit-interval, bounded, or unbounded", label, path, declaration.Scale)
		}
		switch declaration.Direction {
		case schemaOpenValue, "higher-is-better", "lower-is-better", "target":
		default:
			return fmt.Errorf("%s: field %q direction %q must be open, higher-is-better, lower-is-better, or target", label, path, declaration.Direction)
		}
	}
	if declaration.Kind == KindBlob {
		if strings.TrimSpace(declaration.PathPrefix) == "" {
			return fmt.Errorf("%s: field %q blob path_prefix is required", label, path)
		}
		if _, err := closedMediaTypes(label, path, declaration.MediaTypes); err != nil {
			return err
		}
	}
	if declaration.Kind == KindRecordRef {
		ref, err := snapshot.ParseTypeRef(declaration.RecordType)
		if err != nil {
			return fmt.Errorf("%s: field %q record_type: %w", label, path, err)
		}
		if strings.Contains(declaration.RecordType, ",") || ref.String() != declaration.RecordType {
			return fmt.Errorf("%s: field %q record_type must be a singular canonical TypeRef, never a list", label, path)
		}
		switch declaration.Resolution {
		case "resolved", "pinned":
		default:
			return fmt.Errorf("%s: field %q resolution %q must be resolved or pinned", label, path, declaration.Resolution)
		}
	}
	return nil
}

func declaredAttributes(declaration FieldDeclaration) []string {
	var names []string
	for _, candidate := range []struct {
		name string
		set  bool
	}{
		{"absent_image", declaration.AbsentImage != ""},
		{"values", declaration.Values != nil},
		{"minimum", declaration.Minimum != nil},
		{"maximum", declaration.Maximum != nil},
		{"id_field", declaration.IDField != ""},
		{"element", declaration.Element != ""},
		{"unique", declaration.Unique},
		{"sorted", declaration.Sorted},
		{"scale", declaration.Scale != ""},
		{"direction", declaration.Direction != ""},
		{"path_prefix", declaration.PathPrefix != ""},
		{"media_types", declaration.MediaTypes != nil},
		{"record_type", declaration.RecordType != ""},
		{"resolution", declaration.Resolution != ""},
	} {
		if candidate.set {
			names = append(names, candidate.name)
		}
	}
	return names
}

func integerAttribute(label, path, name string, raw json.RawMessage) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	text := strings.TrimSpace(string(raw))
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s: field %q %s must be an integer literal, got %s", label, path, name, text)
	}
	return &value, nil
}

func durationAttribute(label, path, name string, raw json.RawMessage) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("%s: field %q %s must be a duration string: %w", label, path, name, err)
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return nil, fmt.Errorf("%s: field %q %s must be a Go duration string, got %q", label, path, name, text)
	}
	nanoseconds := int64(duration)
	return &nanoseconds, nil
}

// closedMediaTypes reads a blob's media_types: either the explicit "open", or a
// closed array. Nil means open, which is why the attribute is required rather
// than defaulted.
func closedMediaTypes(label, path string, raw json.RawMessage) ([]string, error) {
	if raw == nil {
		return nil, fmt.Errorf("%s: field %q blob media_types is required", label, path)
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single != schemaOpenValue {
			return nil, fmt.Errorf("%s: field %q media_types %q must be %q or a closed array", label, path, single, schemaOpenValue)
		}
		return nil, nil
	}
	var closed []string
	if err := json.Unmarshal(raw, &closed); err != nil {
		return nil, fmt.Errorf("%s: field %q media_types must be %q or a closed array of media types", label, path, schemaOpenValue)
	}
	if len(closed) == 0 {
		return nil, fmt.Errorf("%s: field %q declares an empty closed media_types array", label, path)
	}
	for _, mediaType := range closed {
		if strings.TrimSpace(mediaType) == "" {
			return nil, fmt.Errorf("%s: field %q declares a blank media type", label, path)
		}
	}
	return closed, nil
}

func validateSubjectShape(label string, raw json.RawMessage, shape SubjectShape) error {
	shapeLabel := label + ": subject_shape"
	rawShape, err := requireExactObjectKeys(shapeLabel, raw, subjectShapeKeys)
	if err != nil {
		return err
	}
	if shape.Minimum < 0 {
		return fmt.Errorf("%s: minimum %d is negative", shapeLabel, shape.Minimum)
	}
	if shape.Maximum != nil && *shape.Maximum < shape.Minimum {
		return fmt.Errorf("%s: maximum %d is below minimum %d", shapeLabel, *shape.Maximum, shape.Minimum)
	}
	if len(shape.Roles) == 0 {
		return fmt.Errorf("%s: roles declares nothing; a role absent from the map is forbidden, so every allowed role must be listed", shapeLabel)
	}
	var rawRoles map[string]json.RawMessage
	if err := json.Unmarshal(rawShape["roles"], &rawRoles); err != nil {
		return fmt.Errorf("%s: roles: %w", shapeLabel, err)
	}
	roleNames := make([]string, 0, len(rawRoles))
	for role := range rawRoles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	for _, role := range roleNames {
		if _, err := requireExactObjectKeys(fmt.Sprintf("%s: role %q", shapeLabel, role), rawRoles[role], roleBoundsKeys); err != nil {
			return err
		}
		if !isSubjectRole(SubjectRole(role)) {
			return fmt.Errorf(
				"%s: role %q is not one of primary, base, evidence, context, candidate, reference",
				shapeLabel, role,
			)
		}
		bounds := shape.Roles[SubjectRole(role)]
		if bounds.Minimum < 0 {
			return fmt.Errorf("%s: role %q minimum %d is negative", shapeLabel, role, bounds.Minimum)
		}
		if bounds.Maximum != nil && *bounds.Maximum < bounds.Minimum {
			return fmt.Errorf("%s: role %q maximum %d is below minimum %d", shapeLabel, role, *bounds.Maximum, bounds.Minimum)
		}
	}
	if shape.SubjectType != nil {
		if err := shape.SubjectType.Validate(); err != nil {
			return fmt.Errorf("%s: subject_type: %w", shapeLabel, err)
		}
	}
	switch shape.Ports {
	case PortsAnyExposedInput, PortsCandidatePortsExactly:
	default:
		return fmt.Errorf("%s: ports %q must be %q or %q", shapeLabel, shape.Ports, PortsAnyExposedInput, PortsCandidatePortsExactly)
	}
	return nil
}

func isSubjectRole(role SubjectRole) bool {
	switch role {
	case SubjectRolePrimary, SubjectRoleBase, SubjectRoleEvidence,
		SubjectRoleContext, SubjectRoleCandidate, SubjectRoleReference:
		return true
	default:
		return false
	}
}

func isDeclarableKind(kind SchemaKind) bool {
	switch kind {
	case KindString, KindMarkdown, KindIdentifier, KindInt, KindFloat, KindBool,
		KindTimestamp, KindDuration, KindEnum, KindScore, KindBlob,
		KindEntitySet, KindArray, KindObject, KindAnchor, KindRecordRef:
		return true
	default:
		return false
	}
}

func isScalarKind(kind SchemaKind) bool {
	switch kind {
	case KindString, KindMarkdown, KindIdentifier, KindInt, KindFloat, KindBool,
		KindTimestamp, KindDuration, KindEnum:
		return true
	default:
		return false
	}
}

// isLeafKind separates claims from structure. A container carries no value of its
// own, which is exactly why the epistemic table declares leaves only while the
// schema must declare containers: their shape IS the schema.
//
// An array of a scalar kind is itself a leaf, because the grammar cannot address
// an element of one: scalars have no id and positions are not addresses.
func isLeafKind(declaration FieldDeclaration) bool {
	switch declaration.Kind {
	case KindString, KindMarkdown, KindIdentifier, KindInt, KindFloat, KindBool,
		KindTimestamp, KindDuration, KindEnum, KindRecordRef:
		return true
	case KindArray:
		return isScalarKind(declaration.Element)
	default:
		return false
	}
}

func sortedFieldPaths(fields map[string]FieldDeclaration) []string {
	paths := make([]string, 0, len(fields))
	for path := range fields {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func requireExactObjectKeys(label string, data []byte, required []string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	allowed := make(map[string]struct{}, len(required))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, found := raw[key]; !found {
			return nil, fmt.Errorf(
				"%s: key %q is required; there is no optional key and no default here, because \"absent means X\" is exactly the kind of rule that hides a change inside a frozen digest",
				label, key,
			)
		}
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; !ok {
			return nil, fmt.Errorf("%s: unknown key %q", label, key)
		}
	}
	return raw, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("contains trailing JSON")
		}
		return fmt.Errorf("decode trailing data: %w", err)
	}
	return nil
}

// rejectEpistemicKeys is the belt to DisallowUnknownFields' braces. A schema
// document carries no epistemic field of any kind: status is keyed by (type,
// revision) outside every canonical digest, precisely so a mislabelled status is a
// one-line edit rather than a descriptor bump. Only KEYS are checked — a future
// enum value legitimately named "platform" is data, not a status claim.
func rejectEpistemicKeys(label string, source []byte) error {
	value, err := canonicalJSONValueTree(source)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return walkCanonicalKeys(value, func(key string) error {
		if strings.Contains(strings.ToLower(key), "epistemic") {
			return fmt.Errorf(
				"%s: contains the key %q; epistemic status is declared by (type, revision) outside every canonical digest and never inside a schema document",
				label, key,
			)
		}
		return nil
	})
}

func walkCanonicalKeys(value any, check func(string) error) error {
	switch typed := value.(type) {
	case canonicalObject:
		for _, member := range typed {
			if err := check(member.key); err != nil {
				return err
			}
			if err := walkCanonicalKeys(member.value, check); err != nil {
				return err
			}
		}
	case canonicalArray:
		for _, element := range typed {
			if err := walkCanonicalKeys(element, check); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveDeclarations expands every composite kind into the leaves it implies,
// with their presence DERIVED from the declaration. This is where `forbidden`
// comes from, and the only place it comes from.
func (document SchemaDocument) resolveDeclarations(label string) (map[string]FieldDeclaration, error) {
	resolved := make(map[string]FieldDeclaration, len(document.Fields)*2)
	for path, declaration := range document.Fields {
		resolved[path] = declaration
	}
	add := func(path string, declaration FieldDeclaration) error {
		if _, found := resolved[path]; found {
			return fmt.Errorf(
				"%s: %q is implied by a composite kind and also declared explicitly; a composite kind's subtree is fixed by the dialect",
				label, path,
			)
		}
		resolved[path] = declaration
		return nil
	}
	for _, path := range sortedFieldPaths(document.Fields) {
		declaration := document.Fields[path]
		var implied map[string]FieldDeclaration
		switch declaration.Kind {
		case KindScore:
			implied = scoreLeaves(declaration)
		case KindBlob:
			implied = blobLeaves(declaration)
		case KindAnchor:
			implied = anchorLeaves()
		case KindArray:
			if declaration.Element == KindAnchor {
				implied = anchorLeaves()
			}
		}
		if implied == nil {
			continue
		}
		prefix := path
		if declaration.Kind == KindArray {
			prefix = path + fieldPathSeparator + fieldPathWildcard
		}
		for suffix, leaf := range implied {
			if err := add(prefix+fieldPathSeparator+suffix, leaf); err != nil {
				return nil, err
			}
		}
	}
	return resolved, nil
}

// scoreLeaves is §6.10's derivation table. The same Go Score type gets two
// different declarations across the six documents — pinned in diagnosis/v1, open
// in selection/v1 — which is the clearest demonstration that a declaration
// describes a SITE and not a Go type.
func scoreLeaves(declaration FieldDeclaration) map[string]FieldDeclaration {
	scales := []string{"unit-interval", "bounded", "unbounded"}
	if declaration.Scale != schemaOpenValue {
		scales = []string{declaration.Scale}
	}
	directions := []string{"higher-is-better", "lower-is-better", "target"}
	if declaration.Direction != schemaOpenValue {
		directions = []string{declaration.Direction}
	}
	bounds := PresenceOptional
	switch declaration.Scale {
	case "unit-interval", "unbounded":
		bounds = PresenceForbidden
	case "bounded":
		bounds = PresenceRequired
	}
	target := PresenceOptional
	switch declaration.Direction {
	case "higher-is-better", "lower-is-better":
		target = PresenceForbidden
	case "target":
		target = PresenceRequired
	}
	return map[string]FieldDeclaration{
		// value is optional always: the absent image 0 is a legal value under some
		// scale, so no rule can distinguish an omitted score from a zero one.
		"value":     {Kind: KindFloat, Presence: PresenceOptional, AbsentImage: AbsentImageZero},
		"scale":     {Kind: KindEnum, Presence: PresenceRequired, Values: enumValues(scales)},
		"direction": {Kind: KindEnum, Presence: PresenceRequired, Values: enumValues(directions)},
		"minimum":   {Kind: KindFloat, Presence: bounds, AbsentImage: AbsentImageNull},
		"maximum":   {Kind: KindFloat, Presence: bounds, AbsentImage: AbsentImageNull},
		"target":    {Kind: KindFloat, Presence: target, AbsentImage: AbsentImageNull},
	}
}

func blobLeaves(declaration FieldDeclaration) map[string]FieldDeclaration {
	mediaType := FieldDeclaration{Kind: KindString, Presence: PresenceRequired}
	if closed, err := closedMediaTypes("", "", declaration.MediaTypes); err == nil && len(closed) > 0 {
		mediaType = FieldDeclaration{Kind: KindEnum, Presence: PresenceRequired, Values: enumValues(closed)}
	}
	return map[string]FieldDeclaration{
		"path":       {Kind: KindString, Presence: PresenceRequired, PathPrefix: declaration.PathPrefix},
		"digest":     {Kind: KindIdentifier, Presence: PresenceRequired},
		"media_type": mediaType,
	}
}

// anchorLeaves is fixed by the dialect and takes no attributes, because "resolves
// through a declared subject" is not a per-site choice. Every locator detail is
// optional because locator/kind decides which of them must appear and forbids the
// rest, and that selection is a Go rule.
func anchorLeaves() map[string]FieldDeclaration {
	return map[string]FieldDeclaration{
		"subject":         {Kind: KindIdentifier, Presence: PresenceRequired, resolvesSubject: true},
		"locator":         {Kind: KindObject, Presence: PresenceRequired},
		"locator/kind":    {Kind: KindEnum, Presence: PresenceRequired, Values: enumValues([]string{"file-lines", "log-lines", "json-pointer", "byte-range", "opaque"})},
		"locator/path":    {Kind: KindString, Presence: PresenceOptional},
		"locator/start":   {Kind: KindInt, Presence: PresenceOptional, AbsentImage: AbsentImageNull},
		"locator/end":     {Kind: KindInt, Presence: PresenceOptional, AbsentImage: AbsentImageNull},
		"locator/pointer": {Kind: KindString, Presence: PresenceOptional},
		"locator/value":   {Kind: KindString, Presence: PresenceOptional},
	}
}

func enumValues(values []string) []EnumValue {
	declared := make([]EnumValue, 0, len(values))
	for _, value := range values {
		declared = append(declared, EnumValue{Value: value})
	}
	return declared
}

// validateSchemaDocumentRegistration is the half of loading that needs the rest
// of the package: the digest history, the Go record types, and the epistemic
// table.
func validateSchemaDocumentRegistration(document SchemaDocument) error {
	label := fmt.Sprintf("schema document %s", document.fileName)
	if !IsRecordType(document.Contract) {
		return fmt.Errorf("%s: contract %q is not a record type", label, document.Contract)
	}
	current, found := CurrentSchemaRevisionFor(document.Contract)
	if !found {
		return fmt.Errorf("%s: %q has no schema revision history", label, document.Contract)
	}
	// A document is ADOPTED when its revision is the type's current revision, and
	// STAGED when it is exactly one past it. Both are legal because a document's
	// bytes are reviewed and pinned BEFORE the history adopts them, so a document
	// is one ahead of the newest descriptor for as long as that review lasts — that
	// is how the six revision-2 documents landed. Anything else is a lost or
	// skipped revision.
	if document.Revision != current && document.Revision != current+1 {
		return fmt.Errorf(
			"%s: revision %d is neither the current revision %d of %q nor the next one; revisions are append-only and contiguous",
			label, document.Revision, current, document.Contract,
		)
	}
	if err := document.validateAgainstGoType(label); err != nil {
		return err
	}
	return document.validateAgainstEpistemicTable(label)
}

// validateAgainstGoType is completeness in BOTH directions. A declared field the
// Go type does not have is a lie; a Go field with no declaration is drift the
// parity gate cannot see, which is worse, because nothing then compares the two
// descriptions at all.
func (document SchemaDocument) validateAgainstGoType(label string) error {
	prototype, found := recordEpistemicPrototypes[document.Contract]
	if !found {
		return fmt.Errorf("%s: %q has no record prototype, so its fields cannot be enumerated", label, document.Contract)
	}
	leaves, err := recordLeafFields(reflect.TypeOf(prototype))
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	goLeaves := make(map[string]recordLeaf)
	for _, leaf := range leaves {
		if isBodyFieldPath(leaf.path) {
			goLeaves[leaf.path] = leaf
		}
	}
	declared := make(map[string]FieldDeclaration)
	for _, path := range document.impliedLeafPaths() {
		declared[path] = document.resolved[path]
	}
	for _, path := range sortedPaths(goLeaves) {
		if _, found := declared[path]; !found {
			return fmt.Errorf("%s: Go field %q has no schema declaration", label, path)
		}
	}
	for _, path := range sortedFieldPaths(declared) {
		leaf, found := goLeaves[path]
		if !found {
			return fmt.Errorf("%s: declares field %q, which the Go record type does not have", label, path)
		}
		if err := checkGoLeafAgainstDeclaration(label, path, leaf, declared[path]); err != nil {
			return err
		}
	}
	return nil
}

func checkGoLeafAgainstDeclaration(label, path string, leaf recordLeaf, declaration FieldDeclaration) error {
	want := goKindsFor(declaration)
	matched := false
	for _, kind := range want {
		if leaf.kind == kind {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("%s: field %q is declared %q but the Go field is a %s", label, path, declaration.Kind, leaf.kind)
	}
	if declaration.Kind == KindArray {
		elementKinds := goKindsFor(FieldDeclaration{Kind: declaration.Element})
		elementMatched := false
		for _, kind := range elementKinds {
			if leaf.element == kind {
				elementMatched = true
				break
			}
		}
		if !elementMatched {
			return fmt.Errorf("%s: field %q declares %q elements but the Go elements are %s", label, path, declaration.Element, leaf.element)
		}
	}
	switch declaration.Kind {
	case KindInt, KindFloat, KindBool:
		want := AbsentImageZero
		if leaf.pointer {
			want = AbsentImageNull
		}
		if declaration.AbsentImage != want {
			return fmt.Errorf(
				"%s: field %q declares absent_image %q, but the Go field is %s, so its absent image is %q",
				label, path, declaration.AbsentImage, goFieldRepresentation(leaf.pointer), want,
			)
		}
	}
	return nil
}

func goFieldRepresentation(pointer bool) string {
	if pointer {
		return "a pointer"
	}
	return "a value"
}

func goKindsFor(declaration FieldDeclaration) []reflect.Kind {
	switch declaration.Kind {
	case KindString, KindMarkdown, KindIdentifier, KindTimestamp, KindDuration, KindEnum:
		return []reflect.Kind{reflect.String}
	case KindInt:
		return []reflect.Kind{reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64}
	case KindFloat:
		return []reflect.Kind{reflect.Float32, reflect.Float64}
	case KindBool:
		return []reflect.Kind{reflect.Bool}
	case KindArray, KindEntitySet:
		return []reflect.Kind{reflect.Slice, reflect.Array}
	case KindObject, KindScore, KindBlob, KindAnchor:
		return []reflect.Kind{reflect.Struct}
	default:
		return nil
	}
}

// validateAgainstEpistemicTable is §7: for each (type, revision) the schema's
// implied leaf set equals the epistemic table's key set exactly. The two tables
// answer different questions about the same leaves — what shape a value has,
// versus on whose authority it stands — and this is what stops one from silently
// growing a field the other does not know about.
//
// The revision resolves to the document's own revision when the table has one,
// and otherwise to the revision it supersedes. Every adopted revision has its own
// entry — TestEveryRecordTypeDeclaresEpistemicStatusForEveryRevision requires it —
// so the fallback is reached only by a STAGED document, which is describing the
// validators its predecessor's entry describes until it is adopted.
func (document SchemaDocument) validateAgainstEpistemicTable(label string) error {
	fields, revision, found := epistemicFieldsForRevision(document.Contract, document.Revision, document.Supersedes)
	if !found {
		return fmt.Errorf("%s: %q has no epistemic declaration for revision %d or %d",
			label, document.Contract, document.Revision, document.Supersedes)
	}
	leaves := recordEnvelopeEpistemicFields()
	envelope := make(map[string]struct{}, len(leaves))
	for path := range leaves {
		envelope[path] = struct{}{}
	}
	declared := make(map[string]struct{}, len(envelope))
	for path := range envelope {
		declared[path] = struct{}{}
	}
	for _, path := range document.impliedLeafPaths() {
		declared[path] = struct{}{}
	}
	for _, path := range sortedSet(declared) {
		if _, found := fields[path]; !found {
			return fmt.Errorf("%s: schema leaf %q has no epistemic status at revision %d", label, path, revision)
		}
	}
	for _, path := range sortedStatuses(fields) {
		if _, found := declared[path]; !found {
			return fmt.Errorf("%s: epistemic field %q at revision %d is not a schema leaf", label, path, revision)
		}
	}
	return nil
}

func epistemicFieldsForRevision(ref snapshot.TypeRef, preferred, fallback int) (map[string]EpistemicStatus, int, bool) {
	for _, revision := range []int{preferred, fallback} {
		fields, found := epistemicFieldStatuses[epistemicKey{ref: ref, revision: revision}]
		if found {
			return fields, revision, true
		}
	}
	return nil, 0, false
}

func sortedPaths(leaves map[string]recordLeaf) []string {
	paths := make([]string, 0, len(leaves))
	for path := range leaves {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func sortedSet(set map[string]struct{}) []string {
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func sortedStatuses(fields map[string]EpistemicStatus) []string {
	paths := make([]string, 0, len(fields))
	for path := range fields {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
