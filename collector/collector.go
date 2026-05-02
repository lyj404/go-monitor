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
	var wg sync.WaitGroup
	var mu sync.Mutex
	var m Metrics

	if c.cfg.Monitor.Memory {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if mem, err := CollectMemory(); err == nil {
				mu.Lock()
				m.Memory = mem
				mu.Unlock()
			}
		}()
	}

	if c.cfg.Monitor.CPU {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cpu, err := CollectCPU(); err == nil {
				mu.Lock()
				m.CPU = cpu
				mu.Unlock()
			}
		}()
	}

	if c.cfg.Monitor.NetworkUp || c.cfg.Monitor.NetworkDown {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if net, err := CollectNetwork(); err == nil {
				mu.Lock()
				m.Network = net
				mu.Unlock()
			}
		}()
	}

	if c.cfg.Monitor.DiskRoot {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if disk, err := CollectDisk(); err == nil {
				mu.Lock()
				m.Disk = disk
				mu.Unlock()
			}
		}()
	}

	if c.cfg.Monitor.DiskIO {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if dio, err := CollectDiskIO(); err == nil {
				mu.Lock()
				m.DiskIO = dio
				mu.Unlock()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if selfMem, err := CollectSelfMem(); err == nil {
			mu.Lock()
			m.SelfMem = selfMem
			mu.Unlock()
		}
	}()

	wg.Wait()

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
