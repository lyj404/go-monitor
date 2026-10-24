package alerter

import (
	"testing"
	"time"

	"github.com/lyj404/go-monitor/collector"
	"github.com/lyj404/go-monitor/config"
)

// Simulates the user's production config: CPU alert on, threshold 90, duration 30s.
func TestCheckWithConfigCPUOverThresholdQueuesJob(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}
	// Drain worker-like: just observe the channel.

	cfg := config.Snapshot{
		Alert: config.AlertConfig{
			Enabled:       true,
			CPU:           true,
			CPUThreshold:  90,
			Duration:      0, // fire on first sample; duration window tested separately
			Interval:      600,
			Memory:        true,
			MemoryThreshold: 75,
			Disk:          true,
			DiskThreshold: 74.9,
		},
	}

	m := collector.Metrics{
		CPU:    &collector.CPU{Usage: 95.5},
		Memory: &collector.Memory{Usage: 50},
		Disk:   &collector.Disk{Usage: 40},
	}

	a.CheckWithConfig(m, cfg)

	select {
	case job := <-a.jobs:
		if job.subject != "CPU" {
			t.Fatalf("expected CPU job, got %q", job.subject)
		}
		if job.currentValue != "95.5%" {
			t.Fatalf("unexpected current value: %q", job.currentValue)
		}
	case <-time.After(time.Second):
		t.Fatal("CPU 95.5% >= 90 with duration=0 should enqueue alert job")
	}
}

func TestCheckWithConfigCPUBelowThresholdDoesNotQueue(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}

	cfg := config.Snapshot{
		Alert: config.AlertConfig{
			Enabled:      true,
			CPU:          true,
			CPUThreshold: 90,
			Duration:     0,
			Interval:     600,
		},
	}

	a.CheckWithConfig(collector.Metrics{CPU: &collector.CPU{Usage: 89.9}}, cfg)

	select {
	case job := <-a.jobs:
		t.Fatalf("89.9 < 90 must not enqueue, got %+v", job)
	default:
	}
}

func TestCheckWithConfigCPUDurationRequiresContinuousSamples(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}

	cfg := config.Snapshot{
		Alert: config.AlertConfig{
			Enabled:      true,
			CPU:          true,
			CPUThreshold: 90,
			Duration:     2, // seconds
			Interval:     600,
		},
	}
	high := collector.Metrics{CPU: &collector.CPU{Usage: 95}}
	low := collector.Metrics{CPU: &collector.CPU{Usage: 10}}

	// Sample 1: over threshold, starts window — no fire yet
	a.CheckWithConfig(high, cfg)
	if len(a.jobs) != 0 {
		t.Fatal("first sample must not fire when duration=2s")
	}

	// Dip below threshold — resets window
	a.CheckWithConfig(low, cfg)
	if len(a.jobs) != 0 {
		t.Fatal("dip below threshold must not fire")
	}

	// Back over — restarts window
	a.CheckWithConfig(high, cfg)
	if len(a.jobs) != 0 {
		t.Fatal("restart after dip must not fire immediately")
	}

	// Wait past duration while condition stays met
	time.Sleep(2100 * time.Millisecond)
	a.CheckWithConfig(high, cfg)

	select {
	case job := <-a.jobs:
		if job.subject != "CPU" {
			t.Fatalf("expected CPU after continuous duration, got %q", job.subject)
		}
	default:
		t.Fatal("continuous CPU >= threshold for duration should enqueue")
	}
}

func TestCheckWithConfigDisabledAlertNoops(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}

	cfg := config.Snapshot{
		Alert: config.AlertConfig{
			Enabled:      false, // even with CPU over threshold
			CPU:          true,
			CPUThreshold: 1,
			Duration:     0,
			Interval:     600,
		},
	}
	a.CheckWithConfig(collector.Metrics{CPU: &collector.CPU{Usage: 99}}, cfg)
	if len(a.jobs) != 0 {
		t.Fatal("alert.enabled=false must never enqueue")
	}
}

func TestCheckWithConfigCPUSwitchOffNoops(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}

	cfg := config.Snapshot{
		Alert: config.AlertConfig{
			Enabled:      true,
			CPU:          false,
			CPUThreshold: 1,
			Duration:     0,
			Interval:     600,
		},
	}
	a.CheckWithConfig(collector.Metrics{CPU: &collector.CPU{Usage: 99}}, cfg)
	if len(a.jobs) != 0 {
		t.Fatal("alert.cpu=false must never enqueue")
	}
}

func TestCheckWithConfigNilCPUNoops(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}

	cfg := config.Snapshot{
		Alert: config.AlertConfig{
			Enabled:      true,
			CPU:          true,
			CPUThreshold: 1,
			Duration:     0,
			Interval:     600,
		},
	}
	a.CheckWithConfig(collector.Metrics{CPU: nil}, cfg)
	if len(a.jobs) != 0 {
		t.Fatal("nil CPU metric must never enqueue (collector didn't populate)")
	}
}

func TestSendIntervalSuppressesSecondEmail(t *testing.T) {
	t.Parallel()

	a := &Alerter{
		conditionStartTimes: make(map[string]time.Time),
		lastSent:            make(map[string]time.Time),
		jobs:                make(chan emailJob, 4),
		stopCh:              make(chan struct{}),
	}
	cfg := config.Snapshot{Alert: config.AlertConfig{Interval: 600}}

	if !a.send("CPU", "CPU", "b", "1%", "2%", cfg) {
		t.Fatal("first send should enqueue")
	}
	if a.send("CPU", "CPU", "b", "1%", "2%", cfg) {
		t.Fatal("second send within interval must be dropped silently")
	}
	if len(a.jobs) != 1 {
		t.Fatalf("expected 1 queued job, got %d", len(a.jobs))
	}
}
