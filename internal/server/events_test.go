package server

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

func record(id, title string, results ...notify.DispatchResult) notify.Record {
	return notify.Record{
		Message: models.CaughtMessage{Message: homerun.Message{Title: title, Severity: "warning", System: "kubernetes"}, ObjectID: id},
		At:      time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		Results: results,
	}
}

var (
	sent     = notify.DispatchResult{Output: "teams-platform"}
	failed   = notify.DispatchResult{Output: "pager", Err: errors.New("webhook returned 500")}
	filtered = notify.DispatchResult{Output: "pager", Skipped: true}
	skipped  = notify.DispatchResult{Output: "teams-platform", Skipped: true}
)

func TestEventsRing(t *testing.T) {
	e := newEvents(3, []string{"teams-platform", "pager"})
	for i, id := range []string{"1", "2", "3", "4"} {
		r := record(id, "m"+id, sent, filtered)
		if i == 2 {
			r = record(id, "m"+id, skipped, filtered)
		}
		e.record(r)
	}
	got := e.list(EventQuery{})
	if len(got) != 3 || got[0].ObjectID != "4" || got[2].ObjectID != "2" {
		t.Fatalf("ring: %+v", got)
	}
	if u := e.list(EventQuery{Result: "unrouted"}); len(u) != 1 || u[0].ObjectID != "3" || u[0].Routed {
		t.Errorf("unrouted: %+v", u)
	}
	if l := e.list(EventQuery{Limit: 1}); len(l) != 1 || l[0].ObjectID != "4" {
		t.Errorf("limit: %+v", l)
	}
	if o := e.list(EventQuery{ObjectID: "2"}); len(o) != 1 || o[0].Outputs[0].Result != notify.ResultSent {
		t.Errorf("objectId: %+v", o)
	}
}

func TestMetrics(t *testing.T) {
	e := newEvents(10, []string{"teams-platform", `pa"ger`})
	e.record(record("1", "a", sent, notify.DispatchResult{Output: `pa"ger`, Err: errors.New("x"), Attempts: 3, DeadLettered: true}))
	e.record(record("2", "b", skipped, notify.DispatchResult{Output: `pa"ger`, Skipped: true}))
	var b strings.Builder
	e.writeMetrics(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE notification_catcher_dispatch_total counter",
		`notification_catcher_dispatch_total{output="teams-platform",result="sent"} 1`,
		`notification_catcher_dispatch_total{output="teams-platform",result="filtered"} 1`,
		`notification_catcher_dispatch_total{output="teams-platform",result="failed"} 0`,
		`notification_catcher_dispatch_total{output="pa\"ger",result="failed"} 1`,
		`notification_catcher_retries_total{output="pa\"ger"} 2`,
		`notification_catcher_retries_total{output="teams-platform"} 0`,
		`notification_catcher_dead_letter_total{output="pa\"ger"} 1`,
		`notification_catcher_dead_letter_total{output="teams-platform"} 0`,
		`notification_catcher_messages_total{routed="true"} 1`,
		`notification_catcher_messages_total{routed="false"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q\n%s", want, out)
		}
	}
}

func TestEventsPage(t *testing.T) {
	s := New(Options{Config: testConfig(), Token: "s3cret", SinkURL: "https://sink.example/"})
	s.Record(record("1761-0", "node down", sent, failed))
	s.Record(record("1761-1", "quiet", skipped, filtered))
	ts := newHandlerServer(t, s)

	code, page := get(t, ts.URL+"/events?token=s3cret")
	if code != http.StatusOK {
		t.Fatalf("events: %d", code)
	}
	for _, want := range []string{
		`href="https://sink.example/?objectId=1761-0#notifications"`,
		`class="r r-failed" title="failed: webhook returned 500">pager · failed`,
		`class="r r-sent" title="sent">teams-platform</span>`,
		"no output matched",
		`href="/?token=s3cret"`,                         // tabs keep the token
		`href="/events?result=failed&amp;token=s3cret"`, // filters too
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	if _, page := get(t, ts.URL+"/events?token=s3cret&result=failed"); strings.Contains(page, "quiet") || !strings.Contains(page, "node down") {
		t.Error("result filter")
	}
	if code, _ := get(t, ts.URL+"/events"); code != http.StatusUnauthorized {
		t.Errorf("events without token: %d", code)
	}
	if code, _ := get(t, ts.URL+"/api/events?token=s3cret&result=bogus"); code != http.StatusBadRequest {
		t.Errorf("bad result: %d", code)
	}
	if code, body := get(t, ts.URL+"/metrics"); code != http.StatusOK || !strings.Contains(body, `result="failed"} 1`) {
		t.Errorf("metrics (open, at the root): %d %s", code, body)
	}
}
