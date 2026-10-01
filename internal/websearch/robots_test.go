package websearch

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

const robotsFixture = `
# Example corporate robots.txt
User-agent: *
Disallow: /admin
Disallow: /search
Allow: /search/about
Disallow: /*.json$
Disallow: /private*/data

User-agent: BadBot
User-agent: OtherBot
Disallow: /

Sitemap: https://contoh.co.id/sitemap.xml
`

func TestParseRobots_StarGroup(t *testing.T) {
	rules := parseRobots([]byte(robotsFixture), "HumanInitiativeBot")
	cases := map[string]bool{
		"/":               true,
		"/csr":            true,
		"/admin":          false,
		"/admin/users":    false,
		"/search?q=x":     false,
		"/search/about":   true, // longer Allow beats shorter Disallow
		"/data.json":      false,
		"/data.json?v=1":  true, // $ anchors the end
		"/private-x/data": false,
		"/private/info":   true,
		"/robots.txt":     true,
	}
	for path, want := range cases {
		if got := rules.allowed(path); got != want {
			t.Errorf("allowed(%q) = %v, want %v", path, got, want)
		}
	}
	if len(rules.sitemaps) != 1 || rules.sitemaps[0] != "https://contoh.co.id/sitemap.xml" {
		t.Errorf("sitemaps = %v", rules.sitemaps)
	}
}

func TestParseRobots_SpecificGroupOverridesStar(t *testing.T) {
	rules := parseRobots([]byte(robotsFixture), "BadBot/2.0")
	if rules.allowed("/csr") {
		t.Fatal("BadBot group (Disallow: /) should apply instead of *")
	}
	rules = parseRobots([]byte(robotsFixture), "OtherBot")
	if rules.allowed("/csr") {
		t.Fatal("consecutive User-agent lines must share one group")
	}
}

func TestParseRobots_EmptyDisallowAllowsAll(t *testing.T) {
	rules := parseRobots([]byte("User-agent: *\nDisallow:\n"), "x")
	if !rules.allowed("/anything") {
		t.Fatal("empty Disallow should allow everything")
	}
}

func TestParseRobots_TieGoesToAllow(t *testing.T) {
	rules := parseRobots([]byte("User-agent: *\nDisallow: /page\nAllow: /page\n"), "x")
	if !rules.allowed("/page") {
		t.Fatal("equal-length Allow and Disallow: Allow must win")
	}
}

func TestRobotsChecker_StatusHandlingAndCaching(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   bool
	}{
		{"missing robots.txt allows", 404, true},
		{"server error disallows", 503, false},
		{"parsed rules apply", 200, false}, // /admin is disallowed by the fixture
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			checker := NewRobotsChecker("HumanInitiativeBot", time.Hour, nil,
				func(context.Context, string) (int, []byte, error) {
					calls++
					return tc.status, []byte(robotsFixture), nil
				}, nil)

			u, _ := url.Parse("https://contoh.co.id/admin")
			got, err := checker.Allowed(context.Background(), u)
			if err != nil || got != tc.want {
				t.Fatalf("Allowed = %v, %v; want %v", got, err, tc.want)
			}
			_, _ = checker.Allowed(context.Background(), u)
			if calls != 1 {
				t.Fatalf("robots.txt fetched %d times, want 1 (cached)", calls)
			}
		})
	}
}

func TestRobotsChecker_UnreachableHostReportsTheRealErrorAndRetries(t *testing.T) {
	calls := 0
	dnsErr := errors.New("lookup nosuch.example: no such host")
	checker := NewRobotsChecker("HumanInitiativeBot", time.Hour, nil,
		func(context.Context, string) (int, []byte, error) {
			calls++
			return 0, nil, dnsErr
		}, nil)

	u, _ := url.Parse("https://nosuch.example/csr")
	if _, err := checker.Allowed(context.Background(), u); !errors.Is(err, dnsErr) {
		t.Fatalf("expected the DNS error, got %v", err)
	}
	_, _ = checker.Allowed(context.Background(), u)
	if calls != 2 {
		t.Fatalf("a network failure must not be cached; fetched %d times", calls)
	}
}
