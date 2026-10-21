package teamsmock

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/config"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

func newTestServer(t *testing.T, token string) (*httptest.Server, *Store) {
	t.Helper()
	store := NewStore(0)
	ts := httptest.NewServer(NewServer(store, token).Handler())
	t.Cleanup(ts.Close)
	return ts, store
}

func listMessages(t *testing.T, url string) []Message {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	var out []Message
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The catcher's own TeamsNotifier posts to the mock, and the card is listed
// with its title, facts and severity style.
func TestTeamsNotifierRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t, "")

	n := notify.NewTeamsNotifier(ts.URL+"/webhook/platform", nil)
	msg := homerun.Message{
		Title:    "disk almost full",
		Message:  "/var at 91% (objectId 1700000000000-0)",
		Severity: "warning",
		System:   "kubernetes",
		Tags:     "infra,storage",
	}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := listMessages(t, ts.URL+"/api/messages?channel=platform")
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	m := got[0]
	if m.Title != "disk almost full" || m.Style != "warning" || m.Facts["System"] != "kubernetes" {
		t.Errorf("unexpected summary: %+v", m)
	}
	if !strings.HasPrefix(m.FallbackText, "[warning] disk almost full") {
		t.Errorf("fallbackText = %q", m.FallbackText)
	}

	if got := listMessages(t, ts.URL+"/api/messages?objectId=1700000000000-0"); len(got) != 1 {
		t.Errorf("objectId lookup: want 1, got %d", len(got))
	}
	if got := listMessages(t, ts.URL+"/api/messages?objectId=nope"); len(got) != 0 {
		t.Errorf("objectId miss: want 0, got %d", len(got))
	}
	if got := listMessages(t, ts.URL+"/api/messages?fact=system=KUBERNETES"); len(got) != 1 {
		t.Errorf("fact filter: want 1, got %d", len(got))
	}
	if got := listMessages(t, ts.URL+"/api/messages?fact=System=other"); len(got) != 0 {
		t.Errorf("fact miss: want 0, got %d", len(got))
	}
	if got := listMessages(t, ts.URL+"/api/messages?channel=other"); len(got) != 0 {
		t.Errorf("channel miss: want 0, got %d", len(got))
	}

	resp, err := http.Get(ts.URL + "/api/messages/" + m.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env models.TeamsEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Attachments[0].Content.Version != "1.4" {
		t.Errorf("raw envelope: %+v", env)
	}
}

func TestWebhookAnswers202(t *testing.T) {
	ts, _ := newTestServer(t, "")
	body, _ := json.Marshal(notify.BuildEnvelope(homerun.Message{Title: "x"}))
	resp, err := http.Post(ts.URL+"/webhook/c", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
}

func TestWebhookRejectsNonTeamsShapes(t *testing.T) {
	ts, store := newTestServer(t, "")
	cases := map[string]string{
		"not json":        `{`,
		"wrong type":      `{"type":"card","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard"}}]}`,
		"no attachments":  `{"type":"message","attachments":[]}`,
		"wrong content":   `{"type":"message","attachments":[{"contentType":"text/plain","content":{"type":"AdaptiveCard"}}]}`,
		"not a card":      `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"HeroCard"}}]}`,
		"generic webhook": `{"title":"x","severity":"info"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := http.Post(ts.URL+"/webhook/c", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
	if n := len(store.List(Query{})); n != 0 {
		t.Errorf("rejected posts were stored: %d", n)
	}
}

func TestFaultInjection(t *testing.T) {
	ts, store := newTestServer(t, "")

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/faults/flaky", strings.NewReader(`{"status":500,"count":2}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set fault: %d", resp.StatusCode)
	}

	n := notify.NewTeamsNotifier(ts.URL+"/webhook/flaky", nil)
	msg := homerun.Message{Title: "retry me"}
	for i := 0; i < 2; i++ {
		if err := n.Send(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("send %d: want 500 error, got %v", i, err)
		}
	}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatalf("fault should be used up: %v", err)
	}
	if got := len(store.List(Query{Channel: "flaky"})); got != 1 {
		t.Errorf("stored %d, want 1", got)
	}
}

func TestFaultDelay(t *testing.T) {
	ts, _ := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/faults/slow", strings.NewReader(`{"delay":"200ms"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	n := notify.NewTeamsNotifier(ts.URL+"/webhook/slow", client)
	if err := n.Send(context.Background(), homerun.Message{Title: "x"}); err == nil {
		t.Fatal("want timeout error")
	}
}

func TestRenderUsesCatcherBuilder(t *testing.T) {
	ts, _ := newTestServer(t, "")
	msg := homerun.Message{Title: "match", Severity: "success", Tags: "player_a=Ann,sets=3:1"}
	card := "facts:\n  - title: Player A\n    tag: player_a\n  - title: Sets\n    tag: sets\n"
	body, _ := json.Marshal(RenderRequest{Message: msg, Card: card})

	resp, err := http.Post(ts.URL+"/api/render", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got models.TeamsEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	want := notify.BuildEnvelopeWithCard(msg, config.CardConfig{Facts: []config.CardFactConfig{
		{Title: "Player A", Tag: "player_a"},
		{Title: "Sets", Tag: "sets"},
	}})
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Errorf("render mismatch:\n got %s\nwant %s", gb, wb)
	}
}

func TestRenderRejectsInvalidCard(t *testing.T) {
	ts, _ := newTestServer(t, "")
	body := `{"message":{"title":"x"},"card":"facts:\n  - title: Bad\n    field: nosuchfield\n"}`
	resp, err := http.Post(ts.URL+"/api/render", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestToken(t *testing.T) {
	ts, _ := newTestServer(t, "s3cret")

	for _, tc := range []struct {
		url    string
		header string
		want   int
	}{
		{ts.URL + "/healthz", "", http.StatusOK},
		{ts.URL + "/api/messages", "", http.StatusUnauthorized},
		{ts.URL + "/api/messages?token=wrong", "", http.StatusUnauthorized},
		{ts.URL + "/api/messages?token=s3cret", "", http.StatusOK},
		{ts.URL + "/api/messages", "Bearer s3cret", http.StatusOK},
		{ts.URL + "/", "", http.StatusUnauthorized},
		{ts.URL + "/?token=s3cret", "", http.StatusOK},
	} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s (%q): %d, want %d", tc.url, tc.header, resp.StatusCode, tc.want)
		}
	}

	n := notify.NewTeamsNotifier(ts.URL+"/webhook/c?token=s3cret", nil)
	if err := n.Send(context.Background(), homerun.Message{Title: "x"}); err != nil {
		t.Errorf("webhook with ?token: %v", err)
	}
}

func TestEventsStream(t *testing.T) {
	ts, _ := newTestServer(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, ": connected") {
		t.Fatalf("first line %q", line)
	}

	if err := notify.NewTeamsNotifier(ts.URL+"/webhook/live", nil).Send(ctx, homerun.Message{Title: "streamed"}); err != nil {
		t.Fatal(err)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var m Message
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				t.Fatal(err)
			}
			if m.Title != "streamed" || m.Channel != "live" {
				t.Errorf("event: %+v", m)
			}
			return
		}
	}
}

func TestIndexServed(t *testing.T) {
	ts, _ := newTestServer(t, "")
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET /: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
