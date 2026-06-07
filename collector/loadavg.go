package collector

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type LoadAvg struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
	Cores  int     `json:"cores"`
}

func CollectLoadAvg() (*LoadAvg, error) {
	f, err := os.Open("/proc/loadavg")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return &LoadAvg{}, nil
	}

	parts := strings.Fields(scanner.Text())
	if len(parts) < 3 {
		return &LoadAvg{}, nil
	}

	l1, _ := strconv.ParseFloat(parts[0], 64)
	l5, _ := strconv.ParseFloat(parts[1], 64)
	l15, _ := strconv.ParseFloat(parts[2], 64)

	return &LoadAvg{Load1: l1, Load5: l5, Load15: l15, Cores: runtime.NumCPU()}, nil
}
