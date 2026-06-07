package collector

import (
	"encoding/json"
	"go-monitor/config"
	"log"
	"sync"
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
	CheckWithConfig(Metrics, config.Snapshot)
	Close()
}

type Collector struct {
	cfg     *config.Config
	alerter Alerter

	mu          sync.RWMutex
	metrics     Metrics
	metricsJSON []byte

	snapshotMu sync.RWMutex
	snapshot   config.Snapshot

	done     chan struct{}
	reloadCh chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewCollector(cfg *config.Config, alerter Alerter) *Collector {
	InitNetwork()
	c := &Collector{
		cfg:      cfg,
		alerter:  alerter,
		done:     make(chan struct{}),
		reloadCh: make(chan struct{}, 1),
	}
	c.snapshot = cfg.Snapshot()
	return c
}

func (c *Collector) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.snapshotMu.RLock()
		interval := time.Duration(c.snapshot.Monitor.Interval) * time.Second
		c.snapshotMu.RUnlock()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		c.collect()
		for {
			select {
			case <-ticker.C:
				c.collect()
			case <-c.reloadCh:
				c.snapshotMu.RLock()
				newInterval := time.Duration(c.snapshot.Monitor.Interval) * time.Second
				c.snapshotMu.RUnlock()
				ticker.Reset(newInterval)
			case <-c.done:
				return
			}
		}
	}()
}

func (c *Collector) Stop() {
	c.stopOnce.Do(func() {
		close(c.done)
	})
	c.wg.Wait()
}

// NotifyIntervalChanged signals the collector to reset its ticker.
func (c *Collector) NotifyIntervalChanged() {
	select {
	case c.reloadCh <- struct{}{}:
	default:
	}
}

func (c *Collector) collect() {
	c.snapshotMu.RLock()
	enabled := c.snapshot
	c.snapshotMu.RUnlock()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var m Metrics
	m.Interval = enabled.Monitor.Interval

	if enabled.Monitor.Memory {
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

	if enabled.Monitor.CPU {
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

	if enabled.Monitor.NetworkUp || enabled.Monitor.NetworkDown {
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

	if enabled.Monitor.DiskRoot {
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

	if enabled.Monitor.DiskIO {
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

	// Pre-marshal so /api/metrics can just write bytes — no per-request
	// allocation, no extra lock acquisition on the hot read path.
	data, err := json.Marshal(m)
	if err != nil {
		log.Println("指标序列化失败:", err)
	}

	c.mu.Lock()
	c.metrics = m
	c.metricsJSON = data
	c.mu.Unlock()

	if c.alerter != nil {
		c.alerter.CheckWithConfig(m, enabled)
	}
}

func (c *Collector) GetMetrics() Metrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metrics
}

// GetMetricsJSON returns the pre-marshaled JSON snapshot. The returned slice
// must not be mutated by the caller.
func (c *Collector) GetMetricsJSON() []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metricsJSON
}

func (c *Collector) UpdateSnapshot() {
	c.snapshotMu.Lock()
	c.snapshot = c.cfg.Snapshot()
	c.snapshotMu.Unlock()
}
