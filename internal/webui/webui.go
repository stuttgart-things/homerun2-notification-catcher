// Package webui holds what the catcher's HTML pages share: the vendored page
// assets (Pico, the header font, the logo; static/vendor/README.md), BASE_PATH
// parsing and the token check. The sink (#52) and the catcher's own UI (#22)
// both serve them, from one copy in the binary.
package webui

import (
	"crypto/subtle"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

//go:embed static
var staticFS embed.FS

// Static serves the assets; mount it at "<base>/static/". Open like the
// health checks: the page's <link>/<script> tags can't carry the token.
// Embedded files have no modtime, so without Cache-Control the browser would
// fetch the ~0.5 MB of vendor assets on every load.
func Static() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	files := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}

var basePathSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// ParseBasePath normalises a BASE_PATH to "/a/b" form ("" and "/" become
// ""), as homerun2-core-catcher does. Only plain path segments are accepted:
// the value is written into the page's HTML.
func ParseBasePath(s string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(s), "/")
	if trimmed == "" {
		return "", nil
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if !basePathSegment.MatchString(seg) || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid BASE_PATH %q: segment %q", s, seg)
		}
	}
	return "/" + trimmed, nil
}

// Auth wraps next to require token as "Authorization: Bearer <token>" or
// "?token=<token>". An empty token leaves next open.
func Auth(token string, next http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
