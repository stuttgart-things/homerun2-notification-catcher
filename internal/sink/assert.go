package sink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Assertion is what AssertPosts waits for (#62). Selection fields narrow the
// posts on the sink's side (GET /api/messages); expectations are checked
// here, so a post that was selected but expected wrong is reported as a near
// miss instead of not at all.
type Assertion struct {
	// Selection
	Kind     string
	Channel  string
	ObjectID string // substring of the posted body
	Contains string // substring of the posted body
	Since    time.Time

	// Expectations
	Facts    map[string]string // fact title -> value, case-insensitive
	Title    string            // substring of the title, case-insensitive
	Severity string            // case-insensitive
	Count    int               // at least this many matching posts; 0 means 1
}

// AssertClient polls a sink's API.
type AssertClient struct {
	BaseURL  string // e.g. http://sink:8080/notify, without a trailing slash
	Token    string // SINK_TOKEN, sent as a bearer token
	HTTP     *http.Client
	Interval time.Duration // between polls; 0 means 1s
}

// nearMisses caps how many selected but mismatching posts a failure lists.
const nearMisses = 5

// AssertPosts polls until Count posts match a, and returns them newest
// first. It fails when ctx ends first, listing the near misses, or at once
// when the sink rejects the request (400, 401, 404): retrying will not fix
// those. Unreachable sinks and 5xx are retried.
func (c *AssertClient) AssertPosts(ctx context.Context, a Assertion) ([]*Message, error) {
	if a.ObjectID != "" && a.Contains != "" {
		return nil, errors.New("use either an objectId or contains")
	}
	want := max(a.Count, 1)
	interval := c.Interval
	if interval <= 0 {
		interval = time.Second
	}
	reqURL := c.BaseURL + "/api/messages?" + a.query().Encode()

	var (
		lastErr error
		misses  []string
		polls   int
	)
	for {
		posts, err := c.list(ctx, reqURL)
		var perm *permanentError
		switch {
		case errors.As(err, &perm):
			return nil, perm.err
		case err != nil:
			lastErr = err
		default:
			polls++
			lastErr = nil
			var matched []*Message
			misses = misses[:0]
			for _, m := range posts {
				if why := a.mismatch(m); why == "" {
					matched = append(matched, m)
				} else if len(misses) < nearMisses {
					misses = append(misses, Describe(m)+": "+why)
				}
			}
			if len(matched) >= want {
				return matched, nil
			}
			if len(matched) > 0 {
				misses = append([]string{fmt.Sprintf("%d of %d posts matched", len(matched), want)}, misses...)
			}
		}

		select {
		case <-ctx.Done():
			return nil, a.timeoutError(polls, lastErr, misses)
		case <-time.After(interval):
		}
	}
}

func (a Assertion) query() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("kind", a.Kind)
	set("channel", a.Channel)
	set("objectId", a.ObjectID)
	set("q", a.Contains)
	if !a.Since.IsZero() {
		v.Set("since", a.Since.UTC().Format(time.RFC3339Nano))
	}
	return v
}

// mismatch says why m fails the expectations, "" when it meets them.
func (a Assertion) mismatch(m *Message) string {
	var why []string
	for title, value := range a.Facts {
		got, ok := lookupFact(m.Facts, title)
		switch {
		case !ok:
			why = append(why, fmt.Sprintf("no fact %s", title))
		case !strings.EqualFold(got, value):
			why = append(why, fmt.Sprintf("fact %s = %q, want %q", title, got, value))
		}
	}
	if a.Title != "" && !strings.Contains(strings.ToLower(m.Title), strings.ToLower(a.Title)) {
		why = append(why, fmt.Sprintf("title %q does not contain %q", m.Title, a.Title))
	}
	if a.Severity != "" && !strings.EqualFold(m.Severity, a.Severity) {
		why = append(why, fmt.Sprintf("severity %q, want %q", m.Severity, a.Severity))
	}
	return strings.Join(why, "; ")
}

func (a Assertion) timeoutError(polls int, lastErr error, misses []string) error {
	var b strings.Builder
	b.WriteString("no matching post")
	if sel := a.query().Encode(); sel != "" {
		b.WriteString(" (" + sel + ")")
	}
	switch {
	case polls == 0 && lastErr != nil:
		fmt.Fprintf(&b, ": no poll succeeded: %v", lastErr)
		return errors.New(b.String())
	case lastErr != nil:
		fmt.Fprintf(&b, "; last poll failed: %v", lastErr)
	}
	if len(misses) == 0 {
		b.WriteString("; no post was selected")
	}
	for _, m := range misses {
		b.WriteString("\n  " + m)
	}
	return errors.New(b.String())
}

// permanentError is a sink answer that retrying will not change.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (c *AssertClient) list(ctx context.Context, reqURL string) ([]*Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &permanentError{err}
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("GET %s: %s: %s", reqURL, resp.Status, strings.TrimSpace(string(body)))
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return nil, err
		}
		return nil, &permanentError{err}
	}
	var posts []*Message
	if err := json.NewDecoder(resp.Body).Decode(&posts); err != nil {
		return nil, fmt.Errorf("GET %s: decoding: %w", reqURL, err)
	}
	return posts, nil
}

func lookupFact(facts map[string]string, title string) (string, bool) {
	for t, v := range facts {
		if strings.EqualFold(t, title) {
			return v, true
		}
	}
	return "", false
}

// Describe names a post in assertion output.
func Describe(m *Message) string {
	s := fmt.Sprintf("post %s %s/%s %q", m.ID, m.Kind, m.Channel, m.Title)
	if m.Severity != "" {
		s += " [" + m.Severity + "]"
	}
	return s
}
