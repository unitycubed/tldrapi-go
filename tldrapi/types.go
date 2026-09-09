package tldrapi

// QualityTier is the enum of paid summarization tiers. Free tier is
// the absence of X-Quality (server picks default).
type QualityTier string

const (
	TierQuick    QualityTier = "quick"
	TierStandard QualityTier = "standard"
	TierDeep     QualityTier = "deep"
	TierPremium  QualityTier = "premium"
	TierUltra    QualityTier = "ultra"
)

var validTiers = map[QualityTier]bool{
	TierQuick:    true,
	TierStandard: true,
	TierDeep:     true,
	TierPremium:  true,
	TierUltra:    true,
}

// SummarizeOptions is the per-call knobs bag for Client.Summarize.
// All fields optional; zero-value defaults come from the server.
type SummarizeOptions struct {
	// Tier picks the paid quality tier. Free tier: leave empty.
	Tier QualityTier
	// SessionID resumes a session so tier / model routing is sticky.
	// Optional — omit for stateless one-shot calls.
	SessionID string
	// ModelAlias forces a specific model override (advanced; usually
	// leave empty and let the router pick).
	ModelAlias string
	// AllowOverage: opt-in to going over your daily credit budget.
	AllowOverage bool
	// ExtraHeaders lets callers pass arbitrary headers (rare;
	// primarily for debugging or forwarding X-Trace-ID from a caller).
	ExtraHeaders map[string]string
	// TimeoutSeconds overrides the client-wide default for this one call.
	TimeoutSeconds int
}

// Usage is the per-call cost + model breakdown returned by /summarize.
type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost"`
	ModelUsed    string  `json:"model_used"`
}

// Credits mirrors the X-Credits-* response headers so callers get
// balance info in a single struct alongside the summary.
type Credits struct {
	Charged   string
	Remaining string
	Tier      string
}

// SummarizeResult is what /summarize returns. Raw holds the full
// server response for callers who need fields not modeled here.
type SummarizeResult struct {
	Summary   string
	SessionID string
	Usage     Usage
	RequestID string
	Credits   Credits
	Raw       map[string]any
}

// Rates is the credit-per-call pricing table from /rates.
type Rates struct {
	Quick     int
	Standard  int
	Deep      int
	Premium   int
	Ultra     int
	UpdatedAt string
	Raw       map[string]any
}

// UsageStats is the customer's aggregate usage from /usage.
type UsageStats struct {
	Period           string
	Calls            int
	CreditsCharged   int
	CreditsRemaining int
	Raw              map[string]any
}
