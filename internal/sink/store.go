// Package sink is a notification sink for dev, e2e and card design
// (#52). It takes what the catcher's outputs send -- Teams-shaped posts (a
// TeamsEnvelope holding Adaptive Cards) and generic webhook JSON -- keeps the
// last N posts per channel in memory, and serves them as JSON and as an HTML
// view: one list of everything that arrived, each post shown the way its
// target would show it.
package sink

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// Sink kinds, named after the catcher's output types.
const (
	KindTeams   = "msteams"
	KindWebhook = "webhook"
)

// DefaultCapacity is how many posts a channel keeps before the oldest drops.
const DefaultCapacity = 500

// Message is one post the sink received.
type Message struct {
	ID         string    `json:"id"`
	Seq        uint64    `json:"seq"`
	Kind       string    `json:"kind"`
	Channel    string    `json:"channel"`
	ReceivedAt time.Time `json:"receivedAt"`
	// Envelope is set for KindTeams posts.
	Envelope *models.TeamsEnvelope `json:"envelope,omitempty"`
	// Raw is the request body as posted, so a test can assert on fields the
	// typed envelope does not model.
	Raw json.RawMessage `json:"raw"`
	// Summary fields pulled from the card or the webhook JSON, for the list
	// view and filters.
	Title        string            `json:"title"`
	Severity     string            `json:"severity,omitempty"`
	System       string            `json:"system,omitempty"`
	FallbackText string            `json:"fallbackText,omitempty"`
	Style        string            `json:"style,omitempty"`
	Facts        map[string]string `json:"facts,omitempty"`
}

// Query narrows List. Zero values match everything.
type Query struct {
	Kind    string
	Channel string
	// Since keeps messages received after this time.
	Since time.Time
	// AfterSeq keeps messages with a higher Seq (cursor for polling).
	AfterSeq uint64
	// Contains keeps messages whose raw body contains this substring
	// (case-insensitive). objectId lookups use it: the id is matched
	// wherever the card carries it.
	Contains string
	// Facts keeps messages whose card has every listed fact (title and
	// value compared case-insensitively).
	Facts map[string]string
	Limit int
}

// Store is an in-memory ring buffer per channel. Safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	capacity int
	seq      uint64
	channels map[string][]*Message
	subs     map[chan *Message]struct{}
}

// NewStore returns a store keeping up to capacity posts per channel
// (DefaultCapacity when capacity <= 0).
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Store{
		capacity: capacity,
		channels: map[string][]*Message{},
		subs:     map[chan *Message]struct{}{},
	}
}

// AddTeams stores a Teams post; see add.
func (s *Store) AddTeams(channel string, env models.TeamsEnvelope, raw []byte) *Message {
	m := &Message{Kind: KindTeams, Channel: channel, Envelope: &env}
	summarizeCard(m)
	return s.add(m, raw)
}

// AddWebhook stores a generic webhook post. A body that decodes as a
// homerun.Message (what the catcher's webhook output sends) fills the
// summary fields; any other JSON is kept as is.
func (s *Store) AddWebhook(channel string, raw []byte) *Message {
	m := &Message{Kind: KindWebhook, Channel: channel}
	var msg homerun.Message
	if json.Unmarshal(raw, &msg) == nil {
		summarizeMessage(m, msg)
	}
	return s.add(m, raw)
}

// add assigns ID, Seq and ReceivedAt, stores m and notifies subscribers.
// Channels are per kind: a Teams and a webhook channel may share a name.
func (s *Store) add(m *Message, raw []byte) *Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	m.ID = strconv.FormatUint(s.seq, 10)
	m.Seq = s.seq
	m.ReceivedAt = time.Now().UTC()
	m.Raw = append(json.RawMessage(nil), raw...)
	channel := m.Kind + "/" + m.Channel

	list := append(s.channels[channel], m)
	if len(list) > s.capacity {
		list = list[len(list)-s.capacity:]
	}
	s.channels[channel] = list

	for ch := range s.subs {
		select {
		case ch <- m:
		default: // slow subscriber: drop rather than block the webhook
		}
	}
	return m
}

