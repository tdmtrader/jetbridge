package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var flyPath string
var homeDir string

var atcServer *ghttp.Server

const targetName = "testserver"
const teamName = "main"
const atcVersion = "0.1.0"
const workerVersion = "4.5.6"

var teams = []atc.Team{
	atc.Team{
		ID:   1,
		Name: "main",
	},
	atc.Team{
		ID:   2,
		Name: "other-team",
	},
}

var _ = SynchronizedBeforeSuite(func() []byte {
	binPath, err := gexec.Build("github.com/concourse/concourse/fly", "-buildvcs=false")
	Expect(err).NotTo(HaveOccurred())

	return []byte(binPath)
}, func(data []byte) {
	flyPath = string(data)

	SetDefaultEventuallyTimeout(10 * time.Second)
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	gexec.CleanupBuildArtifacts()
})

func infoHandler() http.HandlerFunc {
	return ghttp.CombineHandlers(
		ghttp.VerifyRequest("GET", "/api/v1/info"),
		ghttp.RespondWithJSONEncoded(200, atc.Info{Version: atcVersion, WorkerVersion: workerVersion}),
	)
}

func tokenHandler() http.HandlerFunc {
	return ghttp.CombineHandlers(
		ghttp.VerifyRequest("POST", "/sky/issuer/token"),
		ghttp.RespondWithJSONEncoded(
			200,
			oauthToken(),
		),
	)
}

func userInfoHandler() http.HandlerFunc {
	return ghttp.CombineHandlers(
		ghttp.VerifyRequest("GET", "/api/v1/user"),
		ghttp.RespondWithJSONEncoded(200, map[string]any{
			"user_name": "user",
			"teams": map[string][]string{
				teamName:          {"owner"},
				"some-team":       {"owner"},
				"some-other-team": {"owner"},
			},
		}),
	)
}

func validAccessToken(expiry time.Time) string {
	accessToken, err := token.Factory{}.GenerateAccessToken(db.Claims{
		Claims: jwt.Claims{Expiry: jwt.NewNumericDate(expiry)}},
	)
	if err != nil {
		panic(err)
	}
	return accessToken
}

func oauthToken() map[string]string {
	return map[string]string{
		"token_type":   "Bearer",
		"access_token": validAccessToken(time.Now()),
		"id_token":     "some-token",
	}
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func createFlyRc(targets rc.Targets) {
	flyrc := filepath.Join(homeDir, ".flyrc")

	flyrcBytes, err := json.Marshal(rc.RC{Targets: targets})
	if err != nil {
		panic(err)
	}

	err = os.WriteFile(flyrc, flyrcBytes, 0600)
	if err != nil {
		panic(err)
	}
}

var _ = BeforeEach(func() {
	atcServer = ghttp.NewServer()

	atcServer.AppendHandlers(
		infoHandler(),
		tokenHandler(),
		userInfoHandler(),
		infoHandler(),
	)

	var err error

	homeDir, err = os.MkdirTemp("", "fly-test")
	Expect(err).NotTo(HaveOccurred())

	os.Setenv("HOME", homeDir)
	loginCmd := exec.Command(flyPath, "-t", targetName, "login", "-u", "user", "-p", "pass", "-c", atcServer.URL(), "-n", teamName)

	session, err := gexec.Start(loginCmd, GinkgoWriter, GinkgoWriter)
	Expect(err).NotTo(HaveOccurred())

	<-session.Exited

	Expect(session.ExitCode()).To(Equal(0))
})

var _ = AfterEach(func() {
	atcServer.Close()
	os.RemoveAll(homeDir)
})

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration Suite")
}

func osFlag(short string, long string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("/%s, /%s", short, long)
	} else {
		return fmt.Sprintf("-%s, --%s", short, long)
	}
}

func userHomeDir() string {
	return os.Getenv("HOME")
}

func Change(fn func() int) *changeMatcher {
	return &changeMatcher{
		fn: fn,
	}
}

