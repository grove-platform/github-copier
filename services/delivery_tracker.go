package services

import (
	"sync"
	"time"
)

const deliveryHistoryMax = 200

// DeliverySnapshot is one observed webhook delivery ID for operator diagnostics.
type DeliverySnapshot struct {
	DeliveryID string    `json:"delivery_id"`
	SeenAt     time.Time `json:"seen_at"`
	Duplicate  bool      `json:"duplicate"`
}

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

	// history is a bounded ring of recent TryRecord outcomes (new vs duplicate) for diagnostics.
	history []DeliverySnapshot

	// stopCleanup signals the background goroutine to stop
	stopCleanup chan struct{}
}

// NewDeliveryTracker creates a tracker that expires entries after the given TTL.
// A background goroutine periodically purges expired entries.
func NewDeliveryTracker(ttl time.Duration) *DeliveryTracker {
	dt := &DeliveryTracker{
		entries:     make(map[string]time.Time),
		ttl:         ttl,
		history:     make([]DeliverySnapshot, 0, 32),
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
			dt.appendHistoryLocked(deliveryID, true)
			return false // duplicate within TTL
		}
		// Expired entry — allow reprocessing
	}

	dt.entries[deliveryID] = time.Now()
	dt.appendHistoryLocked(deliveryID, false)
	return true
}

func (dt *DeliveryTracker) appendHistoryLocked(deliveryID string, duplicate bool) {
	if deliveryID == "" {
		return
	}
	dt.history = append(dt.history, DeliverySnapshot{
		DeliveryID: deliveryID,
		SeenAt:     time.Now().UTC(),
		Duplicate:  duplicate,
	})
	if len(dt.history) > deliveryHistoryMax {
		dt.history = dt.history[len(dt.history)-deliveryHistoryMax:]
	}
}

// HistoryLen returns how many recent delivery observations are buffered for diagnostics.
func (dt *DeliveryTracker) HistoryLen() int {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	return len(dt.history)
}

// RecentDeliveries returns the last up to max observations (newest at end).
func (dt *DeliveryTracker) RecentDeliveries(max int) []DeliverySnapshot {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if len(dt.history) == 0 {
		return nil
	}
	if max <= 0 {
		max = 100
	}
	if max > deliveryHistoryMax {
		max = deliveryHistoryMax
	}
	if len(dt.history) <= max {
		out := make([]DeliverySnapshot, len(dt.history))
		copy(out, dt.history)
		return out
	}
	return append([]DeliverySnapshot(nil), dt.history[len(dt.history)-max:]...)
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
