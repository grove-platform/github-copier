package services

import (
	"testing"
	"time"
)

func TestTokenBucket_AllowsUpToMax(t *testing.T) {
	b := newTokenBucket(3, time.Hour)
	for i := 0; i < 3; i++ {
		if ok, _ := b.Allow("key-a"); !ok {
			t.Fatalf("call %d: want allowed", i+1)
		}
	}
	if ok, reset := b.Allow("key-a"); ok {
		t.Fatalf("4th call must be denied")
	} else if reset.IsZero() {
		t.Errorf("denied call must return non-zero resetAt so we can populate Retry-After")
	}
}

func TestTokenBucket_SeparateKeys(t *testing.T) {
	b := newTokenBucket(1, time.Hour)
	if ok, _ := b.Allow("alice"); !ok {
		t.Fatalf("alice first call must be allowed")
	}
	if ok, _ := b.Allow("bob"); !ok {
		t.Fatalf("bob must have his own bucket; first call must be allowed")
	}
	if ok, _ := b.Allow("alice"); ok {
		t.Fatalf("alice second call must be denied")
	}
}

func TestTokenBucket_ResetsAfterWindow(t *testing.T) {
	b := newTokenBucket(1, 10*time.Millisecond)
	if ok, _ := b.Allow("k"); !ok {
		t.Fatalf("first call must be allowed")
	}
	if ok, _ := b.Allow("k"); ok {
		t.Fatalf("second call within window must be denied")
	}
	time.Sleep(15 * time.Millisecond)
	if ok, _ := b.Allow("k"); !ok {
		t.Fatalf("after window elapses, a new allowance must start")
	}
}
