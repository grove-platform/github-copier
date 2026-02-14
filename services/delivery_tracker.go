package services

import (
	"sync"
	"time"
)

// DeliveryTracker tracks processed GitHub webhook delivery IDs to prevent
// duplicate processing. GitHub retries deliveries on timeout or error, and
// the X-GitHub-Delivery header uniquely identifies each delivery.
//
// This is an in-memory implementation suitable for single-instance deployments.
// For multi-instance deployments, replace with a shared store (e.g. MongoDB or Redis).
type DeliveryTracker struct {
	mu      sync.Mutex
	entries map[string]time.Time
	ttl     time.Duration

	// stopCleanup signals the background goroutine to stop
	stopCleanup chan struct{}
}

// NewDeliveryTracker creates a tracker that expires entries after the given TTL.
// A background goroutine periodically purges expired entries.
func NewDeliveryTracker(ttl time.Duration) *DeliveryTracker {
	dt := &DeliveryTracker{
		entries:     make(map[string]time.Time),
		ttl:         ttl,
		stopCleanup: make(chan struct{}),
	}
	go dt.cleanupLoop()
	return dt
}

// TryRecord attempts to record a delivery ID. Returns true if the ID is new
// (not a duplicate), false if it was already seen within the TTL window.
func (dt *DeliveryTracker) TryRecord(deliveryID string) bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if seenAt, exists := dt.entries[deliveryID]; exists {
		if time.Since(seenAt) < dt.ttl {
			return false // duplicate within TTL
		}
		// Expired entry — allow reprocessing
	}

	dt.entries[deliveryID] = time.Now()
	return true
}

// Len returns the current number of tracked delivery IDs (for diagnostics).
func (dt *DeliveryTracker) Len() int {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	return len(dt.entries)
}

// Stop halts the background cleanup goroutine.
func (dt *DeliveryTracker) Stop() {
	close(dt.stopCleanup)
}

// cleanupLoop periodically removes expired entries to bound memory usage.
func (dt *DeliveryTracker) cleanupLoop() {
	ticker := time.NewTicker(dt.ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dt.purgeExpired()
		case <-dt.stopCleanup:
			return
		}
	}
}

// purgeExpired removes all entries older than TTL.
func (dt *DeliveryTracker) purgeExpired() {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	now := time.Now()
	for id, seenAt := range dt.entries {
		if now.Sub(seenAt) >= dt.ttl {
			delete(dt.entries, id)
		}
	}
}
