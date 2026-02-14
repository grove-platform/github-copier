package services

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDeliveryTracker_TryRecord(t *testing.T) {
	dt := NewDeliveryTracker(1 * time.Hour)
	defer dt.Stop()

	// First call should succeed
	if !dt.TryRecord("delivery-1") {
		t.Error("expected first TryRecord to return true")
	}

	// Duplicate should be rejected
	if dt.TryRecord("delivery-1") {
		t.Error("expected duplicate TryRecord to return false")
	}

	// Different ID should succeed
	if !dt.TryRecord("delivery-2") {
		t.Error("expected TryRecord for new ID to return true")
	}
}

func TestDeliveryTracker_TTLExpiry(t *testing.T) {
	dt := NewDeliveryTracker(50 * time.Millisecond)
	defer dt.Stop()

	if !dt.TryRecord("delivery-1") {
		t.Error("expected first TryRecord to return true")
	}

	// Wait for TTL to expire
	time.Sleep(60 * time.Millisecond)

	// Should allow reprocessing after expiry
	if !dt.TryRecord("delivery-1") {
		t.Error("expected TryRecord to return true after TTL expiry")
	}
}

func TestDeliveryTracker_Len(t *testing.T) {
	dt := NewDeliveryTracker(1 * time.Hour)
	defer dt.Stop()

	if dt.Len() != 0 {
		t.Errorf("expected Len()=0, got %d", dt.Len())
	}

	dt.TryRecord("a")
	dt.TryRecord("b")
	dt.TryRecord("c")
	dt.TryRecord("a") // duplicate, should not increase count

	if dt.Len() != 3 {
		t.Errorf("expected Len()=3, got %d", dt.Len())
	}
}

func TestDeliveryTracker_PurgeExpired(t *testing.T) {
	dt := NewDeliveryTracker(50 * time.Millisecond)
	defer dt.Stop()

	dt.TryRecord("a")
	dt.TryRecord("b")

	if dt.Len() != 2 {
		t.Errorf("expected Len()=2, got %d", dt.Len())
	}

	// Wait for expiry + cleanup cycle (ttl/2 = 25ms, so total ~75ms should trigger)
	time.Sleep(100 * time.Millisecond)

	if dt.Len() != 0 {
		t.Errorf("expected Len()=0 after purge, got %d", dt.Len())
	}
}

func TestDeliveryTracker_ConcurrentAccess(t *testing.T) {
	dt := NewDeliveryTracker(1 * time.Hour)
	defer dt.Stop()

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]bool, goroutines)

	// All goroutines try to record the same delivery ID
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = dt.TryRecord("same-delivery")
		}(i)
	}
	wg.Wait()

	// Exactly one goroutine should succeed
	successCount := 0
	for _, ok := range results {
		if ok {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d", successCount)
	}
}

func TestDeliveryTracker_ConcurrentDifferentIDs(t *testing.T) {
	dt := NewDeliveryTracker(1 * time.Hour)
	defer dt.Stop()

	const goroutines = 100
	var wg sync.WaitGroup

	// Each goroutine records a unique ID
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("delivery-%d", idx)
			if !dt.TryRecord(id) {
				t.Errorf("expected TryRecord(%s) to return true", id)
			}
		}(i)
	}
	wg.Wait()

	if dt.Len() != goroutines {
		t.Errorf("expected Len()=%d, got %d", goroutines, dt.Len())
	}
}
