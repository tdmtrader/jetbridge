package output

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The output plane's storage identity, derived and never chosen.
//
// Requirement 20 is a rule about *who decides*: the bucket, the object-key
// prefix and the opaque scope come only from authenticated deployment and
// tenant configuration plus the active activation epoch. A task, a domain
// consumer, a path parameter or a receipt cannot select or broaden any of
// them. This file is that rule as a pure function, which is the only shape in
// which it can be checked exhaustively -- the daemon's request handling is
// where a caller-supplied field would arrive, and there is exactly one place
// below where such a field is even representable, so that it can be refused
// rather than silently ignored.

const (
	// ScopeDomain separates the opaque scope derivation from every other hash
	// in this system. Without it, two derivations over the same tenant string
	// would agree by accident.
	ScopeDomain = "hangar-output-scope-v1"

	// StoreGCS is the only store that may advertise output capture. Req 19
	// admits the strict native-GCS profile and nothing else: filesystem and
	// S3-compatible stores cannot offer create-if-absent with an exact
	// generation, and a profile that cannot refuse an overwrite cannot make
	// the collision guarantee Req 23 states.
	StoreGCS = "gcs"

	// scopeHexBytes is the width of the derived scope's hash half. Twenty
	// bytes is 160 bits of second-preimage resistance on a value whose only
	// job is to be unguessable and stable, and it leaves the scope at 41
	// characters against hangar.Scope's 63-byte bound.
	scopeHexBytes = 20
)

// NamespaceConfig is the authenticated configuration an output namespace is
// derived from.
//
// CacheBucket and StrictInputBucket are named here rather than assumed absent:
// Req 20 forbids the output bucket from *being* either of them, and a
// deployment that has both cannot state that rule unless it can name them. An
// empty value means "this deployment has no such bucket", which is why they are
// compared only when set.
type NamespaceConfig struct {
	// Store is the store profile the output plane runs against.
	Store string

	// Bucket is the dedicated output bucket.
	Bucket string

	// DeploymentPrefix is the authenticated object-key prefix. It is the
	// foundation's deployment prefix vocabulary, validated by the foundation's
	// own rule so that one deployment's keys are one shape everywhere.
	DeploymentPrefix string

	// TenantID is the authenticated deployment/tenant identity the opaque
	// scope is derived from. It is never rendered into a key.
	TenantID string

	// CacheBucket and StrictInputBucket are the two buckets this one may not
	// be.
	CacheBucket       string
	StrictInputBucket string

	// SharedBucketPrefixOnlyIsolation is a deployment declaring that its
	// trust domains are separated by key prefix inside one bucket. Req 20
	// refuses that as an activation-compatible substitute for separate
	// buckets, and it is a field rather than an inference because a
	// deployment that believes it has isolation must be told it does not.
	SharedBucketPrefixOnlyIsolation bool

	// ActivationEpoch is the active epoch. It participates in the scope
	// derivation, so a rotation publishes into a new scope; the deployment
	// prefix deliberately does not carry it, so one bucket-wide list under the
	// prefix still finds every epoch's objects.
	ActivationEpoch executioncontrol.ActivationEpoch
}

// OutputNamespace is the derived identity. Its fields are unexported and it is
// returned by value: there is no way to build one except by deriving it, and no
// way to widen one after the fact.
type OutputNamespace struct {
	bucket string
	prefix string
	scope  hangar.Scope
	epoch  executioncontrol.ActivationEpoch
}

