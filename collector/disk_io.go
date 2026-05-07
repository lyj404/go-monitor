package collector

import (
	"bufio"
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
	lastDiskStats = make(map[string]diskCounter)
	physicalDiskCache = make(map[string]bool)
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
	if physicalDiskCache == nil || time.Since(physicalDiskRef) > physicalDiskRefD {
		physicalDiskCache = scanPhysicalDisks()
		physicalDiskRef = time.Now()
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
			if readBytes >= last.read {
				deltaRead += readBytes - last.read
			}
			if writeBytes >= last.write {
				deltaWrite += writeBytes - last.write
			}
		}

		lastDiskStats[name] = diskCounter{
			read:  readBytes,
			write: writeBytes,
		}
	}

	var rateR, rateW int64
	if !lastDiskSampled.IsZero() {
		elapsed := now.Sub(lastDiskSampled).Seconds()
		if elapsed > 0 {
			rateR = int64(float64(deltaRead) / elapsed)
			rateW = int64(float64(deltaWrite) / elapsed)
		}
	}
	lastDiskSampled = now

	return &DiskIO{
		ReadBytes:  rateR,
		WriteBytes: rateW,
	}, nil
}
