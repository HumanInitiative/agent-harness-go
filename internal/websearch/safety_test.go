package websearch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.9.10", "10.0.0.1", "172.16.5.4", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", // cloud metadata
		"0.0.0.0", "0.1.2.3", "100.64.0.1", "224.0.0.1", "255.255.255.255", "240.0.0.1",
		"198.18.0.1", "192.0.2.1",
		"::1", "::", "fc00::1", "fd12:3456::1", "fe80::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", // IPv4-mapped forms must not slip through
		"64:ff9b::a00:1", // NAT64 wrapping 10.0.0.1
		"2001:db8::1",
	}
	for _, s := range blocked {
		if IsPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "103.10.20.30", "172.32.0.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if !IsPublicIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestGuard_CheckURL(t *testing.T) {
	g := Guard{}
	cases := []struct {
		url     string
		wantErr error
	}{
		{"https://example.com/page", nil},
		{"http://example.com:80/", nil},
		{"https://example.com:443/", nil},
		{"ftp://example.com/file", ErrInvalidURL},
		{"file:///etc/passwd", ErrInvalidURL},
		{"javascript:alert(1)", ErrInvalidURL},
		{"https://user:pass@example.com/", ErrInvalidURL},
		{"https://example.com:8443/", ErrInvalidURL},
		{"http://127.0.0.1/", ErrSSRF},
		{"http://169.254.169.254/latest/meta-data/", ErrSSRF},
		{"http://[::1]/", ErrSSRF},
		{"http://[::ffff:127.0.0.1]/", ErrSSRF},
		{"http://localhost/", ErrSSRF},
		{"http://api.localhost/", ErrSSRF},
		{"http:///nohost", ErrInvalidURL},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = g.CheckURL(u)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestGuard_CheckURL_CustomPorts(t *testing.T) {
	g := Guard{AllowedPorts: []int{443, 8443}}
	if err := g.CheckURL(mustURL(t, "https://example.com:8443/")); err != nil {
		t.Fatalf("8443 should be allowed: %v", err)
	}
	if err := g.CheckURL(mustURL(t, "http://example.com:80/")); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("80 should be rejected when not listed, got %v", err)
	}
}

// TestGuard_DialRefusesLoopback proves the connection-time check works on its
// own: a hostname that passes CheckURL but resolves to loopback (exactly the
// DNS-rebinding scenario) is still refused when dialing.
func TestGuard_DialRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("internal secret"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{DialContext: Guard{}.DialContext(time.Second)}}
	_, err := client.Get(srv.URL)
	if !errors.Is(err, ErrSSRF) {
		t.Fatalf("expected ErrSSRF when dialing %s, got %v", srv.URL, err)
	}

	// The same server is reachable when private networks are explicitly allowed.
	open := &http.Client{Transport: &http.Transport{DialContext: Guard{AllowPrivateNetworks: true}.DialContext(time.Second)}}
	resp, err := open.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected success with AllowPrivateNetworks, got %v", err)
	}
	_ = resp.Body.Close()
}

func TestGuard_DialChecksResolvedAddressNotName(t *testing.T) {
	dial := Guard{}.DialContext(time.Second)
	// "localhost" resolves to a loopback address; the dialer must refuse it
	// even though no URL check ran.
	_, err := dial(context.Background(), "tcp", net.JoinHostPort("localhost", "1"))
	if err == nil || !strings.Contains(err.Error(), ErrSSRF.Error()) {
		t.Fatalf("expected ErrSSRF dialing localhost, got %v", err)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}
