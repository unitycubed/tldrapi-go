// Package tldrapi — typed errors that map 1:1 to the server's error
// response shape. Callers can either catch the umbrella Error type
// via errors.As, or type-switch on the specific subtypes to branch on
// failure mode without string-matching messages.
//
// Every error carries the raw response body (parsed as JSON when the
// server sent JSON) so callers can surface fields like top_up_url on
// InsufficientCredits without another layer of unwrapping.
package tldrapi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Error is the umbrella type. All server-originated failures embed it.
// Callers who want a blanket safety net can do `errors.As(err, &tldrapi.Error{})`.
type APIError struct {
	Message      string
	StatusCode   int
	RequestID    string
	ResponseBody map[string]any
}

func (e *APIError) Error() string { return e.Message }

// AuthenticationError — 401 or 403. Missing/invalid RapidAPI key,
// RapidAPI proxy-secret mismatch, or revoked account.
type AuthenticationError struct{ *APIError }

// InsufficientCreditsError — 402. Free-tier daily quota consumed OR
// paid-tier balance too low for the requested tier. ResponseBody may
// carry a `top_up_url` field the caller should surface.
type InsufficientCreditsError struct{ *APIError }

// RateLimitError — 429. Retry-After header is exposed as a duration
// in seconds; callers should sleep at least that long before retrying.
// The SDK never auto-retries 429 (would burn credits + worsen throttle).
type RateLimitError struct {
	*APIError
	RetryAfterSeconds int
}

// LanguageNotSupportedError — 400 with error_code=language_not_supported.
// Launch scope is English-only; cross-lingual is a Month 2-3 feature.
type LanguageNotSupportedError struct{ *APIError }

// QualitySelectionRequiresPaidPlanError — 400 with the corresponding
// error_code. Free tier can only request the default tier.
type QualitySelectionRequiresPaidPlanError struct{ *APIError }

// InvalidRequestError — every other 400. Bad JSON, missing fields,
// oversized input_text, unknown model_alias.
type InvalidRequestError struct{ *APIError }

// ServerError — 5xx. Retried automatically by Client with exp backoff
// (default 3 attempts). Callers only see this when retries are exhausted.
type ServerError struct{ *APIError }

// NetworkError — transport-layer failure (DNS, connection refused,
// TLS handshake). Also retried automatically. Wraps the underlying
// net or url error.
type NetworkError struct {
	*APIError
	Cause error
}

func (e *NetworkError) Unwrap() error { return e.Cause }

// TimeoutError — per-request timeout tripped (default 60s).
type TimeoutError struct{ *APIError }

// extractMessage pulls a human-readable string out of whatever the
// server sent us. Falls back to "HTTP <status>" if nothing usable.
func extractMessage(body map[string]any, rawBody []byte, status int) string {
	for _, k := range []string{"message", "detail", "error", "reason"} {
		if v, ok := body[k].(string); ok && v != "" {
			return v
		}
	}
	if len(rawBody) > 0 {
		trimmed := strings.TrimSpace(string(rawBody))
		if trimmed != "" && !strings.HasPrefix(trimmed, "{") {
			if len(trimmed) > 400 {
				return trimmed[:400]
			}
			return trimmed
		}
	}
	return fmt.Sprintf("HTTP %d", status)
}

// errorFromResponse maps an HTTP response to the correct typed error.
// Called by Client after every non-2xx response.
func errorFromResponse(status int, rawBody []byte, requestID string, retryAfterSeconds int) error {
	body := map[string]any{}
	if len(rawBody) > 0 {
		_ = json.Unmarshal(rawBody, &body)
	}
	errCode := ""
	if v, ok := body["error_code"].(string); ok {
		errCode = strings.ToLower(v)
	} else if v, ok := body["error"].(string); ok {
		errCode = strings.ToLower(v)
	}
	msg := extractMessage(body, rawBody, status)
	base := &APIError{
		Message:      msg,
		StatusCode:   status,
		RequestID:    requestID,
		ResponseBody: body,
	}

	switch {
	case status == 401 || status == 403:
		return &AuthenticationError{APIError: base}
	case status == 402:
		return &InsufficientCreditsError{APIError: base}
	case status == 429:
		return &RateLimitError{APIError: base, RetryAfterSeconds: retryAfterSeconds}
	case status == 400:
		switch {
		case errCode == "language_not_supported" || strings.Contains(errCode, "language"):
			return &LanguageNotSupportedError{APIError: base}
		case errCode == "quality_selection_requires_paid_plan":
			return &QualitySelectionRequiresPaidPlanError{APIError: base}
		default:
			return &InvalidRequestError{APIError: base}
		}
	case status >= 500 && status < 600:
		return &ServerError{APIError: base}
	default:
		return base
	}
}
