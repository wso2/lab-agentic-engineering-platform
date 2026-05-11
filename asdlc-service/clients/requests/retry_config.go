package requests

import (
	"net/http"
	"slices"
	"time"
)

// Default retry configuration values. Mirrors agent-manager.
const (
	DefaultRetryWaitMin     = 1 * time.Second
	DefaultRetryWaitMax     = 10 * time.Second
	DefaultRetryAttemptsMax = 3
	DefaultAttemptTimeout   = 30 * time.Second
)

// TransientHTTPErrorCodes are statuses worth retrying for *non-idempotent*
// operations (everything except GET/DELETE).
var TransientHTTPErrorCodes = []int{
	http.StatusTooManyRequests,    // 429
	http.StatusBadGateway,         // 502
	http.StatusServiceUnavailable, // 503
	http.StatusGatewayTimeout,     // 504
}

// TransientHTTPGETErrorCodes adds 500 for idempotent ops where retrying a
// half-applied request is safe.
var TransientHTTPGETErrorCodes = []int{
	http.StatusTooManyRequests,     // 429
	http.StatusInternalServerError, // 500
	http.StatusBadGateway,          // 502
	http.StatusServiceUnavailable,  // 503
	http.StatusGatewayTimeout,      // 504
}

// RequestRetryConfig drives RetryableHTTPClient. Callers usually pass a
// custom RetryOnStatus to bolt on resource-specific behavior — e.g. the
// openchoreo client invalidates its cached token on 401 inside that hook
// before returning true.
type RequestRetryConfig struct {
	RetryWaitMin     time.Duration
	RetryWaitMax     time.Duration
	RetryAttemptsMax int
	AttemptTimeout   time.Duration
	RetryOnStatus    func(status int) bool
}

// getRetryConfig fills unset fields with defaults and wires a sane default
// RetryOnStatus that idempotent-aware-picks from the two transient sets.
// Returns by value so callers can keep their original cfg untouched.
func (cfg RequestRetryConfig) getRetryConfig(req *HttpRequest) RequestRetryConfig {
	if cfg.RetryWaitMin == 0 {
		cfg.RetryWaitMin = DefaultRetryWaitMin
	}
	if cfg.RetryWaitMax == 0 {
		cfg.RetryWaitMax = DefaultRetryWaitMax
	}
	if cfg.RetryAttemptsMax == 0 {
		cfg.RetryAttemptsMax = DefaultRetryAttemptsMax
	}
	if cfg.AttemptTimeout == 0 {
		cfg.AttemptTimeout = DefaultAttemptTimeout
	}
	if cfg.RetryOnStatus == nil {
		cfg.RetryOnStatus = func(status int) bool {
			if req.Method == http.MethodGet || req.Method == http.MethodDelete {
				return slices.Contains(TransientHTTPGETErrorCodes, status)
			}
			return slices.Contains(TransientHTTPErrorCodes, status)
		}
	}
	return cfg
}
