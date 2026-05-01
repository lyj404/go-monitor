package collector

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

type CPU struct {
	Usage float64 `json:"usage"`
}

var (
	lastCPUStats []uint64
	cpuMu        sync.Mutex
	cpuInitialized bool
)

func CollectCPU() (*CPU, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cpuLine string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu ") {
			cpuLine = line
			break
		}
	}

	if cpuLine == "" {
		return nil, nil
	}

	parts := strings.Fields(cpuLine)
	if len(parts) < 5 {
		return nil, nil
	}

	n := len(parts) - 1

	cpuMu.Lock()
	defer cpuMu.Unlock()

	// Reuse lastCPUStats slice if same length
	if cap(lastCPUStats) >= n {
		lastCPUStats = lastCPUStats[:n]
	} else {
		lastCPUStats = make([]uint64, n)
	}

	cpu := &CPU{Usage: 0}

	// Parse current values into a local slice
	cur := make([]uint64, n)
	for i := 0; i < n; i++ {
		cur[i], _ = strconv.ParseUint(parts[i+1], 10, 64)
	}

	if cpuInitialized && len(lastCPUStats) == n {
		var totalDiff, idleDiff uint64
		for i := 0; i < n; i++ {
			diff := cur[i] - lastCPUStats[i]
			totalDiff += diff
			if i == 3 {
				idleDiff = diff
			}
		}

		if totalDiff > 0 {
			cpu.Usage = float64(totalDiff-idleDiff) / float64(totalDiff) * 100
		}
	}

	// Store current values for next round
	copy(lastCPUStats, cur)
	cpuInitialized = true

	return cpu, nil
}
