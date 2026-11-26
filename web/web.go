// Package web embeds the built single-page app and serves it.
//
// The app's source is in this directory and its build output in dist/, which
// is committed so that `go build` needs no Node toolchain; `make web`
// rebuilds it.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the embedded build.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Sub fails only on an invalid path; "dist" is a constant.
		panic("web: " + err.Error())
	}
	return NewHandler(sub)
}

// NewHandler serves fsys, which holds index.html and /assets. A path naming
// a file is served as is; a path with no extension is a client route and
// gets index.html so a reload or a shared link works. A missing path with
// an extension is a 404, not HTML, so a stale asset reference fails loudly
// instead of being parsed as script.
func NewHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch {
		case name == "" || name == "index.html":
			serveIndex(w, r, files)
		case isFile(fsys, name):
			if strings.HasPrefix(name, "assets/") {
				// Vite names assets by content hash, so they never change.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
		case path.Ext(name) == "" && name != "assets" && !strings.HasPrefix(name, "assets/"):
			serveIndex(w, r, files)
		default:
			http.NotFound(w, r)
		}
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, files http.Handler) {
	w.Header().Set("Cache-Control", "no-cache")
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	files.ServeHTTP(w, r2)
}

func isFile(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}
