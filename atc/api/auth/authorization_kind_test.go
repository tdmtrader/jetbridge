package auth_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Authorization kind", func() {
	It("classifies every routed action and rejects unknown actions", func() {
		Expect(atc.Routes).NotTo(BeEmpty())
		for _, route := range atc.Routes {
			kind, known := auth.AuthorizationKindForAction(route.Name)
			Expect(known).To(BeTrue(), route.Name)
			Expect(kind).NotTo(Equal(auth.AuthorizationUnknown), route.Name)
		}
		kind, known := auth.AuthorizationKindForAction("unclassified-operation")
		Expect(known).To(BeFalse())
		Expect(kind).To(Equal(auth.AuthorizationUnknown))
	})

	It("keeps public metadata, private job output and mutations distinct", func() {
		for action, expected := range map[string]auth.AuthorizationKind{
			atc.ListAllPipelines:   auth.AuthorizationDelegated,
			atc.GetPipeline:        auth.AuthorizationPipelineRead,
			atc.ListPipelineBuilds: auth.AuthorizationPipelineRead,
			atc.GetBuild:           auth.AuthorizationBuildRead,
			atc.BuildEvents:        auth.AuthorizationBuildOutput,
			atc.GetConfig:          auth.AuthorizationTeam,
			atc.SaveConfig:         auth.AuthorizationTeam,
			atc.PausePipeline:      auth.AuthorizationTeam,
			atc.UnpausePipeline:    auth.AuthorizationTeam,
			atc.CreateJobBuild:     auth.AuthorizationTeam,
			atc.AbortBuild:         auth.AuthorizationBuildWrite,
			atc.SetLogLevel:        auth.AuthorizationAdmin,
		} {
			kind, known := auth.AuthorizationKindForAction(action)
			Expect(known).To(BeTrue(), action)
			Expect(kind).To(Equal(expected), action)
		}
	})
})
