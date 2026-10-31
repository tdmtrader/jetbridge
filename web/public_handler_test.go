package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/web"
)

var _ = Describe("PublicHandler", func() {
	var handler http.Handler

	BeforeEach(func() {
		var err error
		handler, err = web.NewHandler(lagertest.NewTestLogger("web"), "")
		Expect(err).ToNot(HaveOccurred())
	})

	get := func(target string, header http.Header) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request, err := http.NewRequest("GET", target, nil)
		Expect(err).ToNot(HaveOccurred())
		for k, v := range header {
			request.Header[k] = v
		}
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	It("caches a URL that carries the asset content hash for a year", func() {
		recorder := get("/public/elm-setup.js?id=deadbeef", nil)

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(recorder.Header().Get("Cache-Control")).To(Equal("max-age=31536000, public, immutable"))
	})

	// index.html loads elm-setup.js as /public/elm-setup.js?id=<hash>, but a
	// relative import resolves against that URL and drops the query, so the
	// modules it loads arrive with no content hash in the URL.
	for _, spec := range []string{"./index.mjs", "./graph.mjs", "./d3.v7.min.js", "./mdi-svg.min.js"} {
		It("makes the browser revalidate "+spec+", which elm-setup.js loads without a content hash", func() {
			base, err := url.Parse("/public/elm-setup.js?id=deadbeef")
			Expect(err).ToNot(HaveOccurred())
			ref, err := url.Parse(spec)
			Expect(err).ToNot(HaveOccurred())
			resolved := base.ResolveReference(ref)
			Expect(resolved.RawQuery).To(BeEmpty())

			gzip := http.Header{"Accept-Encoding": {"gzip, deflate, br"}}
			recorder := get(resolved.String(), gzip)
			Expect(recorder.Code).To(Equal(http.StatusOK))
			Expect(recorder.Header().Get("Cache-Control")).To(Equal("no-cache"))
			etag := recorder.Header().Get("ETag")
			Expect(etag).ToNot(BeEmpty())

			revalidated := get(resolved.String(), http.Header{
				"Accept-Encoding": gzip["Accept-Encoding"],
				"If-None-Match":   {etag},
			})
			Expect(revalidated.Code).To(Equal(http.StatusNotModified))
			Expect(revalidated.Body.Len()).To(BeZero())
		})
	}

	It("serves the new content when the cached copy's ETag no longer matches", func() {
		recorder := get("/public/index.mjs", http.Header{"If-None-Match": {`W/"00000000"`}})

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(recorder.Body.Len()).ToNot(BeZero())
	})

	It("still answers a missing file with 404", func() {
		Expect(get("/public/no-such-file.mjs", nil).Code).To(Equal(http.StatusNotFound))
	})
})
