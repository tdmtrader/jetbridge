package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type requestProfile struct {
	authorization     string
	token             string
	subject           string
	connector         string
	userID            string
	name              string
	preferredUsername string
	persist           *sync.Once
}

var (
	anonymousProfile requestProfile
	memberProfile    requestProfile
	adminProfile     requestProfile
	systemProfile    requestProfile

	adminProfileSetup *sync.Once
)

type profileTransport struct {
	mu            sync.RWMutex
	base          *http.Transport
	authorization string
}

func (transport *profileTransport) authorizationSnapshot() string {
	transport.mu.RLock()
	authorization := transport.authorization
	transport.mu.RUnlock()
	return authorization
}

func (transport *profileTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	authorization := transport.authorizationSnapshot()

	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}
	if authorization == "" {
		cloned.Header.Del("Authorization")
	} else {
		cloned.Header.Set("Authorization", authorization)
	}

	return transport.base.RoundTrip(cloned)
}

func (transport *profileTransport) authorizationHeader() http.Header {
	header := make(http.Header)
	if authorization := transport.authorizationSnapshot(); authorization != "" {
		header.Set("Authorization", authorization)
	}
	return header
}

func useProfile(profile requestProfile) {
	GinkgoHelper()
	Expect(apiProfileTransport).NotTo(BeNil())
	ensureProfilePersisted(profile)

	if sameRequestProfile(profile, adminProfile) {
		adminProfileSetup.Do(func() {
			grantProfile(apiDB.Main, adminProfile, accessor.OwnerRole)
			result, err := apiDB.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, apiDB.Main.ID())
			Expect(err).NotTo(HaveOccurred())
			rowsAffected, err := result.RowsAffected()
			Expect(err).NotTo(HaveOccurred())
			Expect(rowsAffected).To(Equal(int64(1)))

			teamFactory := db.NewTeamFactory(apiDB.Conn, apiDB.LockFactory)
			main, found, err := teamFactory.FindTeam(apiDB.Main.Name())
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			apiDB.Main = main
		})
	}

	apiProfileTransport.mu.Lock()
	apiProfileTransport.authorization = profile.authorization
	apiProfileTransport.mu.Unlock()
}

func dialWebsocket(url string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{}
	return dialer.Dial(url, apiProfileTransport.authorizationHeader())
}

func createRequestProfiles() {
	GinkgoHelper()

	adminProfileSetup = new(sync.Once)

	anonymousProfile = requestProfile{}
	memberProfile = newRequestProfile(
		"api-member-token",
		"api-member-subject",
		"api-member-user",
		"API Member",
		"api-member",
	)
	adminProfile = newRequestProfile(
		"api-admin-token",
		"api-admin-subject",
		"api-admin-user",
		"API Administrator",
		"api-admin",
	)
	systemProfile = newRequestProfile(
		"api-system-token",
		"api-system",
		"api-system-user",
		"API System",
		"api-system",
	)
}

func persistRequestProfile(token, subject, userID, name, preferredUsername string) requestProfile {
	GinkgoHelper()

	profile := newRequestProfile(token, subject, userID, name, preferredUsername)
	ensureProfilePersisted(profile)
	return profile
}

func newRequestProfile(token, subject, userID, name, preferredUsername string) requestProfile {
	return requestProfile{
		authorization:     "Bearer " + token,
		token:             token,
		subject:           subject,
		connector:         "test",
		userID:            userID,
		name:              name,
		preferredUsername: preferredUsername,
		persist:           new(sync.Once),
	}
}

func ensureProfilePersisted(profile requestProfile) {
	GinkgoHelper()

	if profile.authorization == "" || profile.persist == nil {
		// Profiles without a token descriptor are persisted explicitly by the
		// spec that constructs them. The standard and dedicated profiles all
		// carry descriptors and take the path below.
		return
	}

	profile.persist.Do(func() {
		rawClaims, err := json.Marshal(map[string]any{
			"sub":                profile.subject,
			"aud":                []string{"api-test"},
			"exp":                time.Now().Add(time.Hour).Unix(),
			"name":               profile.name,
			"preferred_username": profile.preferredUsername,
			"federated_claims": map[string]any{
				"connector_id": profile.connector,
				"user_id":      profile.userID,
			},
		})
		Expect(err).NotTo(HaveOccurred())

		var claims db.Claims
		Expect(json.Unmarshal(rawClaims, &claims)).To(Succeed())
		Expect(apiDB.AccessTokenFactory.CreateAccessToken(profile.token, claims)).To(Succeed())
	})
}

