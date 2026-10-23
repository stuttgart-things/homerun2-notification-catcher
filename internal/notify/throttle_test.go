package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// fakeClock is the injectable time of the throttle tests: no sleeps.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// recordingNotifier keeps every message it was handed.
type recordingNotifier struct{ msgs []homerun.Message }

func (r *recordingNotifier) Send(_ context.Context, msg models.CaughtMessage) error {
	r.msgs = append(r.msgs, msg.Message)
	return nil
}

func throttled(t *testing.T, policy config.ThrottlePolicy) (*Dispatcher, *recordingNotifier, *fakeClock) {
	t.Helper()
	n := &recordingNotifier{}
	d := NewDispatcherWithNotifiers(DispatcherEntry{Name: "teams", Notifier: n, Throttle: policy})
	clk := &fakeClock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	d.now = clk.now
	return d, n, clk
}

func alert(title string) models.CaughtMessage {
	return caught(homerun.Message{Title: title, System: "k8s", Author: "k8s-pitcher", Severity: "warning"})
}

func TestThrottle_CollapsesRepeatsWithinWindow(t *testing.T) {
	d, n, clk := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour})
	ctx := context.Background()

	if r := d.Dispatch(ctx, alert("pod crashlooping"))[0].Result(); r != ResultSent {
		t.Fatalf("first: %s, want sent", r)
	}
	clk.advance(30 * time.Minute)
	if r := d.Dispatch(ctx, alert("pod crashlooping"))[0].Result(); r != ResultThrottled {
		t.Fatalf("repeat inside window: %s, want throttled", r)
	}
	if r := d.Dispatch(ctx, alert("node not ready"))[0].Result(); r != ResultSent {
		t.Fatalf("other key: %s, want sent", r)
	}
	clk.advance(31 * time.Minute)
	if r := d.Dispatch(ctx, alert("pod crashlooping"))[0].Result(); r != ResultSent {
		t.Fatalf("repeat after window: %s, want sent", r)
	}
	if len(n.msgs) != 3 {
		t.Errorf("posts = %d, want 3", len(n.msgs))
	}
}

func TestThrottle_KeyFields(t *testing.T) {
	// Keyed by title alone: the same title from another system is a repeat.
	d, _, _ := throttled(t, config.ThrottlePolicy{Key: []string{"title"}, Window: time.Hour})
	ctx := context.Background()
	d.Dispatch(ctx, alert("disk full"))
	other := alert("disk full")
	other.System = "storage"
	if r := d.Dispatch(ctx, other)[0].Result(); r != ResultThrottled {
		t.Errorf("same title, other system: %s, want throttled", r)
	}
}

func TestThrottle_MaxPerWindowAndSummary(t *testing.T) {
	d, n, clk := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour, Max: 2})
	ctx := context.Background()

	var got []string
	for _, title := range []string{"a", "b", "c", "d", "c", "e"} {
		got = append(got, d.Dispatch(ctx, alert(title))[0].Result())
		clk.advance(time.Minute)
	}
	want := []string{ResultSent, ResultSent, ResultThrottled, ResultThrottled, ResultThrottled, ResultThrottled}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("results = %v, want %v", got, want)
	}

	// No summary while the window is open.
	d.FlushThrottle(ctx)
	if len(n.msgs) != 2 {
		t.Fatalf("posts before window end = %d, want 2", len(n.msgs))
	}

	clk.advance(time.Hour)
	d.FlushThrottle(ctx)
	if len(n.msgs) != 3 {
		t.Fatalf("posts after window end = %d, want 3 (2 + summary)", len(n.msgs))
	}
	s := n.msgs[2]
	if s.Title != "4 notifications held back (throttle)" {
		t.Errorf("summary title = %q", s.Title)
	}
	if !strings.Contains(s.Message, "Most frequent: k8s-pitcher / k8s / c (2)") {
		t.Errorf("summary message lacks the top key: %q", s.Message)
	}

	// Exactly one summary per window.
	d.FlushThrottle(ctx)
	if len(n.msgs) != 3 {
		t.Errorf("second flush sent again: %d posts", len(n.msgs))
	}

	// A new window: posts go out again.
	if r := d.Dispatch(ctx, alert("f"))[0].Result(); r != ResultSent {
		t.Errorf("new window: %s, want sent", r)
	}
}

func TestThrottle_NoSummaryWithoutCap(t *testing.T) {
	// Repeats only: collapsed silently, no summary.
	d, n, clk := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour, Max: 5})
	ctx := context.Background()
	d.Dispatch(ctx, alert("a"))
	d.Dispatch(ctx, alert("a"))
	clk.advance(2 * time.Hour)
	d.FlushThrottle(ctx)
	if len(n.msgs) != 1 {
		t.Errorf("posts = %d, want 1", len(n.msgs))
	}
}

