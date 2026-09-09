# tldrapi-go — Go SDK for TLDRapi

Official Go client for the [TLDRapi](https://tldrapi-summarizer.p.rapidapi.com/)
text-summarization API. Zero third-party dependencies — uses only
`net/http` and `encoding/json` from the standard library.

## Get your app's RapidAPI key

1. Sign in at [rapidapi.com](https://rapidapi.com)
2. Subscribe to the [TLDRapi Summarizer](https://rapidapi.com/thunderAPIs256/api/tldrapi-summarizer) listing (start with **BASIC** — free)
3. Go to **Console** (top nav) → **Applications** → **Add App** (or open an existing one)
4. In the App → **Authorizations** tab → click the copy icon next to your Authorization Key

That's the app's `X-RapidAPI-Key`. Pass it to the SDK constructor.

*Legacy path (deprecated): upper-right (?) → Legacy Developer Dashboard → Add New App → Authorization tab. The new Console path above is simpler.*

The Authorization Key field is the same value in both places — RapidAPI just labels it differently depending on which interface you use:

**New Console:**

![RapidAPI Console — Authorization Method labeled "RAPIDAPI"](https://raw.githubusercontent.com/unitycubed/tldrapi-docs/main/img/rapidapi-key-label-console.png)

**Legacy Developer Dashboard:**

![RapidAPI Legacy Developer Dashboard — Authorization Method labeled "API key"](https://raw.githubusercontent.com/unitycubed/tldrapi-docs/main/img/rapidapi-key-label-legacy.png)



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

Released under the MIT License — see [LICENSE](LICENSE).

Copyright (c) 2026 Ehren Biglari / Unity Cubed.
