# internal/websearch

Web search and page fetching for AI agents, with no paid API keys. It backs
the harness tools `web_search` and `web_fetch`, and will back the CSR prospect
discovery work (Phase 2).

The package imports nothing from the rest of the harness. HTTP clients,
clocks and the PDF extractor are injected, so it could move to its own
module, or sit behind an MCP server, with a few lines of glue.

## Pieces

| File | Responsibility |
|---|---|
| `provider.go`, `searxng.go`, `ddg.go` | `Provider` interface; SearXNG JSON API; DuckDuckGo HTML fallback |
| `router.go` | Ordered failover across providers, circuit breaker, result cache |
| `fetch.go` | Guarded download → text/markdown; redirects, size, content type, charset, WAF detection |
| `safety.go` | SSRF guard (URL checks + connect-time IP checks) |
| `robots.go` | robots.txt parsing per RFC 9309, cached per host |
| `ratelimit.go` | Per-domain politeness limit (waits, never hammers a site) |
| `cache.go` | Bounded TTL + LRU cache |
| `htmltext.go` | HTML → light markdown (main content) + every link on the page |
| `pdf.go` | Optional PDF text via `pdftotext` |
| `wrap.go` | `<web_content untrusted="true">` wrapper, token estimate, truncation |
| `metrics.go` | In-memory counters |

## Enabling the tools in the harness

```bash
# 1. Run SearXNG (strongly recommended; see "Limits" for why).
docker run -d --name searxng -p 127.0.0.1:8888:8080 \
  -e SEARXNG_SECRET="$(openssl rand -hex 32)" \
  -v "$PWD/deploy/searxng/settings.yml:/etc/searxng/settings.yml:ro" \
  searxng/searxng:latest

# 2. Enable the tools.
export WEB_TOOLS_ENABLED=true SEARXNG_URL=http://127.0.0.1:8888
make run
```

```bash
curl -s localhost:8080/api/v1/chat -H "X-API-Key: $API_KEYS" -H 'Content-Type: application/json' \
  -d '{"message": "Cari program TJSL Pertamina untuk bidang pendidikan dan sebutkan sumbernya."}' | jq
```

PDF reports become readable once `pdftotext` is installed (`apt install
poppler-utils` / `brew install poppler`). Without it, PDFs return a clear
error and the harness still starts.

## Using the package directly

```go
log := slog.Default()
fetcher, _ := websearch.NewFetcher(websearch.FetcherOptions{
    UserAgent:     "HumanInitiativeBot/1.0 (+https://example.org/bot)",
    RespectRobots: true,
}, log)

searx, _ := websearch.NewSearXNG("http://127.0.0.1:8888", &http.Client{Timeout: 10 * time.Second},
    "HumanInitiativeBot/1.0 (+https://example.org/bot)", "id")
router, _ := websearch.NewRouter([]websearch.Provider{searx}, websearch.RouterOptions{}, log)

results, err := router.Search(ctx, "program TJSL pendidikan", 5)
page, err := fetcher.Fetch(ctx, results[0].URL)
text, _ := websearch.Truncate(page.Content, 2000)
forModel := websearch.Wrap(page.FinalURL, page.FetchedAt, text)
```

## Safety properties

- **SSRF:** only `http`/`https`, no credentials in URLs, ports 80/443 only.
  Every connection's resolved IP is checked at connect time (this defeats DNS
  rebinding), and every redirect hop is re-validated (max 5). Proxies are
  disabled because they would bypass the IP check.
- **Resources:** body capped (default 5 MiB; oversized HTML is used
  partially, oversized PDF is rejected), request timeout, redirect cap,
  bounded caches.
- **Prompt injection:** callers wrap content with `Wrap`; anything resembling
  the wrapper's own tags inside the content is neutralized.
- **Politeness:** an identifying User-Agent with a contact address,
  robots.txt (enforced for automated crawling; for agent-initiated fetches it
  is configurable, and violations are always logged), 1 request/second per
  domain by default, and a fresh cookie jar per fetch.
- **Logging:** URLs, status, sizes and durations only. Page content is never
  logged.

## Limits, measured on the seed company list

A survey of the 79 seed companies with a domain (homepage only, 2026-10-01):

| Outcome | Count | What it means |
|---|---|---|
| Readable | 51 | 44 of them link to CSR/ESG/sustainability pages from the homepage |
| Rendered by JavaScript | 10 | Empty without a browser; out of scope by design |
| HTTP 403 | 6 | Rejects non-browser clients; we do not fake a browser User-Agent |
| Network error / wrong domain | 7 | Includes a domain that does not exist, and one only reachable via `www.` |
| WAF bot challenge (Imperva) | 4 | Reported as `ErrBlocked` |
| robots.txt disallows all | 1 | Respected |

Two consequences:

1. **DuckDuckGo is blocked by Indonesian ISPs**: the TLS certificate
   presented belongs to the ISP's block page, and the client correctly
   refuses it. On an Indonesian network the DuckDuckGo fallback fails with a
   certificate error. Run SearXNG (ideally on a server outside the ISP
   filter) and list it first.
2. **Search reaches what fetching cannot**: search engines render JavaScript
   and are not blocked by company WAFs, so for about a third of the companies
   search results are the main way in. For example, BRI's site serves a WAF
   challenge, yet a search finds its TJSL pages and a CSR page on its separate
   investor-relations domain.

## Tests

```bash
go test -race ./internal/websearch/...          # offline, httptest fixtures only

# Live checks against the real internet (not run in CI):
go test -tags live -run Live -v ./internal/websearch/
LIVE_SEARXNG_URL=http://127.0.0.1:8888 go test -tags live -run Live_SearXNG -v ./internal/websearch/
LIVE_SEED_CSV=/path/to/csr-seed-companies.csv \
  go test -tags live -run Live_SeedSurvey -v -timeout 10m ./internal/websearch/
```
