package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func fixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte(`<!doctype html><script src="/assets/app-abc123.js"></script>`)},
		"assets/app-abc123.js":  {Data: []byte("console.log(1)")},
		"assets/app-abc123.css": {Data: []byte("body{}")},
		"favicon.svg":           {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
	}
}

func fetch(t *testing.T, h http.Handler, method, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Result()
}

func TestHandlerServesFilesWithTypes(t *testing.T) {
	h := NewHandler(fixture())
	for path, want := range map[string]string{
		"/":                      "text/html",
		"/index.html":            "text/html",
		"/assets/app-abc123.js":  "text/javascript",
		"/assets/app-abc123.css": "text/css",
		"/favicon.svg":           "image/svg+xml",
	} {
		resp := fetch(t, h, http.MethodGet, path)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), want) {
			t.Errorf("%s: %d %q, want %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), want)
		}
	}
}

func TestHandlerFallsBackToIndexForClientRoutes(t *testing.T) {
	h := NewHandler(fixture())
	for _, path := range []string{"/actors", "/actors/42", "/pools", "/decisions"} {
		resp := fetch(t, h, http.MethodGet, path)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(body), "<!doctype html>") {
			t.Errorf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

func TestHandlerMissingAssetIs404(t *testing.T) {
	h := NewHandler(fixture())
	for _, path := range []string{"/assets/gone.js", "/missing.css", "/assets/"} {
		if resp := fetch(t, h, http.MethodGet, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404 rather than HTML", path, resp.StatusCode)
		}
	}
}

func TestHandlerCaching(t *testing.T) {
	h := NewHandler(fixture())
	if cc := fetch(t, h, http.MethodGet, "/assets/app-abc123.js").Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("hashed asset Cache-Control %q", cc)
	}
	for _, p := range []string{"/", "/actors/1"} {
		if cc := fetch(t, h, http.MethodGet, p).Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control %q, want no-cache so a rebuild is picked up", p, cc)
		}
	}
}

// The embedded build must be a real app: an index.html whose assets exist.
func TestEmbeddedBuildIsConsistent(t *testing.T) {
	h := Handler()
	resp := fetch(t, h, http.MethodGet, "/")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	refs := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+\.(js|css)`).FindAllString(string(body), -1)
	if len(refs) == 0 {
		t.Fatal("index.html references no hashed assets; run make web")
	}
	for _, ref := range refs {
		if r := fetch(t, h, http.MethodGet, ref); r.StatusCode != 200 {
			t.Errorf("%s: %d", ref, r.StatusCode)
		}
	}
}
