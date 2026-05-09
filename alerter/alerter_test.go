package alerter

import (
	"testing"
	"time"

	"go-monitor/config"
)

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	a := New()
	a.Close()
	a.Close()
}

func TestSendDropsWhenQueueFull(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 1),
		stopCh:              make(chan struct{}),
	}
	a.jobs <- emailJob{}

	a.send("CPU", "body", config.Config{Alert: config.AlertConfig{Interval: 1}})

	if len(a.jobs) != 1 {
		t.Fatalf("expected full queue to drop new job, len=%d", len(a.jobs))
	}
	if _, ok := a.lastSent["CPU"]; !ok {
		t.Fatal("expected send timestamp to be recorded")
	}
}
