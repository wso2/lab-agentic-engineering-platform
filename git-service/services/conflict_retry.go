package services

// Conflict-retry helper for the artifact save flow (V1 of artifact-store-v2).
//
// Two retry policies live here:
//
//   - CAS retries: PutContents 409 (SHA precondition mismatch) and UpdateRef
//     422 (non-fast-forward). Bounded by both an attempt count and a
//     per-(org, project) leaky bucket, so a thrashing project can't starve
//     legitimate retries from peers.
//
//   - Tag-collision retries: CreateTagRef 422 when an external pusher claimed
//     the same tag name. No leaky bucket — tag collisions can only come from
//     external pushers (the per-project mutex serialises in-project saves),
//     so the bucket would over-fire. Bounded by attempt count only.
//
// See docs/design/artifact-store-v2.md §9.3.

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// casRetryAttempts is the per-call attempt schedule (50ms / 200ms / 800ms,
// each with ±50% jitter applied at runtime). Three attempts is enough to
// shed transient races without amplifying contention.
var casRetryAttempts = []time.Duration{
	50 * time.Millisecond,
	200 * time.Millisecond,
	800 * time.Millisecond,
}

// tagRetryAttempts mirrors casRetryAttempts; tag-collision retries use the
// same backoff but bypass the leaky bucket.
var tagRetryAttempts = []time.Duration{
	50 * time.Millisecond,
	200 * time.Millisecond,
	800 * time.Millisecond,
}

// ErrConflictBudgetExhausted is surfaced when the per-project leaky bucket is
// empty — the save flow short-circuits to a 409 surface rather than spinning.
var ErrConflictBudgetExhausted = errors.New("conflict retry budget exhausted")

// conflictRetrier holds the global leaky-bucket state for CAS retries.
// Bucket: 6 tokens per (org, project), refilling at 6/min (1 every 10s).
type conflictRetrier struct {
	mu      sync.Mutex
	buckets map[string]*bucketState
}

type bucketState struct {
	tokens     int
	lastRefill time.Time
}

const (
	bucketCapacity = 6
	bucketRefillEv = 10 * time.Second // 1 token per 10s = 6/min
)

var globalRetrier = &conflictRetrier{
	buckets: make(map[string]*bucketState),
}

// claim atomically deducts one token from the (org, project) bucket. Returns
// false if the bucket is empty.
func (r *conflictRetrier) claim(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.buckets[key]
	if !ok {
		b = &bucketState{tokens: bucketCapacity, lastRefill: now}
		r.buckets[key] = b
	}
	// Refill: 1 token per bucketRefillEv elapsed since lastRefill, capped.
	elapsed := now.Sub(b.lastRefill)
	if elapsed >= bucketRefillEv {
		toAdd := int(elapsed / bucketRefillEv)
		b.tokens += toAdd
		if b.tokens > bucketCapacity {
			b.tokens = bucketCapacity
		}
		b.lastRefill = now
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// retryOnCASConflict runs `fn` and on a conflict error (ErrSHAMismatch or
// ErrRefNotFastForward) backs off, claims a budget token, and retries.
// Bounded by len(casRetryAttempts). Final failure returns the last error
// wrapped so the caller can branch.
//
// `bucketKey` should be "<orgID>:<projectID>" — uniquely keys the per-project
// leaky bucket.
func retryOnCASConflict(ctx context.Context, bucketKey string, fn func() error) error {
	err := fn()
	if !isCASConflict(err) {
		return err
	}
	for _, delay := range casRetryAttempts {
		if !globalRetrier.claim(bucketKey) {
			return ErrConflictBudgetExhausted
		}
		if jerr := jitterSleep(ctx, delay); jerr != nil {
			return jerr
		}
		err = fn()
		if !isCASConflict(err) {
			return err
		}
	}
	return err
}

// retryOnTagCollision runs `fn` and on ErrTagAlreadyExists backs off and
// retries — without consuming the CAS leaky-bucket budget. See §9.3.
func retryOnTagCollision(ctx context.Context, fn func() error) error {
	err := fn()
	if !errors.Is(err, ErrTagAlreadyExists) {
		return err
	}
	for _, delay := range tagRetryAttempts {
		if jerr := jitterSleep(ctx, delay); jerr != nil {
			return jerr
		}
		err = fn()
		if !errors.Is(err, ErrTagAlreadyExists) {
			return err
		}
	}
	return err
}

// isCASConflict reports whether err is one of the GitHub CAS-mismatch
// sentinels. ErrConflictBudgetExhausted is intentionally NOT counted — that
// terminates retry, it doesn't trigger another.
func isCASConflict(err error) bool {
	return errors.Is(err, ErrSHAMismatch) || errors.Is(err, ErrRefNotFastForward)
}

// jitterSleep waits for `base` with ±50% uniform jitter, respecting ctx
// cancellation. Returns ctx.Err() if the context is cancelled mid-sleep.
func jitterSleep(ctx context.Context, base time.Duration) error {
	jitter := time.Duration(float64(base) * (rand.Float64() - 0.5)) // ±50%
	delay := base + jitter
	if delay < 0 {
		delay = 0
	}
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
