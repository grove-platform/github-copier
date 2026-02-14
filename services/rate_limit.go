package services

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitState holds the most recently observed GitHub API rate limit info.
// Updated atomically by rateLimitTransport on every API response.
type RateLimitState struct {
	mu        sync.RWMutex
	remaining int
	resetAt   time.Time
}

// Get returns the current rate limit state.
func (s *RateLimitState) Get() (remaining int, resetAt time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remaining, s.resetAt
}

// update stores the latest rate limit values from response headers.
func (s *RateLimitState) update(remaining int, resetAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remaining = remaining
	s.resetAt = resetAt
}

// GlobalRateLimitState is the shared rate limit state read by the health/metrics endpoints.
var GlobalRateLimitState = &RateLimitState{remaining: -1} // -1 = not yet observed

// rateLimitTransport is an http.RoundTripper that:
//  1. Records rate limit headers from every GitHub API response.
//  2. On HTTP 403 (primary rate limit) or 429 (secondary/abuse rate limit),
//     waits for the Retry-After / X-RateLimit-Reset period and retries once.
//  3. Respects context cancellation during the wait.
type rateLimitTransport struct {
	base    http.RoundTripper
	metrics *MetricsCollector // optional, may be nil
}

// newRateLimitTransport wraps a base transport with rate limit handling.
func newRateLimitTransport(base http.RoundTripper, metrics *MetricsCollector) *rateLimitTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &rateLimitTransport{base: base, metrics: metrics}
}

// RoundTrip implements http.RoundTripper.
func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.metrics != nil {
		t.metrics.RecordGitHubAPICall()
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if t.metrics != nil {
			t.metrics.RecordGitHubAPIError()
		}
		return resp, err
	}

	// Always record rate limit headers
	t.recordRateLimit(resp)

	// Check for rate limiting (403 primary or 429 secondary/abuse)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if isRateLimited(resp) {
			waitDuration := retryAfterDuration(resp)
			if waitDuration > 0 {
				LogWarning("GitHub API rate limited, waiting before retry",
					"status", resp.StatusCode,
					"wait_seconds", waitDuration.Seconds(),
					"url", req.URL.String(),
				)

				// Wait with context cancellation support
				if err := waitWithContext(req.Context(), waitDuration); err != nil {
					return resp, nil // context cancelled, return original response
				}

				// Close the original response body before retrying
				_ = resp.Body.Close()

				// Retry once
				if t.metrics != nil {
					t.metrics.RecordGitHubAPICall()
				}
				retryResp, retryErr := t.base.RoundTrip(req)
				if retryErr != nil {
					if t.metrics != nil {
						t.metrics.RecordGitHubAPIError()
					}
					return retryResp, retryErr
				}
				t.recordRateLimit(retryResp)
				return retryResp, nil
			}
		}
	}

	if resp.StatusCode >= 400 && t.metrics != nil {
		t.metrics.RecordGitHubAPIError()
	}

	return resp, err
}

// recordRateLimit extracts rate limit info from response headers and updates shared state.
func (t *rateLimitTransport) recordRateLimit(resp *http.Response) {
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	reset := resp.Header.Get("X-RateLimit-Reset")

	if remaining == "" && reset == "" {
		return
	}

	rem, _ := strconv.Atoi(remaining)
	resetUnix, _ := strconv.ParseInt(reset, 10, 64)
	resetTime := time.Unix(resetUnix, 0)

	GlobalRateLimitState.update(rem, resetTime)
}

// isRateLimited checks if a response indicates a rate limit condition.
func isRateLimited(resp *http.Response) bool {
	// HTTP 429 is always a rate limit
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}

	// HTTP 403 with X-RateLimit-Remaining: 0 is a primary rate limit
	if resp.StatusCode == http.StatusForbidden {
		remaining := resp.Header.Get("X-RateLimit-Remaining")
		if remaining == "0" {
			return true
		}
		// Also check for Retry-After header (abuse/secondary rate limit)
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
	}

	return false
}

// retryAfterDuration determines how long to wait before retrying.
// It checks Retry-After header first, then falls back to X-RateLimit-Reset.
// Returns 0 if no retry info is available. Caps at 60 seconds.
func retryAfterDuration(resp *http.Response) time.Duration {
	const maxWait = 60 * time.Second

	// Check Retry-After header (used by secondary/abuse rate limits)
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
			d := time.Duration(seconds) * time.Second
			if d > maxWait {
				return maxWait
			}
			return d
		}
	}

	// Fall back to X-RateLimit-Reset (Unix timestamp)
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if resetUnix, err := strconv.ParseInt(reset, 10, 64); err == nil {
			resetTime := time.Unix(resetUnix, 0)
			d := time.Until(resetTime)
			if d <= 0 {
				return 1 * time.Second // Already past, retry immediately with small buffer
			}
			if d > maxWait {
				return maxWait
			}
			return d
		}
	}

	return 0
}

// waitWithContext sleeps for the given duration, returning early if ctx is cancelled.
func waitWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
