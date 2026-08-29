package alerter

import (
	"testing"
	"time"

	"github.com/lyj404/go-monitor/config"
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

	if ok := a.send("CPU", "CPU", "body", "1%", "2%", config.Snapshot{Alert: config.AlertConfig{Interval: 1}}); ok {
		t.Fatal("expected send to fail when queue is full")
	}

	if len(a.jobs) != 1 {
		t.Fatalf("expected full queue to drop new job, len=%d", len(a.jobs))
	}
	if _, ok := a.lastSent["CPU"]; !ok {
		t.Fatal("expected lastSent to retain timestamp so dropped alert backs off until the next interval")
	}
}

func TestShouldFireDurationZero(t *testing.T) {
	t.Parallel()

	a := New()
	defer a.Close()

	if !a.shouldFire("cpu", true, 0) {
		t.Fatal("duration=0 should fire on first met sample")
	}
	if a.shouldFire("cpu", false, 0) {
		t.Fatal("cleared condition must not fire")
	}
}

func TestCloseMarksAlerterClosed(t *testing.T) {
	t.Parallel()

	a := New()
	a.Close()

	a.sendMu.Lock()
	closed := a.closed
	a.sendMu.Unlock()
	if !closed {
		t.Fatal("expected alerter to be marked closed")
	}
}

func TestSendPrunesExpiredLastSent(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent: map[string]time.Time{
			"old": time.Now().Add(-2 * stateTTL),
		},
		jobs:   make(chan emailJob, 1),
		stopCh: make(chan struct{}),
	}

	a.send("CPU", "CPU", "body", "1%", "2%", config.Snapshot{Alert: config.AlertConfig{Interval: 1}})

	a.sendMu.Lock()
	_, oldExists := a.lastSent["old"]
	a.sendMu.Unlock()
	if oldExists {
		t.Fatal("expected expired lastSent entry to be pruned")
	}
}

func TestWorkerRecordsAlertHistory(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 1),
		stopCh:              make(chan struct{}),
	}

	done := make(chan struct{})
	var gotType, gotMsg string
	a.SetOnAlert(func(alertType, message, currentValue, threshold string) {
		gotType, gotMsg = alertType, message
		close(done)
	})

	if !a.send("CPU", "CPU", "cpu超阈值", "91%", "80%", config.Snapshot{Alert: config.AlertConfig{Interval: 1}}) {
		t.Fatal("expected send to enqueue the job")
	}

	a.handleJob(<-a.jobs)

	<-done
	if gotType != "CPU" || gotMsg != "cpu超阈值" {
		t.Fatalf("unexpected history payload: type=%q msg=%q", gotType, gotMsg)
	}
}