func TestThrottle_ResolvedBypasses(t *testing.T) {
	d, n, clk := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour, Max: 1})
	ctx := context.Background()

	firing := alert("pod crashlooping")
	firing.Tags = "status=firing"
	resolved := alert("pod crashlooping")
	resolved.Tags = "status=resolved"

	d.Dispatch(ctx, firing)
	clk.advance(time.Minute)
	if r := d.Dispatch(ctx, resolved)[0].Result(); r != ResultSent {
		t.Fatalf("resolved after firing: %s, want sent", r)
	}
	// Grafana's prefix counts as resolved too, and max does not apply.
	g := alert("other")
	g.Message.Message = "Resolved: all good"
	if r := d.Dispatch(ctx, g)[0].Result(); r != ResultSent {
		t.Errorf("resolved above max: %s, want sent", r)
	}
	// The resolved one cleared the key; max (1) is still used up.
	if r := d.Dispatch(ctx, firing)[0].Result(); r != ResultThrottled {
		t.Errorf("firing above max: %s, want throttled", r)
	}
	if len(n.msgs) != 3 {
		t.Errorf("posts = %d, want 3", len(n.msgs))
	}
}

func TestThrottle_ResolvedClearsKey(t *testing.T) {
	d, _, _ := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour})
	ctx := context.Background()
	firing := alert("x")
	firing.Tags = "status=firing"
	resolved := alert("x")
	resolved.Tags = "status=resolved"
	d.Dispatch(ctx, firing)
	d.Dispatch(ctx, resolved)
	if r := d.Dispatch(ctx, firing)[0].Result(); r != ResultSent {
		t.Errorf("firing again after resolved: %s, want sent", r)
	}
}

func TestThrottle_SmokeAndReplayBypass(t *testing.T) {
	d, n, _ := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour, Max: 1})
	ctx := context.Background()
	d.Dispatch(ctx, alert("a"))

	// replay: SendTo does not consult the throttle.
	if _, err := d.SendTo(ctx, "teams", alert("a")); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
	// smoke: NoThrottle.
	d.NoThrottle = true
	if r := d.Dispatch(ctx, alert("a"))[0].Result(); r != ResultSent {
		t.Errorf("smoke: %s, want sent", r)
	}
	if len(n.msgs) != 3 {
		t.Errorf("posts = %d, want 3", len(n.msgs))
	}
}

func TestThrottle_PerOutput(t *testing.T) {
	limited, free := &recordingNotifier{}, &recordingNotifier{}
	d := NewDispatcherWithNotifiers(
		DispatcherEntry{Name: "limited", Notifier: limited,
			Throttle: config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour}},
		DispatcherEntry{Name: "free", Notifier: free},
	)
	clk := &fakeClock{t: time.Unix(0, 0)}
	d.now = clk.now
	var records []Record
	d.OnDispatch = func(r Record) { records = append(records, r) }

	d.Dispatch(context.Background(), alert("a"))
	res := d.Dispatch(context.Background(), alert("a"))
	if res[0].Result() != ResultThrottled || res[1].Result() != ResultSent {
		t.Errorf("results = %s/%s, want throttled/sent", res[0].Result(), res[1].Result())
	}
	if len(limited.msgs) != 1 || len(free.msgs) != 2 {
		t.Errorf("posts limited=%d free=%d, want 1/2", len(limited.msgs), len(free.msgs))
	}
	if Undelivered(res) != nil {
		t.Error("a throttled message must be acked")
	}
	if got := records[1].Results[0].Result(); got != ResultThrottled {
		t.Errorf("OnDispatch result = %s, want throttled", got)
	}
}

func TestThrottle_DryRunSummaryNotSent(t *testing.T) {
	d, n, clk := throttled(t, config.ThrottlePolicy{Key: config.DefaultThrottleKey, Window: time.Hour, Max: 1})
	d.DryRun = true
	ctx := context.Background()
	if r := d.Dispatch(ctx, alert("a"))[0].Result(); r != ResultDryRun {
		t.Fatalf("first: %s, want dry-run", r)
	}
	if r := d.Dispatch(ctx, alert("b"))[0].Result(); r != ResultThrottled {
		t.Fatalf("second: %s, want throttled", r)
	}
	clk.advance(time.Hour)
	d.FlushThrottle(ctx)
	if len(n.msgs) != 0 {
		t.Errorf("dry-run sent %d posts", len(n.msgs))
	}
}

func TestNewDispatcher_ThrottleFromConfig(t *testing.T) {
	cfg := &config.NotifyConfig{
		Throttle: config.ThrottleConfig{Window: "1h", Max: 20},
		Outputs: []config.OutputConfig{
			{Name: "a", Type: config.OutputTypeWebhook, URL: "http://x"},
			{Name: "b", Type: config.OutputTypeWebhook, URL: "http://x", Throttle: config.ThrottleConfig{Window: "0"}},
		},
	}
	d, err := NewDispatcher(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.outputs[0].throttle == nil || d.outputs[0].throttle.policy.Max != 20 {
		t.Errorf("output a: throttle %+v, want max 20", d.outputs[0].throttle)
	}
	if d.outputs[1].throttle != nil {
		t.Error("output b: window 0 should turn the throttle off")
	}
	if !d.Throttled() {
		t.Error("Throttled() = false")
	}
}
