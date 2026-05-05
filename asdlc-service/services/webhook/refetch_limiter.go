package webhook

import (
	"sync"
	"time"
)

// RefetchLimiter bounds the rate of forced webhook-secret refetches per
// (ocOrgID, sourceIP) pair. Per phase2.md §8.4: 1 refetch/sec, burst 5.
//
// A forged event stream that triggers HMAC mismatch on every delivery
// would otherwise hammer git-service with refetches; the limiter caps
// the amplification factor.
type RefetchLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64 // bucket capacity
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewRefetchLimiter constructs the limiter with the supplied per-second
// refill rate and burst cap. Defaults: 1 token/s, burst 5.
func NewRefetchLimiter(rate, burst float64) *RefetchLimiter {
	if rate <= 0 {
		rate = 1
	}
	if burst <= 0 {
		burst = 5
	}
	return &RefetchLimiter{
		rate:    rate,
		burst:   burst,
		buckets: map[string]*tokenBucket{},
	}
}

// Allow returns true if a refetch is permitted for the given key right
// now. Consumes one token on success.
func (r *RefetchLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: r.burst, last: now}
		r.buckets[key] = b
	}
	// Refill since last touch, capped at burst.
	delta := now.Sub(b.last).Seconds() * r.rate
	b.tokens = b.tokens + delta
	if b.tokens > r.burst {
		b.tokens = r.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
