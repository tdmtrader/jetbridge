package accessor_test

import (
	"net/http"
	"net/http/httptest"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingAccessFactory keeps the role the handler resolved and the accessor
// the real factory built from it. The handler exposes neither: the role is
// consumed inside Create, and the accessor only ever reaches the wrapped
// handler through the request context.
type recordingAccessFactory struct {
	accessor.AccessFactory

	roles    []string
	accesses []accessor.Access
}

func (f *recordingAccessFactory) Create(req *http.Request, role string) (accessor.Access, error) {
	f.roles = append(f.roles, role)

	access, err := f.AccessFactory.Create(req, role)
	if err != nil {
		return nil, err
	}

	f.accesses = append(f.accesses, access)
	return access, nil
}

var _ = Describe("Handler", func() {

	var (
		logger        lager.Logger
		fixture       *realTeamFixture
		accessFactory *recordingAccessFactory
		fakeAuditor   *auditorfakes.FakeAuditor

		servedRequests []*http.Request

		action      string
		customRoles map[string]string

		r *http.Request
		w *httptest.ResponseRecorder
	)

	BeforeEach(func() {
		logger = lager.NewLogger("test")

		fixture = useRealTeamFixture()
		accessFactory = &recordingAccessFactory{
			AccessFactory: accessor.NewAccessFactory(
				accessor.NewVerifier(fixture.AccessTokenFactory, []string{"some-aud"}),
				fixture.TeamFactory,
				"sub",
				[]string{"some-system-sub"},
				nil,
			),
		}
		fakeAuditor = new(auditorfakes.FakeAuditor)

		servedRequests = nil

		action = "some-action"
		customRoles = map[string]string{"some-action": "some-role"}

		var err error
		r, err = http.NewRequest("GET", "localhost:8080", nil)
		Expect(err).NotTo(HaveOccurred())

		w = httptest.NewRecorder()
	})

	JustBeforeEach(func() {
		handler := accessor.NewHandler(
			logger,
			action,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				servedRequests = append(servedRequests, r)
			}),
			accessFactory,
			fakeAuditor,
			customRoles,
		)

		handler.ServeHTTP(w, r)
	})

	Describe("Accessor Handler", func() {
		Context("when there's a default role for the given action", func() {
			BeforeEach(func() {
				action = atc.SaveConfig
			})

			Context("when the role has not been customized", func() {
				BeforeEach(func() {
					customRoles = map[string]string{}
				})

				It("finds the role", func() {
					Expect(accessFactory.roles).To(Equal([]string{accessor.MemberRole}))
				})
			})

			Context("when the role has been customized", func() {
				BeforeEach(func() {
					customRoles = map[string]string{
						atc.SaveConfig: accessor.ViewerRole,
					}
				})

				It("finds the role", func() {
					Expect(accessFactory.roles).To(Equal([]string{accessor.ViewerRole}))
				})
			})
		})

		Context("when there's no default role for the given action", func() {
			BeforeEach(func() {
				action = "some-admin-role"
			})

			Context("when the role has not been customized", func() {
				BeforeEach(func() {
					customRoles = map[string]string{}
				})

				It("sends a blank role (admin roles don't have defaults)", func() {
					Expect(accessFactory.roles).To(Equal([]string{""}))
				})
			})
		})

		Context("when the request is authenticated", func() {
			BeforeEach(func() {
				fixture.persistAccessToken("some-token", map[string]any{
					"sub":  "some-sub",
					"aud":  []any{"some-aud"},
					"exp":  float64(time.Now().Add(time.Hour).Unix()),
					"name": "some-user",
					"federated_claims": map[string]any{
						"connector_id": "some-connector",
					},
				})
				r.Header.Set("Authorization", "bearer some-token")
			})

			It("audits the event", func() {
				Expect(fakeAuditor.AuditCallCount()).To(Equal(1))
				action, userName, req := fakeAuditor.AuditArgsForCall(0)
				Expect(action).To(Equal("some-action"))
				Expect(userName).To(Equal("some-user"))
				Expect(req).To(Equal(r))
			})

			It("invokes the handler", func() {
				Expect(servedRequests).To(HaveLen(1))
				Expect(accessor.GetAccessor(servedRequests[0])).To(BeIdenticalTo(accessFactory.accesses[0]))
			})

			It("hands the handler an accessor carrying the persisted claims", func() {
				Expect(servedRequests).To(HaveLen(1))
				access := accessor.GetAccessor(servedRequests[0])
				Expect(access.IsAuthenticated()).To(BeTrue())
				Expect(access.Claims()).To(Equal(accessor.Claims{
					Sub:       "some-sub",
					UserName:  "some-user",
					Connector: "some-connector",
				}))
			})
		})

		Context("when the request is not authenticated", func() {
			It("audits the anonymous request", func() {
				Expect(fakeAuditor.AuditCallCount()).To(Equal(1))
				action, userName, req := fakeAuditor.AuditArgsForCall(0)
				Expect(action).To(Equal("some-action"))
				Expect(userName).To(Equal(""))
				Expect(req).To(Equal(r))
			})

			It("invokes the handler", func() {
				Expect(servedRequests).To(HaveLen(1))
				Expect(accessor.GetAccessor(servedRequests[0])).To(BeIdenticalTo(accessFactory.accesses[0]))
			})
		})

		Context("when the accessor factory errors", func() {
			BeforeEach(func() {
				fixture.disconnect()
			})

			It("returns a server error", func() {
				Expect(w.Result().StatusCode).To(Equal(http.StatusInternalServerError))
			})

			It("never reaches the wrapped handler", func() {
				Expect(servedRequests).To(BeEmpty())
			})
		})
	})
})
