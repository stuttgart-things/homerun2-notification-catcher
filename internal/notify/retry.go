package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// StatusError is a non-2xx answer of an output's endpoint.
type StatusError struct {
	StatusCode int
	// RetryAfter is the endpoint's Retry-After, 0 when it sent none.
	RetryAfter time.Duration
	msg        string
}

func (e *StatusError) Error() string { return e.msg }

func newStatusError(resp *http.Response, msg string) error {
	return &StatusError{
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		msg:        msg,
	}
}

// parseRetryAfter reads a Retry-After header: delay seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		if s > 0 {
			return time.Duration(s) * time.Second
		}
		return 0
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// permanentError marks a failure that repeating the send cannot fix: an
// empty URL, a payload that does not marshal.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err} }

// Retryable reports whether a failed send may succeed when repeated (#68):
// connection errors and timeouts, 408, 425, 429 and 5xx. Any other status
// (a wrong URL, a rejected payload) and a canceled context are final.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var pe permanentError
	if errors.As(err, &pe) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		c := se.StatusCode
		return c == http.StatusRequestTimeout || c == http.StatusTooEarly ||
			c == http.StatusTooManyRequests || c >= 500
	}
	return true
}

// DeadLetter parks a message an output could not deliver, so a failed send
// is kept instead of lost (#68). The catcher's is a Redis stream.
type DeadLetter interface {
	Park(ctx context.Context, e DeadLetterEntry) error
}

// DeadLetterEntry is one undeliverable message of one output.
type DeadLetterEntry struct {
	Output   string
	Message  models.CaughtMessage
	Err      error
	Attempts int
	At       time.Time
}

// ParkedEntry is one entry of the dead-letter stream as read back: for the
// UI's Dead letter tab and `notification-catcher replay` (#70).
type ParkedEntry struct {
	// ID is the entry's id in the dead-letter stream.
	ID       string          `json:"id"`
	Output   string          `json:"output"`
	ObjectID string          `json:"objectId,omitempty"`
	StreamID string          `json:"streamId,omitempty"`
	Error    string          `json:"error,omitempty"`
	Attempts int             `json:"attempts"`
	FailedAt time.Time       `json:"failedAt"`
	Message  homerun.Message `json:"message"`
}

// Caught is the parked message as the notifiers take it.
func (e ParkedEntry) Caught() models.CaughtMessage {
	return models.CaughtMessage{Message: e.Message, ObjectID: e.ObjectID, StreamID: e.StreamID}
}

// parkTimeout bounds one dead-letter write. It does not use the dispatch
// context: that one may be what ran out.
const parkTimeout = 5 * time.Second

// send delivers msg to o and retries transient failures with exponential
// backoff. It gives up when the attempts are used up, the error is final,
// the next wait does not fit into ctx's deadline, or the dispatcher stops.
func (d *Dispatcher) send(ctx context.Context, o boundOutput, msg models.CaughtMessage) (int, error) {
	wait := o.retry.Backoff
	for attempt := 1; ; attempt++ {
		err := o.notifier.Send(ctx, msg)
		if err == nil || attempt >= o.retry.Attempts || !Retryable(err) || ctx.Err() != nil {
			return attempt, err
		}
		pause := wait
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > pause {
			pause = se.RetryAfter
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < pause {
			return attempt, err
		}
		slog.Warn("notifier failed, retrying",
			"output", o.name, "objectId", msg.ObjectID, "attempt", attempt, "retry_in", pause.String(), "error", err)
		t := time.NewTimer(pause)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return attempt, err
		case <-d.stop:
			t.Stop()
			return attempt, err
		}
		wait = min(wait*2, config.MaxRetryBackoff)
	}
}

// SendTo delivers msg to the output named name, with its retry policy and
// without its filters: the message matched them when it was dispatched.
// Used to replay a parked send (#70); a failure is not parked again.
func (d *Dispatcher) SendTo(ctx context.Context, name string, msg models.CaughtMessage) (int, error) {
	for _, o := range d.outputs {
		if o.name == name {
			return d.send(ctx, o, msg)
		}
	}
	return 0, permanent(fmt.Errorf("no output %q in the config", name))
}

// park hands a failed send to the dead letter, when one is set.
func (d *Dispatcher) park(o boundOutput, msg models.CaughtMessage, attempts int, sendErr error) (bool, error) {
	if d.DeadLetter == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), parkTimeout)
	defer cancel()
	err := d.DeadLetter.Park(ctx, DeadLetterEntry{
		Output: o.name, Message: msg, Err: sendErr, Attempts: attempts, At: time.Now().UTC(),
	})
	if err != nil {
		slog.Error("dead-letter failed", "output", o.name, "objectId", msg.ObjectID, "error", err)
		return false, err
	}
	return true, nil
}

// Stop ends pending backoff waits: a failed output is parked right away
// instead of retried, so shutdown is not held up by an outage. A send in
// flight still finishes. Call it once, on SIGTERM.
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() { close(d.stop) })
}

// Undelivered joins the dead-letter failures in results: a message whose
// failed send could not be parked either. The catcher does not ack it, so
// it is delivered again (#68).
func Undelivered(results []DispatchResult) error {
	var errs []error
	for _, r := range results {
		if r.ParkErr != nil {
			errs = append(errs, r.ParkErr)
		}
	}
	return errors.Join(errs...)
}
