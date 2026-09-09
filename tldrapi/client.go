// Package tldrapi is the official Go SDK for the TLDRapi text-summarization
// API. Zero-dependency: uses net/http and encoding/json from the standard
// library only.
//
// Auth at launch is RapidAPI-only. Subscribe to the TLDRapi listing on
// RapidAPI, get an X-RapidAPI-Key, and pass it to NewClient. Direct
// signup (bypassing RapidAPI) is a post-launch feature and will get a
// second constructor when it ships.
//
// Basic usage:
//
//	c, err := tldrapi.NewClient(tldrapi.ClientOptions{
//	    RapidAPIKey: os.Getenv("TLDRAPI_RAPIDAPI_KEY"),
//	})
//	if err != nil { log.Fatal(err) }
//	res, err := c.Summarize(ctx, "Long text goes here.", tldrapi.SummarizeOptions{
//	    Tier: tldrapi.TierQuick,
//	})
//	if err != nil { log.Fatal(err) }
//	fmt.Println(res.Summary)
//
// Retries: 5xx and network errors retry 3× with exp backoff + jitter
// by default. 4xx and 429 are NEVER auto-retried (would burn credits
// or worsen a throttle). Configure via ClientOptions.Retries.
package tldrapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Version tracks the SDK version. Emitted in User-Agent.
const Version = "0.1.0"

// DefaultRapidAPIHost is the RapidAPI hostname for TLDRapi. Swap in
// ClientOptions.RapidAPIHost only if the listing is renamed.
const DefaultRapidAPIHost = "tldrapi.p.rapidapi.com"

const (
	defaultTimeout   = 60 * time.Second
	defaultRetries   = 3
	retryBaseBackoff = 500 * time.Millisecond
)

// ClientOptions configures NewClient. RapidAPIKey is the only required
// field; the rest default to sensible values for production usage.
type ClientOptions struct {
	// RapidAPIKey is the X-RapidAPI-Key from your RapidAPI account.
	// Required. Get it by subscribing to the TLDRapi listing on RapidAPI.
	RapidAPIKey string
	// RapidAPIHost lets you override the RapidAPI hostname. Rarely
	// needed — only if the listing is renamed. Defaults to
	// tldrapi.p.rapidapi.com.
	RapidAPIHost string
	// BaseURL lets you override the base URL entirely (staging,
	// mock server, custom domain). Defaults to https://<RapidAPIHost>.
	BaseURL string
	// Timeout is the per-request timeout. Defaults to 60s.
	Timeout time.Duration
	// Retries is how many times to retry 5xx + network errors before
	// giving up. Defaults to 3. Set to 0 to disable retries entirely.
	// 4xx and 429 are NEVER retried regardless of this setting.
	Retries int
	// HTTPClient lets callers inject a custom *http.Client (proxy,
	// custom TLS config, etc.). Defaults to http.DefaultClient with
	// Timeout set.
	HTTPClient *http.Client
	// UserAgent overrides the default User-Agent string. Prefixed by
	// "tldrapi-go/<version>" if empty.
	UserAgent string
}

// Client is a thread-safe TLDRapi API client. Create once, reuse.
type Client struct {
	rapidAPIKey  string
	rapidAPIHost string
	baseURL      string
	timeout      time.Duration
	retries      int
	http         *http.Client
	userAgent    string
}

// NewClient constructs a Client from ClientOptions. Returns an error
// if RapidAPIKey is empty (the only fatal validation).
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.RapidAPIKey == "" {
		return nil, errors.New("tldrapi: RapidAPIKey is required (subscribe on RapidAPI to obtain one)")
	}
	host := opts.RapidAPIHost
	if host == "" {
		host = DefaultRapidAPIHost
	}
	base := opts.BaseURL
	if base == "" {
		base = "https://" + host
	}
	base = strings.TrimRight(base, "/")
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	retries := opts.Retries
	if retries < 0 {
		retries = 0
	}
	if opts.Retries == 0 {
		// distinguish "user set 0" from "user left zero-value"?
		// Zero value = default. To explicitly disable, set -1 (we clamp above).
		retries = defaultRetries
	}
	httpc := opts.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: timeout}
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "tldrapi-go/" + Version
	}
	return &Client{
		rapidAPIKey:  opts.RapidAPIKey,
		rapidAPIHost: host,
		baseURL:      base,
		timeout:      timeout,
		retries:      retries,
		http:         httpc,
		userAgent:    ua,
	}, nil
}

// Summarize sends text to /summarize and returns the summary plus cost + session info.
// Context controls cancellation; pass context.Background() for no cancellation.
func (c *Client) Summarize(ctx context.Context, inputText string, opts SummarizeOptions) (*SummarizeResult, error) {
	if inputText == "" {
		return nil, errors.New("tldrapi: inputText must be non-empty")
	}
	if opts.Tier != "" && !validTiers[opts.Tier] {
		return nil, fmt.Errorf("tldrapi: invalid tier %q (must be one of quick, standard, deep, premium, ultra)", opts.Tier)
	}

	body := map[string]any{"input_text": inputText}
	if opts.SessionID != "" {
		body["session_id"] = opts.SessionID
	}
	if opts.ModelAlias != "" {
		body["model_alias"] = opts.ModelAlias
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tldrapi: marshal request: %w", err)
	}

	headers := c.buildHeaders(opts.Tier, opts.ExtraHeaders)
	if opts.AllowOverage {
		headers["X-Allow-Overage"] = "true"
	}

	rawBody, respHeaders, err := c.request(ctx, "POST", "/summarize", payload, headers, opts.TimeoutSeconds)
	if err != nil {
		return nil, err
	}

	var respJSON map[string]any
	if err := json.Unmarshal(rawBody, &respJSON); err != nil {
		return nil, &ServerError{APIError: &APIError{
			Message:    fmt.Sprintf("unexpected non-JSON summarize response: %s", truncateForLog(rawBody, 200)),
			StatusCode: 200,
			RequestID:  respHeaders.Get("X-Request-Id"),
		}}
	}
	return buildSummarizeResult(respJSON, respHeaders), nil
}

