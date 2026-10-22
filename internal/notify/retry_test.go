package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

func TestRetryable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("dial tcp: connect: no route to host"), true},
		{fmt.Errorf("webhook: %w", context.DeadlineExceeded), true},
		{fmt.Errorf("webhook: %w", context.Canceled), false},
		{&StatusError{StatusCode: 500}, true},
		{&StatusError{StatusCode: 503}, true},
		{&StatusError{StatusCode: 429}, true},
		{&StatusError{StatusCode: 408}, true},
		{&StatusError{StatusCode: 400}, false},
		{&StatusError{StatusCode: 404}, false},
		{fmt.Errorf("wrapped: %w", &StatusError{StatusCode: 502}), true},
		{permanent(errors.New("webhook: URL is empty")), false},
	} {
		if got := Retryable(tc.err); got != tc.want {
			t.Errorf("Retryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":                              0,
		"3":                             3 * time.Second,
		"0":                             0,
		"-1":                            0,
		"soon":                          0,
		"Thu, 22 Oct 2026 08:00:05 GMT": 5 * time.Second,
		"Thu, 22 Oct 2026 07:59:00 GMT": 0,
	} {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

// flakyEndpoint answers the first fails requests with status, then 200.
func flakyEndpoint(t *testing.T, fails int, status int, header http.Header) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if int(calls.Add(1)) <= fails {
			for k, v := range header {
				w.Header()[k] = v
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte("nope"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts, &calls
}

// memDeadLetter records parked entries, or fails with err.
type memDeadLetter struct {
	mu      sync.Mutex
	entries []DeadLetterEntry
	err     error
}

func (m *memDeadLetter) Park(_ context.Context, e DeadLetterEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, e)
	return nil
}

func webhookEntry(name, url string, attempts int) DispatcherEntry {
	return DispatcherEntry{
		Name:     name,
		Notifier: NewWebhookNotifier(url, "", nil, nil),
		Retry:    config.RetryPolicy{Attempts: attempts, Backoff: time.Millisecond},
	}
}

func TestDispatch_RetriesTransientFailure(t *testing.T) {
	ts, calls := flakyEndpoint(t, 2, http.StatusServiceUnavailable, nil)
	dl := &memDeadLetter{}
	d := NewDispatcherWithNotifiers(webhookEntry("hook", ts.URL, 3))
	d.DeadLetter = dl

	r := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))[0]
	if r.Err != nil || r.Attempts != 3 || r.Result() != ResultSent || calls.Load() != 3 {
		t.Fatalf("result %+v after %d calls, want sent on the 3rd attempt", r, calls.Load())
	}
	if len(dl.entries) != 0 {
		t.Errorf("parked %d entries, want none", len(dl.entries))
	}
}

func TestDispatch_NoRetryOnClientError(t *testing.T) {
	ts, calls := flakyEndpoint(t, 5, http.StatusBadRequest, nil)
	d := NewDispatcherWithNotifiers(webhookEntry("hook", ts.URL, 3))

	r := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))[0]
	if r.Err == nil || r.Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("result %+v after %d calls, want one failed attempt", r, calls.Load())
	}
}

func TestDispatch_ParksAfterLastAttempt(t *testing.T) {
	ts, calls := flakyEndpoint(t, 10, http.StatusBadGateway, nil)
	dl := &memDeadLetter{}
	d := NewDispatcherWithNotifiers(webhookEntry("hook", ts.URL, 3))
	d.DeadLetter = dl

	msg := models.CaughtMessage{Message: homerun.Message{Title: "disk"}, ObjectID: "obj-1", StreamID: "1-0"}
	results := d.Dispatch(context.Background(), msg)
	r := results[0]
	if r.Err == nil || r.Attempts != 3 || !r.DeadLettered || calls.Load() != 3 {
		t.Fatalf("result %+v after %d calls, want 3 failed attempts, parked", r, calls.Load())
	}
	if err := Undelivered(results); err != nil {
		t.Errorf("Undelivered = %v, want nil: the failure is parked", err)
	}
	if len(dl.entries) != 1 {
		t.Fatalf("parked %d entries, want 1", len(dl.entries))
	}
	e := dl.entries[0]
	if e.Output != "hook" || e.Message.ObjectID != "obj-1" || e.Attempts != 3 || !strings.Contains(e.Err.Error(), "502") {
		t.Errorf("entry %+v", e)
	}
}

