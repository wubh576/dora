package httpapi

import (
	"bytes"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

type applicationHandler struct {
	api    http.Handler
	static http.Handler
}

func newApplicationHandler(api http.Handler, staticFS fs.FS) http.Handler {
	return &applicationHandler{
		api:    api,
		static: &staticHandler{files: staticFS},
	}
}

func (h *applicationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		h.api.ServeHTTP(w, r)
		return
	}
	h.static.ServeHTTP(w, r)
}

type staticHandler struct {
	files fs.FS
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name, valid := staticPath(r.URL.Path)
	if !valid {
		http.NotFound(w, r)
		return
	}
	if name == "" {
		name = "index.html"
	}

	if h.serveFile(w, r, name) {
		return
	}
	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	if !h.serveFile(w, r, "index.html") {
		http.NotFound(w, r)
	}
}

func staticPath(requestPath string) (string, bool) {
	if strings.Contains(requestPath, `\`) {
		return "", false
	}
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == ".." {
			return "", false
		}
	}

	name := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if name == "" || name == "." {
		return "", true
	}
	return name, fs.ValidPath(name)
}

func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	info, err := fs.Stat(h.files, name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	content, err := fs.ReadFile(h.files, name)
	if err != nil {
		return false
	}

	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path.Base(name), info.ModTime(), bytes.NewReader(content))
	return true
}
