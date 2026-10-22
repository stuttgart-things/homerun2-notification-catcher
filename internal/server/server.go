// Package server is the catcher's optional, read-only HTTP surface (#22):
// a health check for the readiness probe, a page (plus JSON) showing the
// loaded routing config with secrets redacted, the event log of the last
// dispatched messages with each output's result, and their counters as
// Prometheus metrics (#49). Off unless HTTP_ADDR is set;
// the catcher stays a pure consumer otherwise.
package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/webui"
)

// pingTimeout bounds the Redis check per health request; the kubelet's
// probe timeout is 1s by default.
const pingTimeout = 800 * time.Millisecond

// redacted replaces secret values (query values, header values).
const redacted = "REDACTED"

var (
	//go:embed index.html
	indexSrc  string
	indexTmpl = template.Must(template.New("index").Funcs(template.FuncMap{
		"join":  strings.Join,
		"qs":    queryString,
		"lower": strings.ToLower,
	}).Parse(indexSrc))
)

// BuildInfo is shown in the page footer, as in the other homerun2 UIs.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Options configure a Server.
type Options struct {
	// Token, when set, is required on every route but the health checks and
	// /static/, as "Authorization: Bearer <token>" or "?token=<token>".
	Token string
	// BasePath is the URL prefix behind a reverse proxy; see
	// webui.ParseBasePath. Health stays at the root.
	BasePath string
	Build    BuildInfo

	Config        *config.NotifyConfig
	DryRun        bool
	RedisAddr     string
	Streams       []string
	ConsumerGroup string

	// EventsCapacity is how many dispatched messages the event log keeps;
	// 0 means DefaultEventsCapacity.
	EventsCapacity int
	// SinkURL is the notification sink's UI (incl. its BASE_PATH), when the
	// cluster runs one: each event links to its notification there.
	SinkURL string
	// DeadLetterStream is where failed sends are parked (#68); empty: off.
	DeadLetterStream string
}

// Server serves the health check and the config view.
type Server struct {
	opts Options

	mu   sync.RWMutex
	ping func(context.Context) error

	events *events
}

// New returns a Server. Until SetPing is called (the catcher is connected),
// the health check reports Redis as connecting.
func New(opts Options) *Server {
	var outputs []string
	if opts.Config != nil {
		for _, o := range opts.Config.Outputs {
			outputs = append(outputs, o.Name)
		}
	}
	opts.SinkURL = strings.TrimSuffix(opts.SinkURL, "/")
	return &Server{opts: opts, events: newEvents(opts.EventsCapacity, outputs)}
}

// SetPing sets the Redis check the health endpoint runs.
func (s *Server) SetPing(ping func(context.Context) error) {
	s.mu.Lock()
	s.ping = ping
	s.mu.Unlock()
}

// Handler returns the HTTP routes, mounted under Options.BasePath.
func (s *Server) Handler() http.Handler {
	app := http.NewServeMux()
	app.HandleFunc("GET /api/config", webui.Auth(s.opts.Token, s.handleConfigJSON))
	app.HandleFunc("GET /api/events", webui.Auth(s.opts.Token, s.handleEventsJSON))
	app.HandleFunc("GET /events", webui.Auth(s.opts.Token, s.handleEventsPage))
	app.Handle("GET /static/", webui.Static())
	app.HandleFunc("GET /{$}", webui.Auth(s.opts.Token, s.handleIndex))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	base := s.opts.BasePath
	if base == "" {
		mux.Handle("/", app)
		return mux
	}
	toUI := http.RedirectHandler(base+"/", http.StatusTemporaryRedirect)
	mux.Handle(base+"/", http.StripPrefix(base, app))
	mux.Handle(base, toUI)
	mux.Handle("/{$}", toUI)
	return mux
}

