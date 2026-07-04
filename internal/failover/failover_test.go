package failover

import (
	"testing"
	"time"
)

func TestCounterThresholdAndReset(t *testing.T) {
	c := New()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }

	// below threshold: not skipped
	c.OnFailure("k", "m1")
	if c.ShouldSkip("k", "m1", 2, 300) {
		t.Error("should not skip after 1 failure (threshold 2)")
	}
	c.OnFailure("k", "m1")
	if !c.ShouldSkip("k", "m1", 2, 300) {
		t.Error("should skip after 2 failures")
	}

	// success clears
	c.OnSuccess("k", "m1")
	if c.ShouldSkip("k", "m1", 2, 300) {
		t.Error("should not skip after success")
	}

	// stale reset
	c.OnFailure("k", "m2")
	c.OnFailure("k", "m2")
	if !c.ShouldSkip("k", "m2", 2, 300) {
		t.Error("should skip m2 after 2 failures")
	}
	now = now.Add(400 * time.Second) // past reset window
	if c.ShouldSkip("k", "m2", 2, 300) {
		t.Error("should reset and not skip after reset window")
	}
}

func TestCounterNoResetWhenZero(t *testing.T) {
	c := New()
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.OnFailure("k", "m")
	c.OnFailure("k", "m")
	now = now.Add(999999 * time.Second)
	// resetSec=0 means never auto-reset
	if !c.ShouldSkip("k", "m", 2, 0) {
		t.Error("resetSec=0 should never auto-reset")
	}
}
