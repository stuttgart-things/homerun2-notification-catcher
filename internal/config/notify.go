package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// NotifyConfig is the root document for routing configuration.
//
// Loaded once at startup from CONFIG_PATH (default
// /etc/notification-catcher/config.yaml). A missing or unparsable file is a
// fatal startup error — silent misconfig is the worst failure mode for a
// dispatch service.
type NotifyConfig struct {
	// Retry is the default retry policy of every output (#68).
	Retry RetryConfig `yaml:"retry,omitempty"`
	// Throttle is the default throttle of every output (#79); unset: none.
	Throttle ThrottleConfig `yaml:"throttle,omitempty"`
	Outputs  []OutputConfig `yaml:"outputs"`
}

// RetryConfig is how often an output retries a failed send. Unset fields
// fall back to the top-level retry, then to DefaultRetryAttempts and
// DefaultRetryBackoff.
type RetryConfig struct {
	// Attempts counts every send, the first one included: 1 never retries.
	Attempts int `yaml:"attempts,omitempty"`
	// Backoff is the first wait (Go duration); each further wait doubles,
	// up to MaxRetryBackoff.
	Backoff string `yaml:"backoff,omitempty"`
}

// Retry defaults and bounds. The whole fan-out of a message has to finish
// inside the catcher's dispatch budget (30s), so retries stay short.
const (
	DefaultRetryAttempts = 3
	DefaultRetryBackoff  = time.Second
	MaxRetryAttempts     = 10
	MaxRetryBackoff      = 10 * time.Second
)

// RetryPolicy is a resolved RetryConfig.
type RetryPolicy struct {
	Attempts int
	Backoff  time.Duration
}

// RetryFor resolves the retry policy of o: its own fields, then cfg.Retry,
// then the defaults. Validate has checked the values.
func (cfg *NotifyConfig) RetryFor(o OutputConfig) RetryPolicy {
	p := RetryPolicy{Attempts: DefaultRetryAttempts, Backoff: DefaultRetryBackoff}
	for _, r := range []RetryConfig{cfg.Retry, o.Retry} {
		if r.Attempts > 0 {
			p.Attempts = r.Attempts
		}
		if d, err := time.ParseDuration(strings.TrimSpace(r.Backoff)); err == nil {
			p.Backoff = d
		}
	}
	return p
}

// ThrottleConfig caps what an output posts (#79): a message whose key was
// sent within Window is not sent again, and at most Max messages go out per
// Window. Unset fields fall back to the top-level throttle; no Window at
// either level (or "0") means no throttle.
type ThrottleConfig struct {
	// Key lists the message fields that make two messages "the same"
	// (throttleKeyFields); unset: DefaultThrottleKey.
	Key []string `yaml:"key,omitempty"`
	// Window is a Go duration: how long a key is held back after it was
	// sent, and the period Max counts over.
	Window string `yaml:"window,omitempty"`
	// Max caps the posts per Window; 0: no cap, repeats are still collapsed.
	Max int `yaml:"max,omitempty"`
}

// DefaultThrottleKey is the throttle key when none is configured.
var DefaultThrottleKey = []string{"author", "system", "title"}

// ThrottlePolicy is a resolved ThrottleConfig; Window 0 is no throttle.
type ThrottlePolicy struct {
	Key    []string
	Window time.Duration
	Max    int
}

// Enabled reports whether p throttles at all.
func (p ThrottlePolicy) Enabled() bool { return p.Window > 0 }

// ThrottleFor resolves the throttle of o: its own fields, then cfg.Throttle.
// Validate has checked the values.
func (cfg *NotifyConfig) ThrottleFor(o OutputConfig) ThrottlePolicy {
	p := ThrottlePolicy{Key: DefaultThrottleKey}
	for _, t := range []ThrottleConfig{cfg.Throttle, o.Throttle} {
		if len(t.Key) > 0 {
			p.Key = t.Key
		}
		if d, err := time.ParseDuration(strings.TrimSpace(t.Window)); err == nil {
			p.Window = d
		}
		if t.Max > 0 {
			p.Max = t.Max
		}
	}
	if p.Window <= 0 {
		return ThrottlePolicy{}
	}
	return p
}

