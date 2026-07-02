package collector

import (
	"encoding/json"
	"github.com/lyj404/go-monitor/config"
	"log"
	"sync"
	"time"
)

type Metrics struct {
	Interval int       `json:"interval"`
	Memory   *Memory   `json:"memory,omitempty"`
	CPU      *CPU      `json:"cpu,omitempty"`
	Network  *Network  `json:"network,omitempty"`
	Disk     *Disk     `json:"disk,omitempty"`
	DiskIO   *DiskIO   `json:"disk_io,omitempty"`
	SelfMem  *SelfMem  `json:"self_mem,omitempty"`
	Process  *Process  `json:"process,omitempty"`
	Uptime   *Uptime   `json:"uptime,omitempty"`
	TCPStat  *TCPStat  `json:"tcpstat,omitempty"`
	CPUTemp  *CPUTemp  `json:"cpu_temp,omitempty"`
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		if p, err := CollectProcess(); err == nil {
			mu.Lock()
			m.Process = p
			mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if u, err := CollectUptime(); err == nil {
			mu.Lock()
			m.Uptime = u
			mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if ts, err := CollectTCPStat(); err == nil {
			mu.Lock()
			m.TCPStat = ts
			mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if t, err := CollectCPUTemp(); err == nil {
			mu.Lock()
			m.CPUTemp = t
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

	AccumulateMetrics(m)
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

// Hourly metrics accumulator for daily_metrics persistence.
var (
	hourlyMu       sync.Mutex
	hourlyCPUSum   float64
	hourlyCPUMax   float64
	hourlyMemSum   float64
	hourlyMemMax   float64
	hourlyDiskSum  float64
	hourlyDiskMax  float64
	hourlySamples  int
)

// AccumulateMetrics adds the current metrics snapshot to the hourly accumulator.
func AccumulateMetrics(m Metrics) {
	hourlyMu.Lock()
	defer hourlyMu.Unlock()

	if m.CPU != nil {
		hourlyCPUSum += m.CPU.Usage
		if m.CPU.Usage > hourlyCPUMax {
			hourlyCPUMax = m.CPU.Usage
		}
	}
	if m.Memory != nil {
		hourlyMemSum += m.Memory.Usage
		if m.Memory.Usage > hourlyMemMax {
			hourlyMemMax = m.Memory.Usage
		}
	}
	if m.Disk != nil {
		hourlyDiskSum += m.Disk.Usage
		if m.Disk.Usage > hourlyDiskMax {
			hourlyDiskMax = m.Disk.Usage
		}
	}
	hourlySamples++
}

// HourlyMetrics holds the aggregated metrics for one hour.
type HourlyMetrics struct {
	AvgCPU, MaxCPU     float64
	AvgMemory, MaxMemory float64
	AvgDisk, MaxDisk   float64
	SampleCount        int
}

// GetHourlyMetricsAndReset returns the accumulated hourly metrics and resets
// the accumulators. Called by the store's hourly task.
func GetHourlyMetricsAndReset() HourlyMetrics {
	hourlyMu.Lock()
	defer hourlyMu.Unlock()

	var hm HourlyMetrics
	if hourlySamples > 0 {
		hm.AvgCPU = hourlyCPUSum / float64(hourlySamples)
		hm.MaxCPU = hourlyCPUMax
		hm.AvgMemory = hourlyMemSum / float64(hourlySamples)
		hm.MaxMemory = hourlyMemMax
		hm.AvgDisk = hourlyDiskSum / float64(hourlySamples)
		hm.MaxDisk = hourlyDiskMax
		hm.SampleCount = hourlySamples
	}

	hourlyCPUSum, hourlyCPUMax = 0, 0
	hourlyMemSum, hourlyMemMax = 0, 0
	hourlyDiskSum, hourlyDiskMax = 0, 0
	hourlySamples = 0

	return hm
}
