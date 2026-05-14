package requests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// errRetry signals "retry this attempt" inside the loop. Internal only.
var errRetry = errors.New("retry")

// RetryableHTTPClient wraps any HttpClient (typically *http.Client whose
// Transport already includes httpx.WrapTransport for correlation-ID
// propagation) with retry, jittered exponential backoff, and per-attempt
// timeouts. Plug into oapi-codegen via gen.WithHTTPClient(retryable).
// Ported verbatim from agent-manager/clients/requests.
type RetryableHTTPClient struct {
	client HttpClient
	config RequestRetryConfig
}

// NewRetryableHTTPClient — config is optional; defaults apply when omitted.
func NewRetryableHTTPClient(client HttpClient, config ...RequestRetryConfig) *RetryableHTTPClient {
	if client == nil {
		client = &http.Client{}
	}
	var cfg RequestRetryConfig
	if len(config) > 0 {
		cfg = config[0]
	}
	return &RetryableHTTPClient{client: client, config: cfg}
}

// Do executes req with retry. The original body is buffered once up front
// so each retry sees a fresh reader (oapi-codegen's body Readers are not
// reusable on their own). Honors ctx cancellation between attempts.
func (c *RetryableHTTPClient) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	cfg := c.config.getRetryConfig(&HttpRequest{Method: req.Method})
	log := slog.Default().With(
		slog.String("method", req.Method),
		slog.String("url", req.URL.String()),
	)

	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if closeErr := req.Body.Close(); closeErr != nil {
			log.Warn("failed to close request body", slog.String("error", closeErr.Error()))
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
	}

	for attempt := 1; attempt <= cfg.RetryAttemptsMax+1; attempt++ {
		isLastAttempt := attempt == cfg.RetryAttemptsMax+1

		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.doAttempt(ctx, req, cfg, attempt, isLastAttempt, log)
		if !errors.Is(err, errRetry) {
			return resp, err
		}

		if !isLastAttempt {
			wait := calculateBackoff(cfg.RetryWaitMin, cfg.RetryWaitMax, attempt)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry wait: %w", ctx.Err())
			}
		}
	}
	return nil, fmt.Errorf("unreachable: retry loop exited without returning")
}

func (c *RetryableHTTPClient) doAttempt(ctx context.Context, req *http.Request, cfg RequestRetryConfig, attempt int, isLastAttempt bool, log *slog.Logger) (*http.Response, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, cfg.AttemptTimeout)
	defer cancel()

	reqWithTimeout := req.Clone(attemptCtx)
	start := time.Now()
	resp, err := c.client.Do(reqWithTimeout)
	elapsed := time.Since(start)

	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("context cancelled or timed out: %w", ctx.Err())
		}
		if attemptCtx.Err() != nil {
			attrs := []any{
				slog.Int("attempt", attempt),
				slog.Int("maxAttempts", cfg.RetryAttemptsMax+1),
				slog.Duration("timeout", cfg.AttemptTimeout),
			}
			if isLastAttempt {
				log.Warn("HTTP request timed out after all attempts", attrs...)
				return nil, fmt.Errorf("request timed out after %d attempts: %w", attempt, err)
			}
			log.Debug("HTTP request attempt timed out, retrying", attrs...)
			return nil, errRetry
		}
		attrs := []any{
			slog.Int("attempt", attempt),
			slog.Int("maxAttempts", cfg.RetryAttemptsMax+1),
			slog.String("error", err.Error()),
		}
		if isLastAttempt {
			log.Warn("HTTP request failed after all attempts", attrs...)
			return nil, fmt.Errorf("request failed after %d attempts: %w", attempt, err)
		}
		log.Debug("HTTP request failed, retrying", attrs...)
		return nil, errRetry
	}

	if cfg.RetryOnStatus != nil && cfg.RetryOnStatus(resp.StatusCode) {
		attrs := []any{
			slog.Int("attempt", attempt),
			slog.Int("maxAttempts", cfg.RetryAttemptsMax+1),
			slog.Duration("duration", elapsed),
			slog.Int("status", resp.StatusCode),
		}
		if isLastAttempt {
			log.Warn("HTTP request returned retryable status after all attempts", attrs...)
			// Buffer body before attemptCtx cancellation so the caller can read it.
			body, readErr := io.ReadAll(resp.Body)
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Warn("failed to close response body", slog.String("error", closeErr.Error()))
			}
			if readErr != nil {
				return nil, fmt.Errorf("failed to read response body: %w", readErr)
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		}
		log.Debug("HTTP request returned retryable status, retrying", attrs...)
		if _, drainErr := io.Copy(io.Discard, resp.Body); drainErr != nil {
			log.Warn("failed to drain response body", slog.String("error", drainErr.Error()))
		}
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn("failed to close response body", slog.String("error", closeErr.Error()))
		}
		return nil, errRetry
	}

	// Non-retryable response: buffer body before attemptCtx cancels.
	body, readErr := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		log.Warn("failed to close response body", slog.String("error", closeErr.Error()))
	}
	if readErr != nil {
		return nil, fmt.Errorf("failed to read response body: %w", readErr)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// calculateBackoff returns exponential backoff with equal-jitter
// ([base/2, base]) to prevent thundering herd on simultaneous client
// retries. Capped at max.
func calculateBackoff(min, max time.Duration, attempt int) time.Duration {
	base := min * time.Duration(1<<uint(attempt-1))
	if base > max {
		base = max
	}
	half := base / 2
	if half <= 0 {
		return base
	}
	return half + time.Duration(rand.Int64N(int64(half)))
}
