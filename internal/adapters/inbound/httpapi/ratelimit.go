package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter applies a token-bucket limit per API key, so one client
// cannot exhaust the model budget or capacity that other clients share.
//
// Limits are counted in this process's memory: with N instances the
// effective limit is N times the configured one. That is acceptable for a
// single standalone instance; a scaled-out deployment should enforce limits
// in a shared store (e.g. Redis) or at the gateway in front of it.
type RateLimiter struct {
	every time.Duration
	burst int

	mu       sync.Mutex
	limiters map[string]*rate.Limiter // bounded by the number of API keys
}

// NewRateLimiter allows perMinute requests per minute per API key on
// average, with short bursts of up to burst requests.
func NewRateLimiter(perMinute, burst int) (*RateLimiter, error) {
	if perMinute < 1 || burst < 1 {
		return nil, errors.New("httpapi: rate limit and burst must both be >= 1")
	}
	return &RateLimiter{
		every:    time.Minute / time.Duration(perMinute),
		burst:    burst,
		limiters: make(map[string]*rate.Limiter),
	}, nil
}

// Middleware rejects over-limit requests with 429 and a Retry-After header.
// It must run after APIKeyAuth, which identifies the caller.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reservation := l.limiterFor(apiKeyIDFrom(r.Context())).Reserve()
		if delay := reservation.Delay(); delay > 0 {
			// Give the token back: this request is rejected, not queued.
			reservation.Cancel()
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(delay.Seconds()))))
			writeError(w, r, http.StatusTooManyRequests, codeRateLimited, "rate limit exceeded, retry later")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *RateLimiter) limiterFor(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.limiters[key]
	if !ok {
		lim = rate.NewLimiter(rate.Every(l.every), l.burst)
		l.limiters[key] = lim
	}
	return lim
}
