package collector

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DiskIO reports aggregated read/write rates in bytes/sec.
type DiskIO struct {
	ReadBytes  int64 `json:"read"`
	WriteBytes int64 `json:"write"`
}

type diskCounter struct {
	read  int64
	write int64
}

var (
	lastDiskStats     map[string]diskCounter
	lastDiskSampled   time.Time
	diskIOMu          sync.Mutex
	physicalDiskCache map[string]bool
	physicalDiskRef   time.Time
	physicalDiskMu    sync.Mutex
	physicalDiskRefD  = 5 * time.Minute
)

func init() {
	InitDiskIO()
}

// InitDiskIO resets the disk-IO collector's package-level state. Called from
// init() and NewCollector so repeated collector construction (e.g. in tests)
// starts from a clean baseline rather than stale counters from a prior run.
func InitDiskIO() {
	diskIOMu.Lock()
	lastDiskStats = make(map[string]diskCounter)
	lastDiskSampled = time.Time{}
	diskIOMu.Unlock()

	physicalDiskMu.Lock()
	physicalDiskCache = make(map[string]bool)
	physicalDiskRef = time.Time{}
	physicalDiskMu.Unlock()
}

func scanPhysicalDisks() map[string]bool {
	out := make(map[string]bool)
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return out
	}
	for _, entry := range entries {
		out[entry.Name()] = true
	}
	return out
}

func isPhysicalDisk(name string) bool {
	physicalDiskMu.Lock()
	defer physicalDiskMu.Unlock()
	now := time.Now()
	if physicalDiskCache == nil || now.Sub(physicalDiskRef) > physicalDiskRefD {
		physicalDiskCache = scanPhysicalDisks()
		physicalDiskRef = now
	}
	return physicalDiskCache[name]
}

func CollectDiskIO() (*DiskIO, error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var deltaRead, deltaWrite int64
	var counterRegressed bool
	var regressedDisk string

	diskIOMu.Lock()
	defer diskIOMu.Unlock()

	now := time.Now()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}

		name := fields[2]
		if !isPhysicalDisk(name) {
			continue
		}

		readSectors, _ := strconv.ParseInt(fields[5], 10, 64)
		writeSectors, _ := strconv.ParseInt(fields[9], 10, 64)

		readBytes := readSectors * 512
		writeBytes := writeSectors * 512

		last, exists := lastDiskStats[name]
		if exists {
			if readBytes < last.read || writeBytes < last.write {
				counterRegressed = true
				if regressedDisk == "" {
					regressedDisk = name
				}
			} else {
				deltaRead += readBytes - last.read
				deltaWrite += writeBytes - last.write
			}
		}

		lastDiskStats[name] = diskCounter{
			read:  readBytes,
			write: writeBytes,
		}
	}

	if counterRegressed {
		log.Printf("磁盘 IO 计数器回退，跳过本轮速率计算 (disk=%s)", regressedDisk)
		lastDiskSampled = now
		return &DiskIO{}, nil
	}

	var rateR, rateW int64
	if !lastDiskSampled.IsZero() {
		elapsed := now.Sub(lastDiskSampled).Seconds()
		const maxElapsedSeconds = 600.0
		if elapsed > 0 && elapsed <= maxElapsedSeconds {
			rateR = int64(float64(deltaRead) / elapsed)
			rateW = int64(float64(deltaWrite) / elapsed)
		} else if elapsed > maxElapsedSeconds {
			log.Printf("磁盘 IO 采集间隔异常 (%.1fs)，跳过本轮速率计算", elapsed)
		}
	}
	lastDiskSampled = now

	return &DiskIO{
		ReadBytes:  rateR,
		WriteBytes: rateW,
	}, nil
}
