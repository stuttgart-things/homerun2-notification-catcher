// Package teamsmock is a Teams-shaped webhook sink for dev, e2e and card
// design (#52). It accepts what a Teams Workflow webhook accepts (a
// TeamsEnvelope holding one Adaptive Card), keeps the last N posts per
// channel in memory, and serves them as JSON and as an HTML view with the
// cards rendered.
package teamsmock

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// DefaultCapacity is how many posts a channel keeps before the oldest drops.
const DefaultCapacity = 500

// Message is one post the mock received.
type Message struct {
	ID         string               `json:"id"`
	Seq        uint64               `json:"seq"`
	Channel    string               `json:"channel"`
	ReceivedAt time.Time            `json:"receivedAt"`
	Envelope   models.TeamsEnvelope `json:"envelope"`
	// Raw is the request body as posted, so a test can assert on fields the
	// typed envelope does not model.
	Raw json.RawMessage `json:"raw"`
	// Summary fields pulled from the card for the list view and filters.
	Title        string            `json:"title"`
	FallbackText string            `json:"fallbackText,omitempty"`
	Style        string            `json:"style,omitempty"`
	Facts        map[string]string `json:"facts,omitempty"`
}

// Query narrows List. Zero values match everything.
type Query struct {
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

// Add stores one post and notifies subscribers. It assigns ID, Seq and
// ReceivedAt and fills the summary fields from the envelope.
func (s *Store) Add(channel string, env models.TeamsEnvelope, raw []byte) *Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	m := &Message{
		ID:         strconv.FormatUint(s.seq, 10),
		Seq:        s.seq,
		Channel:    channel,
		ReceivedAt: time.Now().UTC(),
		Envelope:   env,
		Raw:        append(json.RawMessage(nil), raw...),
	}
	summarize(m)

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
	for channel, list := range s.channels {
		if q.Channel != "" && channel != q.Channel {
			continue
		}
		for _, m := range list {
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

// Channels returns the channel names with their post counts.
func (s *Store) Channels() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.channels))
	for name, list := range s.channels {
		out[name] = len(list)
	}
	return out
}

// Clear drops all posts of channel, or of every channel when channel is "".
func (s *Store) Clear(channel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel == "" {
		s.channels = map[string][]*Message{}
		return
	}
	delete(s.channels, channel)
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

// summarize fills Title, FallbackText, Style and Facts from the first card.
// The title is the first TextBlock found, as in the catcher's own card.
func summarize(m *Message) {
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
}
