package sink

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"gopkg.in/yaml.v3"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

const (
	adaptiveCardContentType = "application/vnd.microsoft.card.adaptive"

	// maxBodyBytes caps one webhook post. Teams itself rejects cards far
	// smaller than this (~28 KB); the cap only protects the mock.
	maxBodyBytes = 1 << 20
)

var (
	//go:embed index.html
	indexSrc  string
	indexTmpl = template.Must(template.New("index").Parse(indexSrc))

	//go:embed static
	staticFS embed.FS
)

// Fault makes the webhook of one channel misbehave, to exercise the
// catcher's error and retry path: wait Delay, then answer Status (when set)
// instead of accepting the post. Count > 0 limits it to that many posts.
type Fault struct {
	Status int           `json:"status,omitempty"`
	Delay  time.Duration `json:"-"`
	Count  int           `json:"count,omitempty"`
}

type faultJSON struct {
	Status int    `json:"status,omitempty"`
	Delay  string `json:"delay,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// BuildInfo is shown in the page footer, as in the other homerun2 UIs.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Options configure a Server.
type Options struct {
	// Token, when set, is required as "Authorization: Bearer <token>" or
	// "?token=<token>" on every route but the health checks.
	Token string
	// BasePath is the URL prefix behind a reverse proxy, e.g. "/notify"
	// behind homerun2-portal; see ParseBasePath. Health stays at the root.
	BasePath string
	Build    BuildInfo
}

// Server serves the sinks, the JSON API and the HTML view.
type Server struct {
	store *Store
	opts  Options

	mu     sync.Mutex
	faults map[string]*Fault // by "<kind>/<channel>"
}

// NewServer returns a Server on store.
func NewServer(store *Store, opts Options) *Server {
	return &Server{store: store, opts: opts, faults: map[string]*Fault{}}
}

// Handler returns the HTTP routes, mounted under Options.BasePath.
func (s *Server) Handler() http.Handler {
	health := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})

	app := http.NewServeMux()
	app.HandleFunc("POST /teams/{channel}", s.auth(s.handleTeams))
	app.HandleFunc("POST /webhook/{channel}", s.auth(s.handleWebhook))
	app.HandleFunc("POST /inbox", s.auth(s.handleInbox))
	app.HandleFunc("GET /api/notifications", s.auth(s.handleNotifications))
	app.HandleFunc("GET /api/channels", s.auth(s.handleChannels))
	app.HandleFunc("GET /api/messages", s.auth(s.handleList))
	app.HandleFunc("DELETE /api/messages", s.auth(s.handleClear))
	app.HandleFunc("GET /api/messages/{id}", s.auth(s.handleGet))
	app.HandleFunc("GET /api/events", s.auth(s.handleEvents))
	app.HandleFunc("POST /api/render", s.auth(s.handleRender))
	app.HandleFunc("GET /api/faults", s.auth(s.handleListFaults))
	app.HandleFunc("PUT /api/faults/{kind}/{channel}", s.auth(s.handleSetFault))
	app.HandleFunc("DELETE /api/faults/{kind}/{channel}", s.auth(s.handleClearFault))
	static, _ := fs.Sub(staticFS, "static")
	app.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	app.HandleFunc("GET /{$}", s.auth(s.handleIndex))

	mux := http.NewServeMux()
	mux.Handle("GET /health", health)
	mux.Handle("GET /healthz", health)
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

var basePathSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// ParseBasePath normalises a BASE_PATH to "/a/b" form ("" and "/" become
// ""), as homerun2-core-catcher does. Only plain path segments are accepted:
// the value is written into the page's HTML.
func ParseBasePath(s string) (string, error) {
	trimmed := strings.Trim(strings.TrimSpace(s), "/")
	if trimmed == "" {
		return "", nil
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if !basePathSegment.MatchString(seg) || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid BASE_PATH %q: segment %q", s, seg)
		}
	}
	return "/" + trimmed, nil
}

type indexData struct {
	BasePath string
	// PortalTheme links homerun2-portal's design tokens: the request came
	// through the portal's proxy.
	PortalTheme bool
	Version     string
	Commit      string
	Date        string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	commit := s.opts.Build.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	var buf bytes.Buffer
	if err := indexTmpl.Execute(&buf, indexData{
		BasePath:    s.opts.BasePath,
		PortalTheme: r.Header.Get("X-Forwarded-Prefix") != "",
		Version:     s.opts.Build.Version,
		Commit:      commit,
		Date:        s.opts.Build.Date,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	token := s.opts.Token
	if token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleTeams accepts what a Teams Workflow webhook accepts and answers 202
// with an empty body, as Teams does.
func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")
	raw, ok := s.receive(w, r, KindTeams, channel)
	if !ok {
		return
	}
	env, err := ParseEnvelope(raw)
	if err != nil {
		slog.Warn("sink rejected post", "kind", KindTeams, "channel", channel, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m := s.store.AddTeams(channel, env, raw)
	slog.Info("sink received", "kind", KindTeams, "channel", channel, "id", m.ID, "title", m.Title)
	w.WriteHeader(http.StatusAccepted)
}

// handleWebhook accepts any JSON body, as the catcher's generic webhook
// output sends it, and answers 200.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")
	raw, ok := s.receive(w, r, KindWebhook, channel)
	if !ok {
		return
	}
	if !json.Valid(raw) {
		slog.Warn("sink rejected post", "kind", KindWebhook, "channel", channel, "error", "invalid JSON")
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	m := s.store.AddWebhook(channel, raw)
	slog.Info("sink received", "kind", KindWebhook, "channel", channel, "id", m.ID, "title", m.Title)
	w.WriteHeader(http.StatusOK)
}

// handleInbox takes the homerun.Message JSON of a catch-all webhook output
// (no filters), so the notification list also shows messages that went to
// no channel. It answers 200.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes {
		http.Error(w, "unreadable or too large body", http.StatusBadRequest)
		return
	}
	msg, ok := decodeMessage(raw)
	if !ok {
		http.Error(w, "body must be a homerun.Message JSON", http.StatusBadRequest)
		return
	}
	n := s.store.AddInbox(msg)
	slog.Debug("sink inbox", "notification", n.ID, "title", n.Title)
	w.WriteHeader(http.StatusOK)
}

// handleNotifications serves GET /api/notifications: the posts grouped per
// dispatched message, newest first, each with the channels it went to.
// Query parameters: q (substring of title, message, system, tags), limit.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	q := Query{Contains: r.URL.Query().Get("q")}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		q.Limit = n
	}
	writeJSON(w, http.StatusOK, s.store.Notifications(q))
}

// receive applies the channel's fault and reads the body. It writes the
// response itself and returns false when the post ends there.
func (s *Server) receive(w http.ResponseWriter, r *http.Request, kind, channel string) ([]byte, bool) {
	if f := s.takeFault(kind + "/" + channel); f != nil {
		if f.Delay > 0 {
			select {
			case <-time.After(f.Delay):
			case <-r.Context().Done():
				return nil, false
			}
		}
		if f.Status != 0 {
			slog.Info("sink fault injected", "kind", kind, "channel", channel, "status", f.Status, "delay", f.Delay)
			http.Error(w, http.StatusText(f.Status), f.Status)
			return nil, false
		}
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if len(raw) > maxBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return raw, true
}

// ParseEnvelope decodes and checks a webhook body: a message with at least
// one attachment, each an Adaptive Card.
func ParseEnvelope(raw []byte) (models.TeamsEnvelope, error) {
	var env models.TeamsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, fmt.Errorf("invalid JSON: %w", err)
	}
	if env.Type != "message" {
		return env, fmt.Errorf("type must be \"message\", got %q", env.Type)
	}
	if len(env.Attachments) == 0 {
		return env, fmt.Errorf("attachments is empty")
	}
	for i, a := range env.Attachments {
		if a.ContentType != adaptiveCardContentType {
			return env, fmt.Errorf("attachments[%d].contentType must be %q, got %q", i, adaptiveCardContentType, a.ContentType)
		}
		if a.Content.Type != "AdaptiveCard" {
			return env, fmt.Errorf("attachments[%d].content.type must be \"AdaptiveCard\", got %q", i, a.Content.Type)
		}
	}
	return env, nil
}

func (s *Server) handleChannels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Channels())
}

// handleList serves GET /api/messages. Query parameters:
//
//	kind      only this sink kind (msteams, webhook)
//	channel   only this channel
//	since     RFC3339 time; only posts received after it
//	after     sequence number; only posts with a higher seq (poll cursor)
//	objectId  only posts whose body contains this id
//	q         only posts whose body contains this text
//	fact      Title=Value; only cards with that fact (repeatable)
//	limit     at most this many, newest first
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := Query{Kind: v.Get("kind"), Channel: v.Get("channel"), Contains: v.Get("q")}
	if id := v.Get("objectId"); id != "" {
		if q.Contains != "" {
			http.Error(w, "use either objectId or q", http.StatusBadRequest)
			return
		}
		q.Contains = id
	}
	if since := v.Get("since"); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			http.Error(w, "since: "+err.Error(), http.StatusBadRequest)
			return
		}
		q.Since = t
	}
	if after := v.Get("after"); after != "" {
		n, err := strconv.ParseUint(after, 10, 64)
		if err != nil {
			http.Error(w, "after: "+err.Error(), http.StatusBadRequest)
			return
		}
		q.AfterSeq = n
	}
	if limit := v.Get("limit"); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 0 {
			http.Error(w, "limit must be a non-negative integer", http.StatusBadRequest)
			return
		}
		q.Limit = n
	}
	for _, f := range v["fact"] {
		title, value, ok := strings.Cut(f, "=")
		if !ok {
			http.Error(w, "fact must be Title=Value", http.StatusBadRequest)
			return
		}
		if q.Facts == nil {
			q.Facts = map[string]string{}
		}
		q.Facts[strings.TrimSpace(title)] = strings.TrimSpace(value)
	}
	writeJSON(w, http.StatusOK, s.store.List(q))
}

// handleGet returns the raw envelope JSON of one post.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	m := s.store.Get(r.PathValue("id"))
	if m == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(m.Raw)
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	s.store.Clear(r.URL.Query().Get("kind"), r.URL.Query().Get("channel"))
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents streams an Event per new post or inbox entry as server-sent
// events for the HTML view.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := s.store.Subscribe()
	defer cancel()

	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// RenderRequest is the card playground input: a homerun.Message and an
// output's `card:` block as YAML (empty for the default card).
type RenderRequest struct {
	Message homerun.Message `json:"message"`
	Card    string          `json:"card,omitempty"`
}

// handleRender builds the envelope the catcher would send for the request,
// with the catcher's own builder, so the playground preview is the real card.
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	var req RenderRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	card, err := ParseCardConfig(req.Card)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, notify.BuildEnvelopeWithCard(req.Message, card))
}

// ParseCardConfig decodes a `card:` block (the YAML under an output's card
// key, e.g. "facts: [...]") and validates it as the catcher's config loader
// does.
func ParseCardConfig(src string) (config.CardConfig, error) {
	var card config.CardConfig
	if strings.TrimSpace(src) == "" {
		return card, nil
	}
	if err := yaml.Unmarshal([]byte(src), &card); err != nil {
		return card, fmt.Errorf("card: %w", err)
	}
	probe := &config.NotifyConfig{Outputs: []config.OutputConfig{{
		Name:       "playground",
		Type:       config.OutputTypeTeams,
		WebhookURL: "http://playground.invalid",
		Card:       card,
	}}}
	if err := config.Validate(probe); err != nil {
		return card, err
	}
	return card, nil
}

func (s *Server) handleListFaults(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make(map[string]faultJSON, len(s.faults))
	for ch, f := range s.faults {
		fj := faultJSON{Status: f.Status, Count: f.Count}
		if f.Delay > 0 {
			fj.Delay = f.Delay.String()
		}
		out[ch] = fj
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// handleSetFault sets a channel's fault from {"status":500,"delay":"2s","count":3}.
func (s *Server) handleSetFault(w http.ResponseWriter, r *http.Request) {
	key, ok := faultKey(w, r)
	if !ok {
		return
	}
	var fj faultJSON
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&fj); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	f := &Fault{Status: fj.Status, Count: fj.Count}
	if fj.Delay != "" {
		d, err := time.ParseDuration(fj.Delay)
		if err != nil || d < 0 {
			http.Error(w, "delay must be a non-negative Go duration", http.StatusBadRequest)
			return
		}
		f.Delay = d
	}
	if f.Status != 0 && (f.Status < 100 || f.Status > 599) {
		http.Error(w, "status must be an HTTP status code", http.StatusBadRequest)
		return
	}
	if f.Status == 0 && f.Delay == 0 {
		http.Error(w, "set status, delay or both", http.StatusBadRequest)
		return
	}
	if f.Count < 0 {
		http.Error(w, "count must be >= 0", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.faults[key] = f
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearFault(w http.ResponseWriter, r *http.Request) {
	key, ok := faultKey(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	delete(s.faults, key)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func faultKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	kind := r.PathValue("kind")
	if kind != KindTeams && kind != KindWebhook {
		http.Error(w, "kind must be msteams or webhook", http.StatusBadRequest)
		return "", false
	}
	return kind + "/" + r.PathValue("channel"), true
}

// takeFault returns the channel's fault for one post, counting it down and
// dropping it when used up.
func (s *Server) takeFault(key string) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.faults[key]
	if !ok {
		return nil
	}
	used := *f
	if f.Count > 0 {
		f.Count--
		if f.Count == 0 {
			delete(s.faults, key)
		}
	}
	return &used
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
