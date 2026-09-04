package pipelineserver_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline presenter matrix", func() {
	It("reports run creation capability using the configured route role", func() {
		// The `gate: true` rows are about the role, not about the operator
		// gate: they assert a role behaviour that only exists while creation
		// is admitted. The `gate: false` rows below them are this track's
		// check -- the gate narrows the field for every caller, whatever
		// their role.
		original := atc.EnablePipelineRunCreation
		DeferCleanup(func() { atc.EnablePipelineRunCreation = original })

		for i, tc := range []struct {
			name         string
			requiredRole string
			role         string
			admin        bool
			unauth       bool
			otherTeam    bool
			hold         string
			gate         bool
			canCreate    bool
		}{
			{name: "default member", role: accessor.MemberRole, gate: true, canCreate: true},
			{name: "custom operator", requiredRole: accessor.OperatorRole, role: accessor.OperatorRole, gate: true, canCreate: true},
			{name: "custom viewer", requiredRole: accessor.ViewerRole, role: accessor.ViewerRole, gate: true, canCreate: true},
			{name: "custom owner denies member", requiredRole: accessor.OwnerRole, role: accessor.MemberRole, gate: true, canCreate: false},
			{name: "admin", requiredRole: accessor.OwnerRole, role: accessor.OwnerRole, admin: true, gate: true, canCreate: true},
			{name: "unrelated team", role: accessor.MemberRole, otherTeam: true, gate: true, canCreate: false},
			{name: "unauthenticated public viewer", requiredRole: accessor.ViewerRole, role: accessor.ViewerRole, unauth: true, gate: true, canCreate: false},
			{name: "paused", role: accessor.MemberRole, hold: "paused", gate: true, canCreate: true},
			{name: "archived", role: accessor.MemberRole, hold: "archived", gate: true, canCreate: true},

			{name: "gate held, admin", requiredRole: accessor.OwnerRole, role: accessor.OwnerRole, admin: true, canCreate: false},
			{name: "gate held, team owner", requiredRole: accessor.OwnerRole, role: accessor.OwnerRole, canCreate: false},
			{name: "gate held, team member", role: accessor.MemberRole, canCreate: false},
		} {
			tc := tc
			By(tc.name)
			atc.EnablePipelineRunCreation = tc.gate
			auth := atc.TeamAuth{tc.role: {"users": {"test:user"}}}
			if tc.unauth {
				auth = atc.TeamAuth{accessor.ViewerRole: {}}
			}
			team, err := teamFactory.CreateTeam(atc.Team{
				Name: fmt.Sprintf("team-%d", i),
				Auth: auth,
			})
			Expect(err).NotTo(HaveOccurred())
			if tc.admin {
				_, err = dbConn.Exec("UPDATE teams SET admin = true WHERE id = $1", team.ID())
				Expect(err).NotTo(HaveOccurred())
				reloadedTeam, found, err := teamFactory.FindTeam(team.Name())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				team = reloadedTeam
			}
			pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
				Template: true,
				Jobs:     atc.JobConfigs{{Name: "entry"}},
			}, db.ConfigVersion(0), false)
			Expect(err).NotTo(HaveOccurred())
			if tc.hold == "paused" {
				Expect(pipeline.Pause("user")).To(Succeed())
			}
			if tc.hold == "archived" {
				Expect(pipeline.Archive()).To(Succeed())
			}

			accessTeams := []db.Team{team}
			if tc.otherTeam {
				otherTeam, err := teamFactory.CreateTeam(atc.Team{
					Name: fmt.Sprintf("other-%d", i),
					Auth: atc.TeamAuth{tc.role: {"users": {"test:user"}}},
				})
				Expect(err).NotTo(HaveOccurred())
				accessTeams = []db.Team{otherTeam}
			}

			access := accessor.NewAccessor(accessor.Verification{
				HasToken:     !tc.unauth,
				IsTokenValid: !tc.unauth,
				RawClaims: map[string]any{
					"federated_claims": map[string]any{"connector_id": "test", "user_id": "user"},
				},
			}, accessor.ViewerRole, "sub", []string{"system"}, accessTeams, nil)
			request := httptest.NewRequest("GET", "http://example.com", nil)
			response := httptest.NewRecorder()
			customRoles := map[string]string{}
			if tc.requiredRole != "" {
				customRoles[atc.CreatePipelineRun] = tc.requiredRole
			}
			accessFactory := &accessorfakes.FakeAccessFactory{}
			accessFactory.CreateReturns(access, nil)
			accessor.NewHandler(
				lagertest.NewTestLogger("test"),
				atc.GetPipeline,
				pipelineserver.NewServer(lagertest.NewTestLogger("test"), teamFactory, nil, "").GetPipeline(pipeline),
				accessFactory,
				auditor.NewAuditor(false, false, false, false, false, false, false, false, false, lagertest.NewTestLogger("audit")),
				customRoles,
			).ServeHTTP(response, request)
			Expect(response.Code).To(Equal(200))

			var presented atc.Pipeline
			Expect(json.Unmarshal(response.Body.Bytes(), &presented)).To(Succeed())
			Expect(presented.CanCreateRun).NotTo(BeNil())
			Expect(*presented.CanCreateRun).To(Equal(tc.canCreate))
		}
	})

	It("uses the configured run role in both pipeline collections", func() {
		// Both collections, in both gate states: a change that gates the
		// detail payload while the two list payloads keep answering true is
		// the failure this spec exists to catch.
		original := atc.EnablePipelineRunCreation
		DeferCleanup(func() { atc.EnablePipelineRunCreation = original })

		team, err := teamFactory.CreateTeam(atc.Team{
			Name: "operator-team",
			Auth: atc.TeamAuth{accessor.OperatorRole: {"users": {"test:user"}}},
		})
		Expect(err).NotTo(HaveOccurred())
		_, _, err = team.SavePipeline(atc.PipelineRef{Name: "template"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())

		access := accessor.NewAccessor(accessor.Verification{
			HasToken:     true,
			IsTokenValid: true,
			RawClaims: map[string]any{
				"federated_claims": map[string]any{"connector_id": "test", "user_id": "user"},
			},
		}, accessor.ViewerRole, "sub", []string{"system"}, []db.Team{team}, nil)
		server := pipelineserver.NewServer(lagertest.NewTestLogger("test"), teamFactory, db.NewPipelineFactory(dbConn, lockFactory), "")
		listRequest := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
		listRequest.Form = url.Values{":team_name": {team.Name()}}

		for _, gate := range []bool{true, false} {
			atc.EnablePipelineRunCreation = gate
			By(fmt.Sprintf("with run creation admitted: %t", gate))

			for _, tc := range []struct {
				action  string
				handler http.Handler
				request *http.Request
			}{
				{
					action:  atc.ListPipelines,
					handler: http.HandlerFunc(server.ListPipelines),
					request: listRequest,
				},
				{
					action:  atc.ListAllPipelines,
					handler: http.HandlerFunc(server.ListAllPipelines),
					request: httptest.NewRequest(http.MethodGet, "http://example.com", nil),
				},
			} {
				response := httptest.NewRecorder()
				accessFactory := &accessorfakes.FakeAccessFactory{}
				accessFactory.CreateReturns(access, nil)
				accessor.NewHandler(
					lagertest.NewTestLogger("test"),
					tc.action,
					tc.handler,
					accessFactory,
					auditor.NewAuditor(false, false, false, false, false, false, false, false, false, lagertest.NewTestLogger("audit")),
					map[string]string{atc.CreatePipelineRun: accessor.OperatorRole},
				).ServeHTTP(response, tc.request)
				Expect(response.Code).To(Equal(http.StatusOK))

				var presented []atc.Pipeline
				Expect(json.Unmarshal(response.Body.Bytes(), &presented)).To(Succeed())
				Expect(presented).To(HaveLen(1))
				Expect(presented[0].CanCreateRun).NotTo(BeNil())
				Expect(*presented[0].CanCreateRun).To(Equal(gate), tc.action)
			}
		}
	})
})
