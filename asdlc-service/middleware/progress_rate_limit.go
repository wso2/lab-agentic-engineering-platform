package middleware

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ProgressRateLimit returns middleware that token-bucket-rate-limits the
// /progress/* endpoints per org. Per-org rather than per-IP because the
// pathological case is a single tenant flooding Observer with viewers,
// not an external attacker. Capacity comes from
// task-execution-progress.md §6.2: 100 req/s, burst 200.
//
// Buckets are looked up by `{orgHandle}` extracted from the URL path
// pattern; if the orgHandle is empty the limiter falls through (the
// route was misconfigured — let the handler 404).
func ProgressRateLimit(rps rate.Limit, burst int) func(http.Handler) http.Handler {
	store := newLimiterStore(rps, burst)
	retryAfter := int(time.Second / time.Duration(rps))
	if retryAfter < 1 {
		retryAfter = 1
	}
	retryAfterHeader := fmt.Sprintf("%d", retryAfter)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			org := r.PathValue("orgHandle")
			if org == "" {
				next.ServeHTTP(w, r)
				return
			}
			lim := store.get(org)
			if !lim.Allow() {
				w.Header().Set("Retry-After", retryAfterHeader)
				http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type limiterStore struct {
	rps   rate.Limit
	burst int

	mu       sync.RWMutex
	limiters map[string]*rate.Limiter
}

func newLimiterStore(rps rate.Limit, burst int) *limiterStore {
	return &limiterStore{
		rps:      rps,
		burst:    burst,
		limiters: map[string]*rate.Limiter{},
	}
}

func (s *limiterStore) get(key string) *rate.Limiter {
	s.mu.RLock()
	if lim, ok := s.limiters[key]; ok {
		s.mu.RUnlock()
		return lim
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if lim, ok := s.limiters[key]; ok {
		return lim
	}
	lim := rate.NewLimiter(s.rps, s.burst)
	s.limiters[key] = lim
	return lim
}
