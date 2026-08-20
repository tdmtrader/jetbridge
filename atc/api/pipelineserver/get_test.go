package pipelineserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline presenter matrix", func() {
	It("reports run creation capability only for members, owners, and admins regardless of hold state", func() {
		for _, tc := range []struct {
			role      string
			admin     bool
			hold      string
			canCreate bool
		}{
			{role: accessor.ViewerRole, canCreate: false},
			{role: accessor.OperatorRole, canCreate: false},
			{role: accessor.MemberRole, canCreate: true},
			{role: accessor.OwnerRole, canCreate: true},
			{role: accessor.OwnerRole, admin: true, canCreate: true},
			{role: accessor.MemberRole, hold: "paused", canCreate: true},
			{role: accessor.MemberRole, hold: "archived", canCreate: true},
		} {
			tc := tc
			By(fmt.Sprintf("presenting %s as %s", tc.hold, tc.role))
			team, err := teamFactory.CreateTeam(atc.Team{
				Name: fmt.Sprintf("team-%s-%s-%t", tc.role, tc.hold, tc.admin),
				Auth: atc.TeamAuth{tc.role: {"users": {"test:user"}}},
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

			access := accessor.NewAccessor(accessor.Verification{
				HasToken:     true,
				IsTokenValid: true,
				RawClaims: map[string]any{
					"federated_claims": map[string]any{"connector_id": "test", "user_id": "user"},
				},
			}, accessor.ViewerRole, "sub", []string{"system"}, []db.Team{team}, nil)
			request := httptest.NewRequest("GET", "http://example.com", nil)
			request = request.WithContext(context.WithValue(request.Context(), atc.ContextKey("accessor"), access))
			response := httptest.NewRecorder()
			pipelineserver.NewServer(lagertest.NewTestLogger("test"), teamFactory, nil, "").GetPipeline(pipeline).ServeHTTP(response, request)
			Expect(response.Code).To(Equal(200))

			var presented atc.Pipeline
			Expect(json.Unmarshal(response.Body.Bytes(), &presented)).To(Succeed())
			Expect(presented.CanCreateRun).NotTo(BeNil())
			Expect(*presented.CanCreateRun).To(Equal(tc.canCreate))
		}
	})
})
