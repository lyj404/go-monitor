package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

type Network struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

var (
	lastNetworkStats map[string]Network
	networkMu        sync.Mutex
	totalUpload      int64
	totalDownload    int64
	physicalNICCache map[string]bool
	physicalNICOnce  sync.Once
)

func InitNetwork() {
	lastNetworkStats = make(map[string]Network)
	totalUpload = 0
	totalDownload = 0
	physicalNICCache = make(map[string]bool)
}

func scanPhysicalNICs() {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return
	}
	for _, entry := range entries {
		iface := entry.Name()
		_, err := os.Stat("/sys/class/net/" + iface + "/device")
		if err == nil {
			physicalNICCache[iface] = true
		}
	}
}

func isPhysicalNIC(iface string) bool {
	physicalNICOnce.Do(scanPhysicalNICs)
	return physicalNICCache[iface]
}

func CollectNetwork() (*Network, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var currentUpload, currentDownload int64

	networkMu.Lock()
	defer networkMu.Unlock()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 10 {
			continue
		}

		iface := strings.Trim(parts[0], ":")

		if !isPhysicalNIC(iface) {
			continue
		}

		rx, _ := strconv.ParseInt(parts[1], 10, 64)
		tx, _ := strconv.ParseInt(parts[9], 10, 64)

		lastStats, exists := lastNetworkStats[iface]
		if exists {
			currentDownload += rx - lastStats.Download
			currentUpload += tx - lastStats.Upload
		}

		lastNetworkStats[iface] = Network{
			Upload:   tx,
			Download: rx,
		}
	}

	totalUpload += currentUpload
	totalDownload += currentDownload

	return &Network{
		Upload:   currentUpload,
		Download: currentDownload,
	}, nil
}

func GetHourlyTotals() (int64, int64) {
	networkMu.Lock()
	defer networkMu.Unlock()
	return totalUpload, totalDownload
}

func GetHourlyTotalsAndReset() (int64, int64) {
	networkMu.Lock()
	defer networkMu.Unlock()
	upload := totalUpload
	download := totalDownload
	totalUpload = 0
	totalDownload = 0
	return upload, download
}