// DeriveNamespace is the only constructor.
func DeriveNamespace(config NamespaceConfig) (OutputNamespace, error) {
	if config.Store != StoreGCS {
		return OutputNamespace{}, fmt.Errorf("%w: output capture requires the strict native-GCS "+
			"profile; %q cannot offer create-if-absent at an exact generation, so it cannot make "+
			"the collision guarantee", ErrUnsupportedProtocol, config.Store)
	}
	if strings.TrimSpace(config.Bucket) == "" {
		return OutputNamespace{}, fmt.Errorf("%w: no output bucket is configured", ErrIncomplete)
	}
	if config.SharedBucketPrefixOnlyIsolation {
		return OutputNamespace{}, fmt.Errorf("%w: this deployment separates trust domains by key "+
			"prefix inside one bucket. Prefix-only IAM is not an activation-compatible substitute "+
			"for a dedicated output bucket: object-level permission is not expressible in a "+
			"bucket policy, so every principal that can read one prefix can read them all",
			ErrUnauthorized)
	}
	if config.CacheBucket != "" && config.Bucket == config.CacheBucket {
		return OutputNamespace{}, fmt.Errorf("%w: the output bucket is the durable cache bucket "+
			"%q. The cache is fail-open and re-derivable by re-running a step; a durable result "+
			"is neither, and one bucket cannot have both lifetimes", ErrConflict, config.Bucket)
	}
	if config.StrictInputBucket != "" && config.Bucket == config.StrictInputBucket {
		return OutputNamespace{}, fmt.Errorf("%w: the output bucket is the caller-published "+
			"strict-input bucket %q. Strict inputs are published by callers; output objects are "+
			"published only by the output daemon, and sharing the bucket gives one principal "+
			"both roles", ErrConflict, config.Bucket)
	}
	if err := hangar.ValidateDeploymentPrefix(config.DeploymentPrefix); err != nil {
		return OutputNamespace{}, fmt.Errorf("%w: output key prefix: %v", ErrIncomplete, err)
	}
	if strings.TrimSpace(config.TenantID) == "" {
		return OutputNamespace{}, fmt.Errorf("%w: no authenticated tenant identity; the opaque "+
			"scope has nothing to be derived from, and a constant scope is a shared namespace",
			ErrIncomplete)
	}
	if config.ActivationEpoch == 0 {
		return OutputNamespace{}, fmt.Errorf("%w: no active activation epoch", ErrIncomplete)
	}

	return OutputNamespace{
		bucket: config.Bucket,
		prefix: config.DeploymentPrefix,
		scope:  deriveScope(config.TenantID, config.ActivationEpoch),
		epoch:  config.ActivationEpoch,
	}, nil
}

// deriveScope is the opaque half.
//
// The scope is a name for a namespace, not a description of one: nothing about
// the tenant is readable from it, which is what makes it safe to write into an
// object key that inventory lists and a receipt carries.
func deriveScope(tenant string, epoch executioncontrol.ActivationEpoch) hangar.Scope {
	digest := sha256.New()
	digest.Write([]byte(ScopeDomain))
	digest.Write([]byte{0})
	digest.Write([]byte(tenant))
	digest.Write([]byte{0})
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(epoch))
	digest.Write(encoded[:])

	// A leading letter, because hangar.Scope requires a lowercase alphanumeric
	// first character and a hex string can start with a digit -- which would
	// be valid but would make the one-character prefix look like part of the
	// hash to anyone reading a key.
	return hangar.Scope("o" + hex.EncodeToString(digest.Sum(nil)[:scopeHexBytes]))
}

// Bucket is the dedicated output bucket.
// BucketFingerprint is how this namespace's bucket is named to other
// components, and it takes nothing: the bucket it fingerprints is the
// server-derived one this namespace was built from, so there is no argument a
// caller could supply. That shape is the rule rather than a preference --
// checkNoAPIAcceptsAStorageLocation rejects an exported function in this package
// that takes a bucket, and it found this one when it was first written that way.
func (namespace OutputNamespace) BucketFingerprint() string {
	if namespace.bucket == "" || strings.HasPrefix(namespace.bucket, BucketFingerprintScheme) {
		return namespace.bucket
	}

	return BucketFingerprintScheme + namespace.bucket
}

func (namespace OutputNamespace) Bucket() string { return namespace.bucket }

// Prefix is the authenticated deployment prefix.
func (namespace OutputNamespace) Prefix() string { return namespace.prefix }