// Health is the /healthz answer: 200 when Redis answers, else 503.
type Health struct {
	Status           string `json:"status"` // ok | unavailable
	Redis            string `json:"redis"`  // ok | connecting | error: …
	DispatcherLoaded bool   `json:"dispatcher_loaded"`
	DryRun           bool   `json:"dry_run"`
	Outputs          int    `json:"outputs"`
}

func (s *Server) health(ctx context.Context) Health {
	h := Health{
		Status:           "ok",
		Redis:            "ok",
		DispatcherLoaded: s.opts.Config != nil,
		DryRun:           s.opts.DryRun,
	}
	if s.opts.Config != nil {
		h.Outputs = len(s.opts.Config.Outputs)
	}
	s.mu.RLock()
	ping := s.ping
	s.mu.RUnlock()
	if ping == nil {
		h.Redis = "connecting"
	} else {
		ctx, cancel := context.WithTimeout(ctx, pingTimeout)
		defer cancel()
		if err := ping(ctx); err != nil {
			h.Redis = "error: " + err.Error()
		}
	}
	if h.Redis != "ok" {
		h.Status = "unavailable"
	}
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	h := s.health(r.Context())
	code := http.StatusOK
	if h.Status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, h)
}

// ConfigView is the loaded routing config as the UI shows it: secrets
// redacted, everything else as loaded.
type ConfigView struct {
	Build         BuildInfo `json:"build"`
	DryRun        bool      `json:"dryRun"`
	RedisAddr     string    `json:"redisAddr"`
	Streams       []string  `json:"streams"`
	ConsumerGroup string    `json:"consumerGroup"`
	// DeadLetterStream is empty when failed sends are only logged.
	DeadLetterStream string       `json:"deadLetterStream"`
	Outputs          []OutputView `json:"outputs"`
}

// OutputView is one output with its target redacted: URL query values and
// userinfo, and every header value, are replaced -- a Teams Workflow URL
// carries its signature in the query, a webhook its token in a header.
type OutputView struct {
	Name      string                  `json:"name"`
	Type      string                  `json:"type"`
	Target    string                  `json:"target"`
	Method    string                  `json:"method,omitempty"`
	Headers   []string                `json:"headers,omitempty"` // "Name: REDACTED"
	Filters   FiltersView             `json:"filters"`
	CardFacts []config.CardFactConfig `json:"cardFacts,omitempty"`
	Retry     RetryView               `json:"retry"`
}

// RetryView is an output's resolved retry policy (#68).
type RetryView struct {
	Attempts int    `json:"attempts"`
	Backoff  string `json:"backoff"`
}

// FiltersView is config.OutputFilters with JSON names.
type FiltersView struct {
	SeverityMin     string            `json:"severityMin,omitempty"`
	Match           map[string]string `json:"match,omitempty"`
	TagsContain     []string          `json:"tagsContain,omitempty"`
	TagsExclude     []string          `json:"tagsExclude,omitempty"`
	SendResolved    *bool             `json:"sendResolved,omitempty"`
	MessageContains []string          `json:"messageContains,omitempty"`
}

// Empty reports whether no filter is set: the output takes every message.
func (f FiltersView) Empty() bool {
	return f.SeverityMin == "" && len(f.Match) == 0 && len(f.TagsContain) == 0 &&
		len(f.TagsExclude) == 0 && len(f.MessageContains) == 0 && f.SendResolved == nil
}

// DropsResolved reports send_resolved: false.
func (f FiltersView) DropsResolved() bool { return f.SendResolved != nil && !*f.SendResolved }

