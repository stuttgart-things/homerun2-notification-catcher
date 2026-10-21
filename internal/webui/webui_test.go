package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseBasePath(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "notify": "/notify", "/a/b/": "/a/b"} {
		got, err := ParseBasePath(in)
		if err != nil || got != want {
			t.Errorf("ParseBasePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/../x", "/a b", `/"x`} {
		if _, err := ParseBasePath(in); err == nil {
			t.Errorf("ParseBasePath(%q): want error", in)
		}
	}
}

func TestAuth(t *testing.T) {
	h := Auth("s3cret", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for target, want := range map[string]int{
		"/":              http.StatusUnauthorized,
		"/?token=wrong":  http.StatusUnauthorized,
		"/?token=s3cret": http.StatusNoContent,
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", target, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("bearer: %d", rec.Code)
	}

	open := Auth("", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec = httptest.NewRecorder()
	open(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("no token: %d", rec.Code)
	}
}

func TestStaticCached(t *testing.T) {
	rec := httptest.NewRecorder()
	Static().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/vendor/pico-2.1.1.min.css", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") == "" {
		t.Errorf("pico: %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
