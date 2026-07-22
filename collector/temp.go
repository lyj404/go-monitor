package collector

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type CPUTemp struct {
	Temp float64 `json:"temp"` // degrees Celsius
}

func CollectCPUTemp() (*CPUTemp, error) {
	matches, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil || len(matches) == 0 {
		return nil, nil
	}

	// Prefer zones that look like CPU package sensors; fall back to the first readable zone.
	bestPath := ""
	bestScore := -1
	for _, tempPath := range matches {
		zoneDir := filepath.Dir(tempPath)
		score := thermalZoneScore(zoneDir)
		if score > bestScore {
			bestScore = score
			bestPath = tempPath
		}
	}
	if bestPath == "" {
		bestPath = matches[0]
	}

	f, err := os.Open(bestPath)
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return nil, nil
	}

	val, err := strconv.ParseFloat(strings.TrimSpace(scanner.Text()), 64)
	if err != nil {
		return nil, nil
	}

	// sysfs reports millidegrees Celsius
	return &CPUTemp{Temp: val / 1000.0}, nil
}

func thermalZoneScore(zoneDir string) int {
	typePath := filepath.Join(zoneDir, "type")
	data, err := os.ReadFile(typePath)
	if err != nil {
		return 0
	}
	typ := strings.ToLower(strings.TrimSpace(string(data)))
	switch {
	case strings.Contains(typ, "x86_pkg"):
		return 100
	case strings.Contains(typ, "cpu"):
		return 80
	case strings.Contains(typ, "soc"):
		return 60
	case strings.Contains(typ, "pkg"):
		return 50
	default:
		return 10
	}
}
