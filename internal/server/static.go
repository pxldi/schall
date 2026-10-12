package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

// dist is replaced with the Svelte production build in the Docker build.
//
// SvelteKit stores compiled assets under _app. The all: prefix is required
// because embed otherwise excludes directories whose names begin with "_".
//
//go:embed all:dist
var assets embed.FS

func spaHandler() http.Handler {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	return spaHandlerFS(dist)
}

// immutablePrefix is where SvelteKit puts files whose names carry a hash of
// their content. A new build writes new names, so a browser can keep these for
// good and never ask again.
const immutablePrefix = "_app/immutable/"

func spaHandlerFS(dist fs.FS) http.Handler {
	files := http.FileServer(http.FS(dist))
	tags := entityTags{fsys: dist, tags: map[string]string{}}

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.NotFound(response, request)
			return
		}

		cleaned := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if cleaned != "." {
			if file, err := fs.Stat(dist, cleaned); err == nil && !file.IsDir() {
				if strings.HasPrefix(cleaned, immutablePrefix) {
					response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					tags.revalidate(response, cleaned)
				}
				files.ServeHTTP(response, request)
				return
			}
		}

		tags.revalidate(response, "index.html")
		fallbackRequest := request.Clone(request.Context())
		fallbackURL := *request.URL
		fallbackURL.Path = "/"
		fallbackRequest.URL = &fallbackURL
		files.ServeHTTP(response, fallbackRequest)
	})
}

// entityTags hands out an ETag per embedded file. Embedded files have no
// modification time, so without a tag the browser cannot confirm the copy it
// holds and downloads the page shell again on every load. The shell names the
// current build's chunks, so it is confirmed on every load rather than kept.
type entityTags struct {
	fsys fs.FS
	mu   sync.Mutex
	tags map[string]string
}

func (e *entityTags) revalidate(response http.ResponseWriter, name string) {
	response.Header().Set("Cache-Control", "no-cache")
	if tag := e.tag(name); tag != "" {
		response.Header().Set("ETag", tag)
	}
}

func (e *entityTags) tag(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if tag, ok := e.tags[name]; ok {
		return tag
	}
	content, err := fs.ReadFile(e.fsys, name)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(content)
	tag := `"` + hex.EncodeToString(sum[:16]) + `"`
	e.tags[name] = tag
	return tag
}
