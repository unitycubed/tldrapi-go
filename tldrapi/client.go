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
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version tracks the SDK version. Emitted in User-Agent.
const Version = "0.1.0"

// DefaultRapidAPIHost is the RapidAPI hostname for TLDRapi. Swap in
// ClientOptions.RapidAPIHost only if the listing is renamed.
const DefaultRapidAPIHost = "tldrapi-summarizer.p.rapidapi.com"

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
	// tldrapi-summarizer.p.rapidapi.com.
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
	if opts.Config != nil && !opts.Config.IsZero() {
		// Struct tags on SummarizeConfig drop nil fields automatically —
		// marshal-then-unmarshal to get a nested map that plays nice with
		// the surrounding map[string]any wire.
		cfgBytes, err := json.Marshal(opts.Config)
		if err != nil {
			return nil, fmt.Errorf("tldrapi: marshal summarize config: %w", err)
		}
		var cfgMap map[string]any
		if err := json.Unmarshal(cfgBytes, &cfgMap); err != nil {
			return nil, fmt.Errorf("tldrapi: unmarshal summarize config: %w", err)
		}
		if len(cfgMap) > 0 {
			body["config"] = cfgMap
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tldrapi: marshal request: %w", err)
	}

	headers := c.buildHeaders(opts.Tier, opts.ExtraHeaders)
	if opts.AllowOverage {
		headers["X-Allow-Overage"] = "true"
	}
	if opts.AllowDowngrade {
		headers["X-Allow-Downgrade"] = "true"
	}
	if opts.OptionalQuality != "" {
		headers["X-Optional-Quality"] = opts.OptionalQuality
	}
	if opts.OptionalExtractiveLvl != "" {
		headers["X-Optional-Extractive-Lvl"] = opts.OptionalExtractiveLvl
	}
	if opts.OptionalStrategy != "" {
		headers["X-Optional-Strategy"] = opts.OptionalStrategy
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

// SubmitAsync (#423): submit a paid-tier summarize and return the
// request_id. Fetch the result via GetResult(requestID) or block-poll
// via WaitForResult(requestID, ...). Credits are deducted at submit
// time and refunded on failure exactly like Summarize().
func (c *Client) SubmitAsync(ctx context.Context, inputText string, opts SummarizeOptions) (string, error) {
	if inputText == "" {
		return "", errors.New("tldrapi: inputText must be non-empty")
	}
	if opts.Tier != "" && !validTiers[opts.Tier] {
		return "", fmt.Errorf("tldrapi: invalid tier %q", opts.Tier)
	}
	payload, err := json.Marshal(map[string]any{"input_text": inputText})
	if err != nil {
		return "", fmt.Errorf("tldrapi: marshal request: %w", err)
	}
	headers := c.buildHeaders(opts.Tier, opts.ExtraHeaders)
	headers["X-Async"] = "true"
	if opts.AllowDowngrade {
		headers["X-Allow-Downgrade"] = "true"
	}
	if opts.OptionalQuality != "" {
		headers["X-Optional-Quality"] = opts.OptionalQuality
	}
	if opts.OptionalExtractiveLvl != "" {
		headers["X-Optional-Extractive-Lvl"] = opts.OptionalExtractiveLvl
	}
	if opts.OptionalStrategy != "" {
		headers["X-Optional-Strategy"] = opts.OptionalStrategy
	}
	_, respHeaders, err := c.request(ctx, "POST", "/summarize", payload, headers, opts.TimeoutSeconds)
	if err != nil {
		return "", err
	}
	rid := respHeaders.Get("X-Paid-Request-Id")
	if rid == "" {
		return "", &ServerError{APIError: &APIError{
			Message:    "async submit returned no X-Paid-Request-Id",
			StatusCode: 202,
			RequestID:  respHeaders.Get("X-Request-Id"),
		}}
	}
	return rid, nil
}

// GetResult (#423): fetch the async summarize result. Returns
// (nil, nil) if still queued (server 202). Returns (result, nil) on
// 200. Returns error on 4xx/5xx.
func (c *Client) GetResult(ctx context.Context, requestID string) (*SummarizeResult, error) {
	if requestID == "" {
		return nil, errors.New("tldrapi: requestID must be non-empty")
	}
	headers := c.buildHeaders("", nil)
	rawBody, respHeaders, err := c.request(ctx, "GET", "/paid/result/"+requestID, nil, headers, 0)
	if err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.StatusCode == 202 {
			return nil, nil
		}
		return nil, err
	}
	var respJSON map[string]any
	if err := json.Unmarshal(rawBody, &respJSON); err != nil {
		return nil, &ServerError{APIError: &APIError{
			Message:    fmt.Sprintf("unexpected non-JSON result: %s", truncateForLog(rawBody, 200)),
			StatusCode: 200,
			RequestID:  respHeaders.Get("X-Request-Id"),
		}}
	}
	return buildSummarizeResult(respJSON, respHeaders), nil
}

// WaitForResult (#423): block-poll GetResult until ready. `timeout`
// bounds total wait; 0 = no cap. `pollInterval` defaults to 5 seconds.
func (c *Client) WaitForResult(ctx context.Context, requestID string, timeout, pollInterval time.Duration) (*SummarizeResult, error) {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		r, err := c.GetResult(ctx, requestID)
		if err != nil {
			return nil, err
		}
		if r != nil {
			return r, nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return nil, fmt.Errorf("tldrapi: WaitForResult %s still pending after %s", requestID, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// Rates fetches /rates — the credit-per-call table for each quality tier.
// Fields align with openapi.yaml RatesResponse. Pre-1.0 SDK read tier
// ints at top-level and `updated_at`; the server has always nested them
// under `credits_per_call` and emits `credit_costs_updated_at`.
func (c *Client) Rates(ctx context.Context) (*Rates, error) {
	rawBody, _, err := c.request(ctx, "GET", "/rates", nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		m = map[string]any{}
	}
	cpc, _ := m["credits_per_call"].(map[string]any)
	toInt := func(src map[string]any, k string, dflt int) int {
		if src == nil {
			return dflt
		}
		if v, ok := src[k].(float64); ok {
			return int(v)
		}
		return dflt
	}
	updated, _ := m["credit_costs_updated_at"].(string)
	historyURL, _ := m["history_url"].(string)
	return &Rates{
		Quick:                toInt(cpc, "quick", 1),
		Standard:             toInt(cpc, "standard", 5),
		Deep:                 toInt(cpc, "deep", 30),
		Premium:              toInt(cpc, "premium", 110),
		Ultra:                toInt(cpc, "ultra", 400),
		CreditCostsUpdatedAt: updated,
		UpdatedAt:            updated, // Deprecated alias — same value as CreditCostsUpdatedAt.
		HistoryURL:           historyURL,
		Raw:                  m,
	}, nil
}

// Usage fetches /usage — the customer's aggregate usage stats. Fields
// align with openapi.yaml UsageResponse. Pre-1.0 SDK read
// period/calls/credits_charged/credits_remaining — none of which the
// server has ever emitted — so every field returned zero.
func (c *Client) Usage(ctx context.Context) (*UsageStats, error) {
	rawBody, _, err := c.request(ctx, "GET", "/usage", nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		m = map[string]any{}
	}
	toInt := func(src map[string]any, k string) int {
		if src == nil {
			return 0
		}
		if v, ok := src[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	toFloat := func(src map[string]any, k string) float64 {
		if src == nil {
			return 0
		}
		if v, ok := src[k].(float64); ok {
			return v
		}
		return 0
	}
	limitsRaw, _ := m["limits"].(map[string]any)
	limits := UsageLimits{
		PerMinute:  toInt(limitsRaw, "per_minute"),
		Daily:      toInt(limitsRaw, "daily"),
		Credits:    toInt(limitsRaw, "credits"),
		Concurrent: toInt(limitsRaw, "concurrent"),
	}
	endpoints, _ := m["endpoints_used"].(map[string]any)
	plan, _ := m["plan"].(string)
	return &UsageStats{
		UsageCount:            toInt(m, "usage_count"),
		SuccessfulRequests:    toInt(m, "successful_requests"),
		FailedRequests:        toInt(m, "failed_requests"),
		AverageResponseTimeMs: toFloat(m, "average_response_time_ms"),
		EndpointsUsed:         endpoints,
		ErrorRate:             toFloat(m, "error_rate"),
		Plan:                  plan,
		Limits:                limits,
		Raw:                   m,
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

// ─── /convert/{json,html,md}-to-text (text-body) ──────────────────────

// ConvertJsonToText normalizes a JSON string to plaintext via
// POST /convert/json-to-text.
func (c *Client) ConvertJsonToText(ctx context.Context, text string, opts ConvertTextOptions) (*ConvertResult, error) {
	return c.convertText(ctx, "/convert/json-to-text", text, opts)
}

// ConvertHtmlToText strips HTML to clean plaintext via
// POST /convert/html-to-text.
func (c *Client) ConvertHtmlToText(ctx context.Context, text string, opts ConvertTextOptions) (*ConvertResult, error) {
	return c.convertText(ctx, "/convert/html-to-text", text, opts)
}

// ConvertMdToText renders GitHub-flavored Markdown to plaintext via
// POST /convert/md-to-text.
func (c *Client) ConvertMdToText(ctx context.Context, text string, opts ConvertTextOptions) (*ConvertResult, error) {
	return c.convertText(ctx, "/convert/md-to-text", text, opts)
}

func (c *Client) convertText(ctx context.Context, path, text string, opts ConvertTextOptions) (*ConvertResult, error) {
	if text == "" {
		return nil, errors.New("tldrapi: text must be non-empty")
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, fmt.Errorf("tldrapi: marshal convert request: %w", err)
	}
	headers := c.buildHeaders("", opts.ExtraHeaders)
	if opts.AllowOverage {
		headers["X-Allow-Overage"] = "true"
	}
	rawBody, respHeaders, err := c.request(ctx, "POST", path, payload, headers, opts.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildConvertResult(m, respHeaders), nil
}

// ─── /convert/{doc,doc-to-latex,docx}-to-text (multipart) ────────────

// ConvertDocToText extracts plaintext from a doc/docx/odt/rtf file via
// POST /convert/doc-to-text.
func (c *Client) ConvertDocToText(ctx context.Context, file any, opts ConvertFileOptions) (*ConvertResult, error) {
	return c.convertFile(ctx, "/convert/doc-to-text", file, opts)
}

// ConvertDocToLatex extracts LaTeX source from a doc/docx/odt/rtf file
// via POST /convert/doc-to-latex.
func (c *Client) ConvertDocToLatex(ctx context.Context, file any, opts ConvertFileOptions) (*ConvertResult, error) {
	return c.convertFile(ctx, "/convert/doc-to-latex", file, opts)
}

// ConvertDocxToText is a deprecated alias for ConvertDocToText; kept
// for backward compat with the previous endpoint name.
func (c *Client) ConvertDocxToText(ctx context.Context, file any, opts ConvertFileOptions) (*ConvertResult, error) {
	return c.convertFile(ctx, "/convert/docx-to-text", file, opts)
}

func (c *Client) convertFile(ctx context.Context, path string, file any, opts ConvertFileOptions) (*ConvertResult, error) {
	body, contentType, err := buildMultipart(file, opts.Filename)
	if err != nil {
		return nil, err
	}
	headers := c.buildHeaders("", opts.ExtraHeaders)
	delete(headers, "Content-Type")
	headers["Content-Type"] = contentType
	if opts.AllowOverage {
		headers["X-Allow-Overage"] = "true"
	}
	rawBody, respHeaders, err := c.request(ctx, "POST", path, body, headers, opts.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildConvertResult(m, respHeaders), nil
}

// ConvertPdfToLatex extracts LaTeX from a PDF via
// POST /convert/pdf-to-latex. On HTTP 202 the returned result carries
// JobID/PollURL/Status="queued"; poll PdfStatus.
func (c *Client) ConvertPdfToLatex(ctx context.Context, file any, opts ConvertPdfOptions) (*PdfConvertResult, error) {
	body, contentType, err := buildMultipart(file, opts.Filename)
	if err != nil {
		return nil, err
	}
	headers := c.buildHeaders("", opts.ExtraHeaders)
	delete(headers, "Content-Type")
	headers["Content-Type"] = contentType
	if opts.AllowOverage {
		headers["X-Allow-Overage"] = "true"
	}
	if opts.Backend != "" {
		switch opts.Backend {
		case "auto", "text", "modal":
		default:
			return nil, fmt.Errorf("tldrapi: backend must be one of auto,text,modal (got %q)", opts.Backend)
		}
		headers["X-PDF-Backend"] = opts.Backend
	}
	// We need the raw response including status to distinguish 200 vs 202.
	// c.request only surfaces success bodies without status. So thread a
	// small wrapper: parse first, then decide sync vs async by inspecting
	// the parsed body's `status` field (server sets "queued" on 202).
	rawBody, respHeaders, err := c.request(ctx, "POST", "/convert/pdf-to-latex", body, headers, opts.TimeoutSeconds)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	statusStr, _ := m["status"].(string)
	if statusStr == "queued" || statusStr == "running" {
		return buildPdfAsync(m, respHeaders), nil
	}
	return buildPdfSync(m, respHeaders), nil
}

// PdfStatus polls the async /convert/pdf-to-latex/status/:job_id
// endpoint. Returns a PdfConvertResult whose Status field is one of
// queued, running, done, or failed.
func (c *Client) PdfStatus(ctx context.Context, jobID string) (*PdfConvertResult, error) {
	if jobID == "" {
		return nil, errors.New("tldrapi: jobID required")
	}
	path := "/convert/pdf-to-latex/status/" + url.PathEscape(jobID)
	rawBody, respHeaders, err := c.request(ctx, "GET", path, nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	statusStr, _ := m["status"].(string)
	if statusStr == "queued" || statusStr == "running" {
		return buildPdfAsync(m, respHeaders), nil
	}
	return buildPdfSync(m, respHeaders), nil
}

// ─── rates history + usage range ────────────────────────────────────

// RatesHistory fetches the change history for tier credit costs via
// GET /rates/history.
func (c *Client) RatesHistory(ctx context.Context) (*RatesHistory, error) {
	rawBody, _, err := c.request(ctx, "GET", "/rates/history", nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildRatesHistory(m), nil
}

// UsageRange fetches per-day usage between two YYYY-MM-DD dates via
// GET /usage/range?from=...&to=...
func (c *Client) UsageRange(ctx context.Context, from, to string) (*UsageRange, error) {
	if from == "" || to == "" {
		return nil, errors.New("tldrapi: from and to required (YYYY-MM-DD)")
	}
	qs := "?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to)
	rawBody, _, err := c.request(ctx, "GET", "/usage/range"+qs, nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildUsageRange(m), nil
}

// ─── custom prompts (Business/Enterprise) ───────────────────────────

// CustomPromptSubmit submits a custom voice prompt for review via
// POST /custom-prompts/submit.
func (c *Client) CustomPromptSubmit(ctx context.Context, voiceName, instruction string, opts CustomPromptSubmitOptions) (*CustomPromptResult, error) {
	if voiceName == "" || instruction == "" {
		return nil, errors.New("tldrapi: voiceName and instruction required")
	}
	body := map[string]any{"voice_name": voiceName, "instruction": instruction}
	if opts.SessionID != "" {
		body["session_id"] = opts.SessionID
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tldrapi: marshal custom-prompt request: %w", err)
	}
	headers := c.buildHeaders("", opts.ExtraHeaders)
	if opts.AllowOverage {
		headers["X-Allow-Overage"] = "true"
	}
	rawBody, _, err := c.request(ctx, "POST", "/custom-prompts/submit", payload, headers, 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildCustomPromptResult(m), nil
}

// CustomPromptsList returns every custom prompt the caller has submitted
// via POST /custom-prompts/list.
func (c *Client) CustomPromptsList(ctx context.Context, opts CustomPromptListOptions) (*CustomPromptList, error) {
	body := map[string]any{}
	if opts.SessionID != "" {
		body["session_id"] = opts.SessionID
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("tldrapi: marshal custom-prompt list request: %w", err)
	}
	rawBody, _, err := c.request(ctx, "POST", "/custom-prompts/list", payload, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildCustomPromptList(m), nil
}

// CustomPromptGet returns the full detail of a single custom prompt via
// GET /custom-prompts/:id.
func (c *Client) CustomPromptGet(ctx context.Context, promptID string) (*CustomPromptDetail, error) {
	if promptID == "" {
		return nil, errors.New("tldrapi: promptID required")
	}
	path := "/custom-prompts/" + url.PathEscape(promptID)
	rawBody, _, err := c.request(ctx, "GET", path, nil, c.buildHeaders("", nil), 0)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = json.Unmarshal(rawBody, &m)
	if m == nil {
		m = map[string]any{}
	}
	return buildCustomPromptDetail(m), nil
}

// ─── multipart + response builders ──────────────────────────────────

// buildMultipart builds a multipart/form-data body from a file input.
// Accepted inputs: string (filesystem path), []byte, io.Reader.
// Returns body bytes + Content-Type (with boundary).
func buildMultipart(file any, filename string) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	var reader io.Reader
	switch v := file.(type) {
	case string:
		f, err := os.Open(v)
		if err != nil {
			return nil, "", fmt.Errorf("tldrapi: open %s: %w", v, err)
		}
		defer f.Close()
		reader = f
		if filename == "" {
			filename = filepath.Base(v)
		}
	case []byte:
		reader = bytes.NewReader(v)
	case io.Reader:
		reader = v
	default:
		return nil, "", fmt.Errorf("tldrapi: unsupported file input type %T (want string path, []byte, or io.Reader)", file)
	}
	if filename == "" {
		filename = "upload"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, "", fmt.Errorf("tldrapi: create form-file: %w", err)
	}
	if _, err := io.Copy(part, reader); err != nil {
		return nil, "", fmt.Errorf("tldrapi: copy file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("tldrapi: close multipart: %w", err)
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

func toIntOrDefault(m map[string]any, k string, dflt int) int {
	if m == nil {
		return dflt
	}
	if v, ok := m[k].(float64); ok {
		return int(v)
	}
	return dflt
}

func buildConvertResult(m map[string]any, h http.Header) *ConvertResult {
	warnings, _ := m["warnings"].([]any)
	return &ConvertResult{
		Output:      stringOrEmpty(m["output"]),
		OutputFormat: stringOrEmpty(m["output_format"]),
		InputFormat: stringOrEmpty(m["input_format"]),
		InputBytes:  toIntOrDefault(m, "input_bytes", 0),
		OutputChars: toIntOrDefault(m, "output_chars", 0),
		ElapsedMs:   toIntOrDefault(m, "elapsed_ms", 0),
		RequestID:   firstNonEmpty(stringOrEmpty(m["request_id"]), h.Get("X-Request-Id")),
		Warnings:    warnings,
		Raw:         m,
	}
}

func buildPdfSync(m map[string]any, h http.Header) *PdfConvertResult {
	warnings, _ := m["warnings"].([]any)
	return &PdfConvertResult{
		Output:           stringOrEmpty(m["output"]),
		OutputFormat:     stringOrEmpty(m["output_format"]),
		InputFormat:      firstNonEmpty(stringOrEmpty(m["input_format"]), "pdf"),
		InputBytes:       toIntOrDefault(m, "input_bytes", 0),
		Pages:            toIntOrDefault(m, "pages", 0),
		ElapsedMs:        toIntOrDefault(m, "elapsed_ms", 0),
		Backend:          stringOrEmpty(m["backend"]),
		RequestID:        firstNonEmpty(stringOrEmpty(m["request_id"]), h.Get("X-Request-Id")),
		Warnings:         warnings,
		JobID:            stringOrEmpty(m["job_id"]),
		Status:           firstNonEmpty(stringOrEmpty(m["status"]), "done"),
		PollURL:          stringOrEmpty(m["poll_url"]),
		EstimatedSeconds: toIntOrDefault(m, "estimated_seconds", 0),
		Raw:              m,
	}
}

func buildPdfAsync(m map[string]any, h http.Header) *PdfConvertResult {
	warnings, _ := m["warnings"].([]any)
	return &PdfConvertResult{
		InputFormat:      "pdf",
		InputBytes:       toIntOrDefault(m, "input_bytes", 0),
		Pages:            toIntOrDefault(m, "pages", 0),
		Backend:          stringOrEmpty(m["backend"]),
		RequestID:        firstNonEmpty(stringOrEmpty(m["request_id"]), h.Get("X-Request-Id")),
		Warnings:         warnings,
		JobID:            stringOrEmpty(m["job_id"]),
		Status:           firstNonEmpty(stringOrEmpty(m["status"]), "queued"),
		PollURL:          stringOrEmpty(m["poll_url"]),
		EstimatedSeconds: toIntOrDefault(m, "estimated_seconds", 0),
		Raw:              m,
	}
}

func buildRatesHistory(m map[string]any) *RatesHistory {
	raw, _ := m["history"].([]any)
	out := make([]RatesHistoryChange, 0, len(raw))
	for _, r := range raw {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, RatesHistoryChange{
			ChangedAt:     stringOrEmpty(row["changed_at"]),
			Tier:          stringOrEmpty(row["tier"]),
			CreditsBefore: toIntOrDefault(row, "credits_before", 0),
			CreditsAfter:  toIntOrDefault(row, "credits_after", 0),
			Reason:        stringOrEmpty(row["reason"]),
			Operator:      stringOrEmpty(row["operator"]),
		})
	}
	return &RatesHistory{
		History:      out,
		RangeDays:    toIntOrDefault(m, "range_days", 30),
		TotalChanges: toIntOrDefault(m, "total_changes", len(out)),
		Raw:          m,
	}
}

func buildUsageRange(m map[string]any) *UsageRange {
	raw, _ := m["daily"].([]any)
	daily := make([]UsageRangeDay, 0, len(raw))
	for _, r := range raw {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		daily = append(daily, UsageRangeDay{
			Date:        stringOrEmpty(row["date"]),
			CreditsUsed: toIntOrDefault(row, "credits_used", 0),
			CallCount:   toIntOrDefault(row, "call_count", 0),
		})
	}
	return &UsageRange{
		From:        stringOrEmpty(m["from"]),
		To:          stringOrEmpty(m["to"]),
		CreditsUsed: toIntOrDefault(m, "credits_used", 0),
		Daily:       daily,
		Raw:         m,
	}
}

func buildCustomPromptResult(m map[string]any) *CustomPromptResult {
	status := stringOrEmpty(m["status"])
	approvedFlag, _ := m["approved"].(bool)
	superseded := []string{}
	if raw, ok := m["superseded_ids"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				superseded = append(superseded, s)
			}
		}
	}
	updatedInPlace, _ := m["updated_in_place"].(bool)
	return &CustomPromptResult{
		ID:              stringOrEmpty(m["id"]),
		VoiceName:       stringOrEmpty(m["voice_name"]),
		Status:          status,
		Approved:        approvedFlag || status == "approved",
		VoiceReference:  stringOrEmpty(m["voice_reference"]),
		RejectionReason: stringOrEmpty(m["rejection_reason"]),
		UpdatedInPlace:  updatedInPlace,
		SupersededIDs:   superseded,
		Raw:             m,
	}
}

func buildCustomPromptSummary(m map[string]any) CustomPromptSummary {
	return CustomPromptSummary{
		ID:              stringOrEmpty(m["id"]),
		VoiceName:       stringOrEmpty(m["voice_name"]),
		Status:          stringOrEmpty(m["status"]),
		VoiceReference:  stringOrEmpty(m["voice_reference"]),
		ApprovedAlias:   stringOrEmpty(m["approved_alias"]),
		RejectionReason: stringOrEmpty(m["rejection_reason"]),
		SubmittedAt:     stringOrEmpty(m["submitted_at"]),
		ReviewedAt:      stringOrEmpty(m["reviewed_at"]),
	}
}

func buildCustomPromptList(m map[string]any) *CustomPromptList {
	raw, _ := m["custom_prompts"].([]any)
	out := make([]CustomPromptSummary, 0, len(raw))
	for _, r := range raw {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, buildCustomPromptSummary(row))
	}
	return &CustomPromptList{
		CustomerID:    stringOrEmpty(m["customer_id"]),
		CustomPrompts: out,
		Raw:           m,
	}
}

func buildCustomPromptDetail(m map[string]any) *CustomPromptDetail {
	return &CustomPromptDetail{
		ID:               stringOrEmpty(m["id"]),
		CustomerID:       stringOrEmpty(m["customer_id"]),
		VoiceName:        stringOrEmpty(m["voice_name"]),
		Instruction:      stringOrEmpty(m["instruction"]),
		Status:           stringOrEmpty(m["status"]),
		VoiceReference:   stringOrEmpty(m["voice_reference"]),
		ApprovedAlias:    stringOrEmpty(m["approved_alias"]),
		RejectionReason:  stringOrEmpty(m["rejection_reason"]),
		JudgeVerdictJSON: stringOrEmpty(m["judge_verdict_json"]),
		SubmittedAt:      stringOrEmpty(m["submitted_at"]),
		ReviewedAt:       stringOrEmpty(m["reviewed_at"]),
		Raw:              m,
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
