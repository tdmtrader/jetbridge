package web

import (
	"fmt"
	"net/http"

	"github.com/klauspost/compress/gzhttp"
)

const yearInSeconds = 31536000

// CacheNearlyForever lets a browser keep a response for a year without
// revalidating it, but only when the URL carries the content hash (`?id=`)
// that index.html's asset template function appends. Any other URL (a module
// loaded through a relative import, a font referenced from main.css) has
// nothing in it that changes on upgrade, so the browser must revalidate it on
// every use.
func CacheNearlyForever(handler http.Handler) http.Handler {
	withoutGz := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("id") {
			w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d, public, immutable", yearInSeconds))
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		handler.ServeHTTP(w, r)
	})
	return gzhttp.GzipHandler(withoutGz)
}
