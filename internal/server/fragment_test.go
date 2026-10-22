package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

func TestManifest(t *testing.T) {
	ts := newTestServer(t, Options{BasePath: "/notify", Build: BuildInfo{Version: "v3.14.0"}})
	code, body := get(t, ts.URL+"/notify/ui/manifest.json")
	if code != http.StatusOK {
		t.Fatalf("manifest: %d", code)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Title != "Notification Catcher" || m.Version != "v3.14.0" || len(m.Fragments) != 1 {
		t.Fatalf("manifest: %+v", m)
	}
	if f := m.Fragments[0]; f.URL != "/notify/ui/fragment/events" || f.RefreshSeconds < 2 {
		t.Errorf("fragment: %+v", f)
	}
}

func TestEventsFragment(t *testing.T) {
	s := New(Options{BasePath: "/notify", Config: testConfig()})
	ts := newHandlerServer(t, s)
	if _, body := get(t, ts.URL+"/notify/ui/fragment/events"); !strings.Contains(body, "No messages dispatched yet.") {
		t.Errorf("empty fragment: %s", body)
	}

	s.Record(record("1", "<b>disk</b> full", sent, filtered))
	r := record("2", "unrouted", skipped, filtered)
	r.Message.Severity = "critical"
	s.Record(r)
	s.Record(record("3", "pager down", sent, failed))

	code, body := get(t, ts.URL+"/notify/ui/fragment/events")
	if code != http.StatusOK {
		t.Fatalf("fragment: %d", code)
	}
	for _, want := range []string{
		`<div class="hr-frag">`,
		`&lt;b&gt;disk&lt;/b&gt; full`,
		`hr-sev hr-sev-error">critical`,
		`hr-sev hr-sev-warning">warning`,
		`no output matched`,
		`→ teams-platform · failed pager`,
		`href="/notify/events"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<script") {
		t.Error("fragment is not bare HTML")
	}
	if strings.Index(body, "pager down") > strings.Index(body, "disk") {
		t.Error("not newest first")
	}
}

func TestFragmentToken(t *testing.T) {
	ts := newTestServer(t, Options{Token: "s3cret"})
	for _, path := range []string{"/ui/manifest.json", "/ui/fragment/events"} {
		if code, _ := get(t, ts.URL+path); code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", path, code)
		}
	}
}

func TestOutcome(t *testing.T) {
	ev := Event{Routed: true, Outputs: []OutputResult{{Output: "a", Result: notify.ResultDryRun}}}
	if got := outcome(ev); got != "dry-run a" {
		t.Errorf("outcome: %q", got)
	}
}
