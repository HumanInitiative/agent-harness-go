package websearch

import (
	"context"
	"net/url"
	"strings"
)

// Provider is a web search backend. Implementations must honour ctx and
// return typed errors (ErrBlocked, ErrProviderMisconfigured, *StatusError)
// so the Router can decide whether to fail over.
type Provider interface {
	// Name identifies the provider in logs, metrics and Result.Source.
	Name() string
	// Search returns at most limit results for query. An empty result with
	// a nil error means the provider worked but found nothing.
	Search(ctx context.Context, query string, limit int) ([]Result, error)
}

// cleanResults drops results without a usable http(s) URL, removes
// duplicates, trims fields, and applies limit.
func cleanResults(in []Result, limit int) []Result {
	seen := make(map[string]bool, len(in))
	out := make([]Result, 0, min(len(in), limit))
	for _, r := range in {
		if len(out) >= limit {
			break
		}
		u, err := url.Parse(strings.TrimSpace(r.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		key := u.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Result{
			Title:   collapse(r.Title),
			URL:     key,
			Snippet: collapse(r.Snippet),
			Source:  r.Source,
		})
	}
	return out
}
