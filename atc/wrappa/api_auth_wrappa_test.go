package wrappa_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/wrappa"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
)

var _ = Describe("APIAuthWrappa", func() {
	var (
		checkPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
		checkBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
		checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
		checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
	)

	BeforeEach(func() {
		checkPipelineAccessHandlerFactory = auth.NewCheckPipelineAccessHandlerFactory(nil)
		checkBuildReadAccessHandlerFactory = auth.NewCheckBuildReadAccessHandlerFactory(nil)
		checkBuildWriteAccessHandlerFactory = auth.NewCheckBuildWriteAccessHandlerFactory(nil)
		checkWorkerTeamAccessHandlerFactory = auth.NewCheckWorkerTeamAccessHandlerFactory(nil)
	})

	Describe("Wrap", func() {
		It("handles each route", func() {
			inputHandlers := rata.Handlers{}

			for _, route := range atc.Routes {
				inputHandlers[route.Name] = &stupidHandler{}
			}
			Expect(func() {
				wrappa.NewAPIAuthWrappa(
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
				).Wrap(inputHandlers)
			}).NotTo(Panic())
		})
	})
})