func sameRequestProfile(left, right requestProfile) bool {
	return left.authorization == right.authorization &&
		left.connector == right.connector &&
		left.userID == right.userID
}

func grantProfile(team db.Team, profile requestProfile, role string) {
	GinkgoHelper()
	Expect(sameRequestProfile(profile, anonymousProfile)).To(BeFalse(), "the anonymous profile cannot receive a team role")
	Expect(sameRequestProfile(profile, systemProfile)).To(BeFalse(), "the system profile cannot receive a team role")

	auth := make(atc.TeamAuth, len(team.Auth())+1)
	for existingRole, providerAuth := range team.Auth() {
		providerAuthCopy := make(map[string][]string, len(providerAuth))
		for kind, identities := range providerAuth {
			providerAuthCopy[kind] = append([]string(nil), identities...)
		}
		auth[existingRole] = providerAuthCopy
	}

	roleAuth, found := auth[role]
	if !found {
		roleAuth = make(map[string][]string)
		auth[role] = roleAuth
	}
	roleAuth["users"] = append(roleAuth["users"], profile.connector+":"+profile.userID)

	Expect(team.UpdateProviderAuth(auth)).To(Succeed())
}

var _ = Describe("Real API access profiles", func() {
	It("rejects an anonymous request to an authenticated route", func() {
		useProfile(anonymousProfile)

		response, err := client.Get(server.URL + "/api/v1/user")
		Expect(err).NotTo(HaveOccurred())
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())

		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(body).To(Equal([]byte("not authorized")))
	})

	It("forbids an authenticated member without the team role", func() {
		useProfile(memberProfile)

		response, err := client.Get(server.URL + "/api/v1/teams/main")
		Expect(err).NotTo(HaveOccurred())
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())

		Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		Expect(body).To(Equal([]byte("forbidden")))
	})

	It("returns persisted team data to a granted viewer", func() {
		grantProfile(apiDB.Main, memberProfile, accessor.ViewerRole)
		useProfile(memberProfile)

		response, err := client.Get(server.URL + "/api/v1/teams/main")
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		var team atc.Team
		Expect(json.NewDecoder(response.Body).Decode(&team)).To(Succeed())
		Expect(response.Body.Close()).To(Succeed())

		Expect(team.ID).To(Equal(apiDB.Main.ID()))
		Expect(team.Name).To(Equal(apiDB.Main.Name()))
		Expect(team.Auth).To(HaveKey(accessor.ViewerRole))
		Expect(team.Auth[accessor.ViewerRole]["users"]).To(ContainElement("test:" + memberProfile.userID))
	})

	It("accepts the admin and system profiles on privileged routes", func() {
		useProfile(adminProfile)
		adminResponse, err := client.Get(server.URL + "/api/v1/log-level")
		Expect(err).NotTo(HaveOccurred())
		adminBody, err := io.ReadAll(adminResponse.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(adminResponse.Body.Close()).To(Succeed())
		Expect(adminResponse.StatusCode).To(Equal(http.StatusOK))
		Expect(adminBody).To(Equal([]byte(atc.LogLevelDebug)))

		worker := atc.Worker{
			Name:     "profile-system-worker",
			Version:  "1.2.3",
			Platform: "linux",
		}
		payload, err := json.Marshal(worker)
		Expect(err).NotTo(HaveOccurred())
		request, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/api/v1/workers",
			bytes.NewReader(payload),
		)
		Expect(err).NotTo(HaveOccurred())

		useProfile(systemProfile)
		systemResponse, err := client.Do(request)
		Expect(err).NotTo(HaveOccurred())
		_, err = io.Copy(io.Discard, systemResponse.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(systemResponse.Body.Close()).To(Succeed())
		Expect(systemResponse.StatusCode).To(Equal(http.StatusOK))

		persisted, found, err := apiDB.Deps.workerFactory.GetWorker(worker.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(persisted.Name()).To(Equal(worker.Name))
		Expect(persisted.Version()).NotTo(BeNil())
		Expect(*persisted.Version()).To(Equal(worker.Version))
	})
})
