package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRateLimitedCallIsRetriedOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"You are being rate limited.","retry_after":0.05}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := NewClient("app", "bot")
	c.BaseURL = srv.URL
	if err := c.EditOriginal(context.Background(), "itok", ResponseData{Content: "hi"}); err != nil {
		t.Fatalf("after one 429 the retry should succeed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestNetworkErrorsHideTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // every call now fails to connect
	c := NewClient("app", "bot")
	c.BaseURL = srv.URL
	hook := Webhook{ID: "123", Token: "SECRET-WEBHOOK-TOKEN"}
	for name, err := range map[string]error{
		"execute": c.ExecuteWebhook(context.Background(), hook, WebhookMessage{Content: "x"}),
		"delete":  c.DeleteWebhook(context.Background(), hook),
		"edit":    c.EditOriginal(context.Background(), "SECRET-INTERACTION-TOKEN", ResponseData{Content: "x"}),
	} {
		if err == nil {
			t.Fatalf("%s: expected a connection error", name)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%s: token leaked into the error: %v", name, err)
		}
		if !strings.Contains(err.Error(), "/webhooks/") {
			t.Fatalf("%s: error should still say which call failed: %v", name, err)
		}
	}
}