// throttleKeyFields lists the message fields a throttle key can use. status
// is the alert state (notify.AlertStatus), as in filters.match.
var throttleKeyFields = map[string]struct{}{
	"severity":        {},
	"system":          {},
	"author":          {},
	"title":           {},
	"message":         {},
	"tags":            {},
	"url":             {},
	"assigneename":    {},
	"assigneeaddress": {},
	"status":          {},
}

// OutputConfig is one notification sink — a Teams channel, a generic webhook,
// and (Phase 3) email, Slack, etc.
type OutputConfig struct {
	Name       string            `yaml:"name"`
	Type       string            `yaml:"type"`
	WebhookURL string            `yaml:"webhook_url,omitempty"`
	URL        string            `yaml:"url,omitempty"`
	Method     string            `yaml:"method,omitempty"`
	Headers    map[string]string `yaml:"headers,omitempty"`
	Filters    OutputFilters     `yaml:"filters,omitempty"`
	// Card shapes the Adaptive Card of an msteams output. Unset: the default
	// card (title, message, a FactSet of severity/system/author/tags/time).
	Card CardConfig `yaml:"card,omitempty"`
	// Retry overrides the top-level retry policy for this output.
	Retry RetryConfig `yaml:"retry,omitempty"`
	// Throttle overrides the top-level throttle for this output; window: 0
	// turns an inherited one off.
	Throttle ThrottleConfig `yaml:"throttle,omitempty"`
	// Fallback makes the output a catch-all: it sends a message that passes
	// its filters only when no other, non-fallback output matched it --
	// "every error nobody else routed" without repeating each route (#74).
	Fallback bool `yaml:"fallback,omitempty"`
}

// CardConfig customises the Teams card. Facts, when set, replaces the default
// FactSet row for row -- a producer that packs its data into key=value tags
// (zaehlwerk's player_a=..., sets=...) gets one readable row per value instead
// of the raw tag string.
type CardConfig struct {
	Facts []CardFactConfig `yaml:"facts,omitempty"`
}

// CardFactConfig is one FactSet row: Title, and the value from exactly one of
// Tag (the value of `<tag>=` in the message's comma-separated tags) or Field
// (a message field, see cardFields). A row whose value is empty is left out.
type CardFactConfig struct {
	Title string `yaml:"title"`
	Tag   string `yaml:"tag,omitempty"`
	Field string `yaml:"field,omitempty"`
}

// OutputFilters narrows which messages reach an output. Empty filters → match
// every message. Multiple filter fields combine with AND; list fields combine
// with OR internally.
type OutputFilters struct {
	SeverityMin string            `yaml:"severity_min,omitempty"`
	Match       map[string]string `yaml:"match,omitempty"`
	TagsContain []string          `yaml:"tags_contain,omitempty"`
	// TagsExclude drops a message when any listed substring appears in its
	// tags -- e.g. [demo=1] keeps demo matches out of a results channel.
	TagsExclude     []string `yaml:"tags_exclude,omitempty"`
	MessageContains []string `yaml:"message_contains,omitempty"`
	// SendResolved false drops resolved alerts, as Alertmanager's
	// send_resolved does (#71). Unset: they pass.
	SendResolved *bool `yaml:"send_resolved,omitempty"`
}

// Recognised output types. Centralised so validation and the dispatcher agree.
const (
	OutputTypeTeams   = "msteams"
	OutputTypeWebhook = "webhook"
)

// KnownSeverities enumerates the severity values the catcher understands,
// in ascending rank order. The slice is exported so docs / smoke help can
// surface it.
var KnownSeverities = []string{"debug", "info", "success", "warning", "critical", "error"}

// matchFields lists the homerun.Message field names that the `match:` map
// supports. Comparison is case-insensitive on the key. Tags and Message are
// handled by their own `*_contains` filters (substring rather than exact).
// status is not a field: the alert state, firing or resolved, read from the
// message (notify.AlertStatus, #71).
var matchFields = map[string]struct{}{
	"status":          {},
	"severity":        {},
	"system":          {},
	"author":          {},
	"title":           {},
	"assigneename":    {},
	"assigneeaddress": {},
}

