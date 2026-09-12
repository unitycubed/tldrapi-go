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

// SummarizeConfig is the optional per-call generation config for
// Client.Summarize. Every field is optional (pointer type or zero-valued
// string); omitted fields fall through to server tier defaults. Shape
// mirrors openapi.yaml SummarizeRequest.config.
type SummarizeConfig struct {
	ModelAlias      string   `json:"model_alias,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	MaxOutputTokens *int     `json:"max_output_tokens,omitempty"`
	MaxInputTokens  *int     `json:"max_input_tokens,omitempty"`
}

// IsZero reports whether every field is empty, so the caller / marshaller
// can drop the config block entirely instead of sending `{}`.
func (c SummarizeConfig) IsZero() bool {
	return c.ModelAlias == "" && c.Temperature == nil && c.TopP == nil &&
		c.MaxOutputTokens == nil && c.MaxInputTokens == nil
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
	// Config: per-call generation overrides (temperature, top_p,
	// max_output_tokens, max_input_tokens, model_alias). Nil-valued
	// or empty fields fall through to server tier defaults. Matches
	// openapi.yaml SummarizeRequest.config.
	Config *SummarizeConfig
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

// Rates is the credit-per-call pricing table from /rates. Fields align
// with openapi.yaml RatesResponse. Pre-1.0 releases exposed the tiers
// at top-level and `UpdatedAt` from `body.updated_at`; the server never
// emitted those keys. UpdatedAt is kept as a deprecated alias for
// CreditCostsUpdatedAt so existing call sites don't break.
type Rates struct {
	Quick                 int
	Standard              int
	Deep                  int
	Premium               int
	Ultra                 int
	CreditCostsUpdatedAt  string
	UpdatedAt             string // Deprecated: use CreditCostsUpdatedAt.
	HistoryURL            string
	Raw                   map[string]any
}

// UsageLimits is the plan-limit sub-object of UsageStats — populated
// when the server reports them; zero-valued otherwise. Shape mirrors
// openapi.yaml UsageResponse.limits.
type UsageLimits struct {
	PerMinute  int
	Daily      int
	Credits    int
	Concurrent int
}

// UsageStats is the customer's aggregate usage from /usage. Fields
// align with openapi.yaml UsageResponse. Pre-1.0 SDK exposed
// Period/Calls/CreditsCharged/CreditsRemaining which the server has
// never returned — those keys are gone.
type UsageStats struct {
	UsageCount            int
	SuccessfulRequests    int
	FailedRequests        int
	AverageResponseTimeMs float64
	EndpointsUsed         map[string]any
	ErrorRate             float64
	Plan                  string
	Limits                UsageLimits
	Raw                   map[string]any
}

// ConvertResult is the common return of Client.Convert* text and doc
// calls (except PDF, which uses PdfConvertResult). Shape mirrors
// openapi.yaml ConvertTextResponse.
type ConvertResult struct {
	Output      string
	OutputFormat string // "text" | "latex"
	InputFormat string // json | html | md | doc | docx | odt | rtf
	InputBytes  int
	OutputChars int
	ElapsedMs   int
	RequestID   string
	Warnings    []any
	Raw         map[string]any
}

// PdfConvertResult is the return of Client.ConvertPdfToLatex. When the
// server chose the async path (HTTP 202), JobID / PollURL / Status are
// set and Output is empty — poll Client.PdfStatus until Status == "done".
type PdfConvertResult struct {
	// sync fields (or filled once an async job completes)
	Output       string
	OutputFormat string
	InputFormat  string
	InputBytes   int
	Pages        int
	ElapsedMs    int
	Backend      string
	RequestID    string
	Warnings     []any
	// async fields
	JobID            string
	Status           string // queued | running | done | failed
	PollURL          string
	EstimatedSeconds int
	Raw              map[string]any
}

// ConvertTextOptions is the per-call options bag for text-body
// /convert endpoints.
type ConvertTextOptions struct {
	AllowOverage   bool
	ExtraHeaders   map[string]string
	TimeoutSeconds int
}

// ConvertFileOptions is the per-call options bag for multipart
// /convert endpoints (except PDF).
type ConvertFileOptions struct {
	Filename       string
	AllowOverage   bool
	ExtraHeaders   map[string]string
	TimeoutSeconds int
}

// ConvertPdfOptions is the per-call options bag for /convert/pdf-to-latex.
// Backend picks the extraction backend: "auto" (default), "text", or
// "modal".
type ConvertPdfOptions struct {
	Filename       string
	Backend        string
	AllowOverage   bool
	ExtraHeaders   map[string]string
	TimeoutSeconds int
}

// RatesHistoryChange is one row of Rates.HistoryURL / Client.RatesHistory.
type RatesHistoryChange struct {
	ChangedAt     string
	Tier          string
	CreditsBefore int
	CreditsAfter  int
	Reason        string
	Operator      string
}

// RatesHistory is the return of Client.RatesHistory.
type RatesHistory struct {
	History      []RatesHistoryChange
	RangeDays    int
	TotalChanges int
	Raw          map[string]any
}

// UsageRangeDay is one day-row inside a UsageRange.
type UsageRangeDay struct {
	Date        string
	CreditsUsed int
	CallCount   int
}

// UsageRange is the return of Client.UsageRange.
type UsageRange struct {
	From        string
	To          string
	CreditsUsed int
	Daily       []UsageRangeDay
	Raw         map[string]any
}

// CustomPromptResult is the return of Client.CustomPromptSubmit.
type CustomPromptResult struct {
	ID              string
	VoiceName       string
	Status          string
	Approved        bool
	VoiceReference  string
	RejectionReason string
	UpdatedInPlace  bool
	SupersededIDs   []string
	Raw             map[string]any
}

// CustomPromptSummary is one row from Client.CustomPromptsList.
type CustomPromptSummary struct {
	ID              string
	VoiceName       string
	Status          string
	VoiceReference  string
	ApprovedAlias   string
	RejectionReason string
	SubmittedAt     string
	ReviewedAt      string
}

// CustomPromptList is the return of Client.CustomPromptsList.
type CustomPromptList struct {
	CustomerID     string
	CustomPrompts  []CustomPromptSummary
	Raw            map[string]any
}

// CustomPromptDetail is the return of Client.CustomPromptGet.
type CustomPromptDetail struct {
	ID               string
	CustomerID       string
	VoiceName        string
	Instruction      string
	Status           string
	VoiceReference   string
	ApprovedAlias    string
	RejectionReason  string
	JudgeVerdictJSON string
	SubmittedAt      string
	ReviewedAt       string
	Raw              map[string]any
}

// CustomPromptSubmitOptions is the per-call bag for CustomPromptSubmit.
type CustomPromptSubmitOptions struct {
	SessionID    string
	AllowOverage bool
	ExtraHeaders map[string]string
}

// CustomPromptListOptions is the per-call bag for CustomPromptsList.
type CustomPromptListOptions struct {
	SessionID string
}
