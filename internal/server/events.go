package server

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

// DefaultEventsCapacity is how many dispatched messages the event log keeps.
const DefaultEventsCapacity = 200

// Event is one dispatched message in the event log (#22 phase 2).
type Event struct {
	ObjectID string         `json:"objectId,omitempty"`
	At       time.Time      `json:"at"`
	Title    string         `json:"title"`
	Severity string         `json:"severity,omitempty"`
	System   string         `json:"system,omitempty"`
	Author   string         `json:"author,omitempty"`
	Routed   bool           `json:"routed"` // any output matched
	Failed   bool           `json:"failed"` // any matching output failed
	Outputs  []OutputResult `json:"outputs"`
}

// OutputResult is what one output did with the message.
type OutputResult struct {
	Output string `json:"output"`
	Result string `json:"result"` // sent | failed | filtered | dry-run
	Error  string `json:"error,omitempty"`
	// Attempts counts the sends, retries included (#68); 0 when filtered.
	Attempts     int  `json:"attempts,omitempty"`
	DeadLettered bool `json:"deadLettered,omitempty"`
}

// events is the in-process event log and the dispatch counters. Per pod and
// lost on restart: the filtered results only exist in the process, so a
// Redis-backed log could not show them (decided in #22).
type events struct {
	mu       sync.Mutex
	buf      []Event // ring, oldest overwritten
	next     int
	full     bool
	counts   map[[2]string]uint64 // {output, result}
	retries  map[string]uint64    // by output
	parked   map[string]uint64    // dead-lettered, by output
	messages map[bool]uint64      // by routed
	outputs  []string
}

func newEvents(capacity int, outputs []string) *events {
	if capacity <= 0 {
		capacity = DefaultEventsCapacity
	}
	e := &events{
		buf:      make([]Event, capacity),
		counts:   map[[2]string]uint64{},
		retries:  map[string]uint64{},
		parked:   map[string]uint64{},
		messages: map[bool]uint64{false: 0, true: 0},
		outputs:  outputs,
	}
	// Every series from the start, so rate() and absent-alerts work before
	// the first failure.
	for _, o := range outputs {
		e.retries[o], e.parked[o] = 0, 0
		for _, r := range notify.Results {
			e.counts[[2]string{o, r}] = 0
		}
	}
	return e
}

func (e *events) record(rec notify.Record) {
	m := rec.Message
	ev := Event{
		ObjectID: m.ObjectID,
		At:       rec.At,
		Title:    m.Title,
		Severity: m.Severity,
		System:   m.System,
		Author:   m.Author,
		Outputs:  make([]OutputResult, 0, len(rec.Results)),
	}
	for _, r := range rec.Results {
		or := OutputResult{Output: r.Output, Result: r.Result(), Attempts: r.Attempts, DeadLettered: r.DeadLettered}
		if r.Err != nil {
			or.Error = r.Err.Error()
		}
		ev.Routed = ev.Routed || or.Result != notify.ResultFiltered
		ev.Failed = ev.Failed || or.Result == notify.ResultFailed
		ev.Outputs = append(ev.Outputs, or)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.buf[e.next] = ev
	e.next = (e.next + 1) % len(e.buf)
	e.full = e.full || e.next == 0
	for _, or := range ev.Outputs {
		e.counts[[2]string{or.Output, or.Result}]++
		if or.Attempts > 1 {
			e.retries[or.Output] += uint64(or.Attempts - 1)
		}
		if or.DeadLettered {
			e.parked[or.Output]++
		}
	}
	e.messages[ev.Routed]++
}

// EventQuery narrows list.
type EventQuery struct {
	Result   string // only events with an output of this result; "unrouted" for none matched
	ObjectID string
	Limit    int
}

// list returns the events newest first.
func (e *events) list(q EventQuery) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.next
	if e.full {
		n = len(e.buf)
	}
	out := []Event{}
	for i := 1; i <= n; i++ {
		ev := e.buf[(e.next-i+len(e.buf))%len(e.buf)]
		if q.ObjectID != "" && ev.ObjectID != q.ObjectID {
			continue
		}
		if q.Result != "" && !ev.has(q.Result) {
			continue
		}
		out = append(out, ev)
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out
}

func (ev Event) has(result string) bool {
	if result == "unrouted" {
		return !ev.Routed
	}
	for _, o := range ev.Outputs {
		if o.Result == result {
			return true
		}
	}
	return false
}

// writeMetrics writes the counters in the Prometheus text format.
func (e *events) writeMetrics(w io.Writer) {
	e.mu.Lock()
	keys := make([][2]string, 0, len(e.counts))
	for k := range e.counts {
		keys = append(keys, k)
	}
	counts := make(map[[2]string]uint64, len(e.counts))
	for k, v := range e.counts {
		counts[k] = v
	}
	retries, parked := maps.Clone(e.retries), maps.Clone(e.parked)
	routed, unrouted := e.messages[true], e.messages[false]
	e.mu.Unlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	_, _ = io.WriteString(w, "# HELP notification_catcher_dispatch_total Messages per output and result (sent, failed, filtered, dry-run).\n")
	_, _ = io.WriteString(w, "# TYPE notification_catcher_dispatch_total counter\n")
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "notification_catcher_dispatch_total{output=\"%s\",result=\"%s\"} %d\n",
			escapeLabel(k[0]), escapeLabel(k[1]), counts[k])
	}
	writeByOutput(w, "notification_catcher_retries_total", "Sends repeated after a transient failure, per output.", retries)
	writeByOutput(w, "notification_catcher_dead_letter_total", "Sends parked in the dead-letter stream after their retries, per output.", parked)
	_, _ = io.WriteString(w, "# HELP notification_catcher_messages_total Dispatched messages, by whether any output matched.\n")
	_, _ = io.WriteString(w, "# TYPE notification_catcher_messages_total counter\n")
	_, _ = fmt.Fprintf(w, "notification_catcher_messages_total{routed=\"true\"} %d\n", routed)
	_, _ = fmt.Fprintf(w, "notification_catcher_messages_total{routed=\"false\"} %d\n", unrouted)
}

func writeByOutput(w io.Writer, name, help string, m map[string]uint64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	for _, o := range slices.Sorted(maps.Keys(m)) {
		_, _ = fmt.Fprintf(w, "%s{output=\"%s\"} %d\n", name, escapeLabel(o), m[o])
	}
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }

// Record adds a dispatched message to the event log and the counters. Pass
// it as notify.Dispatcher.OnDispatch.
func (s *Server) Record(rec notify.Record) { s.events.record(rec) }

func (s *Server) eventQuery(r *http.Request) (EventQuery, error) {
	v := r.URL.Query()
	q := EventQuery{Result: v.Get("result"), ObjectID: strings.TrimSpace(v.Get("objectId"))}
	switch q.Result {
	case "", "unrouted", notify.ResultSent, notify.ResultFailed, notify.ResultFiltered, notify.ResultDryRun:
	default:
		return q, fmt.Errorf("result must be one of %s, unrouted", strings.Join(notify.Results, ", "))
	}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 0 {
			return q, fmt.Errorf("limit must be a non-negative integer")
		}
		q.Limit = n
	}
	return q, nil
}

func (s *Server) handleEventsJSON(w http.ResponseWriter, r *http.Request) {
	q, err := s.eventQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.events.list(q))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.events.writeMetrics(w)
	s.writeDeadLetterPending(r.Context(), w)
}
