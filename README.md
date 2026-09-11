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

## Quality tiers

Pass one of `TierQuick`, `TierStandard`, `TierDeep`, `TierPremium`,
`TierUltra`. Leave `Tier` empty for the free tier's default.

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
