package web_test

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/web"
)

var _ = Describe("CacheNearlyForever", func() {
	It("caches a URL that carries a content hash for a year", func() {
		insideHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "The wrapped handler was called!")
		})

		wrappedHandler := web.CacheNearlyForever(insideHandler)
		recorder := httptest.NewRecorder()
		request, err := http.NewRequest("GET", "/main.css?id=deadbeef", nil)
		Expect(err).ToNot(HaveOccurred())

		wrappedHandler.ServeHTTP(recorder, request)

		Expect(recorder.Body.String()).To(Equal("The wrapped handler was called!"))
		Expect(recorder.Header().Get("Cache-Control")).To(Equal("max-age=31536000, public, immutable"))
		Expect(recorder.Header().Get("Content-Encoding")).ToNot(Equal("gzip"))
	})

	It("makes the browser revalidate a URL without a content hash", func() {
		insideHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "The wrapped handler was called!")
		})

		wrappedHandler := web.CacheNearlyForever(insideHandler)
		recorder := httptest.NewRecorder()
		request, err := http.NewRequest("GET", "/index.mjs", nil)
		Expect(err).ToNot(HaveOccurred())

		wrappedHandler.ServeHTTP(recorder, request)

		Expect(recorder.Body.String()).To(Equal("The wrapped handler was called!"))
		Expect(recorder.Header().Get("Cache-Control")).To(Equal("no-cache"))
	})

	Context("when accept encoding uses gzip", func() {
		It("returns a gzipped asset", func() {

			insideHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, strings.Repeat("abc123", 1000))
			})

			wrappedHandler := web.CacheNearlyForever(insideHandler)
			recorder := httptest.NewRecorder()
			request, err := http.NewRequest("GET", "/", nil)
			Expect(err).ToNot(HaveOccurred())
			request.Header["Accept-Encoding"] = []string{"gzip, deflate, br"}

			wrappedHandler.ServeHTTP(recorder, request) // request is never used

			reader, err := gzip.NewReader(recorder.Body)
			Expect(err).ToNot(HaveOccurred())
			body, err := io.ReadAll(reader)
			Expect(err).ToNot(HaveOccurred())

			Expect(string(body)).To(Equal(strings.Repeat("abc123", 1000)))
			Expect(recorder.Header().Get("Content-Encoding")).To(Equal("gzip"))
		})
	})
})
