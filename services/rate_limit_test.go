package services

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// mockTransport is a configurable http.RoundTripper for testing.
type mockTransport struct {
	handler func(req *http.Request) (*http.Response, error)
	calls   int
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls++
	return m.handler(req)
}

func TestRateLimitTransport_RecordsRateLimitHeaders(t *testing.T) {
	// Save and restore global state
	origRemaining, origReset := GlobalRateLimitState.Get()
	defer GlobalRateLimitState.update(origRemaining, origReset)

	resetTime := time.Now().Add(30 * time.Minute).Unix()

	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header: http.Header{
					"X-Ratelimit-Remaining": []string{"4500"},
					"X-Ratelimit-Reset":     []string{strconv.FormatInt(resetTime, 10)},
				},
				Body: http.NoBody,
			}, nil
		},
	}

	rt := newRateLimitTransport(mock, nil)
	req, _ := http.NewRequest("GET", "https://api.github.com/repos", nil)
	_, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	remaining, reset := GlobalRateLimitState.Get()
	if remaining != 4500 {
		t.Errorf("expected remaining=4500, got %d", remaining)
	}
	if reset.Unix() != resetTime {
		t.Errorf("expected resetAt=%d, got %d", resetTime, reset.Unix())
	}
}

func TestRateLimitTransport_RetriesOn429(t *testing.T) {
	callCount := 0
	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return &http.Response{
					StatusCode: 429,
					Header: http.Header{
						"Retry-After": []string{"1"},
					},
					Body: http.NoBody,
				}, nil
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       http.NoBody,
			}, nil
		},
	}

	rt := newRateLimitTransport(mock, nil)
	req, _ := http.NewRequest("GET", "https://api.github.com/repos", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 after retry, got %d", resp.StatusCode)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (original + retry), got %d", callCount)
	}
}

func TestRateLimitTransport_RetriesOn403WithRateLimitExhausted(t *testing.T) {
	resetTime := time.Now().Add(1 * time.Second).Unix()
	callCount := 0

	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return &http.Response{
					StatusCode: 403,
					Header: http.Header{
						"X-Ratelimit-Remaining": []string{"0"},
						"X-Ratelimit-Reset":     []string{strconv.FormatInt(resetTime, 10)},
					},
					Body: http.NoBody,
				}, nil
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       http.NoBody,
			}, nil
		},
	}

	rt := newRateLimitTransport(mock, nil)
	req, _ := http.NewRequest("GET", "https://api.github.com/repos", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 after retry, got %d", resp.StatusCode)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestRateLimitTransport_NoRetryOnRegular403(t *testing.T) {
	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 403,
				Header: http.Header{
					"X-Ratelimit-Remaining": []string{"4999"}, // Not exhausted
				},
				Body: http.NoBody,
			}, nil
		},
	}

	rt := newRateLimitTransport(mock, nil)
	req, _ := http.NewRequest("GET", "https://api.github.com/repos", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	if resp.StatusCode != 403 {
		t.Errorf("expected status 403 (no retry), got %d", resp.StatusCode)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call (no retry), got %d", mock.calls)
	}
}

func TestRateLimitTransport_RespectsContextCancellation(t *testing.T) {
	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 429,
				Header: http.Header{
					"Retry-After": []string{"60"}, // 60 seconds
				},
				Body: http.NoBody,
			}, nil
		},
	}

	rt := newRateLimitTransport(mock, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error: %v", err)
	}

	// Should return the original 429 response since context cancelled during wait
	if resp.StatusCode != 429 {
		t.Errorf("expected original 429 response, got %d", resp.StatusCode)
	}
	// Should not have retried (only 1 call to mock)
	if mock.calls != 1 {
		t.Errorf("expected 1 call (no retry due to context), got %d", mock.calls)
	}
}

func TestRateLimitTransport_RecordsMetrics(t *testing.T) {
	mc := NewMetricsCollector()

	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{},
				Body:       http.NoBody,
			}, nil
		},
	}

	rt := newRateLimitTransport(mock, mc)
	req, _ := http.NewRequest("GET", "https://api.github.com/repos", nil)
	_, _ = rt.RoundTrip(req)

	mc.mu.RLock()
	defer mc.mu.RUnlock()
	if mc.githubAPICalls != 1 {
		t.Errorf("expected 1 API call recorded, got %d", mc.githubAPICalls)
	}
	if mc.githubAPIErrors != 0 {
		t.Errorf("expected 0 API errors, got %d", mc.githubAPIErrors)
	}
}

