package sink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

// caught wraps msg as the catcher hands it to the notifiers, without an
// objectId.
func caught(msg homerun.Message) models.CaughtMessage {
	return models.CaughtMessage{Message: msg}
}

func sendCard(t *testing.T, url string, msg homerun.Message) {
	t.Helper()
	if err := notify.NewTeamsNotifier(url, nil).Send(context.Background(), caught(msg)); err != nil {
		t.Fatal(err)
	}
}

func assertCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestAssertPostsMatches(t *testing.T) {
	ts, _ := newTestServer(t, "")
	sendCard(t, ts.URL+"/teams/platform", homerun.Message{
		Title: "cluster built", Message: "objectId 1700000000000-0",
		Severity: "success", System: "cluster-build-watch",
	})

	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	posts, err := c.AssertPosts(assertCtx(t, time.Second), Assertion{
		Kind: KindTeams, Channel: "platform", ObjectID: "1700000000000-0",
		Facts: map[string]string{"system": "CLUSTER-BUILD-WATCH"},
		Title: "built", Severity: "SUCCESS",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Title != "cluster built" {
		t.Fatalf("posts = %+v", posts)
	}
}

// A post that was selected but carries the wrong fact is listed with why.
func TestAssertPostsNearMiss(t *testing.T) {
	ts, _ := newTestServer(t, "")
	sendCard(t, ts.URL+"/teams/platform", homerun.Message{
		Title: "cluster built", Severity: "success", System: "request-vm",
	})

	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	_, err := c.AssertPosts(assertCtx(t, 100*time.Millisecond), Assertion{
		Channel: "platform",
		Facts:   map[string]string{"System": "cluster-build-watch", "Cluster": "x"},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		"no matching post (channel=platform)",
		`"cluster built"`,
		`fact System = "request-vm", want "cluster-build-watch"`,
		"no fact Cluster",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestAssertPostsNothingSelected(t *testing.T) {
	ts, _ := newTestServer(t, "")
	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	_, err := c.AssertPosts(assertCtx(t, 50*time.Millisecond), Assertion{ObjectID: "nope"})
	if err == nil || !strings.Contains(err.Error(), "no post was selected") {
		t.Fatalf("err = %v", err)
	}
}

// The post arrives while AssertPosts is polling.
func TestAssertPostsWaits(t *testing.T) {
	ts, _ := newTestServer(t, "")
	go func() {
		time.Sleep(50 * time.Millisecond)
		// not sendCard: t.Fatal must not run outside the test goroutine
		if err := notify.NewTeamsNotifier(ts.URL+"/teams/late", nil).Send(context.Background(),
			caught(homerun.Message{Title: "late", Severity: "info"})); err != nil {
			t.Error(err)
		}
	}()

	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	if _, err := c.AssertPosts(assertCtx(t, 2*time.Second), Assertion{Channel: "late"}); err != nil {
		t.Fatal(err)
	}
}

func TestAssertPostsCount(t *testing.T) {
	ts, _ := newTestServer(t, "")
	sendCard(t, ts.URL+"/teams/a", homerun.Message{Title: "one", Severity: "info"})

	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	_, err := c.AssertPosts(assertCtx(t, 50*time.Millisecond), Assertion{Channel: "a", Count: 2})
	if err == nil || !strings.Contains(err.Error(), "1 of 2 posts matched") {
		t.Fatalf("err = %v", err)
	}
	sendCard(t, ts.URL+"/teams/a", homerun.Message{Title: "two", Severity: "info"})
	posts, err := c.AssertPosts(assertCtx(t, time.Second), Assertion{Channel: "a", Count: 2})
	if err != nil || len(posts) != 2 || posts[0].Title != "two" {
		t.Fatalf("posts = %+v, err = %v", posts, err)
	}
}

// A wrong token fails at once instead of polling until the timeout.
func TestAssertPostsToken(t *testing.T) {
	ts, _ := newTestServer(t, "s3cret")
	sendCard(t, ts.URL+"/teams/a?token=s3cret", homerun.Message{Title: "x", Severity: "info"})

	start := time.Now()
	c := &AssertClient{BaseURL: ts.URL, Token: "wrong"}
	if _, err := c.AssertPosts(assertCtx(t, 5*time.Second), Assertion{}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a 401 should not be retried")
	}

	c.Token = "s3cret"
	if _, err := c.AssertPosts(assertCtx(t, time.Second), Assertion{}); err != nil {
		t.Fatal(err)
	}
}

// An unreachable or failing sink is retried, and the timeout says so.
func TestAssertPostsRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	_, err := c.AssertPosts(assertCtx(t, 100*time.Millisecond), Assertion{})
	if err == nil || !strings.Contains(err.Error(), "no poll succeeded") || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("calls = %d, want retries", n)
	}
}

func TestAssertPostsSince(t *testing.T) {
	ts, _ := newTestServer(t, "")
	sendCard(t, ts.URL+"/teams/a", homerun.Message{Title: "old", Severity: "info"})

	c := &AssertClient{BaseURL: ts.URL, Interval: 10 * time.Millisecond}
	_, err := c.AssertPosts(assertCtx(t, 50*time.Millisecond), Assertion{Since: time.Now().Add(time.Second)})
	if err == nil {
		t.Fatal("a post from before since matched")
	}
}
