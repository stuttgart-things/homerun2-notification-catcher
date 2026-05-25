package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v3"
)

func TestWebhookNotifier_PostsMessageJSON(t *testing.T) {
	var (
		gotMethod  string
		gotCT      string
		gotAuth    string
		gotBody    []byte
		gotMessage homerun.Message
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(gotBody, &gotMessage)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, "", map[string]string{"Authorization": "Bearer abc"}, &http.Client{Timeout: 2 * time.Second})
	msg := homerun.Message{Title: "boom", Severity: "critical", System: "kubernetes"}
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST (default)", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotAuth != "Bearer abc" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotMessage.Title != "boom" || gotMessage.Severity != "critical" {
		t.Errorf("body didn't round-trip homerun.Message: %+v\nraw: %s", gotMessage, gotBody)
	}
}

func TestWebhookNotifier_CustomMethod(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, "PUT", nil, &http.Client{Timeout: 2 * time.Second})
	_ = n.Send(context.Background(), homerun.Message{Title: "x"})
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

func TestWebhookNotifier_ErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream is angry"))
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, "", nil, &http.Client{Timeout: 2 * time.Second})
	err := n.Send(context.Background(), homerun.Message{Title: "x"})
	if err == nil {
		t.Fatal("expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "upstream is angry") {
		t.Errorf("error should mention status and body, got %v", err)
	}
}

func TestWebhookNotifier_RejectsEmptyURL(t *testing.T) {
	n := NewWebhookNotifier("", "", nil, nil)
	err := n.Send(context.Background(), homerun.Message{Title: "x"})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestWebhookNotifier_ImplementsNotifier(t *testing.T) {
	var _ Notifier = (*WebhookNotifier)(nil)
}
