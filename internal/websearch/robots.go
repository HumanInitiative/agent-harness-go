package websearch

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// maxRobotsBytes is how much of a robots.txt is parsed; RFC 9309 requires
// parsers to handle at least 500 KiB.
const maxRobotsBytes = 500 << 10

// robotsRules holds the rules of the one robots.txt group that applies to us.
type robotsRules struct {
	allowAll    bool
	disallowAll bool
	rules       []robotsRule
	// Sitemaps lists the Sitemap: URLs, which apply to every user agent.
	sitemaps []string
}

type robotsRule struct {
	allow   bool
	pattern string
}

// parseRobots implements RFC 9309: the group naming our product token is
// used if there is one, otherwise the "*" group; within it the most
// specific (longest) matching rule wins, and Allow wins a tie.
func parseRobots(body []byte, productToken string) robotsRules {
	if len(body) > maxRobotsBytes {
		body = body[:maxRobotsBytes]
	}
	token := strings.ToLower(productToken)

	type group struct {
		agents []string
		rules  []robotsRule
	}
	var groups []*group
	var current *group
	lastWasAgent := false
	var sitemaps []string

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), maxRobotsBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			// Consecutive user-agent lines share one group.
			if current == nil || !lastWasAgent {
				current = &group{}
				groups = append(groups, current)
			}
			current.agents = append(current.agents, strings.ToLower(value))
			lastWasAgent = true
		case "allow", "disallow":
			lastWasAgent = false
			if current == nil || value == "" {
				continue // an empty Disallow allows everything
			}
			current.rules = append(current.rules, robotsRule{allow: key == "allow", pattern: value})
		case "sitemap":
			sitemaps = append(sitemaps, value)
		default:
			lastWasAgent = false
		}
	}

	pick := func(match func(agent string) bool) []robotsRule {
		var rules []robotsRule
		found := false
		for _, g := range groups {
			for _, a := range g.agents {
				if match(a) {
					rules = append(rules, g.rules...)
					found = true
					break
				}
			}
		}
		if !found {
			return nil
		}
		if rules == nil {
			rules = []robotsRule{}
		}
		return rules
	}

	rules := pick(func(a string) bool { return token != "" && a != "*" && strings.Contains(token, a) })
	if rules == nil {
		rules = pick(func(a string) bool { return a == "*" })
	}
	return robotsRules{rules: rules, sitemaps: sitemaps}
}

// allowed reports whether path (with query) may be fetched.
func (r robotsRules) allowed(path string) bool {
	if r.disallowAll {
		return false
	}
	if r.allowAll || path == "/robots.txt" {
		return true
	}
	best, bestLen, bestAllow := false, -1, true
	for _, rule := range r.rules {
		if robotsMatch(rule.pattern, path) {
			l := len(rule.pattern)
			if l > bestLen || (l == bestLen && rule.allow && !bestAllow) {
				best, bestLen, bestAllow = true, l, rule.allow
			}
		}
	}
	if !best {
		return true
	}
	return bestAllow
}

// robotsMatch matches a robots.txt path pattern where '*' matches any
// sequence and a trailing '$' anchors the end.
func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	for i, part := range parts[1:] {
		last := i == len(parts)-2
		if last && anchored {
			return strings.HasSuffix(path[pos:], part)
		}
		idx := strings.Index(path[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}
	if anchored {
		return pos == len(path)
	}
	return true
}

// RobotsChecker answers robots.txt questions for any URL, fetching and
// caching each host's robots.txt.
type RobotsChecker struct {
	productToken string
	fetch        func(ctx context.Context, robotsURL string) (status int, body []byte, err error)
	cache        *Cache[robotsRules]
}

// NewRobotsChecker builds a checker. productToken is our crawler's name as
// it appears in the User-Agent (e.g. "HumanInitiativeBot"); fetch retrieves
// a robots.txt and is normally Fetcher.fetchRaw.
func NewRobotsChecker(productToken string, ttl time.Duration, now func() time.Time,
	fetch func(ctx context.Context, robotsURL string) (int, []byte, error), metrics *Metrics) *RobotsChecker {
	return &RobotsChecker{
		productToken: productToken,
		fetch:        fetch,
		cache:        NewCache[robotsRules]("robots", ttl, 5000, now, metrics),
	}
}

// Allowed reports whether u may be fetched under its host's robots.txt.
// Following RFC 9309, a missing robots.txt (4xx) allows everything and a
// server error (5xx) disallows everything for now.
//
// When the host cannot be reached at all (DNS failure, connection reset,
// TLS error, redirect loop), Allowed returns that error instead of a
// verdict: fetching the page would fail the same way, and "disallowed by
// robots.txt" would hide the real problem — such as a domain that does not
// exist.
func (c *RobotsChecker) Allowed(ctx context.Context, u *url.URL) (bool, error) {
	rules, err := c.rulesFor(ctx, u)
	if err != nil {
		return false, err
	}
	return rules.allowed(u.EscapedPath() + queryPart(u)), nil
}

// Sitemaps returns the Sitemap: URLs declared in u's host's robots.txt.
func (c *RobotsChecker) Sitemaps(ctx context.Context, u *url.URL) ([]string, error) {
	rules, err := c.rulesFor(ctx, u)
	return rules.sitemaps, err
}

func (c *RobotsChecker) rulesFor(ctx context.Context, u *url.URL) (robotsRules, error) {
	origin := u.Scheme + "://" + u.Host
	if rules, ok := c.cache.Get(origin); ok {
		return rules, nil
	}
	status, body, err := c.fetch(ctx, origin+"/robots.txt")
	if err != nil {
		// Not cached: the next attempt should try again.
		return robotsRules{}, fmt.Errorf("websearch: %s is unreachable: %w", u.Host, err)
	}
	var rules robotsRules
	switch {
	case status >= 500:
		rules = robotsRules{disallowAll: true}
	case status >= 400:
		rules = robotsRules{allowAll: true}
	case status >= 200 && status < 300:
		rules = parseRobots(body, c.productToken)
	default:
		rules = robotsRules{allowAll: true}
	}
	c.cache.Set(origin, rules)
	return rules, nil
}

func queryPart(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}
