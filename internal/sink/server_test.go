package sink

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
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
	ts := httptest.NewServer(NewServer(store, Options{Token: token}).Handler())
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

	n := notify.NewTeamsNotifier(ts.URL+"/teams/platform", nil)
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

func TestTeamsAnswers202(t *testing.T) {
	ts, _ := newTestServer(t, "")
	body, _ := json.Marshal(notify.BuildEnvelope(homerun.Message{Title: "x"}))
	resp, err := http.Post(ts.URL+"/teams/c", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
}

func TestTeamsRejectsNonTeamsShapes(t *testing.T) {
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
			resp, err := http.Post(ts.URL+"/teams/c", "application/json", strings.NewReader(body))
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

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/faults/msteams/flaky", strings.NewReader(`{"status":500,"count":2}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set fault: %d", resp.StatusCode)
	}

	n := notify.NewTeamsNotifier(ts.URL+"/teams/flaky", nil)
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
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/faults/msteams/slow", strings.NewReader(`{"delay":"200ms"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	n := notify.NewTeamsNotifier(ts.URL+"/teams/slow", client)
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

	n := notify.NewTeamsNotifier(ts.URL+"/teams/c?token=s3cret", nil)
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

	if err := notify.NewTeamsNotifier(ts.URL+"/teams/live", nil).Send(ctx, homerun.Message{Title: "streamed"}); err != nil {
		t.Fatal(err)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			var ev Event
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Post == nil || ev.Post.Title != "streamed" || ev.Post.Channel != "live" ||
				ev.Notification == nil || ev.Notification.ID != ev.Post.NotificationID {
				t.Errorf("event: %+v", ev)
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

// The catcher's generic WebhookNotifier posts to /webhook/{channel}; the
// homerun.Message it carries fills the summary fields.
func TestWebhookNotifierRoundTrip(t *testing.T) {
	ts, _ := newTestServer(t, "")

	n := notify.NewWebhookNotifier(ts.URL+"/webhook/slack-standin", "", nil, nil)
	if err := n.Send(context.Background(), homerun.Message{
		Title: "vm ready", Severity: "success", System: "request-vm", Author: "crossplane",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := notify.NewTeamsNotifier(ts.URL+"/teams/slack-standin", nil).Send(context.Background(), homerun.Message{Title: "same name"}); err != nil {
		t.Fatal(err)
	}

	got := listMessages(t, ts.URL+"/api/messages?kind=webhook")
	if len(got) != 1 {
		t.Fatalf("want 1 webhook post, got %d", len(got))
	}
	m := got[0]
	if m.Kind != KindWebhook || m.Channel != "slack-standin" || m.Title != "vm ready" ||
		m.Severity != "success" || m.System != "request-vm" || m.Facts["Author"] != "crossplane" || m.Envelope != nil {
		t.Errorf("unexpected post: %+v", m)
	}
	if got := listMessages(t, ts.URL+"/api/messages?channel=slack-standin"); len(got) != 2 {
		t.Errorf("channel across kinds: want 2, got %d", len(got))
	}
	if got := listMessages(t, ts.URL+"/api/messages?kind=msteams&fact=System=request-vm"); len(got) != 0 {
		t.Errorf("kind+fact: want 0, got %d", len(got))
	}
}

func TestWebhookAcceptsAnyJSON(t *testing.T) {
	ts, _ := newTestServer(t, "")
	for body, want := range map[string]int{
		`{"text":"from somewhere else"}`: http.StatusOK,
		`[1,2,3]`:                        http.StatusOK,
		`{`:                              http.StatusBadRequest,
	} {
		resp, err := http.Post(ts.URL+"/webhook/any", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", body, resp.StatusCode, want)
		}
	}
	if got := listMessages(t, ts.URL+"/api/messages?q=somewhere"); len(got) != 1 || got[0].Title != "" {
		t.Errorf("non-homerun JSON: %+v", got)
	}
}

func TestFaultIsPerKind(t *testing.T) {
	ts, _ := newTestServer(t, "")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/faults/webhook/c", strings.NewReader(`{"status":503}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if err := notify.NewWebhookNotifier(ts.URL+"/webhook/c", "", nil, nil).Send(context.Background(), homerun.Message{}); err == nil {
		t.Error("webhook/c: want 503")
	}
	if err := notify.NewTeamsNotifier(ts.URL+"/teams/c", nil).Send(context.Background(), homerun.Message{}); err != nil {
		t.Errorf("teams/c: %v", err)
	}

	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/faults/slack/c", strings.NewReader(`{"status":503}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown kind: %d, want 400", resp.StatusCode)
	}
}

func TestBasePath(t *testing.T) {
	ts := httptest.NewServer(NewServer(NewStore(0), Options{
		BasePath: "/notify",
		Build:    BuildInfo{Version: "v9.9.9", Commit: "0123456789abcdef"},
	}).Handler())
	defer ts.Close()

	if err := notify.NewTeamsNotifier(ts.URL+"/notify/teams/c", nil).Send(context.Background(), homerun.Message{Title: "x"}); err != nil {
		t.Fatalf("sink under base path: %v", err)
	}
	if got := listMessages(t, ts.URL+"/notify/api/messages"); len(got) != 1 {
		t.Errorf("api under base path: %d", len(got))
	}

	resp, err := http.Get(ts.URL + "/notify/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	for _, want := range []string{`data-base="/notify"`, `/notify/static/favicon.png`, "v9.9.9", "0123456"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("index missing %q", want)
		}
	}
	if strings.Contains(string(body), "0123456789") {
		t.Error("commit not shortened")
	}

	for path, want := range map[string]int{
		"/health":                    http.StatusOK,
		"/healthz":                   http.StatusOK,
		"/notify/static/favicon.png": http.StatusOK,
		"/teams/c":                   http.StatusNotFound,
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, resp.StatusCode, want)
		}
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = noRedirect.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != "/notify/" {
		t.Errorf("GET /: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestPortalTheme(t *testing.T) {
	ts, _ := newTestServer(t, "")
	get := func(prefix string) string {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
		if prefix != "" {
			req.Header.Set("X-Forwarded-Prefix", prefix)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if strings.Contains(get(""), "homerun2-tokens.css") {
		t.Error("tokens linked without portal")
	}
	if !strings.Contains(get("/notify"), "homerun2-tokens.css") {
		t.Error("tokens not linked behind portal")
	}
}

func TestParseBasePath(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "notify": "/notify", "/a/b/": "/a/b"} {
		got, err := ParseBasePath(in)
		if err != nil || got != want {
			t.Errorf("ParseBasePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/../x", "/a b", `/"x`} {
		if _, err := ParseBasePath(in); err == nil {
			t.Errorf("ParseBasePath(%q): want error", in)
		}
	}
}

func listNotifications(t *testing.T, url string) []Notification {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out []Notification
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// One catcher dispatch to a Teams and a webhook output is one notification
// with two channels; the inbox adds messages that went nowhere.
func TestNotificationsGroupDispatch(t *testing.T) {
	ts, _ := newTestServer(t, "")
	ctx := context.Background()

	cfg := &config.NotifyConfig{Outputs: []config.OutputConfig{
		{Name: "all", Type: "webhook", URL: ts.URL + "/inbox"},
		{Name: "teams", Type: "msteams", WebhookURL: ts.URL + "/teams/platform",
			Filters: config.OutputFilters{SeverityMin: "warning"},
			// custom facts without Severity: grouping falls back to fallbackText
			Card: config.CardConfig{Facts: []config.CardFactConfig{{Title: "Cluster", Tag: "cluster"}}}},
		{Name: "slack", Type: "webhook", URL: ts.URL + "/webhook/slack",
			Filters: config.OutputFilters{SeverityMin: "critical"}},
	}}
	d, err := notify.NewDispatcher(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	crit := homerun.Message{Title: "node down", Message: "node-3 NotReady", Severity: "critical", System: "kubernetes", Author: "alertmanager", Tags: "cluster=labda"}
	warn := homerun.Message{Title: "disk", Severity: "warning", System: "kubernetes"}
	info := homerun.Message{Title: "cluster built", Severity: "info", System: "git-pitcher"}
	for _, m := range []homerun.Message{crit, warn, info, crit} {
		for _, r := range d.Dispatch(ctx, m) {
			if r.Err != nil {
				t.Fatalf("%s: %v", r.Output, r.Err)
			}
		}
	}

	got := listNotifications(t, ts.URL+"/api/notifications")
	if len(got) != 4 {
		t.Fatalf("want 4 notifications (critical twice), got %d: %+v", len(got), got)
	}
	channels := func(n Notification) string {
		var cs []string
		for _, d := range n.Deliveries {
			cs = append(cs, d.Kind+"/"+d.Channel)
		}
		return strings.Join(cs, ",")
	}
	for i, want := range []struct {
		title, channels string
	}{
		{"node down", "msteams/platform,webhook/slack"}, // the repeat is its own notification
		{"cluster built", ""},
		{"disk", "msteams/platform"},
		{"node down", "msteams/platform,webhook/slack"},
	} {
		n := got[i]
		if n.Title != want.title || channels(n) != want.channels || !n.Inbox {
			t.Errorf("notification %d: %q %q inbox=%v, want %q %q", i, n.Title, channels(n), n.Inbox, want.title, want.channels)
		}
	}
	if n := got[0]; n.Author != "alertmanager" || n.Message != "node-3 NotReady" || n.Tags != "cluster=labda" {
		t.Errorf("fields from the inbox message: %+v", n)
	}

	posts := listMessages(t, ts.URL+"/api/messages?channel=platform")
	if len(posts) != 3 || posts[0].NotificationID != got[0].ID {
		t.Errorf("post → notification link: %+v", posts)
	}
	if got := listNotifications(t, ts.URL+"/api/notifications?q=disk&limit=5"); len(got) != 1 {
		t.Errorf("q filter: %d", len(got))
	}
}

// Without an inbox, Teams-only fields come from the card.
func TestNotificationFromCardOnly(t *testing.T) {
	ts, _ := newTestServer(t, "")
	msg := homerun.Message{Title: "t", Message: "b", Severity: "error", System: "s", Author: "a", URL: "https://x.example"}
	if err := notify.NewTeamsNotifier(ts.URL+"/teams/c", nil).Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	got := listNotifications(t, ts.URL+"/api/notifications")
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	n := got[0]
	if n.Title != "t" || n.Message != "b" || n.Severity != "error" || n.System != "s" || n.Author != "a" || n.URL != "https://x.example" || n.Inbox {
		t.Errorf("notification: %+v", n)
	}
}

func TestInboxRejectsNonMessages(t *testing.T) {
	ts, _ := newTestServer(t, "")
	for body, want := range map[string]int{
		`{"title":"x"}`: http.StatusOK,
		`{"text":"x"}`:  http.StatusBadRequest,
		`{`:             http.StatusBadRequest,
	} {
		resp, err := http.Post(ts.URL+"/inbox", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", body, resp.StatusCode, want)
		}
	}
}
