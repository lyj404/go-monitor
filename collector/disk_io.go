package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

type DiskIO struct {
	ReadBytes  int64 `json:"read"`
	WriteBytes int64 `json:"write"`
}

var (
	lastDiskStats      map[string]DiskIO
	diskIOMu           sync.Mutex
	physicalDiskCache  map[string]bool
	physicalDiskOnce   sync.Once
)

func init() {
	lastDiskStats = make(map[string]DiskIO)
	physicalDiskCache = make(map[string]bool)
}

func scanPhysicalDisks() {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return
	}
	for _, entry := range entries {
		physicalDiskCache[entry.Name()] = true
	}
}

func isPhysicalDisk(name string) bool {
	physicalDiskOnce.Do(scanPhysicalDisks)
	return physicalDiskCache[name]
}

func CollectDiskIO() (*DiskIO, error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var currentRead, currentWrite int64

	diskIOMu.Lock()
	defer diskIOMu.Unlock()

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
			currentRead += readBytes - last.ReadBytes
			currentWrite += writeBytes - last.WriteBytes
		}

		lastDiskStats[name] = DiskIO{
			ReadBytes:  readBytes,
			WriteBytes: writeBytes,
		}
	}

	return &DiskIO{
		ReadBytes:  currentRead,
		WriteBytes: currentWrite,
	}, nil
}
