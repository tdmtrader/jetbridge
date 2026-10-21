package web

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

func PublicHandler(publicFS fs.FS, live bool) http.Handler {
	mime.AddExtensionType(".woff2", "font/woff2")
	mime.AddExtensionType(".webmanifest", "application/manifest+json")

	assets := newAssetCache(publicFS, live)
	files := http.FileServer(http.FS(publicFS))

	// The ETag lets a browser revalidate a URL without a content hash cheaply:
	// http.FileServer answers a matching If-None-Match with 304. It is weak
	// because gzhttp may serve the same file gzipped or not.
	withETag := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if hash, err := assets.hash(name); err == nil {
			w.Header().Set("ETag", fmt.Sprintf(`W/"%s"`, hash))
		}
		files.ServeHTTP(w, r)
	})

	return CacheNearlyForever(http.StripPrefix("/public/", withETag))
}