// Get returns the post with id, or nil.
func (s *Store) Get(id string) *Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.channels {
		for _, m := range list {
			if m.ID == id {
				return m
			}
		}
	}
	return nil
}

// List returns the posts matching q, newest first.
func (s *Store) List(q Query) []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	contains := strings.ToLower(q.Contains)
	out := []*Message{}
	for _, list := range s.channels {
		for _, m := range list {
			if (q.Kind != "" && m.Kind != q.Kind) || (q.Channel != "" && m.Channel != q.Channel) {
				break // a list holds one kind and channel
			}
			if m.Seq <= q.AfterSeq || (!q.Since.IsZero() && !m.ReceivedAt.After(q.Since)) {
				continue
			}
			if contains != "" && !strings.Contains(strings.ToLower(string(m.Raw)), contains) {
				continue
			}
			if !hasFacts(m, q.Facts) {
				continue
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

// Channels returns the post counts per "<kind>/<channel>".
func (s *Store) Channels() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.channels))
	for name, list := range s.channels {
		out[name] = len(list)
	}
	return out
}

// Clear drops the posts matching kind and channel; empty matches all.
func (s *Store) Clear(kind, channel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.channels {
		k, c, _ := strings.Cut(key, "/")
		if (kind == "" || k == kind) && (channel == "" || c == channel) {
			delete(s.channels, key)
		}
	}
}

// Subscribe returns a channel receiving every new post until cancel is called.
func (s *Store) Subscribe() (<-chan *Message, func()) {
	ch := make(chan *Message, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func hasFacts(m *Message, want map[string]string) bool {
	for title, value := range want {
		found := false
		for t, v := range m.Facts {
			if strings.EqualFold(t, title) && strings.EqualFold(v, value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// summarizeMessage fills the summary fields of a webhook post from the
// homerun.Message it carries.
func summarizeMessage(m *Message, msg homerun.Message) {
	m.Title = msg.Title
	m.Severity = msg.Severity
	m.System = msg.System
	add := func(title, value string) {
		if v := strings.TrimSpace(value); v != "" {
			if m.Facts == nil {
				m.Facts = map[string]string{}
			}
			m.Facts[title] = v
		}
	}
	add("Severity", msg.Severity)
	add("System", msg.System)
	add("Author", msg.Author)
	add("Tags", msg.Tags)
	add("Time", msg.Timestamp)
}

// summarizeCard fills Title, FallbackText, Style and Facts from the first
// card. The title is the first TextBlock found, as in the catcher's own
// card. Severity and System come from the facts, else severity from the
// "[severity] " prefix of the fallback text.
func summarizeCard(m *Message) {
	if len(m.Envelope.Attachments) == 0 {
		return
	}
	card := m.Envelope.Attachments[0].Content
	m.FallbackText = card.FallbackText
	var walk func([]models.CardElement)
	walk = func(elems []models.CardElement) {
		for _, e := range elems {
			if e.Type == "Container" && m.Style == "" {
				m.Style = e.Style
			}
			if e.Type == "TextBlock" && m.Title == "" {
				m.Title = e.Text
			}
			for _, f := range e.Facts {
				if m.Facts == nil {
					m.Facts = map[string]string{}
				}
				m.Facts[f.Title] = f.Value
			}
			walk(e.Items)
		}
	}
	walk(card.Body)

	m.Severity = factValue(m.Facts, "Severity")
	m.System = factValue(m.Facts, "System")
	if m.Severity == "" && strings.HasPrefix(m.FallbackText, "[") {
		if sev, _, ok := strings.Cut(m.FallbackText[1:], "] "); ok {
			m.Severity = sev
		}
	}
}

func factValue(facts map[string]string, title string) string {
	for t, v := range facts {
		if strings.EqualFold(t, title) {
			return v
		}
	}
	return ""
}
