package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network reports per-interface aggregated rates in bytes/sec.
type Network struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

type netCounter struct {
	rx int64
	tx int64
}

var (
	lastNetworkStats   map[string]netCounter
	lastNetworkSampled time.Time
	networkMu          sync.Mutex
	totalUpload        int64
	totalDownload      int64

	physicalNICCache    map[string]bool
	physicalNICRefresh  time.Time
	physicalNICCacheMu  sync.Mutex
	physicalNICRefreshD = 5 * time.Minute
)

func InitNetwork() {
	networkMu.Lock()
	lastNetworkStats = make(map[string]netCounter)
	totalUpload = 0
	totalDownload = 0
	lastNetworkSampled = time.Time{}
	networkMu.Unlock()

	physicalNICCacheMu.Lock()
	physicalNICCache = make(map[string]bool)
	physicalNICRefresh = time.Time{}
	physicalNICCacheMu.Unlock()
}

func scanPhysicalNICs() map[string]bool {
	out := make(map[string]bool)
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return out
	}
	for _, entry := range entries {
		iface := entry.Name()
		if _, err := os.Stat("/sys/class/net/" + iface + "/device"); err == nil {
			out[iface] = true
		}
	}
	return out
}

func isPhysicalNIC(iface string) bool {
	physicalNICCacheMu.Lock()
	defer physicalNICCacheMu.Unlock()
	now := time.Now()
	if physicalNICCache == nil || now.Sub(physicalNICRefresh) > physicalNICRefreshD {
		physicalNICCache = scanPhysicalNICs()
		physicalNICRefresh = now
	}
	return physicalNICCache[iface]
}

func CollectNetwork() (*Network, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var deltaUpload, deltaDownload int64

	networkMu.Lock()
	defer networkMu.Unlock()

	now := time.Now()

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

		last, exists := lastNetworkStats[iface]
		if exists {
			if rx >= last.rx {
				deltaDownload += rx - last.rx
			}
			if tx >= last.tx {
				deltaUpload += tx - last.tx
			}
		}

		lastNetworkStats[iface] = netCounter{rx: rx, tx: tx}
	}

	totalUpload += deltaUpload
	totalDownload += deltaDownload

	var rateUp, rateDown int64
	if !lastNetworkSampled.IsZero() {
		elapsed := now.Sub(lastNetworkSampled).Seconds()
		if elapsed > 0 {
			rateUp = int64(float64(deltaUpload) / elapsed)
			rateDown = int64(float64(deltaDownload) / elapsed)
		}
	}
	lastNetworkSampled = now

	return &Network{
		Upload:   rateUp,
		Download: rateDown,
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