// cardFields lists the homerun.Message field names a card fact can show.
var cardFields = map[string]struct{}{
	"severity":        {},
	"system":          {},
	"author":          {},
	"title":           {},
	"message":         {},
	"tags":            {},
	"timestamp":       {},
	"url":             {},
	"assigneename":    {},
	"assigneeaddress": {},
}

// LoadNotifyConfig reads, interpolates, and validates the YAML at path.
//
// Errors include the file path so the operator can find the problem. ${VAR}
// references in scalar values must resolve to a non-empty env value or load
// fails. Comments are not interpolated — documenting an optional secret like
// `# or set ${ALT_VAR} for a dedicated webhook` is safe even when ALT_VAR is
// unset.
func LoadNotifyConfig(path string) (*NotifyConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("notify config: read %s: %w", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("notify config: parse %s: %w", path, err)
	}

	if err := interpolateNode(&root); err != nil {
		return nil, fmt.Errorf("notify config: %s: %w", path, err)
	}

	var cfg NotifyConfig
	if err := root.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("notify config: decode %s: %w", path, err)
	}

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("notify config: %s: %w", path, err)
	}
	return &cfg, nil
}

// LoadNotifyConfigPath resolves the config file path from CONFIG_PATH (with
// the documented default) and delegates to LoadNotifyConfig.
func LoadNotifyConfigPath() (*NotifyConfig, error) {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "/etc/notification-catcher/config.yaml"
	}
	return LoadNotifyConfig(path)
}

var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// interpolateNode walks the YAML node tree in place and replaces ${VAR}
// placeholders in scalar values with os.Getenv(VAR). Comments live on
// Node.{Head,Line,Foot}Comment fields and are never visited, so documentation
// like `# also set ${ALT_VAR}` is inert — earlier raw-text interpolation
// would have trapped on it.
//
// Missing or empty env values are collected and returned in a single
// deduplicated error so operators see every misconfig at once instead of
// fixing one var per restart.
func interpolateNode(root *yaml.Node) error {
	missing := map[string]struct{}{}
	walkAndInterpolate(root, missing)
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for n := range missing {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Errorf("unresolved env vars: %s", strings.Join(names, ", "))
}

func walkAndInterpolate(n *yaml.Node, missing map[string]struct{}) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode {
		n.Value = envRefPattern.ReplaceAllStringFunc(n.Value, func(match string) string {
			name := envRefPattern.FindStringSubmatch(match)[1]
			v, ok := os.LookupEnv(name)
			if !ok || v == "" {
				missing[name] = struct{}{}
				return match
			}
			return v
		})
		return
	}
	for _, c := range n.Content {
		walkAndInterpolate(c, missing)
	}
}

