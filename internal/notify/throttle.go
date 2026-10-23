package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// throttleTopKeys is how many keys a throttle summary names.
const throttleTopKeys = 5

// throttle is one output's throttle state (#79): when each key was last
// sent, and the posts of the current window. In memory, per pod, like the
// event log; a restart forgets it.
type throttle struct {
	policy config.ThrottlePolicy

	mu       sync.Mutex
	sent     map[string]time.Time // key -> last sent
	start    time.Time            // current window; zero: none open
	posts    int                  // sent in the window
	held     int                  // held back in the window
	capped   bool                 // max was reached in the window
	heldKeys map[string]int
	pending  []throttleSummary // closed capped windows, not yet sent
}

// throttleSummary is the one post an output sends for a window in which it
// reached max: how many messages it held back and the most frequent keys.
type throttleSummary struct {
	From, To time.Time
	Held     int
	Max      int
	Window   time.Duration
	Top      []string // "key (count)", most frequent first
}

func newThrottle(p config.ThrottlePolicy) *throttle {
	return &throttle{policy: p, sent: map[string]time.Time{}, heldKeys: map[string]int{}}
}

// key joins the policy's key fields of msg.
func (t *throttle) key(msg homerun.Message) string {
	parts := make([]string, len(t.policy.Key))
	for i, f := range t.policy.Key {
		parts[i] = strings.TrimSpace(getField(msg, f))
	}
	return strings.Join(parts, " / ")
}

// admit reports whether msg may be sent at now. A key sent within the window
// is held back, and so is every message once max posts went out in the
// current window. A resolved alert always passes and is not counted; it
// clears its key, so the next firing one is news again.
func (t *throttle) admit(msg homerun.Message, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.roll(now)
	key := t.key(msg)
	if AlertStatus(msg) == StatusResolved {
		delete(t.sent, key)
		return true
	}
	if t.start.IsZero() {
		t.start = now
	}
	if last, ok := t.sent[key]; ok && now.Sub(last) < t.policy.Window {
		t.hold(key)
		return false
	}
	if t.policy.Max > 0 && t.posts >= t.policy.Max {
		t.capped = true
		t.hold(key)
		return false
	}
	t.posts++
	t.sent[key] = now
	return true
}

func (t *throttle) hold(key string) {
	t.held++
	t.heldKeys[key]++
}

// roll closes the current window once it is over: a capped one becomes a
// pending summary, and keys sent before the window are forgotten.
func (t *throttle) roll(now time.Time) {
	if t.start.IsZero() || now.Sub(t.start) < t.policy.Window {
		return
	}
	if t.capped {
		t.pending = append(t.pending, throttleSummary{
			From: t.start, To: t.start.Add(t.policy.Window),
			Held: t.held, Max: t.policy.Max, Window: t.policy.Window,
			Top: topKeys(t.heldKeys, throttleTopKeys),
		})
	}
	t.start, t.posts, t.held, t.capped = time.Time{}, 0, 0, false
	t.heldKeys = map[string]int{}
	for k, at := range t.sent {
		if now.Sub(at) >= t.policy.Window {
			delete(t.sent, k)
		}
	}
}

// due closes a window that is over and returns the summaries to send.
func (t *throttle) due(now time.Time) []throttleSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.roll(now)
	out := t.pending
	t.pending = nil
	return out
}

// topKeys lists the n most frequent keys as "key (count)".
func topKeys(counts map[string]int, n int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = fmt.Sprintf("%s (%d)", k, counts[k])
	}
	return out
}

// message is the summary as the output's notifier takes it.
func (s throttleSummary) message(output string) homerun.Message {
	body := fmt.Sprintf("%s reached its limit of %d posts per %s between %s and %s and held back %d notifications.",
		output, s.Max, s.Window, s.From.UTC().Format(time.RFC3339), s.To.UTC().Format(time.RFC3339), s.Held)
	if len(s.Top) > 0 {
		body += " Most frequent: " + strings.Join(s.Top, ", ") + "."
	}
	return homerun.Message{
		Title:     fmt.Sprintf("%d notifications held back (throttle)", s.Held),
		Message:   body,
		Severity:  "warning",
		System:    "homerun2-notification-catcher",
		Author:    "notification-catcher",
		Tags:      "throttle",
		Timestamp: s.To.UTC().Format(time.RFC3339),
	}
}

// Throttled reports whether any output has a throttle, i.e. whether
// RunThrottle has anything to do.
func (d *Dispatcher) Throttled() bool {
	for _, o := range d.outputs {
		if o.throttle != nil {
			return true
		}
	}
	return false
}

// FlushThrottle sends the summary of every throttle window that is over and
// in which its output reached max (#79), with the output's retry policy. A
// summary that fails is logged, not parked; with DryRun it is only logged.
func (d *Dispatcher) FlushThrottle(ctx context.Context) {
	for _, o := range d.outputs {
		if o.throttle == nil {
			continue
		}
		for _, s := range o.throttle.due(d.clock()) {
			msg := models.CaughtMessage{Message: s.message(o.name)}
			attrs := []any{"output", o.name, "held", s.Held, "from", s.From, "top", s.Top}
			if d.DryRun {
				slog.Info("dry-run: would send throttle summary", attrs...)
				continue
			}
			attempts, err := d.send(ctx, o, msg)
			if err != nil {
				slog.Error("throttle summary failed", append(attrs, "attempts", attempts, "error", err)...)
				continue
			}
			slog.Info("throttle summary sent", append(attrs, "attempts", attempts)...)
		}
	}
}

// RunThrottle calls FlushThrottle every interval until ctx is done, so a
// summary goes out shortly after its window ends even when no further
// message arrives.
func (d *Dispatcher) RunThrottle(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fctx, cancel := context.WithTimeout(ctx, every)
			d.FlushThrottle(fctx)
			cancel()
		}
	}
}

// clock is the dispatcher's time source; tests replace now.
func (d *Dispatcher) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}
