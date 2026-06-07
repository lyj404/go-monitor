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

	// Read the first thermal zone
	f, err := os.Open(matches[0])
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
