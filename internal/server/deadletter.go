package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

// DeadLetterReader reads the dead-letter stream for the Dead letter tab and
// the pending gauge (#70). catcher.RedisDeadLetter is one.
type DeadLetterReader interface {
	Recent(ctx context.Context, n int64) ([]notify.ParkedEntry, error)
	Len(ctx context.Context) (int64, error)
}

// deadLetterPageSize is how many entries the tab shows, newest first.
const deadLetterPageSize = 200

// deadLetterTimeout bounds the stream reads of one request or scrape.
const deadLetterTimeout = 2 * time.Second

// ReplayBinary is the catcher binary's path in the ko-built image, for the
// replay command the tab shows.
const ReplayBinary = "/ko-app/homerun2-notification-catcher"

// SetDeadLetter sets the dead-letter stream the tab and the gauge read;
// called once the catcher is connected.
func (s *Server) SetDeadLetter(dl DeadLetterReader) {
	s.mu.Lock()
	s.deadLetter = dl
	s.mu.Unlock()
}

func (s *Server) deadLetterReader() DeadLetterReader {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deadLetter
}

// DeadLetterView is the Dead letter tab and /api/dead-letter.
type DeadLetterView struct {
	Stream string `json:"stream"` // empty: the dead letter is off
	// Pending is the stream's length, Entries its newest deadLetterPageSize
	// entries.
	Pending int64                `json:"pending"`
	Entries []notify.ParkedEntry `json:"entries"`
	// Outputs counts the entries shown per output, sorted by name, for the
	// replay commands.
	Outputs []OutputCount `json:"outputs,omitempty"`
	// Error is set when the stream could not be read.
	Error string `json:"error,omitempty"`
}

// OutputCount is one output's share of the shown entries.
type OutputCount struct {
	Output string `json:"output"`
	Count  int    `json:"count"`
}

func (s *Server) deadLetterView(ctx context.Context) DeadLetterView {
	v := DeadLetterView{Stream: s.opts.DeadLetterStream, Entries: []notify.ParkedEntry{}}
	dl := s.deadLetterReader()
	if v.Stream == "" {
		return v
	}
	if dl == nil {
		v.Error = "not connected to Redis yet"
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, deadLetterTimeout)
	defer cancel()
	n, err := dl.Len(ctx)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	entries, err := dl.Recent(ctx, deadLetterPageSize)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Pending, v.Entries = n, entries
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Output]++
	}
	for o, c := range counts {
		v.Outputs = append(v.Outputs, OutputCount{Output: o, Count: c})
	}
	sort.Slice(v.Outputs, func(i, j int) bool { return v.Outputs[i].Output < v.Outputs[j].Output })
	return v
}

func (s *Server) handleDeadLetterJSON(w http.ResponseWriter, r *http.Request) {
	v := s.deadLetterView(r.Context())
	status := http.StatusOK
	if v.Error != "" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, v)
}

func (s *Server) handleDeadLetterPage(w http.ResponseWriter, r *http.Request) {
	d := s.pageData(r, "dead-letter")
	d.DeadLetter = s.deadLetterView(r.Context())
	d.SinkURL = s.opts.SinkURL
	d.ReplayBinary = ReplayBinary
	s.render(w, r, d)
}

// writeDeadLetterPending writes notification_catcher_dead_letter_pending,
// the stream's length, when the dead letter is on and readable: an alert on
// a growing backlog. Left out when the read fails, so a scrape never shows
// a made-up 0.
func (s *Server) writeDeadLetterPending(ctx context.Context, w io.Writer) {
	dl := s.deadLetterReader()
	if s.opts.DeadLetterStream == "" || dl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deadLetterTimeout)
	defer cancel()
	n, err := dl.Len(ctx)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP notification_catcher_dead_letter_pending Entries in the dead-letter stream, waiting for a replay.\n"+
		"# TYPE notification_catcher_dead_letter_pending gauge\n"+
		"notification_catcher_dead_letter_pending %d\n", n)
}
