package teamsmock

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

//go:embed index.html
var indexHTML []byte

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

// Server serves the webhook, the JSON API and the HTML view.
type Server struct {
	store *Store
	// token, when set, is required as "Authorization: Bearer <token>" or
	// "?token=<token>" on every route but /healthz.
	token string

	mu     sync.Mutex
	faults map[string]*Fault
}

// NewServer returns a Server on store. An empty token disables auth.
func NewServer(store *Store, token string) *Server {
	return &Server{store: store, token: token, faults: map[string]*Fault{}}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("POST /webhook/{channel}", s.auth(s.handleWebhook))
	mux.HandleFunc("GET /api/channels", s.auth(s.handleChannels))
	mux.HandleFunc("GET /api/messages", s.auth(s.handleList))
	mux.HandleFunc("DELETE /api/messages", s.auth(s.handleClear))
	mux.HandleFunc("GET /api/messages/{id}", s.auth(s.handleGet))
	mux.HandleFunc("GET /api/events", s.auth(s.handleEvents))
	mux.HandleFunc("POST /api/render", s.auth(s.handleRender))
	mux.HandleFunc("GET /api/faults", s.auth(s.handleListFaults))
	mux.HandleFunc("PUT /api/faults/{channel}", s.auth(s.handleSetFault))
	mux.HandleFunc("DELETE /api/faults/{channel}", s.auth(s.handleClearFault))
	mux.HandleFunc("GET /{$}", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	}))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleWebhook accepts what a Teams Workflow webhook accepts and answers
// 202 with an empty body, as Teams does.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")

	if f := s.takeFault(channel); f != nil {
		if f.Delay > 0 {
			select {
			case <-time.After(f.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if f.Status != 0 {
			slog.Info("teams-mock fault injected", "channel", channel, "status", f.Status, "delay", f.Delay)
			http.Error(w, http.StatusText(f.Status), f.Status)
			return
		}
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(raw) > maxBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	env, err := ParseEnvelope(raw)
	if err != nil {
		slog.Warn("teams-mock rejected post", "channel", channel, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	m := s.store.Add(channel, env, raw)
	slog.Info("teams-mock card received", "channel", channel, "id", m.ID, "title", m.Title)
	w.WriteHeader(http.StatusAccepted)
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
//	channel   only this channel
//	since     RFC3339 time; only posts received after it
//	after     sequence number; only posts with a higher seq (poll cursor)
//	objectId  only posts whose body contains this id
//	q         only posts whose body contains this text
//	fact      Title=Value; only cards with that fact (repeatable)
//	limit     at most this many, newest first
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := Query{Channel: v.Get("channel"), Contains: v.Get("q")}
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
	s.store.Clear(r.URL.Query().Get("channel"))
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents streams new posts as server-sent events for the HTML view.
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
		case m := <-ch:
			b, err := json.Marshal(m)
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
	s.faults[r.PathValue("channel")] = f
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearFault(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	delete(s.faults, r.PathValue("channel"))
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// takeFault returns the channel's fault for one post, counting it down and
// dropping it when used up.
func (s *Server) takeFault(channel string) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.faults[channel]
	if !ok {
		return nil
	}
	used := *f
	if f.Count > 0 {
		f.Count--
		if f.Count == 0 {
			delete(s.faults, channel)
		}
	}
	return &used
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
