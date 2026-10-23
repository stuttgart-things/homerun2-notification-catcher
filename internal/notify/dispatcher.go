package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// Dispatcher fans a homerun.Message out to every configured output whose
// filters match. One Dispatcher is built per process from NotifyConfig and is
// safe for concurrent calls — each underlying Notifier is too.
//
// DryRun, when true, makes Dispatch log "would have sent" at INFO and return
// DispatchResult{DryRun: true, Err: nil} for matching outputs *without* invoking
// Notifier.Send. Filter evaluation runs as normal so skipped outputs still
// report Skipped: true. Useful for the first reconciliation after a config
// change — flip DRY_RUN=true, watch logs, confirm routing, then flip back.
type Dispatcher struct {
	outputs []boundOutput
	DryRun  bool
	// OnDispatch, when set, gets every dispatched message with the result
	// of each output -- the HTTP UI's event log and metrics (#22, #49).
	// Called synchronously at the end of Dispatch; keep it cheap.
	OnDispatch func(Record)
	// DeadLetter, when set, gets each send that still fails after its
	// retries (#68). Nil: the failure is only logged.
	DeadLetter DeadLetter
	// NoThrottle skips every output's throttle (#79): smoke sends what it
	// is asked to.
	NoThrottle bool

	now      func() time.Time // nil: time.Now; tests set it
	stop     chan struct{}
	stopOnce sync.Once
}

// Results of one output for one message, as logged and counted.
const (
	ResultSent     = "sent"
	ResultFailed   = "failed"
	ResultFiltered = "filtered"
	ResultDryRun   = "dry-run"
	// ResultThrottled: the output's throttle held the message back (#79).
	ResultThrottled = "throttled"
)

// Results lists the result values, for zero-initialised metrics.
var Results = []string{ResultSent, ResultFailed, ResultFiltered, ResultDryRun, ResultThrottled}

// Record is one dispatched message and what each output did with it.
type Record struct {
	Message models.CaughtMessage
	At      time.Time
	Results []DispatchResult
}

type boundOutput struct {
	name     string
	filters  config.OutputFilters
	notifier Notifier
	retry    config.RetryPolicy
	fallback bool
	throttle *throttle // nil: none
}

// DispatchResult records what happened for one output for one message. Used by
// the smoke subcommand and may be useful for tests / future metrics.
type DispatchResult struct {
	Output  string
	Skipped bool // filters didn't match, or Covered
	// Covered: a fallback output whose filters matched, left out because a
	// non-fallback output matched the message (#74). Skipped is set too.
	Covered bool
	DryRun  bool // filters matched but Send was skipped (Dispatcher.DryRun=true)
	// Throttled: filters matched, the output's throttle held it back (#79).
	Throttled bool
	Err       error // nil on success/skipped/dry-run, non-nil if Send failed
	// Attempts counts the sends, retries included; 0 when filtered or dry-run.
	Attempts int
	// DeadLettered: the failed send was parked in the dead letter.
	DeadLettered bool
	// ParkErr: the failed send could not be parked either (#68).
	ParkErr error
}

// Result names r: sent, failed, filtered, dry-run or throttled.
func (r DispatchResult) Result() string {
	switch {
	case r.Skipped:
		return ResultFiltered
	case r.Throttled:
		return ResultThrottled
	case r.DryRun:
		return ResultDryRun
	case r.Err != nil:
		return ResultFailed
	default:
		return ResultSent
	}
}

// NewDispatcher binds each OutputConfig to a concrete Notifier. httpClient may
// be nil — each Notifier provides its own default with a 10s timeout.
func NewDispatcher(cfg *config.NotifyConfig, httpClient *http.Client) (*Dispatcher, error) {
	if cfg == nil {
		return nil, fmt.Errorf("dispatcher: config is nil")
	}
	outputs := make([]boundOutput, 0, len(cfg.Outputs))
	for _, oc := range cfg.Outputs {
		n, err := buildNotifier(oc, httpClient)
		if err != nil {
			return nil, fmt.Errorf("dispatcher: output %q: %w", oc.Name, err)
		}
		outputs = append(outputs, boundOutput{
			name:     oc.Name,
			filters:  oc.Filters,
			notifier: n,
			retry:    cfg.RetryFor(oc),
			fallback: oc.Fallback,
			throttle: throttleFor(cfg.ThrottleFor(oc)),
		})
	}
	return &Dispatcher{outputs: outputs, stop: make(chan struct{})}, nil
}

// NewDispatcherWithNotifiers is a test-friendly constructor that skips
// notifier construction. Each entry pairs an OutputConfig (for the name and
// filters) with a pre-built Notifier (e.g. a stub).
func NewDispatcherWithNotifiers(entries ...DispatcherEntry) *Dispatcher {
	outputs := make([]boundOutput, 0, len(entries))
	for _, e := range entries {
		outputs = append(outputs, boundOutput{
			name:     e.Name,
			filters:  e.Filters,
			notifier: e.Notifier,
			retry:    e.Retry,
			fallback: e.Fallback,
			throttle: throttleFor(e.Throttle),
		})
	}
	return &Dispatcher{outputs: outputs, stop: make(chan struct{})}
}

// DispatcherEntry is the unit accepted by NewDispatcherWithNotifiers.
type DispatcherEntry struct {
	Name     string
	Filters  config.OutputFilters
	Notifier Notifier
	Retry    config.RetryPolicy // zero: one attempt, no retry
	Fallback bool
	Throttle config.ThrottlePolicy // zero: no throttle
}

