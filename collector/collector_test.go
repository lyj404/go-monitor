package collector

import (
	"testing"
	"time"

	"go-monitor/config"
)

func TestCollectorStopIsIdempotent(t *testing.T) {
	t.Parallel()

	c := NewCollector(&config.Config{Monitor: config.MonitorConfig{Interval: 1}}, nil)
	c.Start()
	time.Sleep(10 * time.Millisecond)
	c.Stop()
	c.Stop()
}
