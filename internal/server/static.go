package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
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
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.NotFound(response, request)
			return
		}

		cleaned := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if cleaned != "." {
			if file, err := fs.Stat(dist, cleaned); err == nil && !file.IsDir() {
				files.ServeHTTP(response, request)
				return
			}
		}

		fallbackRequest := request.Clone(request.Context())
		fallbackURL := *request.URL
		fallbackURL.Path = "/"
		fallbackRequest.URL = &fallbackURL
		files.ServeHTTP(response, fallbackRequest)
	})
}
