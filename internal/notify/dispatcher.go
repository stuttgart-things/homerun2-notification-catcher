package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
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
}

type boundOutput struct {
	name     string
	filters  config.OutputFilters
	notifier Notifier
}

// DispatchResult records what happened for one output for one message. Used by
// the smoke subcommand and may be useful for tests / future metrics.
type DispatchResult struct {
	Output  string
	Skipped bool  // filters didn't match
	DryRun  bool  // filters matched but Send was skipped (Dispatcher.DryRun=true)
	Err     error // nil on success/skipped/dry-run, non-nil if Send failed
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
		})
	}
	return &Dispatcher{outputs: outputs}, nil
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
		})
	}
	return &Dispatcher{outputs: outputs}
}

// DispatcherEntry is the unit accepted by NewDispatcherWithNotifiers.
type DispatcherEntry struct {
	Name     string
	Filters  config.OutputFilters
	Notifier Notifier
}

// Dispatch sends msg to every output whose filters match. The call is
// synchronous: each Notifier.Send is invoked in order, and per-output failures
// are logged but do not abort the rest. The returned slice mirrors the output
// order so callers can map results back to config entries.
//
// When Dispatcher.DryRun is true, matching outputs skip the Notifier.Send call
// and log at INFO instead. Skipped outputs (filters didn't match) are reported
// the same way in both modes.
func (d *Dispatcher) Dispatch(ctx context.Context, msg homerun.Message) []DispatchResult {
	results := make([]DispatchResult, 0, len(d.outputs))
	for _, o := range d.outputs {
		if !Matches(o.filters, msg) {
			results = append(results, DispatchResult{Output: o.name, Skipped: true})
			continue
		}
		if d.DryRun {
			slog.Info("dry-run: would send",
				"output", o.name,
				"title", msg.Title,
				"severity", msg.Severity,
				"system", msg.System,
			)
			results = append(results, DispatchResult{Output: o.name, DryRun: true})
			continue
		}
		err := o.notifier.Send(ctx, msg)
		if err != nil {
			slog.Error("notifier failed", "output", o.name, "title", msg.Title, "error", err)
		} else {
			slog.Debug("notifier sent", "output", o.name, "title", msg.Title)
		}
		results = append(results, DispatchResult{Output: o.name, Err: err})
	}
	return results
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
		return NewTeamsNotifier(o.WebhookURL, client), nil
	case config.OutputTypeWebhook:
		return NewWebhookNotifier(o.URL, o.Method, o.Headers, client), nil
	default:
		return nil, fmt.Errorf("unknown output type %q", o.Type)
	}
}
