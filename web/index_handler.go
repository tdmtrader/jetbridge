package web

import (
	"crypto/sha256"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sync"

	"code.cloudfoundry.org/lager/v3"
)

type assetCache struct {
	fs      fs.FS
	cache   map[string]string
	cacheMu sync.RWMutex
	dynamic bool // true if using filesystem that can change
}

func newAssetCache(fs fs.FS, dynamic bool) *assetCache {
	return &assetCache{
		fs:      fs,
		cache:   make(map[string]string),
		dynamic: dynamic,
	}
}

func (ac *assetCache) asset(path string) (string, error) {
	hash, err := ac.hash(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/public/%s?id=%s", path, hash), nil
}

// hash returns the first 8 hex characters of the sha256 of the file's contents.
func (ac *assetCache) hash(path string) (string, error) {
	if !ac.dynamic {
		// Try reading from cache first for embedded assets
		ac.cacheMu.RLock()
		if hash, ok := ac.cache[path]; ok {
			ac.cacheMu.RUnlock()
			return hash, nil
		}
		ac.cacheMu.RUnlock()
	}

	contents, err := fs.ReadFile(ac.fs, path)
	if err != nil {
		return "", fmt.Errorf("read asset %s: %w", path, err)
	}

	sum := sha256.Sum256(contents)
	hash := fmt.Sprintf("%x", sum[:4])

	// Only cache if not in dynamic mode
	if !ac.dynamic {
		ac.cacheMu.Lock()
		ac.cache[path] = hash
		ac.cacheMu.Unlock()
	}

	return hash, nil
}

func IndexHandler(logger lager.Logger, publicFS fs.FS, live bool) http.Handler {
	assets := newAssetCache(publicFS, live)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log := logger.Session("index")

		funcs := template.FuncMap{
			"asset": assets.asset,
		}

		t, err := template.New("web").Funcs(funcs).ParseFS(publicFS, "index.html")
		if err != nil {
			log.Error("failed-to-parse-templates", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		err = t.ExecuteTemplate(w, "index.html", indexTemplateData{
			CSRFToken: r.FormValue("csrf_token"),
			AuthToken: r.Header.Get("Authorization"),
		})
		if err != nil {
			log.Error("failed-to-build-template", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	})
}

type indexTemplateData struct {
	CSRFToken string
	AuthToken string
}
