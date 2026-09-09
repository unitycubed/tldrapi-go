package tldrapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mockServer builds an httptest.Server whose handler is `h`, and a
// Client pointed at it (with retries=1 to keep tests fast).
func mockServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	c, err := NewClient(ClientOptions{
		RapidAPIKey: "test-key",
		BaseURL:     srv.URL,
		Retries:     -1, // disable retries — tests want deterministic behavior
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return srv, c
}

func TestNewClient_RequiresKey(t *testing.T) {
	_, err := NewClient(ClientOptions{})
	if err == nil {
		t.Fatal("expected error for empty RapidAPIKey")
	}
}

func TestSummarize_Success(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summarize" {
			t.Errorf("path = %q, want /summarize", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("X-RapidAPI-Key"); got != "test-key" {
			t.Errorf("X-RapidAPI-Key = %q, want test-key", got)
		}
		if got := r.Header.Get("X-RapidAPI-Host"); got != DefaultRapidAPIHost {
			t.Errorf("X-RapidAPI-Host = %q, want %s", got, DefaultRapidAPIHost)
		}
		if got := r.Header.Get("X-Quality"); got != "quick" {
			t.Errorf("X-Quality = %q, want quick", got)
		}
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		if parsed["input_text"] != "hello" {
			t.Errorf("input_text = %v, want hello", parsed["input_text"])
		}
		w.Header().Set("X-Request-Id", "req-123")
		w.Header().Set("X-Credits-Charged", "1")
		w.Header().Set("X-Credits-Remaining", "99")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"summary": "hi.",
			"session_id": "sess-1",
			"usage": {"input_tokens": 5, "output_tokens": 3, "total_cost": 0.001, "model_used": "openrouter-llama-3.1-8b"}
		}`))
	})
	defer srv.Close()

	res, err := c.Summarize(context.Background(), "hello", SummarizeOptions{Tier: TierQuick})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res.Summary != "hi." {
		t.Errorf("Summary = %q, want hi.", res.Summary)
	}
	if res.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", res.SessionID)
	}
	if res.Usage.InputTokens != 5 {
		t.Errorf("InputTokens = %d, want 5", res.Usage.InputTokens)
	}
	if res.Usage.ModelUsed != "openrouter-llama-3.1-8b" {
		t.Errorf("ModelUsed = %q, want openrouter-llama-3.1-8b", res.Usage.ModelUsed)
	}
	if res.RequestID != "req-123" {
		t.Errorf("RequestID = %q, want req-123", res.RequestID)
	}
	if res.Credits.Remaining != "99" {
		t.Errorf("Credits.Remaining = %q, want 99", res.Credits.Remaining)
	}
}

func TestSummarize_EmptyInput(t *testing.T) {
	c, _ := NewClient(ClientOptions{RapidAPIKey: "k"})
	_, err := c.Summarize(context.Background(), "", SummarizeOptions{})
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected non-empty error, got %v", err)
	}
}

func TestSummarize_InvalidTier(t *testing.T) {
	c, _ := NewClient(ClientOptions{RapidAPIKey: "k"})
	_, err := c.Summarize(context.Background(), "x", SummarizeOptions{Tier: "purple"})
	if err == nil || !strings.Contains(err.Error(), "invalid tier") {
		t.Fatalf("expected invalid-tier error, got %v", err)
	}
}

func TestSummarize_AuthError(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_key","message":"bad key"}`))
	})
	defer srv.Close()

	_, err := c.Summarize(context.Background(), "x", SummarizeOptions{})
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("want AuthenticationError, got %T: %v", err, err)
	}
	if authErr.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", authErr.StatusCode)
	}
}

func TestSummarize_InsufficientCredits(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		_, _ = w.Write([]byte(`{"error":"insufficient_credits","top_up_url":"https://rapidapi.com/x"}`))
	})
	defer srv.Close()

	_, err := c.Summarize(context.Background(), "x", SummarizeOptions{})
	var e *InsufficientCreditsError
	if !errors.As(err, &e) {
		t.Fatalf("want InsufficientCreditsError, got %T: %v", err, err)
	}
	if e.ResponseBody["top_up_url"] != "https://rapidapi.com/x" {
		t.Errorf("top_up_url not preserved: %v", e.ResponseBody)
	}
}

func TestSummarize_RateLimit(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"rate_limited","message":"slow down"}`))
	})
	defer srv.Close()

	_, err := c.Summarize(context.Background(), "x", SummarizeOptions{})
	var e *RateLimitError
	if !errors.As(err, &e) {
		t.Fatalf("want RateLimitError, got %T: %v", err, err)
	}
	if e.RetryAfterSeconds != 42 {
		t.Errorf("RetryAfterSeconds = %d, want 42", e.RetryAfterSeconds)
	}
}

func TestSummarize_LanguageNotSupported(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error_code":"language_not_supported","message":"nope"}`))
	})
	defer srv.Close()

	_, err := c.Summarize(context.Background(), "x", SummarizeOptions{})
	var e *LanguageNotSupportedError
	if !errors.As(err, &e) {
		t.Fatalf("want LanguageNotSupportedError, got %T: %v", err, err)
	}
}

func TestSummarize_ServerError_RetryThenSucceed(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"try again"}`))
			return
		}
		_, _ = w.Write([]byte(`{"summary":"ok","session_id":"s","usage":{}}`))
	}))
	defer srv.Close()
	c, _ := NewClient(ClientOptions{
		RapidAPIKey: "k", BaseURL: srv.URL, Retries: 3, Timeout: 5 * time.Second,
	})
	res, err := c.Summarize(context.Background(), "x", SummarizeOptions{})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if res.Summary != "ok" {
		t.Errorf("Summary = %q, want ok", res.Summary)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("calls = %d, want 3", atomic.LoadInt32(&calls))
	}
}

func TestSummarize_ClientErrorNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()
	c, _ := NewClient(ClientOptions{RapidAPIKey: "k", BaseURL: srv.URL, Retries: 5, Timeout: 5 * time.Second})
	_, err := c.Summarize(context.Background(), "x", SummarizeOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1 (4xx must not retry)", atomic.LoadInt32(&calls))
	}
}

func TestRates(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rates" || r.Method != "GET" {
			t.Errorf("path=%s method=%s", r.URL.Path, r.Method)
		}
		_, _ = w.Write([]byte(`{"quick":1,"standard":5,"deep":30,"premium":110,"ultra":400,"updated_at":"2026-09-01"}`))
	})
	defer srv.Close()
	rates, err := c.Rates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rates.Quick != 1 || rates.Ultra != 400 || rates.UpdatedAt != "2026-09-01" {
		t.Errorf("unexpected rates: %+v", rates)
	}
}

func TestUsage(t *testing.T) {
	srv, c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"period":"month","calls":42,"credits_charged":210,"credits_remaining":790}`))
	})
	defer srv.Close()
	u, err := c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.Calls != 42 || u.CreditsRemaining != 790 {
		t.Errorf("unexpected usage: %+v", u)
	}
}

func TestContext_Cancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c, _ := NewClient(ClientOptions{RapidAPIKey: "k", BaseURL: srv.URL, Retries: -1, Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Summarize(ctx, "x", SummarizeOptions{})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