func TestDispatch_ParkFailureIsUndelivered(t *testing.T) {
	ts, _ := flakyEndpoint(t, 10, http.StatusInternalServerError, nil)
	d := NewDispatcherWithNotifiers(webhookEntry("hook", ts.URL, 1))
	d.DeadLetter = &memDeadLetter{err: errors.New("redis: connection refused")}

	results := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))
	if results[0].DeadLettered || Undelivered(results) == nil {
		t.Fatalf("results %+v: want undelivered", results)
	}
}

func TestDispatch_NoDeadLetterOnlyLogs(t *testing.T) {
	ts, _ := flakyEndpoint(t, 10, http.StatusInternalServerError, nil)
	d := NewDispatcherWithNotifiers(webhookEntry("hook", ts.URL, 1))

	results := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))
	if results[0].Result() != ResultFailed || Undelivered(results) != nil {
		t.Fatalf("results %+v: want failed, acked", results)
	}
}

func TestDispatch_HonoursRetryAfter(t *testing.T) {
	ts, calls := flakyEndpoint(t, 1, http.StatusTooManyRequests, http.Header{"Retry-After": {"1"}})
	d := NewDispatcherWithNotifiers(webhookEntry("hook", ts.URL, 2))

	start := time.Now()
	r := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))[0]
	if r.Err != nil || calls.Load() != 2 {
		t.Fatalf("result %+v after %d calls", r, calls.Load())
	}
	if el := time.Since(start); el < time.Second {
		t.Errorf("retried after %s, want the 1s of Retry-After", el)
	}
}

// A wait that does not fit into the dispatch budget is not started.
func TestDispatch_StopsWhenBudgetIsGone(t *testing.T) {
	ts, calls := flakyEndpoint(t, 10, http.StatusServiceUnavailable, nil)
	e := webhookEntry("hook", ts.URL, 5)
	e.Retry.Backoff = time.Second
	d := NewDispatcherWithNotifiers(e)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := d.Dispatch(ctx, caught(homerun.Message{Title: "t"}))[0]
	if r.Err == nil || r.Attempts != 1 || calls.Load() != 1 {
		t.Fatalf("result %+v after %d calls, want one attempt", r, calls.Load())
	}
	if el := time.Since(start); el > 150*time.Millisecond {
		t.Errorf("took %s: waited although the budget could not hold the backoff", el)
	}
}

func TestDispatch_StopEndsBackoff(t *testing.T) {
	ts, calls := flakyEndpoint(t, 10, http.StatusServiceUnavailable, nil)
	e := webhookEntry("hook", ts.URL, 5)
	e.Retry.Backoff = 5 * time.Second
	dl := &memDeadLetter{}
	d := NewDispatcherWithNotifiers(e)
	d.DeadLetter = dl

	time.AfterFunc(100*time.Millisecond, d.Stop)
	start := time.Now()
	r := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))[0]
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("took %s after Stop", el)
	}
	if r.Attempts != 1 || !r.DeadLettered || calls.Load() != 1 {
		t.Fatalf("result %+v after %d calls, want parked after one attempt", r, calls.Load())
	}
}

// One output retrying through an outage does not hold up the others.
func TestDispatch_OutputsRunInParallel(t *testing.T) {
	down, _ := flakyEndpoint(t, 10, http.StatusServiceUnavailable, nil)
	up, upCalls := flakyEndpoint(t, 0, 0, nil)
	slow := webhookEntry("down", down.URL, 3)
	slow.Retry.Backoff = 300 * time.Millisecond

	var upDone time.Duration
	start := time.Now()
	d := NewDispatcherWithNotifiers(slow, DispatcherEntry{
		Name: "up",
		Notifier: notifierFunc(func(ctx context.Context, m models.CaughtMessage) error {
			defer func() { upDone = time.Since(start) }()
			return NewWebhookNotifier(up.URL, "", nil, nil).Send(ctx, m)
		}),
	})
	results := d.Dispatch(context.Background(), caught(homerun.Message{Title: "t"}))
	if results[0].Attempts != 3 || results[1].Err != nil || upCalls.Load() != 1 {
		t.Fatalf("results %+v", results)
	}
	if upDone > 200*time.Millisecond {
		t.Errorf("the healthy output was done after %s, behind the retrying one", upDone)
	}
}

type notifierFunc func(context.Context, models.CaughtMessage) error

func (f notifierFunc) Send(ctx context.Context, m models.CaughtMessage) error { return f(ctx, m) }