// Rates fetches /rates — the credit-per-call table for each quality tier.
func (c *Client) Rates(ctx context.Context) (*Rates, error) {
	rawBody, _, err := c.request(ctx, "GET", "/rates", nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		m = map[string]any{}
	}
	toInt := func(k string, dflt int) int {
		if v, ok := m[k].(float64); ok {
			return int(v)
		}
		return dflt
	}
	updated, _ := m["updated_at"].(string)
	return &Rates{
		Quick:     toInt("quick", 1),
		Standard:  toInt("standard", 5),
		Deep:      toInt("deep", 30),
		Premium:   toInt("premium", 110),
		Ultra:     toInt("ultra", 400),
		UpdatedAt: updated,
		Raw:       m,
	}, nil
}

// Usage fetches /usage — the customer's aggregate usage stats.
func (c *Client) Usage(ctx context.Context) (*UsageStats, error) {
	rawBody, _, err := c.request(ctx, "GET", "/usage", nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		m = map[string]any{}
	}
	toInt := func(k string) int {
		if v, ok := m[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	period, _ := m["period"].(string)
	return &UsageStats{
		Period:           period,
		Calls:            toInt("calls"),
		CreditsCharged:   toInt("credits_charged"),
		CreditsRemaining: toInt("credits_remaining"),
		Raw:              m,
	}, nil
}

// buildHeaders assembles the base header set for every request.
func (c *Client) buildHeaders(tier QualityTier, extra map[string]string) map[string]string {
	h := map[string]string{
		"Content-Type":     "application/json",
		"User-Agent":       c.userAgent,
		"X-RapidAPI-Key":   c.rapidAPIKey,
		"X-RapidAPI-Host":  c.rapidAPIHost,
	}
	if tier != "" {
		h["X-Quality"] = string(tier)
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// request is the retry-aware transport core. Returns raw body + response
// headers on success, typed error on any non-2xx or transport failure.
func (c *Client) request(ctx context.Context, method, path string, body []byte, headers map[string]string, timeoutSeconds int) ([]byte, http.Header, error) {
	if _, err := url.Parse(c.baseURL + path); err != nil {
		return nil, nil, fmt.Errorf("tldrapi: invalid URL: %w", err)
	}
	timeout := c.timeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+path, reqBody)
		if err != nil {
			cancel()
			return nil, nil, fmt.Errorf("tldrapi: build request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			// Distinguish timeout from generic network error via ctx.
			if reqCtx.Err() == context.DeadlineExceeded {
				lastErr = &TimeoutError{APIError: &APIError{
					Message: fmt.Sprintf("request timed out after %s", timeout),
				}}
			} else {
				lastErr = &NetworkError{
					APIError: &APIError{Message: err.Error()},
					Cause: err,
				}
			}
			if attempt < c.retries {
				sleepBackoff(attempt)
				continue
			}
			return nil, nil, lastErr
		}

		rawBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = &NetworkError{
				APIError: &APIError{Message: "read response body: " + readErr.Error()},
				Cause: readErr,
			}
			if attempt < c.retries {
				sleepBackoff(attempt)
				continue
			}
			return nil, nil, lastErr
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return rawBody, resp.Header, nil
		}
		if resp.StatusCode >= 500 && attempt < c.retries {
			sleepBackoff(attempt)
			continue
		}
		// 4xx (including 429) and exhausted-retry 5xx: raise typed error.
		return nil, resp.Header, errorFromResponse(
			resp.StatusCode, rawBody,
			resp.Header.Get("X-Request-Id"),
			parseRetryAfter(resp.Header.Get("Retry-After")),
		)
	}
	// Unreachable — the loop always returns.
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, errors.New("tldrapi: unknown transport failure")
}

func sleepBackoff(attempt int) {
	// 500ms * 2^attempt + up to 200ms jitter. Attempt 0 → ~500-700ms.
	base := retryBaseBackoff * (1 << attempt)
	jitter := time.Duration(rand.Int63n(int64(200 * time.Millisecond)))
	time.Sleep(base + jitter)
}

func parseRetryAfter(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func extractCredits(h http.Header) Credits {
	return Credits{
		Charged:   h.Get("X-Credits-Charged"),
		Remaining: h.Get("X-Credits-Remaining"),
		Tier:      h.Get("X-Credits-Tier"),
	}
}

func buildSummarizeResult(body map[string]any, headers http.Header) *SummarizeResult {
	usageMap, _ := body["usage"].(map[string]any)
	if usageMap == nil {
		usageMap = map[string]any{}
	}
	toFloat := func(v any) float64 {
		f, _ := v.(float64)
		return f
	}
	usage := Usage{
		InputTokens:  int(toFloat(usageMap["input_tokens"])),
		OutputTokens: int(toFloat(usageMap["output_tokens"])),
		TotalCost:    toFloat(usageMap["total_cost"]),
		ModelUsed:    stringOrEmpty(usageMap["model_used"]),
	}
	return &SummarizeResult{
		Summary:   stringOrEmpty(body["summary"]),
		SessionID: stringOrEmpty(body["session_id"]),
		Usage:     usage,
		RequestID: headers.Get("X-Request-Id"),
		Credits:   extractCredits(headers),
		Raw:       body,
	}
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

func truncateForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
