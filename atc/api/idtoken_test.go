package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/go-jose/go-jose/v4"

	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("IDToken API", func() {
	Describe("GET /.well-known/openid-configuration", func() {
		type openidConfig struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		var response *http.Response
		var info openidConfig

		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/.well-known/openid-configuration")
			Expect(err).NotTo(HaveOccurred())
			json.NewDecoder(response.Body).Decode(&info)
		})

		It("returns Content-Type 'application/json'", func() {
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
		})

		It("contains correct issuer", func() {
			Expect(info.Issuer).To(Equal(externalURL))
		})

		It("contains correct issuer", func() {
			Expect(info.JWKSURI).To(Equal(externalURL + "/.well-known/jwks.json"))
		})
	})

	Describe("GET /.well-known/jwks.json", func() {
		// Real signing keys in a real table, served by the real
		// SigningKeyFactory. Previously two FakeSigningKeys handed back by a
		// GetAllKeysStub, which meant the endpoint was never shown to read what
		// CreateKey actually stores -- including whether the stored JWK survives
		// the round trip as public.
		var (
			realdb   *realDB
			server   *httptest.Server
			keyIDs   []string
			response *http.Response
			jwks     jose.JSONWebKeySet
		)

		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()

			keyIDs = nil
			for _, keyType := range []db.SigningKeyType{db.SigningKeyTypeRSA, db.SigningKeyTypeEC} {
				jwk, err := idtoken.GenerateNewKey(keyType)
				Expect(err).NotTo(HaveOccurred())
				Expect(realdb.Deps.signingKeyFactory.CreateKey(*jwk)).To(Succeed())
				keyIDs = append(keyIDs, jwk.KeyID)
			}
		})

		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/.well-known/jwks.json")
			Expect(err).NotTo(HaveOccurred())
			Expect(json.NewDecoder(response.Body).Decode(&jwks)).To(Succeed())
		})

		It("returns Content-Type 'application/json'", func() {
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
		})

		It("contains correct keys", func() {
			Expect(jwks.Keys).To(HaveLen(2))

			var served []string
			for _, key := range jwks.Keys {
				served = append(served, key.KeyID)
			}
			// ConsistOf, not indexed: GetAllKeys orders by the table, and the
			// endpoint's contract is the set it publishes, not the order.
			Expect(served).To(ConsistOf(keyIDs))
		})

		It("does not contain private keys", func() {
			for _, key := range jwks.Keys {
				Expect(key.IsPublic()).To(BeTrue())
			}
		})

	})
})
