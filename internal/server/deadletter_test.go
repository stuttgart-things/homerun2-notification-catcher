package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

type fakeDeadLetter struct {
	entries []notify.ParkedEntry
	err     error
}

func (f *fakeDeadLetter) Recent(_ context.Context, n int64) ([]notify.ParkedEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries[:min(int(n), len(f.entries))], nil
}

func (f *fakeDeadLetter) Len(context.Context) (int64, error) {
	return int64(len(f.entries)), f.err
}

func parked() *fakeDeadLetter {
	at := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	return &fakeDeadLetter{entries: []notify.ParkedEntry{
		{ID: "1700000000002-0", Output: "teams-platform", ObjectID: "obj-2", Error: "teams: webhook returned 503", Attempts: 3, FailedAt: at,
			Message: homerun.Message{Title: "disk full", Severity: "critical"}},
		{ID: "1700000000001-0", Output: "pager", ObjectID: "obj-1", Error: "dial tcp: no route to host", Attempts: 3, FailedAt: at},
		{ID: "1700000000000-0", Output: "teams-platform", ObjectID: "obj-0", Attempts: 1, FailedAt: at},
	}}
}

func TestDeadLetter_PageAndAPI(t *testing.T) {
	s := New(Options{Config: testConfig(), DeadLetterStream: "dead", SinkURL: "https://sink.example"})
	s.SetDeadLetter(parked())
	ts := newHandlerServer(t, s)

	code, body := get(t, ts.URL+"/api/dead-letter")
	var v DeadLetterView
	if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil {
		t.Fatalf("api: %d %s", code, body)
	}
	if v.Stream != "dead" || v.Pending != 3 || len(v.Entries) != 3 ||
		len(v.Outputs) != 2 || v.Outputs[0] != (OutputCount{"pager", 1}) || v.Outputs[1] != (OutputCount{"teams-platform", 2}) {
		t.Errorf("view %+v", v)
	}

	code, page := get(t, ts.URL+"/dead-letter")
	if code != http.StatusOK {
		t.Fatalf("page: %d", code)
	}
	for _, want := range []string{
		`class="active">Dead letter`, "disk full", "teams: webhook returned 503", "1700000000002-0",
		ReplayBinary + " replay --output teams-platform --dry-run", "replay --output pager --dry-run",
		"https://sink.example/?objectId=obj-2",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	_, metrics := get(t, ts.URL+"/metrics")
	if !strings.Contains(metrics, "notification_catcher_dead_letter_pending 3\n") {
		t.Errorf("metrics lack the pending gauge:\n%s", metrics)
	}
}

func TestDeadLetter_OffAndUnreadable(t *testing.T) {
	off := newHandlerServer(t, New(Options{Config: testConfig()}))
	if _, page := get(t, off.URL+"/dead-letter"); !strings.Contains(page, "DEAD_LETTER_STREAM=off") {
		t.Error("off: page does not say so")
	}
	if _, m := get(t, off.URL+"/metrics"); strings.Contains(m, "dead_letter_pending") {
		t.Error("off: pending gauge rendered")
	}

	s := New(Options{Config: testConfig(), DeadLetterStream: "dead"})
	ts := newHandlerServer(t, s)
	if code, _ := get(t, ts.URL+"/api/dead-letter"); code != http.StatusServiceUnavailable {
		t.Errorf("not connected: %d, want 503", code)
	}
	s.SetDeadLetter(&fakeDeadLetter{err: errors.New("redis down")})
	code, body := get(t, ts.URL+"/api/dead-letter")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "redis down") {
		t.Errorf("unreadable: %d %s", code, body)
	}
	if _, m := get(t, ts.URL+"/metrics"); strings.Contains(m, "dead_letter_pending") {
		t.Error("unreadable: pending gauge rendered, a made-up 0")
	}
}
