// Package web embeds the built Vue SPA (internal/web/dist) and serves it,
// falling back to a placeholder page when the SPA has not been built yet.
// The dist directory is produced by `make build-web`.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
//go:embed placeholder.html
var assets embed.FS

// Handler serves the SPA with history-mode fallback. When the SPA has not
// been built (dist contains no index.html), a placeholder page is served
// instead.
func Handler() http.Handler {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		placeholder, err := fs.ReadFile(assets, "placeholder.html")
		if err != nil {
			panic(err)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(placeholder)
		})
	}
	return spaHandler(dist)
}

// spaHandler serves static files, falling back to index.html for client-side
// routes.
func spaHandler(dist fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err != nil {
			// SPA fallback: serve index.html for client-side routes.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
