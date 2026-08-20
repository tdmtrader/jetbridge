package accessor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/auditor"
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

// recordingAuditor keeps what the handler audited, then hands it to the real
// auditor. The real one writes a log line and returns nothing, so the request
// the handler audited is otherwise unobservable.
type recordingAuditor struct {
	auditor.Auditor

	audited []auditedEvent
}

type auditedEvent struct {
	action   string
	userName string
	request  *http.Request
}

func (a *recordingAuditor) Audit(action string, userName string, r *http.Request) {
	a.audited = append(a.audited, auditedEvent{action, userName, r})

	a.Auditor.Audit(action, userName, r)
}

var _ = Describe("Handler", func() {

	var (
		logger        lager.Logger
		fixture       *realTeamFixture
		accessFactory *recordingAccessFactory
		auditRecorder *recordingAuditor

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
		auditRecorder = &recordingAuditor{
			Auditor: auditor.NewAuditor(
				true, true, true, true, true, true, true, true, true,
				lagertest.NewTestLogger("audit"),
			),
		}

		servedRequests = nil

		action = atc.GetUser
		customRoles = map[string]string{atc.GetUser: "some-role"}

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
			auditRecorder,
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
				action = atc.ListActiveUsersSince
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
				Expect(auditRecorder.audited).To(HaveLen(1))
				Expect(auditRecorder.audited[0].action).To(Equal(atc.GetUser))
				Expect(auditRecorder.audited[0].userName).To(Equal("some-user"))
				Expect(auditRecorder.audited[0].request).To(BeIdenticalTo(r))
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
				Expect(auditRecorder.audited).To(HaveLen(1))
				Expect(auditRecorder.audited[0].action).To(Equal(atc.GetUser))
				Expect(auditRecorder.audited[0].userName).To(Equal(""))
				Expect(auditRecorder.audited[0].request).To(BeIdenticalTo(r))
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

	Describe("RequiredRole", func() {
		It("falls back to the default role when the request has no custom mapping", func() {
			Expect(accessor.RequiredRole(context.Background(), atc.CreatePipelineRun)).To(Equal(accessor.MemberRole))
		})
	})
})
