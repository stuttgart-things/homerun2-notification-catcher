package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// NotifyConfig is the root document for routing configuration.
//
// Loaded once at startup from CONFIG_PATH (default
// /etc/notification-catcher/config.yaml). A missing or unparsable file is a
// fatal startup error — silent misconfig is the worst failure mode for a
// dispatch service.
type NotifyConfig struct {
	Outputs []OutputConfig `yaml:"outputs"`
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
}

// OutputFilters narrows which messages reach an output. Empty filters → match
// every message. Multiple filter fields combine with AND; list fields combine
// with OR internally.
type OutputFilters struct {
	SeverityMin     string            `yaml:"severity_min,omitempty"`
	Match           map[string]string `yaml:"match,omitempty"`
	TagsContain     []string          `yaml:"tags_contain,omitempty"`
	MessageContains []string          `yaml:"message_contains,omitempty"`
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
var matchFields = map[string]struct{}{
	"severity":        {},
	"system":          {},
	"author":          {},
	"title":           {},
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