// Validate enforces invariants the dispatcher relies on. Errors collect
// per-output context so operators can fix without trial and error.
func Validate(cfg *NotifyConfig) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := validateRetry("retry", cfg.Retry); err != nil {
		return err
	}
	if err := validateThrottle("throttle", cfg.Throttle, ""); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for i, o := range cfg.Outputs {
		prefix := fmt.Sprintf("output[%d]", i)
		if o.Name != "" {
			prefix = fmt.Sprintf("output[%d] %q", i, o.Name)
		}
		if o.Name == "" {
			return fmt.Errorf("%s: name is required", prefix)
		}
		if _, dup := seen[o.Name]; dup {
			return fmt.Errorf("%s: duplicate name", prefix)
		}
		seen[o.Name] = struct{}{}

		switch o.Type {
		case OutputTypeTeams:
			if strings.TrimSpace(o.WebhookURL) == "" {
				return fmt.Errorf("%s: webhook_url is required for type %q", prefix, o.Type)
			}
		case OutputTypeWebhook:
			if strings.TrimSpace(o.URL) == "" {
				return fmt.Errorf("%s: url is required for type %q", prefix, o.Type)
			}
		case "":
			return fmt.Errorf("%s: type is required", prefix)
		default:
			return fmt.Errorf("%s: unknown type %q (known: %s, %s)", prefix, o.Type, OutputTypeTeams, OutputTypeWebhook)
		}

		if s := strings.TrimSpace(o.Filters.SeverityMin); s != "" {
			if _, ok := severityRank(s); !ok {
				return fmt.Errorf("%s: filters.severity_min %q is not one of %s", prefix, s, strings.Join(KnownSeverities, "|"))
			}
		}
		for k := range o.Filters.Match {
			if _, ok := matchFields[strings.ToLower(k)]; !ok {
				return fmt.Errorf("%s: filters.match key %q is not a recognised field", prefix, k)
			}
		}
		if err := validateRetry(prefix+": retry", o.Retry); err != nil {
			return err
		}
		if err := validateThrottle(prefix+": throttle", o.Throttle, cfg.Throttle.Window); err != nil {
			return err
		}
		if len(o.Card.Facts) > 0 && o.Type != OutputTypeTeams {
			return fmt.Errorf("%s: card is only supported for type %q", prefix, OutputTypeTeams)
		}
		for j, f := range o.Card.Facts {
			fp := fmt.Sprintf("%s: card.facts[%d]", prefix, j)
			if strings.TrimSpace(f.Title) == "" {
				return fmt.Errorf("%s: title is required", fp)
			}
			tag, field := strings.TrimSpace(f.Tag), strings.TrimSpace(f.Field)
			if (tag == "") == (field == "") {
				return fmt.Errorf("%s: set exactly one of tag or field", fp)
			}
			if field != "" {
				if _, ok := cardFields[strings.ToLower(field)]; !ok {
					return fmt.Errorf("%s: field %q is not a recognised field", fp, field)
				}
			}
		}
	}
	return nil
}

func validateRetry(prefix string, r RetryConfig) error {
	if r.Attempts < 0 || r.Attempts > MaxRetryAttempts {
		return fmt.Errorf("%s.attempts %d: must be 1..%d", prefix, r.Attempts, MaxRetryAttempts)
	}
	if b := strings.TrimSpace(r.Backoff); b != "" {
		d, err := time.ParseDuration(b)
		if err != nil || d < 0 || d > MaxRetryBackoff {
			return fmt.Errorf("%s.backoff %q: must be a Go duration from 0 to %s", prefix, b, MaxRetryBackoff)
		}
	}
	return nil
}

// validateThrottle checks one throttle block; inherited is the window it
// falls back to, so key or max without any window is an error.
func validateThrottle(prefix string, t ThrottleConfig, inherited string) error {
	for _, k := range t.Key {
		if _, ok := throttleKeyFields[strings.ToLower(strings.TrimSpace(k))]; !ok {
			return fmt.Errorf("%s.key %q is not a recognised field", prefix, k)
		}
	}
	if t.Max < 0 {
		return fmt.Errorf("%s.max %d: must not be negative", prefix, t.Max)
	}
	w := strings.TrimSpace(t.Window)
	if w != "" {
		if d, err := time.ParseDuration(w); err != nil || d < 0 {
			return fmt.Errorf("%s.window %q: must be a Go duration, 0 for none", prefix, w)
		}
	}
	if w == "" && strings.TrimSpace(inherited) == "" && (len(t.Key) > 0 || t.Max > 0) {
		return fmt.Errorf("%s: window is required with key or max", prefix)
	}
	return nil
}

// severityRank returns the ordinal rank for a known severity (case-insensitive)
// and false for anything else. Exported helpers in the notify package wrap this
// for filtering — kept here so config validation and routing share one table.
func severityRank(s string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return 0, true
	case "info":
		return 1, true
	case "success":
		return 2, true
	case "warning":
		return 3, true
	case "critical", "error":
		return 4, true
	default:
		return 0, false
	}
}

// SeverityRank is the public accessor for callers in the notify package.
func SeverityRank(s string) (int, bool) { return severityRank(s) }
