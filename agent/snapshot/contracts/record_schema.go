package contracts

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/concourse/concourse/agent/snapshot"
)

// recordSchemaDescriptors are frozen contract-identity documents. Their
// digests are carried by record values so changing a built-in validator cannot
// silently preserve the same advertised contract identity.
//
// These strings are deliberately compact and canonical. A semantic validator
// change must update the matching descriptor and, after external release,
// introduce a new TypeRef version.
var recordSchemaDescriptors = map[snapshot.TypeRef]string{
	"review/v1":            `{"contract":"review/v1","envelope":"record/v1","revision":1}`,
	"diagnosis/v1":         `{"contract":"diagnosis/v1","envelope":"record/v1","revision":1}`,
	"validation/v1":        `{"contract":"validation/v1","envelope":"record/v1","revision":1}`,
	"repository-change/v1": `{"contract":"repository-change/v1","envelope":"record/v1","revision":1}`,
	"selection/v1":         `{"contract":"selection/v1","envelope":"record/v1","revision":1}`,
	"measurements/v1":      `{"contract":"measurements/v1","envelope":"record/v1","revision":1}`,
}

func SchemaDigestFor(ref snapshot.TypeRef) (snapshot.Digest, bool) {
	descriptor, found := recordSchemaDescriptors[ref]
	if !found {
		return "", false
	}
	sum := sha256.Sum256([]byte(descriptor))
	return snapshot.Digest("sha256:" + hex.EncodeToString(sum[:])), true
}

func IsRecordType(ref snapshot.TypeRef) bool {
	_, found := recordSchemaDescriptors[ref]
	return found
}