// MatchPairs lists Match as "key=value", sorted, for the page.
func (f FiltersView) MatchPairs() []string {
	out := make([]string, 0, len(f.Match))
	for k, v := range f.Match {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func (s *Server) view() ConfigView {
	v := ConfigView{
		Build:            s.opts.Build,
		DryRun:           s.opts.DryRun,
		RedisAddr:        s.opts.RedisAddr,
		Streams:          s.opts.Streams,
		ConsumerGroup:    s.opts.ConsumerGroup,
		DeadLetterStream: s.opts.DeadLetterStream,
		Outputs:          []OutputView{},
	}
	if s.opts.Config == nil {
		return v
	}
	for _, o := range s.opts.Config.Outputs {
		target := o.URL
		if o.Type == config.OutputTypeTeams {
			target = o.WebhookURL
		}
		ov := OutputView{
			Name:   o.Name,
			Type:   o.Type,
			Target: RedactURL(target),
			Method: o.Method,
			Filters: FiltersView{
				SeverityMin:     o.Filters.SeverityMin,
				Match:           o.Filters.Match,
				TagsContain:     o.Filters.TagsContain,
				TagsExclude:     o.Filters.TagsExclude,
				SendResolved:    o.Filters.SendResolved,
				MessageContains: o.Filters.MessageContains,
			},
			CardFacts: o.Card.Facts,
		}
		rp := s.opts.Config.RetryFor(o)
		ov.Retry = RetryView{Attempts: rp.Attempts, Backoff: rp.Backoff.String()}
		for name := range o.Headers {
			ov.Headers = append(ov.Headers, name+": "+redacted)
		}
		sort.Strings(ov.Headers)
		v.Outputs = append(v.Outputs, ov)
	}
	return v
}

// RedactURL keeps scheme, host, path and the query keys of raw, and replaces
// the query values, any userinfo, and path segments that look like a key
// (16+ characters mixing letters and digits: the legacy Teams connector URL
// carries its secret in the path; a workflow id goes too). An unparsable URL
// is redacted whole.
func RedactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return redacted
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	if u.RawQuery != "" {
		q := u.Query()
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = url.QueryEscape(k) + "=" + redacted
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	if u.Path != "" {
		segs := strings.Split(u.Path, "/")
		for i, seg := range segs {
			if looksLikeKey(seg) {
				segs[i] = redacted
			}
		}
		u.Path, u.RawPath = strings.Join(segs, "/"), ""
	}
	u.Fragment = ""
	return u.String()
}

func looksLikeKey(seg string) bool {
	if len(seg) < 16 {
		return false
	}
	return strings.ContainsAny(seg, "0123456789") &&
		strings.ContainsAny(strings.ToLower(seg), "abcdefghijklmnopqrstuvwxyz")
}

func (s *Server) handleConfigJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.view())
}

type indexData struct {
	Page     string // config | events
	BasePath string
	// Token is passed on in links when the page was opened with ?token=.
	Token string
	// PortalTheme links homerun2-portal's design tokens: the request came
	// through the portal's proxy.
	PortalTheme bool
	Commit      string
	Health      Health
	ConfigView

	// events page
	Events  []Event
	Query   EventQuery
	SinkURL string
	Results []string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, s.pageData(r, "config"))
}

func (s *Server) handleEventsPage(w http.ResponseWriter, r *http.Request) {
	q, err := s.eventQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d := s.pageData(r, "events")
	d.Query = q
	d.Events = s.events.list(q)
	d.SinkURL = s.opts.SinkURL
	d.Results = append([]string{"unrouted"}, notify.Results...)
	s.render(w, r, d)
}

func (s *Server) pageData(r *http.Request, page string) indexData {
	commit := s.opts.Build.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	return indexData{
		Page:        page,
		BasePath:    s.opts.BasePath,
		Token:       r.URL.Query().Get("token"),
		PortalTheme: r.Header.Get("X-Forwarded-Prefix") != "",
		Commit:      commit,
		Health:      s.health(r.Context()),
		ConfigView:  s.view(),
	}
}

func (s *Server) render(w http.ResponseWriter, _ *http.Request, d indexData) {
	var buf bytes.Buffer
	if err := indexTmpl.Execute(&buf, d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// queryString builds "?k=v&…" from key/value pairs, skipping empty values,
// and adds token when set, so links keep the page's ?token=.
func queryString(token string, kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	if token != "" {
		v.Set("token", token)
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
