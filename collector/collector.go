package collector

import (
	"go-monitor/config"
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	Interval int      `json:"interval"`
	Memory   *Memory  `json:"memory,omitempty"`
	CPU      *CPU     `json:"cpu,omitempty"`
	Network  *Network `json:"network,omitempty"`
	Disk     *Disk    `json:"disk,omitempty"`
	DiskIO   *DiskIO  `json:"disk_io,omitempty"`
	SelfMem  *SelfMem `json:"self_mem,omitempty"`
}

type Alerter interface {
	Check(Metrics)
}

type Collector struct {
	cfg          *config.Config
	alerter      Alerter
	metrics      Metrics
	mu           sync.RWMutex
	done         chan struct{}
	intervalChanged atomic.Bool
}

func NewCollector(cfg *config.Config, alerter Alerter) *Collector {
	InitNetwork()
	return &Collector{
		cfg:     cfg,
		alerter: alerter,
		done:    make(chan struct{}),
	}
}

func (c *Collector) Start() {
	go func() {
		interval := time.Duration(c.cfg.Monitor.Interval) * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		c.collect()
		for {
			select {
			case <-ticker.C:
				c.collect()
				// Check if interval changed, reset ticker if so
				if c.intervalChanged.Swap(false) {
					newInterval := time.Duration(c.cfg.Monitor.Interval) * time.Second
					ticker.Reset(newInterval)
				}
			case <-c.done:
				return
			}
		}
	}()
}

func (c *Collector) Stop() {
	close(c.done)
}

// NotifyIntervalChanged signals the collector to reset its ticker
func (c *Collector) NotifyIntervalChanged() {
	c.intervalChanged.Store(true)
}

func (c *Collector) collect() {
	var m Metrics

	if c.cfg.Monitor.Memory {
		mem, err := CollectMemory()
		if err == nil {
			m.Memory = mem
		}
	}

	if c.cfg.Monitor.CPU {
		cpu, err := CollectCPU()
		if err == nil {
			m.CPU = cpu
		}
	}

	if c.cfg.Monitor.NetworkUp || c.cfg.Monitor.NetworkDown {
		net, err := CollectNetwork()
		if err == nil {
			m.Network = net
		}
	}

	if c.cfg.Monitor.DiskRoot {
		disk, err := CollectDisk()
		if err == nil {
			m.Disk = disk
		}
	}

	if c.cfg.Monitor.DiskIO {
		dio, err := CollectDiskIO()
		if err == nil {
			m.DiskIO = dio
		}
	}

	selfMem, err := CollectSelfMem()
	if err == nil {
		m.SelfMem = selfMem
	}

	// Single lock write for all metrics
	c.mu.Lock()
	c.metrics = m
	c.mu.Unlock()

	if c.alerter != nil {
		c.alerter.Check(m)
	}
}

func (c *Collector) GetMetrics() Metrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := c.metrics
	m.Interval = c.cfg.Monitor.Interval
	return m
}
