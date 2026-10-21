package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
)

const teamsURL = "https://prod-12.westeurope.logic.azure.com:443/workflows/0123456789abcdef0123456789abcdef/triggers/manual/paths/invoke?api-version=2016-06-01&sp=%2Ftriggers%2Fmanual%2Frun&sig=SuperSecretSig123"

func testConfig() *config.NotifyConfig {
	return &config.NotifyConfig{Outputs: []config.OutputConfig{
		{Name: "teams-platform", Type: config.OutputTypeTeams, WebhookURL: teamsURL,
			Filters: config.OutputFilters{SeverityMin: "warning", Match: map[string]string{"system": "kubernetes"}},
			Card:    config.CardConfig{Facts: []config.CardFactConfig{{Title: "Cluster", Tag: "cluster"}}}},
		{Name: "pager", Type: config.OutputTypeWebhook, URL: "https://user:pw@hooks.example/notify",
			Method: "PUT", Headers: map[string]string{"Authorization": "Bearer t0ken-value"}},
	}}
}

func newTestServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	if opts.Config == nil {
		opts.Config = testConfig()
	}
	ts := httptest.NewServer(New(opts).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHealth(t *testing.T) {
	s := New(Options{Config: testConfig(), DryRun: true})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	check := func(wantCode int, wantRedis string) {
		t.Helper()
		code, body := get(t, ts.URL+"/healthz")
		var h Health
		if err := json.Unmarshal([]byte(body), &h); err != nil {
			t.Fatal(err)
		}
		if code != wantCode || h.Redis != wantRedis || !h.DispatcherLoaded || !h.DryRun || h.Outputs != 2 {
			t.Errorf("%d %+v, want %d redis=%q", code, h, wantCode, wantRedis)
		}
	}
	check(http.StatusServiceUnavailable, "connecting")

	s.SetPing(func(context.Context) error { return nil })
	check(http.StatusOK, "ok")

	s.SetPing(func(context.Context) error { return errors.New("dial tcp: refused") })
	check(http.StatusServiceUnavailable, "error: dial tcp: refused")
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"": "",
		"http://homerun2-notification-catcher-sink.homerun2.svc/teams/backstage-template-execution": "http://homerun2-notification-catcher-sink.homerun2.svc/teams/backstage-template-execution",
		"https://user:pw@hooks.example/notify":                                                      "https://REDACTED@hooks.example/notify",
		"https://hooks.example/x?token=abc&b=1#frag":                                                "https://hooks.example/x?b=REDACTED&token=REDACTED",
		teamsURL: "https://prod-12.westeurope.logic.azure.com:443/workflows/REDACTED/triggers/manual/paths/invoke?api-version=REDACTED&sig=REDACTED&sp=REDACTED",
		"https://x.webhook.office.com/webhookb2/a1b2c3d4-e5f6-7890-abcd-ef1234567890@tenant/IncomingWebhook/0f1e2d3c4b5a69788796a5b4c3d2e1f0/abc": "https://x.webhook.office.com/webhookb2/REDACTED/IncomingWebhook/REDACTED/abc",
		"://bad": "REDACTED",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// No secret of the config reaches the JSON or the page.
func TestConfigRedacted(t *testing.T) {
	ts := newTestServer(t, Options{Streams: []string{"messages"}, ConsumerGroup: "g", RedisAddr: "redis:6379"})
	secrets := []string{"SuperSecretSig123", "0123456789abcdef0123456789abcdef", "t0ken-value", "pw@"}

	code, body := get(t, ts.URL+"/api/config")
	if code != http.StatusOK {
		t.Fatalf("api/config: %d", code)
	}
	var v ConfigView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Outputs) != 2 || v.Outputs[0].Filters.SeverityMin != "warning" || v.Outputs[0].CardFacts[0].Tag != "cluster" ||
		v.Outputs[1].Method != "PUT" || v.Outputs[1].Headers[0] != "Authorization: REDACTED" {
		t.Errorf("view: %+v", v)
	}

	code, page := get(t, ts.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("index: %d", code)
	}
	for _, want := range []string{"teams-platform", "severity ≥</b> warning", "system=kubernetes", "Cluster <b>← tag cluster", "PUT https://REDACTED@hooks.example/notify", "messages", "redis:6379"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, secret := range secrets {
		if strings.Contains(body, secret) || strings.Contains(page, secret) {
			t.Errorf("secret %q leaked", secret)
		}
	}
}

func TestToken(t *testing.T) {
	ts := newTestServer(t, Options{Token: "s3cret"})
	for path, want := range map[string]int{
		"/":                        http.StatusUnauthorized,
		"/api/config":              http.StatusUnauthorized,
		"/api/config?token=s3cret": http.StatusOK,
		"/healthz":                 http.StatusServiceUnavailable, // open, Redis not connected yet
		"/static/vendor/fonts.css": http.StatusOK,
	} {
		if code, _ := get(t, ts.URL+path); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

func TestBasePath(t *testing.T) {
	ts := newTestServer(t, Options{BasePath: "/catcher"})
	for path, want := range map[string]int{
		"/catcher/":                        http.StatusOK,
		"/catcher/api/config":              http.StatusOK,
		"/catcher/static/vendor/fonts.css": http.StatusOK,
		"/healthz":                         http.StatusServiceUnavailable,
		"/api/config":                      http.StatusNotFound,
	} {
		if code, _ := get(t, ts.URL+path); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if _, page := get(t, ts.URL+"/catcher/"); !strings.Contains(page, `href="/catcher/static/vendor/pico-2.1.1.min.css"`) {
		t.Error("assets not under the base path")
	}
}

func TestNoOutputs(t *testing.T) {
	ts := newTestServer(t, Options{Config: &config.NotifyConfig{}})
	if _, page := get(t, ts.URL+"/"); !strings.Contains(page, "No outputs configured") {
		t.Error("empty config not explained")
	}
}