// Scope is the derived opaque scope.
func (namespace OutputNamespace) Scope() hangar.Scope { return namespace.scope }

// ActivationEpoch is the epoch this namespace was derived under.
func (namespace OutputNamespace) ActivationEpoch() executioncontrol.ActivationEpoch {
	return namespace.epoch
}

// IsZero reports an underived namespace, so a caller that forgot to derive one
// fails a check rather than publishing to "".
func (namespace OutputNamespace) IsZero() bool { return namespace.bucket == "" }

// ObjectKey is where the canonical bytes of one digest live.
//
// It is the foundation's own TreeKey over the derived scope and prefix, so an
// output object and a strict-input object have the same key shape in different
// buckets. Nothing about the key is caller-reachable: the prefix and scope come
// off this value and the digest is computed from the sealed bytes.
func (namespace OutputNamespace) ObjectKey(digest hangar.Digest) (string, error) {
	if namespace.IsZero() {
		return "", fmt.Errorf("%w: no output namespace has been derived", ErrIncomplete)
	}

	return hangar.TreeKey(namespace.prefix, namespace.scope, digest)
}

// ListPrefix is the bucket-wide prefix inventory sweeps under.
//
// It stops at the deployment prefix rather than descending into the scope,
// because a rotation derives a new scope and an inventory that swept only the
// current one would call every previous epoch's object an unmanaged stranger.
func (namespace OutputNamespace) ListPrefix() string {
	if namespace.prefix == "" {
		return "hangar/v1/scopes/"
	}

	return namespace.prefix + "/hangar/v1/scopes/"
}

// Ref is the tree ref for a digest and the generation the store
// assigned it.
func (namespace OutputNamespace) Ref(digest hangar.Digest, generation int64) hangar.TreeRef {
	return hangar.TreeRef{Scope: namespace.scope, Digest: digest, Generation: generation}
}

// CallerNamespaceRequest is the one place a caller-chosen bucket, scope or key
// is representable at all.
//
// It exists so that "a caller cannot choose the namespace" is a refusal with a
// message rather than a field that is quietly dropped. A hostile client really
// can put these in a request body; the daemon decodes them into this and calls
// Refuse, and the difference between ignoring a field and refusing it is
// whether an operator ever finds out.
type CallerNamespaceRequest struct {
	Bucket string `json:"bucket,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Key    string `json:"key,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// Validate reports the caller-chosen field, if there is one.
//
// It is spelled Validate because that is this package's one name for "this
// value is allowed to exist as it stands", and because the wire contract
// requires every frozen type to bound itself. The only valid value is the
// empty one: a request that names nothing is served from the derived
// namespace.
func (request CallerNamespaceRequest) Validate() error {
	for _, chosen := range []struct {
		field, value string
	}{
		{"bucket", request.Bucket},
		{"scope", request.Scope},
		{"key", request.Key},
		{"prefix", request.Prefix},
	} {
		if chosen.value == "" {
			continue
		}

		return fmt.Errorf("%w: the request names a %s (%q). The output bucket, prefix and opaque "+
			"scope are server-derived from authenticated deployment configuration and the active "+
			"epoch; a request that could name one could publish into another tenant's namespace "+
			"or read one", ErrUnauthorized, chosen.field, chosen.value)
	}

	return nil
}

// MarkerFor is the ownership evidence written at creation for one capture.
//
// It is here, beside the derivation, because the marker's scope has to be the
// derived one and nothing else: an object whose marker says a scope its key
// does not is exactly the corruption ParseObjectMarker exists to notice, and
// building both from the same value is how that stays impossible.
func (namespace OutputNamespace) MarkerFor(reservation ReservationID, digest hangar.Digest, createdAt Timestamp) ObjectMarker {
	return ObjectMarker{
		Version:         MarkerVersion,
		Scope:           namespace.scope,
		Digest:          digest,
		ReservationID:   reservation,
		ActivationEpoch: namespace.epoch,
		CreatedAt:       createdAt,
	}
}
