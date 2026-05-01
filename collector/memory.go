package collector

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type Memory struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Available uint64  `json:"available"`
	Usage     float64 `json:"usage"`
}

func CollectMemory() (*Memory, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}

	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	mem := &Memory{}
	var gotTotal, gotAvailable bool

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			mem.Total = parseMemValue(line) * 1024
			gotTotal = true
		} else if strings.HasPrefix(line, "MemAvailable:") {
			mem.Available = parseMemValue(line) * 1024
			gotAvailable = true
		}
		if gotTotal && gotAvailable {
			break
		}
	}

	mem.Used = mem.Total - mem.Available

	if mem.Total > 0 {
		mem.Usage = float64(mem.Used) / float64(mem.Total) * 100
	}

	return mem, nil
}

func parseMemValue(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		val, _ := strconv.ParseUint(parts[1], 10, 64)
		return val
	}
	return 0
}
