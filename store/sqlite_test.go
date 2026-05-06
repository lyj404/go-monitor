package store

import (
	"testing"
	"time"
)

func TestNextHour(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 10, 23, 45, 0, time.UTC)
	want := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	if got := nextHour(now); !got.Equal(want) {
		t.Fatalf("nextHour(%v) = %v, want %v", now, got, want)
	}
}
