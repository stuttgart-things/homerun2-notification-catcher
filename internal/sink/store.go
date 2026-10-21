// Package sink is a notification sink for dev, e2e and card design
// (#52). It takes what the catcher's outputs send -- Teams-shaped posts (a
// TeamsEnvelope holding Adaptive Cards) and generic webhook JSON -- keeps the
// last N posts per channel in memory, and serves them as JSON and as an HTML
// view.
//
// Posts that carry the same message (one catcher dispatch fanning out to
// several outputs) are grouped into one Notification, so the view lists each
// notification once with the channels it went to. An optional catch-all
// output to /inbox adds the messages that went to no channel at all.
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

// groupWindow is how far apart posts of one dispatch can arrive: the
// catcher's whole fan-out per message is capped at 30s.
const groupWindow = 30 * time.Second

// Delivery is one post of a notification: where it went.
type Delivery struct {
	PostID     string    `json:"postId"`
	Kind       string    `json:"kind"`
	Channel    string    `json:"channel"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// Notification is one message the catcher dispatched, with the posts it
// became. Message fields come from a post that carries the homerun.Message
// (webhook, inbox) when there is one, else from the Teams card.
type Notification struct {
	ID        string    `json:"id"`
	Seq       uint64    `json:"seq"`
	FirstSeen time.Time `json:"firstSeen"`
	Title     string    `json:"title"`
	Message   string    `json:"message,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	System    string    `json:"system,omitempty"`
	Author    string    `json:"author,omitempty"`
	Tags      string    `json:"tags,omitempty"`
	Timestamp string    `json:"timestamp,omitempty"`
	URL       string    `json:"url,omitempty"`
	// Inbox is true when the catch-all /inbox output saw the message.
	Inbox      bool       `json:"inbox"`
	Deliveries []Delivery `json:"deliveries"`

	key     string
	hasFull bool // fields came from a homerun.Message
}

// Event is what Subscribe delivers: a new post (nil for an inbox entry) and
// the notification it belongs to, as of now.
type Event struct {
	Post         *Message      `json:"post,omitempty"`
	Notification *Notification `json:"notification"`
}

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
	Body         string            `json:"body,omitempty"`
	Severity     string            `json:"severity,omitempty"`
	System       string            `json:"system,omitempty"`
	FallbackText string            `json:"fallbackText,omitempty"`
	Style        string            `json:"style,omitempty"`
	Facts        map[string]string `json:"facts,omitempty"`
	// NotificationID is the notification this post was grouped into.
	NotificationID string `json:"notificationId"`

	msg *homerun.Message // set when the body is a homerun.Message
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
	// notifications, oldest first, capped at 4 * capacity.
	notifications []*Notification
	nseq          uint64
	subs          map[chan Event]struct{}
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
		subs:     map[chan Event]struct{}{},
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
	if msg, ok := decodeMessage(raw); ok {
		m.msg = &msg
		summarizeMessage(m, msg)
	}
	return s.add(m, raw)
}

// AddInbox records a message the catch-all output saw. It only marks (or
// creates) the notification; the inbox is not a channel.
func (s *Store) AddInbox(msg homerun.Message) *Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	n := s.group(messageKey(msg), now, func(n *Notification) bool { return n.Inbox })
	n.Inbox = true
	n.fillFromMessage(msg)
	s.publish(Event{Notification: n.clone()})
	return n.clone()
}

// decodeMessage reads raw as a homerun.Message; ok is false when it is not
// one (no title, message, severity or system).
func decodeMessage(raw []byte) (homerun.Message, bool) {
	var msg homerun.Message
	if json.Unmarshal(raw, &msg) != nil {
		return msg, false
	}
	return msg, msg.Title != "" || msg.Message != "" || msg.Severity != "" || msg.System != ""
}

// messageKey identifies a message across outputs: severity, title and body
// normalised the way the catcher's Teams card builds them, so a card and the
// webhook JSON of the same message get the same key.
func messageKey(msg homerun.Message) string {
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = "Notification"
	}
	body := strings.TrimSpace(msg.Message)
	if body == "" {
		body = title
	}
	return strings.ToLower(strings.TrimSpace(msg.Severity)) + "\x00" + title + "\x00" + body
}

// group returns the notification for key: the newest one within
// groupWindow that taken does not reject (taken reports the slot this post
// would fill as already used, i.e. a repeat of the message), else a new one.
// Callers hold s.mu.
func (s *Store) group(key string, now time.Time, taken func(*Notification) bool) *Notification {
	for i := len(s.notifications) - 1; i >= 0; i-- {
		n := s.notifications[i]
		if now.Sub(n.FirstSeen) > groupWindow {
			break
		}
		if key != "" && n.key == key && !taken(n) {
			return n
		}
	}
	s.nseq++
	n := &Notification{
		ID:         strconv.FormatUint(s.nseq, 10),
		Seq:        s.nseq,
		FirstSeen:  now,
		Deliveries: []Delivery{},
		key:        key,
	}
	s.notifications = append(s.notifications, n)
	if max := 4 * s.capacity; len(s.notifications) > max {
		s.notifications = s.notifications[len(s.notifications)-max:]
	}
	return n
}