func TestRateLimitTransport_RecordsErrorMetrics(t *testing.T) {
	mc := NewMetricsCollector()

	mock := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	rt := newRateLimitTransport(mock, mc)
	req, _ := http.NewRequest("GET", "https://api.github.com/repos", nil)
	_, _ = rt.RoundTrip(req)

	mc.mu.RLock()
	defer mc.mu.RUnlock()
	if mc.githubAPICalls != 1 {
		t.Errorf("expected 1 API call recorded, got %d", mc.githubAPICalls)
	}
	if mc.githubAPIErrors != 1 {
		t.Errorf("expected 1 API error, got %d", mc.githubAPIErrors)
	}
}

func TestIsRateLimited(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		headers    http.Header
		want       bool
	}{
		{
			name:       "429 is always rate limited",
			statusCode: 429,
			headers:    http.Header{},
			want:       true,
		},
		{
			name:       "403 with remaining 0",
			statusCode: 403,
			headers:    http.Header{"X-Ratelimit-Remaining": []string{"0"}},
			want:       true,
		},
		{
			name:       "403 with Retry-After",
			statusCode: 403,
			headers:    http.Header{"Retry-After": []string{"60"}},
			want:       true,
		},
		{
			name:       "403 with remaining > 0 (not rate limited)",
			statusCode: 403,
			headers:    http.Header{"X-Ratelimit-Remaining": []string{"4999"}},
			want:       false,
		},
		{
			name:       "200 is never rate limited",
			statusCode: 200,
			headers:    http.Header{},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tt.statusCode, Header: tt.headers}
			if got := isRateLimited(resp); got != tt.want {
				t.Errorf("isRateLimited() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryAfterDuration(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		wantMin time.Duration
		wantMax time.Duration
	}{
		{
			name:    "Retry-After in seconds",
			headers: http.Header{"Retry-After": []string{"5"}},
			wantMin: 5 * time.Second,
			wantMax: 5 * time.Second,
		},
		{
			name:    "Retry-After capped at 60s",
			headers: http.Header{"Retry-After": []string{"120"}},
			wantMin: 60 * time.Second,
			wantMax: 60 * time.Second,
		},
		{
			name: "X-RateLimit-Reset fallback",
			headers: http.Header{
				"X-Ratelimit-Reset": []string{strconv.FormatInt(time.Now().Add(10*time.Second).Unix(), 10)},
			},
			wantMin: 8 * time.Second,  // Allow some clock skew
			wantMax: 12 * time.Second, // Allow some clock skew
		},
		{
			name:    "no headers returns 0",
			headers: http.Header{},
			wantMin: 0,
			wantMax: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: tt.headers}
			got := retryAfterDuration(resp)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("retryAfterDuration() = %v, want between %v and %v", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestRateLimitState_Concurrent(t *testing.T) {
	state := &RateLimitState{remaining: -1}

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			state.update(i, time.Now())
		}
		close(done)
	}()

	// Concurrent reads while writing
	for i := 0; i < 1000; i++ {
		state.Get()
	}

	<-done // Wait for writer to finish
}

func TestCurrentRateLimitInfo_DefaultState(t *testing.T) {
	// Save and restore
	origRemaining, origReset := GlobalRateLimitState.Get()
	defer GlobalRateLimitState.update(origRemaining, origReset)

	GlobalRateLimitState.update(-1, time.Time{})
	info := currentRateLimitInfo()
	if info.Remaining != -1 {
		t.Errorf("expected remaining=-1 for default state, got %d", info.Remaining)
	}
}

func TestCurrentRateLimitInfo_WithData(t *testing.T) {
	origRemaining, origReset := GlobalRateLimitState.Get()
	defer GlobalRateLimitState.update(origRemaining, origReset)

	resetAt := time.Now().Add(30 * time.Minute)
	GlobalRateLimitState.update(4500, resetAt)

	info := currentRateLimitInfo()
	if info.Remaining != 4500 {
		t.Errorf("expected remaining=4500, got %d", info.Remaining)
	}
	if info.ResetAt.Unix() != resetAt.Unix() {
		t.Errorf("expected resetAt=%v, got %v", resetAt, info.ResetAt)
	}
}