func throttleFor(p config.ThrottlePolicy) *throttle {
	if !p.Enabled() {
		return nil
	}
	return newThrottle(p)
}

// Dispatch sends msg to every output whose filters match. The call is
// synchronous: it returns when every output is done. Matching outputs run in
// parallel (#68), so one that retries through an outage does not eat the
// others' share of ctx; messages still go out one at a time, so each channel
// gets them in stream order. Per-output failures are logged but do not abort
// the rest. The returned slice mirrors the output order so callers can map
// results back to config entries.
//
// A transient failure is retried with backoff (the output's retry policy);
// a send that still fails goes to DeadLetter when one is set.
//
// Every delivery is logged at INFO ("notifier sent", with the output's name,
// the message's objectId and the attempts), so `kubectl logs` shows whether a
// message reached e.g. Teams without looking at the channel. When
// Dispatcher.DryRun is true, matching outputs skip the Notifier.Send call and
// log "dry-run: would send" at INFO instead. Skipped outputs (filters didn't
// match) are reported the same way in both modes.
//
// A fallback output takes a matching message only when no non-fallback
// output matched it (#74); otherwise its result is Covered.
//
// An output's throttle (#79) then holds back repeats and messages above its
// max (result throttled, not sent, not dead-lettered), unless NoThrottle.
func (d *Dispatcher) Dispatch(ctx context.Context, msg models.CaughtMessage) []DispatchResult {
	results := make([]DispatchResult, len(d.outputs))
	matched := make([]bool, len(d.outputs))
	routed := false
	for i, o := range d.outputs {
		matched[i] = Matches(o.filters, msg.Message)
		routed = routed || (matched[i] && !o.fallback)
	}
	var wg sync.WaitGroup
	for i, o := range d.outputs {
		if !matched[i] {
			results[i] = DispatchResult{Output: o.name, Skipped: true}
			continue
		}
		if o.fallback && routed {
			results[i] = DispatchResult{Output: o.name, Skipped: true, Covered: true}
			continue
		}
		if o.throttle != nil && !d.NoThrottle && !o.throttle.admit(msg.Message, d.clock()) {
			slog.Debug("notifier throttled", "output", o.name, "objectId", msg.ObjectID, "title", msg.Title)
			results[i] = DispatchResult{Output: o.name, Throttled: true}
			continue
		}
		if d.DryRun {
			slog.Info("dry-run: would send",
				"output", o.name,
				"objectId", msg.ObjectID,
				"title", msg.Title,
				"severity", msg.Severity,
				"system", msg.System,
			)
			results[i] = DispatchResult{Output: o.name, DryRun: true}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = d.deliver(ctx, o, msg)
		}()
	}
	wg.Wait()
	d.summarize(msg, results)
	return results
}

// deliver sends msg to one output, with retries, and parks it on failure.
func (d *Dispatcher) deliver(ctx context.Context, o boundOutput, msg models.CaughtMessage) DispatchResult {
	attempts, err := d.send(ctx, o, msg)
	res := DispatchResult{Output: o.name, Err: err, Attempts: attempts}
	if err == nil {
		slog.Info("notifier sent",
			"output", o.name,
			"objectId", msg.ObjectID,
			"title", msg.Title,
			"severity", msg.Severity,
			"system", msg.System,
			"attempts", attempts,
		)
		return res
	}
	res.DeadLettered, res.ParkErr = d.park(o, msg, attempts, err)
	slog.Error("notifier failed",
		"output", o.name,
		"objectId", msg.ObjectID,
		"title", msg.Title,
		"attempts", attempts,
		"dead_lettered", res.DeadLettered,
		"error", err,
	)
	return res
}

// summarize logs one line per message -- "dispatched" with each output's
// result, or "no output matched" -- so a log query by objectId shows the
// whole fan-out (#49), and hands the record to OnDispatch.
func (d *Dispatcher) summarize(msg models.CaughtMessage, results []DispatchResult) {
	outputs := make(map[string]string, len(results))
	routed := false
	for _, r := range results {
		outputs[r.Output] = r.Result()
		routed = routed || !r.Skipped
	}
	attrs := []any{
		"objectId", msg.ObjectID,
		"title", msg.Title,
		"severity", msg.Severity,
		"system", msg.System,
	}
	if routed {
		slog.Info("dispatched", append(attrs, "outputs", outputs)...)
	} else {
		slog.Info("no output matched", attrs...)
	}
	if d.OnDispatch != nil {
		d.OnDispatch(Record{Message: msg, At: time.Now().UTC(), Results: results})
	}
}

// OutputNames returns the configured output names, in config order. Used by
// the startup log line so operators can confirm what's loaded.
func (d *Dispatcher) OutputNames() []string {
	names := make([]string, len(d.outputs))
	for i, o := range d.outputs {
		names[i] = o.name
	}
	return names
}

func buildNotifier(o config.OutputConfig, client *http.Client) (Notifier, error) {
	switch o.Type {
	case config.OutputTypeTeams:
		return NewTeamsNotifier(o.WebhookURL, client).WithCard(o.Card), nil
	case config.OutputTypeWebhook:
		return NewWebhookNotifier(o.URL, o.Method, o.Headers, client), nil
	default:
		return nil, fmt.Errorf("unknown output type %q", o.Type)
	}
}
