// Package webui serves the WebUI from disk, falling back to embedded HTML.
package webui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed fallback.html
var fallbackFS embed.FS

// Handler serves the WebUI files. It first tries to serve from workdir/webroot/,
// and falls back to the embedded fallback.html. All responses include CORS headers.
type Handler struct {
	Webroot string // path to workdir/webroot/
}

// NewHandler creates a new WebUI handler.
func NewHandler(workDir string) *Handler {
	return &Handler{
		Webroot: filepath.Join(workDir, "webroot"),
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	// Clean the path
	urlPath := r.URL.Path
	if urlPath == "/" {
		urlPath = "/index.html"
	}
	urlPath = strings.TrimPrefix(urlPath, "/")

	// Try disk first
	diskPath := filepath.Join(h.Webroot, filepath.Clean(urlPath))
	if !strings.HasPrefix(diskPath, h.Webroot) {
		// Path traversal attempt
		h.serveFallback(w, r)
		return
	}

	if info, err := os.Stat(diskPath); err == nil && !info.IsDir() {
		// Set MIME type
		ext := filepath.Ext(diskPath)
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
		http.ServeFile(w, r, diskPath)
		return
	}

	// Fall back to embedded
	h.serveFallback(w, r)
}

// serveFallback serves the embedded fallback.html.
func (h *Handler) serveFallback(w http.ResponseWriter, r *http.Request) {
	data, err := fallbackFS.ReadFile("fallback.html")
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// FileServer returns an http.Handler that serves files from the given directory.
// This is a convenience wrapper used when the handler is registered directly.
func FileServer(workDir string) http.Handler {
	return NewHandler(workDir)
}

// Ensure fs is used.
var _ = fs.StatFS(nil)
