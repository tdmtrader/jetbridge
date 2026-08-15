package buildserver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/api/auth"
	. "github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ScopedHandlerFactory", func() {
	var (
		response *http.Response
		server   *httptest.Server
		handler  http.Handler
	)

	BeforeEach(func() {
		logger := lagertest.NewTestLogger("test")
		handlerFactory := NewScopedHandlerFactory(logger)
		handler = handlerFactory.HandlerFor(renderBuild)
	})

	JustBeforeEach(func() {
		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST", server.URL, nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		Expect(response.Body.Close()).To(Succeed())
		server.Close()
	})

	Context("build is in the context", func() {
		var contextBuild db.BuildForAPI

		BeforeEach(func() {
			// The factory only passes this through, so what matters is that the
			// response identifies the very build the context carried.
			contextBuild = buildForAPI(createBuild(createTeam("some-team")))
			handler = &buildContextHandler{next: handler, build: contextBuild}
		})

		It("renders the build from context", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("X-Concourse-Scoped-Build")).To(Equal(fmt.Sprint(contextBuild.ID())))
		})
	})

	Context("build not found in the context", func() {
		It("returns 500", func() {
			Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
		})
	})
})

func renderBuild(build db.BuildForAPI) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Concourse-Scoped-Build", fmt.Sprint(build.ID()))
	})
}

type buildContextHandler struct {
	next  http.Handler
	build db.BuildForAPI
}

func (h *buildContextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), auth.BuildContextKey, h.build)
	h.next.ServeHTTP(w, r.WithContext(ctx))
}