type changeMatcher struct {
	fn     func() int
	amount int

	before int
	after  int
}

func (cm *changeMatcher) By(amount int) *changeMatcher {
	cm.amount = amount

	return cm
}

func (cm *changeMatcher) Match(actual any) (success bool, err error) {
	cm.before = cm.fn()

	ac, ok := actual.(func())
	if !ok {
		return false, errors.New("expected a function")
	}

	ac()

	cm.after = cm.fn()

	return (cm.after - cm.before) == cm.amount, nil
}

func (cm *changeMatcher) FailureMessage(actual any) (message string) {
	return fmt.Sprintf("Expected value to change by %d but it changed from %d to %d", cm.amount, cm.before, cm.after)
}

func (cm *changeMatcher) NegatedFailureMessage(actual any) (message string) {
	return fmt.Sprintf("Expected value not to change by %d but it changed from %d to %d", cm.amount, cm.before, cm.after)
}

// Self-signed fixture for the --ca-cert specs, valid to 2076.
//
// The previous pair was minted in 2016 for ten years and expired on
// 2026-08-06, taking one spec red with an x509 error that reads like a
// code fault. Expiry buys nothing here -- the private key is committed
// two constants below -- so it is dated long enough not to become a
// dated landmine again.
const serverCert = `-----BEGIN CERTIFICATE-----
MIIDIzCCAgugAwIBAgIUQYQSfVEgIAa5rJ1HhX2mJWg3LZkwDQYJKoZIhvcNAQEL
BQAwEjEQMA4GA1UECgwHQWNtZSBDbzAgFw0yNjA4MTEwMzM0MzVaGA8yMDc2MDgx
MDAzMzQzNVowEjEQMA4GA1UECgwHQWNtZSBDbzCCASIwDQYJKoZIhvcNAQEBBQAD
ggEPADCCAQoCggEBAKiKrkcMsjVIzH0Wedo0V+roAlMuxTFOUAkJnKiuWsRvt+l8
w9nABYm0awL9/Q28QLqkFKEZDHUh/4ubjnsN66alqNdGy1OL45aHbC5dZfKGxT8U
gkxm/8QNNwLMIVZyDj1okMBcdN1HUYd7VFWWyvsYinYCzi0qy4aHyhH1xxWTwBQx
zla5iaRdqLsWRFFWVAI/yCHdHB/j1RmPuX8X/rD9To/rVwV+aRTcISPvj4IvRUjW
4nWMPbrT4z4hDQceJUWndmveEDMVRDK36Xg71J5qaZzudFv1C+rRIlFy4fQKHRdi
+z3lOct0C9EpGnxq0GhygGzmXYTG/vXhr6tJsDECAwEAAaNvMG0wHQYDVR0OBBYE
FOXQdlXIq0WIXdkuweIwVmv6gYVJMB8GA1UdIwQYMBaAFOXQdlXIq0WIXdkuweIw
Vmv6gYVJMBoGA1UdEQQTMBGHBH8AAAGCCWxvY2FsaG9zdDAPBgNVHRMBAf8EBTAD
AQH/MA0GCSqGSIb3DQEBCwUAA4IBAQBctmOKEDTJC7M1IXopXLoiYNPS4wmiH/ai
bHkIKi81Bfu7BirwFb/3JdAcc2mBh1v3NgWCOTcRZNORhpmHUBOszS45vT7nBFl0
smxXBvU3D+o6eST+OSFVwfkvPROesrzYtuzYm93ShVqVAOIA+bguXlO9upDMeUZr
YlCW1ocH9J42zEw0eP8XjWvX7A2Q361Jk0jrQ6ktv4z/tI2mLOpnXTOwux6vUffv
3JDG932ni6UXYpOX/I4gccYtZJ1/Pfr239xdS+Wcd9fohcAmWgYqtA2Qz0OP3a4/
i/8HYkFmVrR2CI/qUU0laEBJEOM/zuOFhjwVp2kqaEZOI/8+CjiX
-----END CERTIFICATE-----
`

const serverKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAqIquRwyyNUjMfRZ52jRX6ugCUy7FMU5QCQmcqK5axG+36XzD
2cAFibRrAv39DbxAuqQUoRkMdSH/i5uOew3rpqWo10bLU4vjlodsLl1l8obFPxSC
TGb/xA03AswhVnIOPWiQwFx03UdRh3tUVZbK+xiKdgLOLSrLhofKEfXHFZPAFDHO
VrmJpF2ouxZEUVZUAj/IId0cH+PVGY+5fxf+sP1Oj+tXBX5pFNwhI++Pgi9FSNbi
dYw9utPjPiENBx4lRad2a94QMxVEMrfpeDvUnmppnO50W/UL6tEiUXLh9AodF2L7
PeU5y3QL0SkafGrQaHKAbOZdhMb+9eGvq0mwMQIDAQABAoIBAAXVpTJa92leT4vu
BQJfiQKeDQSNrDBohl9FuKljPcuqlWqNEeeeHOL3PrQgEExTLcd4SimIiTSd3G+c
Dzrl0LhYaEepkPzfrR2HKyDQxWh3r2jfYCJed3C1R2f+opHQSXtpPQeXu8j0QNeI
lrMO0RCPuS1cLACGyHxsA3wLLtzpjdvxd20XDTlJZPG37uT5joEOBSWWG3KzUyg+
WCYfYA4kAGyPfKpxCIJinGTjtd307YUGYO4MV2qyFQuZNlNcHVPHrUIaXZi9bLH3
OzYDXQhugL2Z/Y9EAIHCrxwfnTtR8KdFTYhYdP60Etb8rleJ2t5LlNcDr/wTTSzj
OzOTZDECgYEA1U5h7WRfuy660gXJOAmrXlvBi249PV/ATzfREWyaWm+IWtFOp2hJ
RoRmoL0rwYDt62KKqa7fKoGjB7zzoL2OUut3qhTos2W3AeRmiUepuBwhhX+7n+5K
KWd6PofqKLq5XxUmmokNJL2EJ/85rdJWtuWxrr+QZFimxMtOCW5ozJkCgYEAykab
gjaXfkAU1byCGNfvn6Cfceu0SjGJr2X2PboGA4uOcjM9V0xzLEcLbljuM+DWcLnv
llrUSkF00JhrzW6p816AleRqGTqXrCGy7pdbdvFs84+eLSyjUnmAQmUclczjLhbG
9uBYFg1H0ErWl2B1Q2J8w0eNqKIX58YMkqdxZ1kCgYEAujXfD1pcqA+3T7l1W9I1
I/5+C7aFB5sbSwyzGr7wUJqlMoMeYs6LiV/0J8Z8+EQRbzdrTY43i+f35r1xAZX5
NTISGQx/yHy3MpOtX5KL+wmzydMkfA2N+G85LHWCWWQIh5TzSlzyeGxpfnE0bSX+
RVRntOHOr4skqw/AZENagaECgYEAhmVTfbj3/xJkxX5ykj8nH1CBoBeTupgfe0Kr
0WeAB2r6QjZ5Uz+gZpLtrWu5GQ8Sa+OepK/EzXGgQ9iCCAS3NtRbazxQomKj0+Kw
GIbIZscSNOH/ntRBz9KavYKg84cmisDngbCd1kkMpgCThBC62QLfEoDARoMsjvqv
7+EBIEECgYA0qWqxophUWqBlXGeHS+XhlAtyYYtbuQW/CGpS6qA3j+4n+bcbziPW
hb0txIKJXzaMDiL+jXt4+TVNg6zSmkg0G2v/SZrTF+8QylQ1PGISFbR6f7nhhGRL
pbjWcf9qXxtCJdZdBm/BNDiJB6c6DDz/OIJwqgpCTsxiQgEdXRT4ig==
-----END RSA PRIVATE KEY-----`
