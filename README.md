# tldrapi-go — Go SDK for TLDRapi

Official Go client for [TLDRapi](https://tldrapi.com) — turn any
content into a clean summary in one API call.

- **Free tier** — 100 credits per month, no card, no trial expiry
- **20+ input formats** — text, HTML, Markdown, PDF (with OCR), .docx,
  .doc, .odt, .rtf, .epub, JSON, YAML, CSV, transcripts
- **5 quality tiers** — pick latency vs. depth per call
- **Custom voice styles** — 20+ built-in voices; paid tiers can define
  their own with plain-English instructions
- **Multi-provider routing** — automatic failover across Anthropic,
  OpenAI, Groq, Gemini, and OpenRouter
- **Refunds you don't have to ask for** — every summary is judge-scored
  and mis-summaries are auto-refunded
- **Zero third-party deps** — stdlib `net/http` + `encoding/json` only

```sh
go get github.com/unitycubed/tldrapi-go/tldrapi
```

Go 1.21+.

## Table of contents

- [Getting your free key](#getting-your-free-key)
- [Hello world](#hello-world)
- [Examples gallery](#examples-gallery)
  - [Summarize an article by URL](#summarize-an-article-by-url)
  - [Summarize a PDF (with OCR)](#summarize-a-pdf-with-ocr)
  - [Summarize a Word / RTF / EPUB file](#summarize-a-word--rtf--epub-file)
  - [Summarize a long document asynchronously](#summarize-a-long-document-asynchronously)
  - [Pin a session across many summaries](#pin-a-session-across-many-summaries)
  - [Batch summarize in parallel (goroutines)](#batch-summarize-in-parallel-goroutines)
  - [Convert-only: extract text without summarizing](#convert-only-extract-text-without-summarizing)
  - [PDF → LaTeX](#pdf--latex)
  - [Custom voice: teach the model your tone](#custom-voice-teach-the-model-your-tone)
  - [Handle a rate-limit with backoff](#handle-a-rate-limit-with-backoff)
  - [Show live credit balance to your user](#show-live-credit-balance-to-your-user)
  - [Advanced quality controls — 3 axes, 30 named presets](#advanced-quality-controls)
- [Quality tiers](#quality-tiers)
- [Error handling](#error-handling)
- [Configuration + retries](#configuration--retries)
- [License](#license)

## Getting your free key

1. Sign in at [rapidapi.com](https://rapidapi.com)
2. Subscribe to the [TLDRapi Summarizer](https://rapidapi.com/thunderAPIs256/api/tldrapi-summarizer)
   listing — choose **BASIC (Free)**
3. Open the listing → **Console** → **Applications** → **Add App**
4. In the App → **Authorizations** tab → copy the Authorization Key

Pass it to `NewClient` as `RapidAPIKey`. Everything on the free tier
works exactly like the paid tiers — same endpoints, same response
shape, same SDK — just with a 100-credit monthly cap.

## Hello world

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/unitycubed/tldrapi-go/tldrapi"
)

func main() {
    c, err := tldrapi.NewClient(tldrapi.ClientOptions{
        RapidAPIKey: os.Getenv("TLDRAPI_RAPIDAPI_KEY"),
    })
    if err != nil { log.Fatal(err) }

    res, err := c.Summarize(context.Background(),
        "Some long article body here...", tldrapi.SummarizeOptions{})
    if err != nil { log.Fatal(err) }

    fmt.Println(res.Summary)
    fmt.Printf("credits remaining: %s\n", res.Credits.Remaining)
}
```

## Examples gallery

### Summarize an article by URL

```go
r, _ := c.Summarize(ctx,
    "https://arxiv.org/abs/1706.03762",
    tldrapi.SummarizeOptions{Tier: tldrapi.TierDeep})
fmt.Println(r.Summary)
```

Works with HTML pages, news sites, GitHub READMEs, blog posts, and
academic PDFs served over HTTP.

### Summarize a PDF (with OCR)

```go
f, _ := os.Open("report.pdf")
defer f.Close()
text, _ := c.ConvertPdfToLatex(ctx, f, tldrapi.ConvertPdfOptions{
    Filename: "report.pdf",
})

// Scanned PDF (no selectable text) — force OCR
scanned, _ := os.Open("scanned.pdf")
defer scanned.Close()
ocr, _ := c.ConvertPdfToLatex(ctx, scanned, tldrapi.ConvertPdfOptions{
    Filename: "scanned.pdf",
    Backend:  "modal",
})
```

### Summarize a Word / RTF / EPUB file

```go
f, _ := os.Open("chapter.docx")
defer f.Close()
doc, _ := c.ConvertDocxToText(ctx, f, tldrapi.ConvertFileOptions{
    Filename: "chapter.docx",
})

r, _ := c.Summarize(ctx, doc.Output,
    tldrapi.SummarizeOptions{Tier: tldrapi.TierPremium})
fmt.Println(r.Summary)
```

Same pattern via `ConvertDocToText` for `.doc`, `.odt`, `.rtf`, and
via `ConvertHtmlToText` / `ConvertMdToText` / `ConvertJsonToText` for
those text-based formats.

### Summarize a long document asynchronously

```go
requestID, _ := c.SubmitAsync(ctx, giantDoc,
    tldrapi.SummarizeOptions{Tier: tldrapi.TierUltra})

// blocks + polls until 200; 5-min cap here
res, _ := c.WaitForResult(ctx, requestID, 5*time.Minute, 5*time.Second)
fmt.Println(res.Summary)
```

Manual polling:

```go
requestID, _ := c.SubmitAsync(ctx, giantDoc,
    tldrapi.SummarizeOptions{Tier: tldrapi.TierUltra})

for {
    r, _ := c.GetResult(ctx, requestID)
    if r != nil { fmt.Println(r.Summary); break }
    time.Sleep(5 * time.Second)
}
```

Credits are deducted at submit and refunded on failure, same as sync.

### Pin a session across many summaries

```go
r1, _ := c.Summarize(ctx, "Doc 1", tldrapi.SummarizeOptions{})
r2, _ := c.Summarize(ctx, "Doc 2",
    tldrapi.SummarizeOptions{SessionID: r1.SessionID})
r3, _ := c.Summarize(ctx, "Doc 3",
    tldrapi.SummarizeOptions{SessionID: r1.SessionID})
```

Useful when you want consistent voice across a run — legal briefs,
book chapters, tickets in the same support thread.

### Batch summarize in parallel (goroutines)

```go
var wg sync.WaitGroup
sem := make(chan struct{}, 5)                // cap at 5 concurrent
results := make([]*tldrapi.SummarizeResult, len(texts))

for i, t := range texts {
    wg.Add(1)
    sem <- struct{}{}
    go func(i int, t string) {
        defer wg.Done()
        defer func() { <-sem }()
        r, _ := c.Summarize(ctx, t,
            tldrapi.SummarizeOptions{Tier: tldrapi.TierQuick})
        results[i] = r
    }(i, t)
}
wg.Wait()
```

The client is concurrent-safe. Free-tier is rate-limited to ~3 rps;
paid tiers are much higher.

### Convert-only: extract text without summarizing

```go
r, _ := c.ConvertHtmlToText(ctx, "<h1>Hi</h1><p>Content...</p>",
    tldrapi.ConvertTextOptions{})
fmt.Println(r.Output)
```

### PDF → LaTeX

```go
f, _ := os.Open("paper.pdf")
defer f.Close()
r, _ := c.ConvertPdfToLatex(ctx, f, tldrapi.ConvertPdfOptions{
    Filename: "paper.pdf",
})

if r.JobID != "" {
    // async path — poll until done
    for r.Status != "done" {
        time.Sleep(5 * time.Second)
        r, _ = c.PdfStatus(ctx, r.JobID)
    }
}
fmt.Println(r.Output)  // LaTeX source
```

### Custom voice: teach the model your tone

```go
sub, _ := c.CustomPromptSubmit(ctx, "brand-tone",
    "Write in the second person, active voice. Prefer verbs over "+
    "nouns. Keep sentences under 20 words. Avoid corporate jargon "+
    "('leverage', 'synergy'). Aim for the reading level of a "+
    "well-written newspaper.",
    tldrapi.CustomPromptSubmitOptions{})

// Once approved:
r, _ := c.Summarize(ctx, text,
    tldrapi.SummarizeOptions{
        ExtraHeaders: map[string]string{"X-Voice-Name": "brand-tone"},
    })
```

Approval is automatic — the server runs the instruction against a
judge that checks for policy compliance.

### Handle a rate-limit with backoff

```go
var rl *tldrapi.RateLimitError
for attempt := 0; attempt < 3; attempt++ {
    r, err := c.Summarize(ctx, text,
        tldrapi.SummarizeOptions{Tier: tldrapi.TierDeep})
    if err == nil { fmt.Println(r.Summary); break }
    if errors.As(err, &rl) {
        secs := rl.RetryAfterSeconds
        if secs == 0 { secs = 60 }
        time.Sleep(time.Duration(secs) * time.Second)
        continue
    }
    log.Fatal(err)
}
```

### Show live credit balance to your user

```go
u, _ := c.Usage(ctx)
fmt.Printf("You have %d credits left (%s)\n",
    u.CreditsRemaining, u.Plan)

r, _ := c.Summarize(ctx, text, tldrapi.SummarizeOptions{})
fmt.Printf("That call cost %s credits. Remaining: %s\n",
    r.Credits.Charged, r.Credits.Remaining)
```

### Advanced quality controls

Every summarize call has three orthogonal knobs. You can send zero of
them (defaults are fine), or a named preset, or set 1-3 optional axes,
or combine — axes override the preset and the server returns
`X-Quality-Warning`.

**30 named presets.** `Tier` accepts one of five short canonical names
(`TierQuick`, `TierStandard`, `TierDeep`, `TierPremium`, `TierUltra`)
or a raw string for one of 25 compound presets (e.g. `"thorough-quick"`,
`"complete-premium"`).

**Three optional axis overrides** on `SummarizeOptions`:

- `OptionalQuality` — `quick | standard | deep | premium | ultra`
- `OptionalExtractiveLvl` — `minimal | brief | balanced | thorough | detailed | complete`
- `OptionalStrategy` — `contextual-compression | premium-single-shot | hierarchical-merge`

```go
// named preset
r, _ := c.Summarize(ctx, text,
    tldrapi.SummarizeOptions{Tier: "thorough-quick"})

// preset + one axis override — axes win, warning header returned
r, _ = c.Summarize(ctx, text, tldrapi.SummarizeOptions{
    Tier:                  tldrapi.TierPremium,
    OptionalExtractiveLvl: "brief",
})

// all three axes, no preset
r, _ = c.Summarize(ctx, text, tldrapi.SummarizeOptions{
    OptionalQuality:       "ultra",
    OptionalExtractiveLvl: "complete",
    OptionalStrategy:      "premium-single-shot",
})

// permissive downgrade on paid-tier
r, _ = c.Summarize(ctx, text, tldrapi.SummarizeOptions{
    Tier:           tldrapi.TierPremium,
    AllowDowngrade: true,
})
```

## Quality tiers

| Tier      | Reads at once   | Best for                          |
|-----------|----------------:|-----------------------------------|
| quick     |     4K tokens   | Short texts, previews             |
| standard  |    16K tokens   | Default — most articles           |
| deep      |    32K tokens   | Longer content, deeper reasoning  |
| premium   |    64K tokens   | Substantial documents             |
| ultra     |   100K tokens   | Long-form / research-grade        |

Live rates at [/rates](https://tldrapi.com/rates) or `c.Rates(ctx)`.

### Paid-tier quality guarantees

Default = strict wait for the tier's canonical primary model. Opt into
permissive fallback with `AllowDowngrade: true` — the worker walks DOWN
the ladder (premium → deep → standard → quick) and returns whichever
tier's primary is available. Response carries `X-Quality-Actual` and
`X-Original-Tier` when a downgrade happened, and the credit-cost delta
is automatically refunded.

## Error handling

Type-switch or `errors.As` on the specific subtype:

```go
res, err := c.Summarize(ctx, txt, tldrapi.SummarizeOptions{})
if err != nil {
    var rl *tldrapi.RateLimitError
    var ic *tldrapi.InsufficientCreditsError
    var au *tldrapi.AuthenticationError
    switch {
    case errors.As(err, &rl):
        time.Sleep(time.Duration(rl.RetryAfterSeconds) * time.Second)
    case errors.As(err, &ic):
        topUp, _ := ic.ResponseBody["top_up_url"].(string)
        log.Fatalf("out of credits — top up at %s", topUp)
    case errors.As(err, &au):
        log.Fatal("bad RapidAPI key")
    default:
        log.Fatal(err)
    }
}
```

All errors wrap `*tldrapi.APIError` which carries `StatusCode`,
`RequestID` (attach when reporting bugs), and `ResponseBody`.

## Configuration + retries

```go
c, _ := tldrapi.NewClient(tldrapi.ClientOptions{
    RapidAPIKey:  "YOUR_KEY",
    RapidAPIHost: "tldrapi-summarizer.p.rapidapi.com",
    Timeout:      60 * time.Second,
    Retries:      3,      // -1 to disable
})
```

Automatic retries on 5xx and transient network errors with exponential
backoff + jitter (3 attempts default). 4xx and 429 are **not** retried
— honor `Retry-After` yourself via `RateLimitError.RetryAfterSeconds`.

## License

MIT — see [LICENSE](./LICENSE).

Copyright (c) 2026 Ehren Biglari / Unity Cubed.
