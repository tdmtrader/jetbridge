package idtoken_test

import (
	"crypto/rand"
	"math"
	"math/big"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/go-jose/go-jose/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	rsaJWK *jose.JSONWebKey
	ecJWK  *jose.JSONWebKey

	postgresRunner    postgresrunner.Runner
	dbConn            db.DbConn
	signingKeyFactory db.SigningKeyFactory
)

func TestIDToken(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "IDToken Suite")
}

var _ = postgresrunner.GinkgoRunner(&postgresRunner)

// Key generation is expensive, so it happens once for the process rather than
// once per spec. It cannot live in a BeforeSuite: postgresrunner.GinkgoRunner
// already installs one, and Ginkgo permits only a single suite setup node.
var generateKeysOnce sync.Once

var _ = BeforeEach(func() {
	generateKeysOnce.Do(func() {
		var err error
		rsaJWK, err = idtoken.GenerateNewKey(db.SigningKeyTypeRSA)
		Expect(err).ToNot(HaveOccurred())

		ecJWK, err = idtoken.GenerateNewKey(db.SigningKeyTypeEC)
		Expect(err).ToNot(HaveOccurred())
	})

	postgresRunner.CreateTestDBFromTemplate()
	dbConn = postgresRunner.OpenConn()
	signingKeyFactory = db.NewSigningKeyFactory(dbConn)
})

var _ = AfterEach(func() {
	Expect(dbConn.Close()).To(Succeed())
	postgresRunner.DropTestDB()
})

// saveSigningKey stores a real signing key and then backdates it.
//
// SigningKeyFactory.CreateKey(jwk) takes no timestamp -- signing_keys.created_at
// is DEFAULT now() (1746768931_add_signing_keys.up.sql:5) -- so age is applied
// with an UPDATE afterwards. That is deliberately not a reason to widen the
// production interface: a test wanting an old key is not a reason for callers to
// be able to choose creation times.
func saveSigningKey(jwk jose.JSONWebKey, age time.Duration) db.SigningKey {
	num, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	Expect(err).NotTo(HaveOccurred())
	jwk.KeyID = strconv.Itoa(int(num.Int64()))

	Expect(signingKeyFactory.CreateKey(jwk)).To(Succeed())

	if age != 0 {
		_, err := dbConn.Exec(
			`UPDATE signing_keys SET created_at = now() - $2::interval WHERE kid = $1`,
			jwk.KeyID, age.String(),
		)
		Expect(err).NotTo(HaveOccurred())
	}

	for _, key := range allSigningKeys() {
		if key.ID() == jwk.KeyID {
			return key
		}
	}
	Fail("signing key " + jwk.KeyID + " was not stored")
	return nil
}

func allSigningKeys() []db.SigningKey {
	keys, err := signingKeyFactory.GetAllKeys()
	Expect(err).NotTo(HaveOccurred())
	return keys
}

func signingKeyExists(id string) bool {
	for _, key := range allSigningKeys() {
		if key.ID() == id {
			return true
		}
	}
	return false
}
