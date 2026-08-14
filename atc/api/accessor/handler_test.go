package accessor_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/auditor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Handler", func() {
	const (
		token  = "handler-token"
		userID = "handler-user-id"
	)

	var (
		accessFactory *accessor.Factory
		auditLogger   *lagertest.TestLogger
		teamName      string

		serve func(string, map[string]string, http.Handler) *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		fixture := useRealTeamFixture()
		team := persistRoleTeam(
			fixture.TeamFactory,
			"viewer-team",
			accessor.ViewerRole,
			userID,
		)
		teamName = team.Name()

		fixture.persistAccessToken(token, map[string]any{
			"sub":                "handler-sub",
			"aud":                []any{"some-aud"},
			"exp":                float64(time.Now().Add(time.Hour).Unix()),
			"name":               "Handler User",
			"preferred_username": "handler",
			"email":              "handler@example.com",
			"federated_claims": map[string]any{
				"connector_id": "test",
				"user_id":      userID,
			},
		})

		accessFactory = accessor.NewAccessFactory(
			accessor.NewVerifier(fixture.AccessTokenFactory, []string{"some-aud"}),
			fixture.TeamFactory,
			"sub",
			[]string{"some-system-sub"},
			nil,
		)

		auditLogger = lagertest.NewTestLogger("audit")
		aud := auditor.NewAuditor(
			true, true, true, true, true, true, true, true, true,
			auditLogger,
		)
		handlerLogger := lagertest.NewTestLogger("handler")

		serve = func(action string, customRoles map[string]string, next http.Handler) *httptest.ResponseRecorder {
			request := httptest.NewRequest(http.MethodGet, "http://localhost:8080", nil)
			request.Header.Set("Authorization", "bearer "+token)
			response := httptest.NewRecorder()

			accessor.NewHandler(
				handlerLogger,
				action,
				next,
				accessFactory,
				aud,
				customRoles,
			).ServeHTTP(response, request)

			return response
		}
	})

	It("resolves default and custom roles through real team authorization", func() {
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if accessor.GetAccessor(r).IsAuthorized(teamName) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		})

		defaultRole := serve(atc.SaveConfig, map[string]string{}, downstream)
		Expect(defaultRole.Code).To(Equal(http.StatusForbidden))

		customRole := serve(atc.SaveConfig, map[string]string{
			atc.SaveConfig: accessor.ViewerRole,
		}, downstream)
		Expect(customRole.Code).To(Equal(http.StatusNoContent))
	})

	It("passes exact persisted claims downstream and writes a real audit event", func() {
		response := serve(atc.GetUser, map[string]string{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(json.NewEncoder(w).Encode(accessor.GetAccessor(r).Claims())).To(Succeed())
		}))

		Expect(response.Code).To(Equal(http.StatusOK))

		var claims accessor.Claims
		Expect(json.NewDecoder(response.Body).Decode(&claims)).To(Succeed())
		Expect(claims).To(Equal(accessor.Claims{
			Sub:               "handler-sub",
			UserID:            userID,
			UserName:          "Handler User",
			PreferredUsername: "handler",
			Email:             "handler@example.com",
			Connector:         "test",
		}))

		logs := auditLogger.Logs()
		Expect(logs).To(HaveLen(1))
		Expect(logs[0].Message).To(Equal("audit.audit"))
		Expect(logs[0].Data).To(HaveKeyWithValue("action", atc.GetUser))
		Expect(logs[0].Data).To(HaveKeyWithValue("user", "Handler User"))
	})
})
