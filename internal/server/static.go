package server

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/tradesys/dashboard/web"
)

// staticHandler serves the built single-page app.
//
// It deliberately does not use http.FileServer, which redirects "/index.html"
// to "./" and would bounce the root request. Files are resolved and written
// here so that: the root path serves index.html directly with no redirect,
// content-hashed assets under /assets get a long cache lifetime while
// index.html gets none, and any unmatched path falls through to the SPA shell
// so client-side routes such as /algorithms survive a hard reload.
func (s *Server) staticHandler() http.HandlerFunc {
	root, err := s.frontendFS()
	if err != nil {
		s.deps.Log.Error("frontend assets are unavailable", "err", err)
		return func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusInternalServerError, "frontend_missing",
				"The frontend was not built into this binary.")
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and HEAD are served here.")
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}

		if s.serveFile(w, r, root, name) {
			return
		}
		// Unknown path: hand back the SPA shell and let the client router
		// decide whether it is a real route or a 404.
		s.serveFile(w, r, root, "index.html")
	}
}

// frontendFS picks the asset source: a directory override for frontend
// iteration without rebuilding the Go binary, otherwise the embedded bundle.
func (s *Server) frontendFS() (fs.FS, error) {
	if dir := os.Getenv("WEB_DIST_DIR"); dir != "" {
		if _, err := os.Stat(path.Join(dir, "index.html")); err == nil {
			s.deps.Log.Info("serving frontend from disk", "dir", dir)
			return os.DirFS(dir), nil
		}
		s.deps.Log.Warn("WEB_DIST_DIR has no index.html; falling back to embedded assets", "dir", dir)
	}
	return web.Dist()
}

// serveFile writes one asset and reports whether it existed.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, root fs.FS, name string) bool {
	f, err := root.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.deps.Log.Warn("could not open static asset", "path", name, "err", err)
		}
		return false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}

	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, "assets/") {
		// Vite content-hashes these filenames, so they are immutable.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// index.html above all: a stale copy pins operators to the previous
		// build after a deploy.
		w.Header().Set("Cache-Control", "no-cache")
	}

	// ServeContent handles range requests and conditional GETs, but needs a
	// seeker. Embedded and on-disk files both provide one; anything exotic
	// falls back to buffering.
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, modTime(info), rs)
		return true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		s.deps.Log.Error("could not read static asset", "path", name, "err", err)
		return false
	}
	http.ServeContent(w, r, name, modTime(info), bytes.NewReader(data))
	return true
}

// modTime returns the file's modification time, or the zero time for embedded
// files (which have none) so ServeContent simply omits Last-Modified.
func modTime(info fs.FileInfo) time.Time {
	t := info.ModTime()
	if t.IsZero() {
		return time.Time{}
	}
	return t
}
