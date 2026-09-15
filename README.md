> ### ⚠️ Service notice
>
> **The RapidAPI listing that backs this SDK is temporarily unavailable while we work through a launch-day issue. Please check back in a few days.**

# tldrapi-go — Go SDK for TLDRapi

Official Go client for the [TLDRapi](https://unitycubed.dev/TLDRapi/)
text-summarization API. Zero third-party dependencies — uses only
`net/http` and `encoding/json` from the standard library.

## Install

```sh
go get github.com/unitycubed/tldrapi-go/tldrapi
```

Requires Go 1.21+.

## Auth

Subscribe to the [TLDRapi listing on RapidAPI](https://rapidapi.com/)
and get your `X-RapidAPI-Key`. Pass it to `NewClient`.

## Usage

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
    if err != nil {
        log.Fatal(err)
    }

    res, err := c.Summarize(context.Background(),
        "Long text goes here.",
        tldrapi.SummarizeOptions{Tier: tldrapi.TierQuick})
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(res.Summary)
    fmt.Printf("cost=$%.6f  credits_remaining=%s\n",
        res.Usage.TotalCost, res.Credits.Remaining)
}
```

## Quality levels

Pass one of `TierQuick`, `TierStandard`, `TierDeep`, `TierPremium`,
`TierUltra`. Leave `Tier` empty for the free tier's default.

Credit cost scales with input size (v2.1):
`cost = 1 + Σ over chunks of (base × ceil(chunk_tokens / 1000))`.
Base costs and chunk caps are dynamic — fetch the current schedule
with `c.Rates(ctx)` or from `GET /rates`.

## Advanced quality controls (v-session129+)

Every summarize call is parameterized by three orthogonal knobs. Send
zero (default `standard`) — or set `Tier` for a named preset — or
set 1-3 optional axis fields. Both together: axes override the preset
and server returns `X-Quality-Warning`.

**30 named presets.** `Tier` accepts any of `{minimal|brief|balanced|
thorough|detailed|complete}-{quick|standard|deep|premium|ultra}`
(e.g. `TierQuick`, or raw string `"thorough-standard"` /
`"complete-quick"`). The 5 short canonical names are the
SCORECARD-validated highlighted presets; the other 25 are extrapolated.

**Three optional axis overrides** on `SummarizeOptions`:

- `OptionalQuality` — `quick | standard | deep | premium | ultra`
- `OptionalExtractiveLvl` — `minimal | brief | balanced | thorough | detailed | complete`
- `OptionalStrategy` — `contextual-compression | premium-single-shot | hierarchical-merge`

```go
// Named preset (extrapolated)
r, _ := c.Summarize(ctx, text, tldrapi.SummarizeOptions{Tier: "thorough-quick"})

// One axis override
r, _ := c.Summarize(ctx, text, tldrapi.SummarizeOptions{
    Tier: tldrapi.TierPremium,
    OptionalExtractiveLvl: "brief",
})

// All three axes
r, _ := c.Summarize(ctx, text, tldrapi.SummarizeOptions{
    OptionalQuality: "ultra",
    OptionalExtractiveLvl: "complete",
    OptionalStrategy: "premium-single-shot",
})
```

### Paid-tier quality guarantees

Default = strict wait for the tier's primary model. Opt into
permissive fallback with `AllowDowngrade: true`. Response may then
set `X-Quality-Actual` naming the tier that actually served.

```go
r, _ := c.Summarize(ctx, text, tldrapi.SummarizeOptions{
    Tier: tldrapi.TierPremium,
    AllowDowngrade: true,
})
```

### Async submit + poll

```go
rid, _ := c.SubmitAsync(ctx, text, tldrapi.SummarizeOptions{Tier: tldrapi.TierUltra})
r, _ := c.WaitForResult(ctx, rid, 5*time.Minute, 5*time.Second)
```

Or manual: `c.GetResult(ctx, rid)` returns `(nil, nil)` while queued,
`(*SummarizeResult, nil)` when ready. Credits deducted at submit time,
refunded on failure like sync.

## Error handling

Type-switch or `errors.As` on the specific subtype:

```go
res, err := c.Summarize(ctx, txt, tldrapi.SummarizeOptions{})
if err != nil {
    var rl *tldrapi.RateLimitError
    var ic *tldrapi.InsufficientCreditsError
    switch {
    case errors.As(err, &rl):
        time.Sleep(time.Duration(rl.RetryAfterSeconds) * time.Second)
    case errors.As(err, &ic):
        topUp, _ := ic.ResponseBody["top_up_url"].(string)
        log.Fatalf("out of credits — top up at %s", topUp)
    default:
        log.Fatal(err)
    }
}
```

All errors wrap `*tldrapi.APIError` which carries `StatusCode`,
`RequestID`, and `ResponseBody` for debugging.

## Retries

5xx and network errors auto-retry 3× with exponential backoff + jitter.
4xx (including 429) is **never** auto-retried — that would burn credits
or worsen a throttle. Callers are expected to honor `Retry-After` on
`RateLimitError` themselves.

Configure via `ClientOptions.Retries`. Set to `-1` to disable retries
entirely.

## Timeouts

Per-request default is 60s. Override globally via `ClientOptions.Timeout`,
or per-call via `SummarizeOptions.TimeoutSeconds`. Pass a `context` with
a deadline for cancellation.

## License

MIT — see [LICENSE](./LICENSE).
