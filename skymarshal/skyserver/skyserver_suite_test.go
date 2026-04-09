package skyserver_test

import (
	"crypto/rand"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/skymarshal/skyserver"
	"github.com/concourse/concourse/skymarshal/token/tokenfakes"

	"github.com/onsi/gomega/ghttp"
	"golang.org/x/oauth2"
)

var (
	fakeTokenMiddleware *tokenfakes.FakeMiddleware
	skyServer           *httptest.Server
	dexServer           *ghttp.Server
	config              *skyserver.SkyConfig
	stateSigningKey     []byte
)

func TestSkyServer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Sky Server Suite")
}

var _ = BeforeEach(func() {
	var err error

	fakeTokenMiddleware = new(tokenfakes.FakeMiddleware)

	dexServer = ghttp.NewTLSServer()

	stateSigningKey = make([]byte, 32)
	_, err = rand.Read(stateSigningKey)
	Expect(err).NotTo(HaveOccurred())

	endpoint := oauth2.Endpoint{
		AuthURL:   dexServer.URL() + "/auth",
		TokenURL:  dexServer.URL() + "/token",
		AuthStyle: oauth2.AuthStyleInHeader,
	}

	oauthConfig := &oauth2.Config{
		Endpoint:     endpoint,
		ClientID:     "dex-client-id",
		ClientSecret: "dex-client-secret",
		Scopes:       []string{"some-scope"},
	}

	config = &skyserver.SkyConfig{
		Logger:          lagertest.NewTestLogger("sky"),
		TokenMiddleware: fakeTokenMiddleware,
		OAuthConfig:     oauthConfig,
		HTTPClient:      dexServer.HTTPTestServer.Client(),
		StateSigningKey: stateSigningKey,
	}

	server, err := skyserver.NewSkyServer(config)
	Expect(err).NotTo(HaveOccurred())

	skyServer = httptest.NewUnstartedServer(skyserver.NewSkyHandler(server))
})

var _ = AfterEach(func() {
	skyServer.Close()
	dexServer.Close()
})