func (n *Notification) fillFromMessage(msg homerun.Message) {
	if n.hasFull {
		return
	}
	n.hasFull = true
	n.Title = msg.Title
	n.Message = msg.Message
	n.Severity = msg.Severity
	n.System = msg.System
	n.Author = msg.Author
	n.Tags = msg.Tags
	n.Timestamp = msg.Timestamp
	n.URL = msg.URL
}

// fillFromPost fills a notification that has no homerun.Message yet from a
// post's summary.
func (n *Notification) fillFromPost(m *Message) {
	if m.msg != nil {
		n.fillFromMessage(*m.msg)
		return
	}
	if n.hasFull || n.Title != "" {
		return
	}
	n.Title = m.Title
	n.Message = m.Body
	n.Severity = m.Severity
	n.System = m.System
	n.Author = factValue(m.Facts, "Author")
	n.Tags = factValue(m.Facts, "Tags")
	n.Timestamp = factValue(m.Facts, "Time")
	if m.Envelope != nil && len(m.Envelope.Attachments) > 0 {
		for _, a := range m.Envelope.Attachments[0].Content.Actions {
			if a.URL != "" {
				n.URL = a.URL
				break
			}
		}
	}
}

func (n *Notification) clone() *Notification {
	c := *n
	c.Deliveries = append([]Delivery{}, n.Deliveries...)
	return &c
}

// postKey is the grouping key of a post; "" never groups.
func postKey(m *Message) string {
	switch {
	case m.msg != nil:
		return messageKey(*m.msg)
	case m.Kind == KindTeams && m.Title != "":
		return messageKey(homerun.Message{Title: m.Title, Message: m.Body, Severity: m.Severity})
	default:
		return ""
	}
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

	n := s.group(postKey(m), m.ReceivedAt, func(n *Notification) bool {
		for _, d := range n.Deliveries {
			if d.Kind == m.Kind && d.Channel == m.Channel {
				return true
			}
		}
		return false
	})
	n.Deliveries = append(n.Deliveries, Delivery{PostID: m.ID, Kind: m.Kind, Channel: m.Channel, ReceivedAt: m.ReceivedAt})
	n.fillFromPost(m)
	m.NotificationID = n.ID

	list := append(s.channels[channel], m)
	if len(list) > s.capacity {
		list = list[len(list)-s.capacity:]
	}
	s.channels[channel] = list

	s.publish(Event{Post: m, Notification: n.clone()})
	return m
}

// publish sends ev to every subscriber. Callers hold s.mu.
func (s *Store) publish(ev Event) {
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block the webhook
		}
	}
}

// Notifications returns the notifications matching q, newest first. Of q
// only Contains (title, message, system, tags) and Limit apply.
func (s *Store) Notifications(q Query) []*Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	contains := strings.ToLower(q.Contains)
	out := []*Notification{}
	for i := len(s.notifications) - 1; i >= 0; i-- {
		n := s.notifications[i]
		if contains != "" && !strings.Contains(strings.ToLower(n.Title+"\n"+n.Message+"\n"+n.System+"\n"+n.Tags), contains) {
			continue
		}
		out = append(out, n.clone())
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out
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

// Clear drops the posts matching kind and channel; empty matches all and
// also drops the notifications.
func (s *Store) Clear(kind, channel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "" && channel == "" {
		s.notifications = nil
	}
	for key := range s.channels {
		k, c, _ := strings.Cut(key, "/")
		if (kind == "" || k == kind) && (channel == "" || c == channel) {
			delete(s.channels, key)
		}
	}
}

// Subscribe returns a channel receiving an Event per new post or inbox entry
// until cancel is called.
func (s *Store) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
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
	m.Body = msg.Message
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

// summarizeCard fills Title, Body, FallbackText, Style and Facts from the
// first card. Title and Body are the first two TextBlocks, as in the
// catcher's own card. Severity and System come from the facts, else
// severity from the "[severity] " prefix of the fallback text.
func summarizeCard(m *Message) {
	if len(m.Envelope.Attachments) == 0 {
		return
	}
	card := m.Envelope.Attachments[0].Content
	m.FallbackText = card.FallbackText
	texts := 0
	var walk func([]models.CardElement)
	walk = func(elems []models.CardElement) {
		for _, e := range elems {
			if e.Type == "Container" && m.Style == "" {
				m.Style = e.Style
			}
			if e.Type == "TextBlock" {
				switch texts {
				case 0:
					m.Title = e.Text
				case 1:
					m.Body = e.Text
				}
				texts++
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
